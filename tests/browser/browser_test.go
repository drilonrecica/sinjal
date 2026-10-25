package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// target is a checked HTTP endpoint whose answer the test switches.
type target struct {
	*httptest.Server
	failing atomic.Bool
}

func newTarget(t *testing.T) *target {
	t.Helper()
	tg := &target{}
	tg.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tg.failing.Load() {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(tg.Close)
	return tg
}

// TestLogin: setup creates the admin; a wrong password is refused with the
// generic message; the right one opens the app; signing out closes it.
func TestLogin(t *testing.T) {
	ctx := newBrowser(t)
	s := start(t)
	createAdmin(t, ctx, s)

	do(t, ctx, "sign in with a wrong password",
		chromedp.Navigate(s.base+"/login"),
		chromedp.SendKeys(chromedp.CSS("#login"), adminLogin),
		chromedp.SendKeys(chromedp.CSS("#password"), "not the password"),
		chromedp.Submit(chromedp.CSS("#password")),
		chromedp.WaitVisible(chromedp.CSS("#form-error")),
	)
	if got := text(t, ctx, "#form-error"); got != "Incorrect username or password." {
		t.Errorf("error after a wrong password = %q", got)
	}
	if n := eval[int](t, ctx, `document.querySelectorAll("form.signout").length`); n != 0 {
		t.Fatal("a wrong password opened the app")
	}

	signIn(t, ctx, s, adminPassword)
	if n := eval[int](t, ctx, `document.querySelectorAll("aside.monitor-column").length`); n != 1 {
		t.Errorf("signed-in shell has %d monitor columns, want 1", n)
	}

	do(t, ctx, "sign out",
		chromedp.Submit(chromedp.CSS("form.signout button")),
		chromedp.WaitVisible(chromedp.CSS(`form[action="/login"] #password`)),
		chromedp.Navigate(s.base+"/monitors"),
		chromedp.WaitVisible(chromedp.CSS(`form[action="/login"] #password`)),
	)
	if loc := get(t, ctx, "location", chromedp.Location()); !strings.Contains(loc, "/login") {
		t.Errorf("/monitors after signing out ended on %s, want the sign-in page", loc)
	}
}

// TestCreateHTTPMonitor: the create form makes a monitor, the detail page
// shows it up after its first check, and the monitor column lists it.
func TestCreateHTTPMonitor(t *testing.T) {
	ctx, s := signedInAdmin(t)
	tg := newTarget(t)

	id := createHTTPMonitor(t, ctx, s, "Checkout API", tg.URL+"/health", "30")
	if got := text(t, ctx, "header.monitor-header h1"); got != "Checkout API" {
		t.Errorf("detail heading = %q", got)
	}
	waitState(t, ctx, "header.monitor-header", "up", 15*time.Second)
	if got := text(t, ctx, "header.monitor-header .status-text"); got != "Up" {
		t.Errorf("status label = %q, want Up", got)
	}
	// The Overview body follows the first check without a reload: never
	// "Up" over "Last success: never" or an empty chart.
	do(t, ctx, "wait for the overview to show the first check",
		chromedp.Poll[chromedp.Void](`(() => {
			const o = document.querySelector("#monitor-overview-`+id+`");
			const dt = [...(o?.querySelectorAll("dt") ?? [])].find(d => d.textContent === "Last success");
			return dt && dt.nextElementSibling.textContent !== "never" && !o.textContent.includes("No checks were stored");
		})()`, chromedp.WithPollingTimeout(10*time.Second)))
	do(t, ctx, "find the monitor in the column", chromedp.WaitVisible(chromedp.CSS("#col-"+id)))
	if got := text(t, ctx, "#col-"+id+" .column-name"); got != "Checkout API" {
		t.Errorf("column row name = %q", got)
	}
}

// TestMonitorFormTestNow: the name follows the URL, Test now answers in
// place without saving, a closed section summarises itself and an error
// opens the section that holds the field.
func TestMonitorFormTestNow(t *testing.T) {
	ctx, s := signedInAdmin(t)
	tg := newTarget(t)
	do(t, ctx, "fill the URL",
		chromedp.Navigate(s.base+"/monitors/new?type=http"),
		chromedp.WaitVisible(chromedp.CSS("#test-now")),
		chromedp.SendKeys(chromedp.CSS("#url"), tg.URL+"/health"),
	)
	if got := eval[string](t, ctx, `document.querySelector("#name").value`); got != "127.0.0.1" {
		t.Errorf("name = %q, want it to follow the URL's host", got)
	}
	eval[bool](t, ctx, `window.sinjalNoReload = true`)
	do(t, ctx, "test now",
		chromedp.Click(chromedp.CSS("#test-now")),
		chromedp.WaitVisible(chromedp.CSS("#test-result .test-up")),
	)
	if got := text(t, ctx, "#test-result .test-headline"); got != "Up" {
		t.Errorf("test result = %q, want Up", got)
	}
	if !eval[bool](t, ctx, `window.sinjalNoReload === true`) {
		t.Error("Test now reloaded the page")
	}
	tg.failing.Store(true)
	do(t, ctx, "test a failing target",
		chromedp.Click(chromedp.CSS("#test-now")),
		chromedp.WaitVisible(chromedp.CSS("#test-result .test-down")),
	)
	if got := text(t, ctx, "#test-result .test-headline"); !strings.Contains(got, "Would be down") {
		t.Errorf("failing test result = %q", got)
	}

	// Edit the interval in its section, close it: the summary follows.
	do(t, ctx, "change the interval",
		chromedp.Click(chromedp.CSS("#section-checking > summary")),
		chromedp.WaitVisible(chromedp.CSS("#interval")),
		chromedp.SetValue(chromedp.CSS("#interval"), "120"),
		chromedp.Click(chromedp.CSS("#section-checking > summary")),
		chromedp.Poll[chromedp.Void](`document.querySelector("#section-checking .form-row-summary").textContent.startsWith("Every 120 s")`,
			chromedp.WithPollingTimeout(5*time.Second)),
	)
	// A server-side error opens the section with the field.
	do(t, ctx, "save with a bad timeout",
		chromedp.SetValue(chromedp.CSS("#timeout"), "abc"),
		chromedp.Click(chromedp.CSS(`form.monitor-form button[type="submit"]`)),
		chromedp.WaitVisible(chromedp.CSS("#summary-title")),
	)
	if !eval[bool](t, ctx, `document.querySelector("#section-checking").open && !document.querySelector("#section-advanced").open`) {
		t.Error("the error did not open the Checking section alone")
	}
}

// TestSSETransition: a monitor going down reaches an open page over SSE
// and htmx swaps the header and the column row in place, without a reload.
func TestSSETransition(t *testing.T) {
	ctx, s := signedInAdmin(t)
	tg := newTarget(t)

	id := createHTTPMonitor(t, ctx, s, "Payments", tg.URL+"/health", "10")
	waitState(t, ctx, "header.monitor-header", "up", 15*time.Second)
	do(t, ctx, "wait for the event stream",
		chromedp.Poll[chromedp.Void](`document.querySelector("[sse-connect]")?.["htmx-internal-data"]?.sseEventSource?.readyState === EventSource.OPEN`,
			chromedp.WithPollingTimeout(10*time.Second)))
	// A full reload would drop this marker; a swap keeps it.
	eval[bool](t, ctx, `window.sinjalNoReload = true`)

	tg.failing.Store(true)
	waitState(t, ctx, "header.monitor-header", "down", 25*time.Second)
	waitState(t, ctx, "#col-"+id, "down", 5*time.Second)
	if got := text(t, ctx, "header.monitor-header .status-text"); got != "Down" {
		t.Errorf("status label = %q, want Down", got)
	}
	// The Overview body follows too: the failure and its incident's event.
	do(t, ctx, "wait for the overview to show the failure",
		chromedp.Poll[chromedp.Void](`(() => {
			const o = document.querySelector("#monitor-overview-`+id+`");
			const dt = [...(o?.querySelectorAll("dt") ?? [])].find(d => d.textContent === "Last failure");
			return dt && dt.nextElementSibling.textContent !== "never" && o.textContent.includes("Declared down");
		})()`, chromedp.WithPollingTimeout(10*time.Second)))
	if !eval[bool](t, ctx, `window.sinjalNoReload === true`) {
		t.Error("the page reloaded: the transition did not arrive over SSE")
	}
}

// TestThemeSwitch: choosing a theme on the Appearance page applies it at
// once, keeps it across pages, and follows the user to a new browser.
func TestThemeSwitch(t *testing.T) {
	ctx, s := signedInAdmin(t)
	theme := func(ctx context.Context) string {
		return eval[string](t, ctx, `document.documentElement.dataset.theme`)
	}
	if got := theme(ctx); got != "stark" {
		t.Errorf("default theme = %q, want stark", got)
	}

	do(t, ctx, "choose Terminal",
		chromedp.Navigate(s.base+"/account/appearance"),
		chromedp.Click(chromedp.CSS("#theme-terminal")),
		chromedp.Click(chromedp.CSS(`form[action="/account/appearance"] button[type="submit"]`)),
		chromedp.WaitVisible(chromedp.CSS(".auth-notice")),
	)
	if got := text(t, ctx, ".auth-notice"); got != "Your appearance was saved." {
		t.Errorf("notice = %q", got)
	}
	if got := theme(ctx); got != "terminal" {
		t.Errorf("theme after saving = %q, want terminal", got)
	}

	do(t, ctx, "open another page", chromedp.Navigate(s.base+"/monitors"), chromedp.WaitReady(chromedp.CSS("form.signout")))
	if got := theme(ctx); got != "terminal" {
		t.Errorf("theme on the next page = %q, want terminal", got)
	}

	// The choice is stored for the user, not only in this browser.
	other := visitor(t, ctx)
	signIn(t, other, s, adminPassword)
	if got := theme(other); got != "terminal" {
		t.Errorf("theme in a new browser = %q, want terminal", got)
	}
}

// TestStatusPageModes walks one status page through the four access modes
// and opens it each time as a stranger: public shows it, password asks for
// it, authenticated sends to sign-in, unlisted answers only at the secret
// address shown once. The monitor's own name is never published.
func TestStatusPageModes(t *testing.T) {
	ctx, s := signedInAdmin(t)
	tg := newTarget(t)
	const (
		internalName = "billing-db primary (internal)"
		publicName   = "Billing"
		pagePassword = "page password 42"
	)
	mon := createHTTPMonitor(t, ctx, s, internalName, tg.URL+"/health", "30")

	do(t, ctx, "create a public status page",
		chromedp.Navigate(s.base+"/status-pages/new"),
		chromedp.SendKeys(chromedp.CSS("#title"), "Acme status"),
		chromedp.SendKeys(chromedp.CSS("#slug"), "acme"),
		chromedp.Click(chromedp.CSS("#visibility-public")),
		chromedp.Click(chromedp.CSS("#m_show_"+mon)),
		chromedp.SendKeys(chromedp.CSS("#m_name_"+mon), publicName),
		chromedp.Click(chromedp.CSS(`form.monitor-form button[type="submit"]`)),
		chromedp.WaitVisible(chromedp.CSS(".sp-list")),
	)
	edit := get(t, ctx, "read the edit link", chromedp.AttributeValue(chromedp.CSS(`.sp-list a[href$="/edit"]`), "href")).Value
	if edit == "" {
		t.Fatal("the status page list has no edit link")
	}

	// save switches the page to mode, with password for a password page,
	// and waits for the page the save ends on.
	save := func(mode, password, landing string) {
		t.Helper()
		steps := []chromedp.Action[chromedp.Void]{
			chromedp.Navigate(s.base + edit),
			chromedp.Click(chromedp.CSS("#visibility-" + mode)),
		}
		if password != "" {
			steps = append(steps, chromedp.SendKeys(chromedp.CSS("#password"), password))
		}
		steps = append(steps,
			chromedp.Click(chromedp.CSS(`form.monitor-form button[type="submit"]`)),
			chromedp.WaitVisible(chromedp.CSS(landing)))
		do(t, ctx, "switch the page to "+mode, steps...)
	}
	// shows checks that a stranger's tab shows the page and its public
	// name, and nothing of the monitor's own.
	shows := func(v context.Context, mode string) {
		t.Helper()
		do(t, v, mode+": the page renders", chromedp.WaitVisible(chromedp.CSS(".public-service-name")))
		if got := text(t, v, ".public-service-name"); got != publicName {
			t.Errorf("%s: service name = %q, want %q", mode, got, publicName)
		}
		body := eval[string](t, v, `document.documentElement.outerHTML`)
		for _, leak := range []string{internalName, tg.URL} {
			if strings.Contains(body, leak) {
				t.Errorf("%s: the page shows %q", mode, leak)
			}
		}
	}

	// Public: anyone at the address.
	v := visitor(t, ctx)
	do(t, v, "open the public page", chromedp.Navigate(s.base+"/status/acme"))
	shows(v, "public")

	// Password: a form first; a wrong password is refused.
	save("password", pagePassword, ".sp-list")
	v = visitor(t, ctx)
	do(t, v, "open the password page",
		chromedp.Navigate(s.base+"/status/acme"),
		chromedp.WaitVisible(chromedp.CSS("form.public-password")),
		chromedp.SendKeys(chromedp.CSS("#password"), "wrong guess"),
		chromedp.Submit(chromedp.CSS("#password")),
		chromedp.WaitVisible(chromedp.CSS("#password-error")),
	)
	if n := eval[int](t, v, `document.querySelectorAll(".public-service").length`); n != 0 {
		t.Error("password: a wrong password showed the page")
	}
	do(t, v, "enter the page password",
		chromedp.SendKeys(chromedp.CSS("#password"), pagePassword),
		chromedp.Submit(chromedp.CSS("#password")),
	)
	shows(v, "password")

	// Authenticated: a stranger is sent to sign in; the admin sees it.
	save("authenticated", "", ".sp-list")
	v = visitor(t, ctx)
	do(t, v, "open the authenticated page",
		chromedp.Navigate(s.base+"/status/acme"),
		chromedp.WaitVisible(chromedp.CSS(`form[action="/login"]`)),
	)
	if n := eval[int](t, v, `document.querySelectorAll(".public-service").length`); n != 0 {
		t.Error("authenticated: a stranger saw the page")
	}
	do(t, ctx, "open the authenticated page signed in", chromedp.Navigate(s.base+"/status/acme"))
	shows(ctx, "authenticated")

	// Unlisted: the secret address is shown once and opens the page; the
	// slug address is gone.
	save("unlisted", "", "#page-url")
	secret, err := url.Parse(text(t, ctx, "#page-url"))
	if err != nil || !strings.HasPrefix(secret.Path, "/s/") {
		t.Fatalf("secret address %q: %v", secret, err)
	}
	v = visitor(t, ctx)
	do(t, v, "open the secret address", chromedp.Navigate(s.base+secret.Path))
	shows(v, "unlisted")
	resp, err := http.Get(s.base + "/status/acme")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unlisted: /status/acme = %d, want 404", resp.StatusCode)
	}
}
