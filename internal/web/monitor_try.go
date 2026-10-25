package web

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drilonrecica/sinjal/internal/monitor"
	"github.com/drilonrecica/sinjal/internal/secret"
	"github.com/drilonrecica/sinjal/internal/store"
	"github.com/drilonrecica/sinjal/internal/vault"
	"github.com/drilonrecica/sinjal/web/templates"
)

// The monitor form's helpers (docs/03 "Monitor creation/editing"): Test
// now, which runs one check with the form as posted and saves nothing, and
// the summaries of its collapsed rows. Admins only, like the form.

const (
	// testNowBound bounds one test: the check's own timeout is at most 60 s
	// shorter than the interval, so this only stops a stuck one.
	testNowBound = 20 * time.Second
	// testNowLimit tests per user per testNowWindow. A test makes a request
	// to any address, like a monitor does, so it is bounded like one.
	testNowLimit    = 10
	testNowWindow   = time.Minute
	testNowCapacity = 64
)

// tryNew serves POST /monitors/test, Test now on the create page.
func (h *Monitors) tryNew(w http.ResponseWriter, r *http.Request) {
	h.try(w, r, nil)
}

// tryExisting serves POST /monitors/{id}/test, Test now on the edit page:
// a secret left blank is the stored one, as when saving.
func (h *Monitors) tryExisting(w http.ResponseWriter, r *http.Request) {
	m, err := store.GetMonitor(r.Context(), h.db.Reader, chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		h.fail(w, r, "loading a monitor", err)
		return
	}
	h.try(w, r, &m)
}

func (h *Monitors) try(w http.ResponseWriter, r *http.Request, old *store.Monitor) {
	ctx, now := r.Context(), h.now()
	cs, _ := SessionFromContext(ctx)
	if h.tries.Blocked(cs.User.ID, now) {
		render(w, r, h.log, http.StatusTooManyRequests, templates.TestResultView(templates.TestResult{
			Error: "That was a lot of tests in a minute. Wait a moment, then try again."}))
		return
	}
	h.tries.Add(cs.User.ID, now)
	r.Body = http.MaxBytesReader(w, r.Body, monitorFormMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	f := formFromValues(r.PostForm)
	id := ""
	if old != nil {
		id, f.ID, f.Type = old.ID, old.ID, old.Type
	}
	if f.Type == store.TypeHeartbeat {
		http.Error(w, "a heartbeat monitor runs no check of its own", http.StatusBadRequest)
		return
	}
	stored, err := h.storedSecrets(r, id)
	if err != nil {
		h.fail(w, r, "reading secret names", err)
		return
	}
	in := monitorFromForm(f)
	var edits secretEdits
	if in.Type == store.TypeHTTP {
		edits = secretsFromValues(r.PostForm, stored, in.HTTP.Headers, f.Errors)
	}
	prepared, err := store.PrepareMonitor(ctx, h.db.Reader, id, in)
	if err != nil && !mergeFieldErrors(f.Errors, err) {
		h.fail(w, r, "validating a monitor", err)
		return
	}
	if len(f.Errors) > 0 {
		render(w, r, h.log, http.StatusOK, templates.TestResultView(templates.TestResult{Problems: f.Problems()}))
		return
	}
	secrets, err := h.trySecrets(ctx, id, edits)
	switch {
	case errors.Is(err, vault.ErrDecrypt), errors.Is(err, vault.ErrMalformed), errors.Is(err, vault.ErrUnknownVersion):
		render(w, r, h.log, http.StatusOK, templates.TestResultView(templates.TestResult{
			Error: "A stored secret cannot be decrypted. Enter it again, then test."}))
		return
	case err != nil:
		h.fail(w, r, "reading monitor secrets", err)
		return
	}
	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), testNowBound)
	defer cancel()
	res, err := h.engine.Try(tctx, prepared, secrets)
	if err != nil {
		render(w, r, h.log, http.StatusOK, templates.TestResultView(templates.TestResult{Error: "This monitor cannot be tested: " + err.Error()}))
		return
	}
	h.log.Info("monitor tested", "user_id", cs.User.ID, "monitor_id", id, "type", prepared.Type, "success", res.Success)
	v := templates.TestResult{Success: res.Success, Message: res.Message, Snippet: res.Snippet}
	if !res.Success {
		v.Kind = failureKinds[res.Kind]
		if v.Kind == "" {
			v.Kind = "Other"
		}
	}
	if res.Status != "" {
		v.Facts = append(v.Facts, "HTTP "+res.Status)
	}
	if res.Duration > 0 {
		v.Facts = append(v.Facts, formatLatency(res.Duration))
	}
	if res.TLSNotAfter != nil {
		v.Facts = append(v.Facts, certificateFact(*res.TLSNotAfter, now))
	}
	render(w, r, h.log, http.StatusOK, templates.TestResultView(v))
}

// trySecrets are the secrets a test sends: the stored ones of an existing
// monitor with the form's edits applied, in memory only.
func (h *Monitors) trySecrets(ctx context.Context, id string, s secretEdits) (map[string]secret.String, error) {
	out := map[string]secret.String{}
	if id != "" {
		stored, err := store.Secrets(ctx, h.db.Reader, h.key, id)
		if err != nil {
			return nil, err
		}
		out = stored
	}
	switch s.auth {
	case "":
		delete(out, monitor.SecretBasicAuth)
		delete(out, monitor.SecretBearerToken)
	case "basic":
		delete(out, monitor.SecretBearerToken)
	case "bearer":
		delete(out, monitor.SecretBasicAuth)
	}
	for _, name := range s.headerRemove {
		delete(out, name)
	}
	set := map[string]string{monitor.SecretBasicAuth: s.basic, monitor.SecretBearerToken: s.bearer}
	for name, v := range s.headerSet {
		set[name] = v
	}
	for name, v := range set {
		if v != "" {
			out[name] = secret.String(v)
		}
	}
	return out, nil
}

// certificateFact is "certificate valid until 2027-01-23 (in 90 days)".
func certificateFact(notAfter, now time.Time) string {
	days := int(math.Floor(notAfter.Sub(now).Hours() / 24))
	if days < 0 {
		return "certificate expired on " + notAfter.UTC().Format("2006-01-02")
	}
	return "certificate valid until " + notAfter.UTC().Format("2006-01-02") + " (in " + plural(days, "day") + ")"
}

// formSummary serves POST /monitors/form-summary?section=…: the summary
// line of one collapsed row, from the form's values as they are now.
func (h *Monitors) formSummary(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("section")
	if !templates.IsFormSection(key) {
		http.Error(w, "unknown section", http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, monitorFormMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	f := formFromValues(r.PostForm)
	if key == "alerts" {
		if err := h.fillChoices(r.Context(), &f); err != nil {
			h.fail(w, r, "listing form choices", err)
			return
		}
	}
	f.SecretHeaders = postedSecretHeaders(r.PostForm)
	render(w, r, h.log, http.StatusOK, templates.SectionSummaryText(f.SectionSummary(key)))
}

// postedSecretHeaders are the secret headers a post keeps or adds: the
// stored ones (each has an sh_value.<Name> input) not marked for removal,
// and a new one. Only their number is summarised.
func postedSecretHeaders(v url.Values) []string {
	var out []string
	for k := range v {
		if name, ok := strings.CutPrefix(k, "sh_value."); ok && v.Get("sh_remove."+name) == "" {
			out = append(out, name)
		}
	}
	if name := strings.TrimSpace(v.Get("sh_new_name")); name != "" {
		out = append(out, name)
	}
	return out
}
