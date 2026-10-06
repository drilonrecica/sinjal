package httpcheck

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/drilonrecica/sinjal/internal/monitor"
	"github.com/drilonrecica/sinjal/internal/secret"
)

// Failure kinds (docs/06, docs/30). Stored in check_results.error_kind.
const (
	KindTimeout       = "timeout"
	KindDNS           = "dns"
	KindConnect       = "connect"
	KindTLS           = "tls"
	KindHTTPStatus    = "http_status"
	KindBodyAssertion = "body_assertion"
	KindJSONAssertion = "json_assertion"
	KindJSONParse     = "json_parse"
	KindProtocol      = "protocol"
	KindUnknown       = "unknown"
)

// SnippetMax caps the diagnostic snippet stored with a failed check.
const SnippetMax = 4096

// Config is one HTTP check, built from a monitor's validated settings and
// its decrypted secrets.
type Config struct {
	URL             string
	Method          string
	Headers         []monitor.Header
	Body            string // sent with POST
	UserAgent       string // "" sends the pool's default
	Expected        monitor.StatusExpr
	BodyContains    string // case-sensitive; "" disables
	BodyNotContains string // case-sensitive; "" disables
	JSONAssertions  []monitor.JSONAssertion
	MaxBodyBytes    int64 // read cap for assertions on the body
	FollowRedirects bool
	Insecure        bool
	ProxyURL        string
	IPFamily        string
	Timeout         time.Duration
	Secrets         map[string]secret.String // by monitor secret name
}

// Result is the outcome of one check. A successful check carries no body
// data: response bodies are never kept unless the check failed, and then
// only as a capped, scrubbed snippet.
type Result struct {
	Started    time.Time
	Finished   time.Time
	Duration   time.Duration
	Success    bool
	StatusCode int    // 0 when no response arrived
	Kind       string // "" on success
	Message    string
	Snippet    string
}

// Check runs one HTTP check within cfg.Timeout. It never returns an error:
// every failure is a Result with a kind and a message, and secret values
// are scrubbed from both message and snippet.
func (p *Pool) Check(ctx context.Context, cfg Config) Result {
	start := time.Now()
	res := Result{Started: start}
	scrub := newScrubber(cfg.Secrets)
	done := func() Result {
		res.Duration = time.Since(start)
		res.Finished = start.Add(res.Duration)
		res.Message = scrub.clean(res.Message)
		return res
	}

	client, err := p.client(transportKey{insecure: cfg.Insecure, proxy: cfg.ProxyURL, ipFamily: cfg.IPFamily}, cfg.FollowRedirects)
	if err != nil {
		res.Kind, res.Message = KindUnknown, err.Error()
		return done()
	}
	reqCtx := ctx
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	var body io.Reader
	if cfg.Method == http.MethodPost && cfg.Body != "" {
		body = strings.NewReader(cfg.Body)
	}
	req, err := http.NewRequestWithContext(reqCtx, cfg.Method, cfg.URL, body)
	if err != nil {
		res.Kind, res.Message = KindProtocol, err.Error()
		return done()
	}
	p.setHeaders(req, cfg)

	resp, err := client.Do(req)
	if err != nil {
		res.Kind, res.Message = classify(ctx, err, cfg.Timeout)
		return done()
	}
	defer resp.Body.Close()
	res.StatusCode = resp.StatusCode

	if !cfg.Expected.Match(resp.StatusCode) {
		res.Kind = KindHTTPStatus
		res.Message = fmt.Sprintf("status %d, expected %s", resp.StatusCode, cfg.Expected)
		res.Snippet = scrub.snippet(resp.Body)
		return done()
	}
	if cfg.BodyContains == "" && cfg.BodyNotContains == "" && len(cfg.JSONAssertions) == 0 {
		// Nothing needs the body: stop reading (docs/06 "stop reading once
		// enough data exists"). A short drain lets small responses keep their
		// connection; larger ones are closed instead.
		io.CopyN(io.Discard, resp.Body, SnippetMax)
		res.Success = true
		return done()
	}
	data, truncated, err := readCapped(resp.Body, cfg.MaxBodyBytes)
	if err != nil {
		res.Kind, res.Message = classify(ctx, err, cfg.Timeout)
		return done()
	}
	res.Kind, res.Message, res.Snippet = assertBody(cfg, data, truncated, scrub)
	res.Success = res.Kind == ""
	return done()
}

