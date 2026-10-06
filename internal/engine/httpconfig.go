package engine

import (
	"strconv"
	"time"

	"github.com/drilonrecica/sinjal/internal/monitor"
	"github.com/drilonrecica/sinjal/internal/monitor/httpcheck"
	"github.com/drilonrecica/sinjal/internal/results"
	"github.com/drilonrecica/sinjal/internal/secret"
	"github.com/drilonrecica/sinjal/internal/store"
)

// buildHTTPConfig turns a stored HTTP monitor into one check. The stored
// text was validated when it was saved, so an error here means the row was
// changed behind the application's back.
func buildHTTPConfig(m store.Monitor, c store.HTTPConfig, secrets map[string]secret.String) (httpcheck.Config, error) {
	expected, err := monitor.ParseStatus(c.ExpectedStatus)
	if err != nil {
		return httpcheck.Config{}, err
	}
	headers, err := monitor.ParseHeaders(c.Headers)
	if err != nil {
		return httpcheck.Config{}, err
	}
	assertions, err := monitor.ParseJSONAssertions(c.JSONAssertions)
	if err != nil {
		return httpcheck.Config{}, err
	}
	return httpcheck.Config{
		URL:             c.URL,
		Method:          c.Method,
		Headers:         headers,
		Body:            c.RequestBody,
		UserAgent:       c.CustomUserAgent,
		Expected:        expected,
		BodyContains:    c.BodyContains,
		BodyNotContains: c.BodyNotContains,
		JSONAssertions:  assertions,
		MaxBodyBytes:    int64(c.MaxBodyBytes),
		FollowRedirects: c.FollowRedirects,
		Insecure:        c.InsecureSkipVerify,
		ProxyURL:        c.ProxyURL,
		IPFamily:        c.IPFamily,
		Timeout:         time.Duration(m.TimeoutMS) * time.Millisecond,
		Secrets:         secrets,
	}, nil
}

// toResult is a finished HTTP check in the form the result processor
// stores. The certificate expiry drives the TLS warning only while expiry
// checks are on; the certificate itself is always in the metadata.
func toResult(monitorID string, tlsExpiry bool, r httpcheck.Result) results.Result {
	out := results.Result{
		MonitorID: monitorID,
		CheckedAt: r.Started,
		Duration:  r.Duration,
		Success:   r.Success,
		Kind:      r.Kind,
		Message:   r.Message,
		Snippet:   r.Snippet,
		Metadata:  r.MetadataJSON(),
	}
	if r.StatusCode != 0 {
		out.Status = strconv.Itoa(r.StatusCode)
	}
	if tlsExpiry && r.TLS != nil {
		notAfter := r.TLS.NotAfter
		out.TLSNotAfter = &notAfter
	}
	return out
}