// jsonActualMax caps the actual value quoted in a JSON assertion snippet.
const jsonActualMax = 200

// assertBody applies the text assertions, then the JSON ones. All must hold;
// the first failure is reported. kind is "" when everything passed.
func assertBody(cfg Config, body []byte, truncated bool, scrub scrubber) (kind, msg, snippet string) {
	text := string(body)
	if cfg.BodyContains != "" && !strings.Contains(text, cfg.BodyContains) {
		return KindBodyAssertion, fmt.Sprintf("body does not contain %q", cfg.BodyContains), scrub.snippetBytes(body)
	}
	if cfg.BodyNotContains != "" && strings.Contains(text, cfg.BodyNotContains) {
		return KindBodyAssertion, fmt.Sprintf("body contains %q", cfg.BodyNotContains), scrub.snippetBytes(body)
	}
	if len(cfg.JSONAssertions) == 0 {
		return "", "", ""
	}
	if truncated {
		return KindJSONParse, fmt.Sprintf("body is larger than the %d byte read limit", cfg.MaxBodyBytes), scrub.snippetBytes(body)
	}
	doc, err := monitor.DecodeJSON(body)
	if err != nil {
		return KindJSONParse, "body is not valid JSON: " + err.Error(), scrub.snippetBytes(body)
	}
	for _, a := range cfg.JSONAssertions {
		ok, actual, err := monitor.EvalJSONAssertion(a, doc)
		if err != nil {
			return KindJSONAssertion, fmt.Sprintf("%s %s: %v", a.Path, a.Op, err), ""
		}
		if ok {
			continue
		}
		return KindJSONAssertion, fmt.Sprintf("%s %s failed", a.Path, a.Op), scrub.clip(jsonSnippet(a, actual), SnippetMax)
	}
	return "", "", ""
}

// jsonSnippet describes a failed JSON assertion: path, operator, expected
// value and what was found, the latter cut to jsonActualMax bytes.
func jsonSnippet(a monitor.JSONAssertion, actual string) string {
	if actual == "" {
		actual = "(missing)"
	} else if len(actual) > jsonActualMax {
		actual = actual[:jsonActualMax] + "..."
	}
	s := a.Path + " " + a.Op
	if len(a.Value) > 0 {
		s += " " + string(a.Value)
	}
	return s + "; actual: " + actual
}

// setHeaders applies, in order, the User-Agent, the plain headers, the
// secret headers and the auth secret; a later one wins on a name clash.
func (p *Pool) setHeaders(req *http.Request, cfg Config) {
	ua := cfg.UserAgent
	if ua == "" {
		ua = p.userAgent
	}
	req.Header.Set("User-Agent", ua)
	for _, h := range cfg.Headers {
		req.Header.Set(h.Name, h.Value)
	}
	names := make([]string, 0, len(cfg.Secrets))
	for n := range cfg.Secrets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if h, ok := strings.CutPrefix(n, monitor.SecretHeaderPrefix); ok {
			req.Header.Set(h, cfg.Secrets[n].Reveal())
		}
	}
	if v, ok := cfg.Secrets[monitor.SecretBasicAuth]; ok {
		user, pass, _ := strings.Cut(v.Reveal(), ":")
		req.SetBasicAuth(user, pass)
	} else if v, ok := cfg.Secrets[monitor.SecretBearerToken]; ok {
		req.Header.Set("Authorization", "Bearer "+v.Reveal())
	}
}

// classify maps a transport error to a failure kind and a short message.
// By the time an error is neither timeout, DNS, connection nor TLS, the
// exchange itself went wrong (malformed response, early close, redirect
// limit), so the fallback is protocol. Only cancellation by the caller is
// unknown: that is not a verdict about the target.
func classify(parent context.Context, err error, timeout time.Duration) (string, string) {
	if ue, ok := err.(*url.Error); ok {
		err = ue.Err
	}
	var (
		netErr  net.Error
		dnsErr  *net.DNSError
		opErr   *net.OpError
		verErr  *tls.CertificateVerificationError
		unkAuth x509.UnknownAuthorityError
		hostErr x509.HostnameError
		invErr  x509.CertificateInvalidError
		recErr  tls.RecordHeaderError
		alert   tls.AlertError
	)
	switch {
	case errors.Is(parent.Err(), context.Canceled):
		return KindUnknown, "check cancelled"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return KindTimeout, fmt.Sprintf("no response within %s", timeout)
	case errors.As(err, &dnsErr):
		return KindDNS, dnsErr.Error()
	case errors.As(err, &verErr), errors.As(err, &unkAuth), errors.As(err, &hostErr),
		errors.As(err, &invErr), errors.As(err, &recErr), errors.As(err, &alert):
		return KindTLS, err.Error()
	case errors.As(err, &opErr):
		return KindConnect, err.Error()
	default:
		return KindProtocol, err.Error()
	}
}

// readCapped reads at most max bytes of r and reports whether more
// followed. The body read cap of docs/06 (default 1 MiB) uses it.
func readCapped(r io.Reader, max int64) ([]byte, bool, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if int64(len(b)) > max {
		return b[:max], true, err
	}
	return b, false, err
}

// scrubber replaces known secret values with [REDACTED]. Basic auth also
// covers the password alone and the base64 form sent on the wire.
type scrubber struct {
	r      *strings.Replacer
	maxLen int
}

func newScrubber(secrets map[string]secret.String) scrubber {
	var vals []string
	for name, s := range secrets {
		v := s.Reveal()
		vals = append(vals, v)
		if name == monitor.SecretBasicAuth {
			_, pass, _ := strings.Cut(v, ":")
			vals = append(vals, pass, base64.StdEncoding.EncodeToString([]byte(v)))
		}
	}
	// Longest first, so a value containing another is replaced whole.
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	var s scrubber
	var pairs []string
	for _, v := range vals {
		if v == "" {
			continue
		}
		pairs = append(pairs, v, "[REDACTED]")
		s.maxLen = max(s.maxLen, len(v))
	}
	if len(pairs) > 0 {
		s.r = strings.NewReplacer(pairs...)
	}
	return s
}

func (s scrubber) clean(text string) string {
	if s.r == nil {
		return text
	}
	return s.r.Replace(text)
}

// snippet reads a failure snippet: scrubbed, cut to SnippetMax and made
// valid UTF-8. It reads the longest secret's length past the cap and scrubs
// before cutting, so a secret straddling the cut cannot leave a prefix.
func (s scrubber) snippet(body io.Reader) string {
	b, _, _ := readCapped(body, int64(SnippetMax+s.maxLen))
	return s.clip(string(b), SnippetMax)
}

// snippetBytes is snippet for a body that is already in memory.
func (s scrubber) snippetBytes(b []byte) string {
	if n := SnippetMax + s.maxLen; len(b) > n {
		b = b[:n]
	}
	return s.clip(string(b), SnippetMax)
}

// clip scrubs text, cuts it to max bytes and makes it valid UTF-8.
func (s scrubber) clip(text string, max int) string {
	text = s.clean(text)
	if len(text) > max {
		text = text[:max]
	}
	text = strings.ToValidUTF8(text, "\uFFFD")
	// The replacement character can be longer than the bytes it replaced.
	for len(text) > max {
		_, size := utf8.DecodeLastRuneInString(text)
		text = text[:len(text)-size]
	}
	return text
}
