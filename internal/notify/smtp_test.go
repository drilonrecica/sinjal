package notify

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/sinjal/internal/secret"
)

// fakeSMTP is a small SMTP server on 127.0.0.1 with a certificate for
// "localhost". It records each command with whether the link was TLS
// when it arrived, and each accepted message.
type fakeSMTP struct {
	ln          net.Listener
	cfg         *tls.Config
	implicitTLS bool
	noSTARTTLS  bool
	user, pass  string // AUTH PLAIN is offered when user is set
	rejectRcpt  string
	stall       bool // greet nobody, answer nothing

	mu    sync.Mutex
	cmds  []fakeCmd
	mails []fakeMail
}

type fakeCmd struct {
	line string
	tls  bool
}

type fakeMail struct {
	from string
	to   []string
	data []byte
}

// testCert is a self-signed certificate for localhost and the pool that
// trusts it.
func testCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// start listens and serves until the test ends; it returns the pool that
// trusts the server and the port.
func (f *fakeSMTP) start(t *testing.T) (*x509.CertPool, int) {
	t.Helper()
	cert, pool := testCert(t)
	f.cfg = &tls.Config{Certificates: []tls.Certificate{cert}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.ln = ln
	var wg sync.WaitGroup
	t.Cleanup(func() { ln.Close(); wg.Wait() })
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() { defer wg.Done(); defer conn.Close(); f.serve(conn) }()
		}
	}()
	return pool, ln.Addr().(*net.TCPAddr).Port
}

func (f *fakeSMTP) serve(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	isTLS := f.implicitTLS
	if isTLS {
		conn = tls.Server(conn, f.cfg)
	}
	if f.stall {
		_, _ = io.Copy(io.Discard, conn) // until the client gives up
		return
	}
	tp := textproto.NewConn(conn)
	reply := func(s string) { _ = tp.PrintfLine("%s", s) }
	reply("220 fake ESMTP")
	var mail fakeMail
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.cmds = append(f.cmds, fakeCmd{line, isTLS})
		f.mu.Unlock()
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			ext := []string{"fake"}
			if !isTLS && !f.noSTARTTLS {
				ext = append(ext, "STARTTLS")
			}
			if f.user != "" {
				ext = append(ext, "AUTH PLAIN")
			}
			for i, e := range ext {
				sep := "-"
				if i == len(ext)-1 {
					sep = " "
				}
				reply("250" + sep + e)
			}
		case "STARTTLS":
			reply("220 go ahead")
			tc := tls.Server(conn, f.cfg)
			if tc.Handshake() != nil {
				return
			}
			conn, isTLS = tc, true
			tp = textproto.NewConn(conn)
		case "AUTH":
			b64, _ := strings.CutPrefix(arg, "PLAIN ")
			raw, _ := base64.StdEncoding.DecodeString(b64)
			if string(raw) == "\x00"+f.user+"\x00"+f.pass {
				reply("235 2.7.0 accepted")
			} else {
				reply("535 5.7.8 bad credentials")
			}
		case "MAIL":
			mail = fakeMail{from: between(arg)}
			reply("250 ok")
		case "RCPT":
			if to := between(arg); to == f.rejectRcpt {
				reply("550 5.1.1 no such user")
			} else {
				mail.to = append(mail.to, to)
				reply("250 ok")
			}
		case "DATA":
			reply("354 go ahead")
			data, err := tp.ReadDotBytes()
			if err != nil {
				return
			}
			mail.data = data
			f.mu.Lock()
			f.mails = append(f.mails, mail)
			f.mu.Unlock()
			reply("250 queued")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 not here")
		}
	}
}

func between(s string) string {
	_, s, _ = strings.Cut(s, "<")
	s, _, _ = strings.Cut(s, ">")
	return s
}

func (f *fakeSMTP) recorded() ([]fakeCmd, []fakeMail) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCmd(nil), f.cmds...), append([]fakeMail(nil), f.mails...)
}

func smtpChannel(port int, security string) SMTP {
	return SMTP{Host: "localhost", Port: port, Security: security, Username: "sinjal", Password: "hunter2-secret",
		From: "sinjal@example.com", To: []string{"ops@example.com", "oncall@example.org"}}
}

func downMessage() Message {
	e := baseEvent(KindDown)
	e.Reason, e.Attempts, e.Latency = "timeout after 5s", 2, ms(74)
	return Render(e, cest)
}

// parseMail reads a received message and decodes its quoted-printable body.
func parseMail(t *testing.T, data []byte) (*mail.Message, string) {
	t.Helper()
	msg, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("not a mail message: %v\n%s", err, data)
	}
	body, err := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if err != nil {
		t.Fatal(err)
	}
	return msg, string(body)
}

func TestSendEmailSTARTTLS(t *testing.T) {
	f := &fakeSMTP{user: "sinjal", pass: "hunter2-secret"}
	roots, port := f.start(t)
	m := downMessage()
	if err := sendEmail(context.Background(), smtpChannel(port, SecuritySTARTTLS), m, roots, failed); err != nil {
		t.Fatal(err)
	}
	cmds, mails := f.recorded()
	if len(mails) != 1 {
		t.Fatalf("got %d messages, want 1", len(mails))
	}
	got := mails[0]
	if got.from != "sinjal@example.com" || strings.Join(got.to, ",") != "ops@example.com,oncall@example.org" {
		t.Errorf("envelope = %q %q", got.from, got.to)
	}
	sawAuth := false
	for _, c := range cmds {
		verb, _, _ := strings.Cut(c.line, " ")
		if verb == "STARTTLS" || verb == "EHLO" {
			continue
		}
		if !c.tls {
			t.Errorf("%q sent before TLS", c.line)
		}
		sawAuth = sawAuth || verb == "AUTH"
	}
	if !sawAuth {
		t.Error("no AUTH sent")
	}

	msg, body := parseMail(t, got.data)
	subject, want := m.Email()
	if body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
	if s := msg.Header.Get("Subject"); s != subject {
		t.Errorf("Subject = %q, want %q", s, subject)
	}
	for k, v := range map[string]string{
		"From": "sinjal@example.com", "To": "ops@example.com, oncall@example.org",
		"Date": "Tue, 06 Oct 2026 16:42:13 +0000", "MIME-Version": "1.0",
		"Content-Type": "text/plain; charset=utf-8", "Content-Transfer-Encoding": "quoted-printable",
	} {
		if got := msg.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if id := msg.Header.Get("Message-ID"); !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, "@example.com>") {
		t.Errorf("Message-ID = %q", id)
	}
}

func TestSendEmailImplicitTLS(t *testing.T) {
	f := &fakeSMTP{implicitTLS: true, user: "sinjal", pass: "hunter2-secret"}
	roots, port := f.start(t)
	if err := sendEmail(context.Background(), smtpChannel(port, SecurityTLS), downMessage(), roots, failed); err != nil {
		t.Fatal(err)
	}
	cmds, mails := f.recorded()
	if len(mails) != 1 {
		t.Fatalf("got %d messages, want 1", len(mails))
	}
	for _, c := range cmds {
		if !c.tls || strings.HasPrefix(c.line, "STARTTLS") {
			t.Errorf("unexpected %q (tls %v)", c.line, c.tls)
		}
	}
}

func TestSendEmailWithoutAuth(t *testing.T) {
	f := &fakeSMTP{}
	roots, port := f.start(t)
	c := smtpChannel(port, SecuritySTARTTLS)
	c.Username, c.Password = "", ""
	if err := sendEmail(context.Background(), c, downMessage(), roots, failed); err != nil {
		t.Fatal(err)
	}
	cmds, mails := f.recorded()
	if len(mails) != 1 {
		t.Fatalf("got %d messages, want 1", len(mails))
	}
	for _, cmd := range cmds {
		if strings.HasPrefix(cmd.line, "AUTH") {
			t.Errorf("AUTH sent without a user name: %q", cmd.line)
		}
	}
}

// A server that does not offer STARTTLS gets nothing but the greeting:
// no fallback to plain text, so the password never travels in clear.
func TestSendEmailRefusesWithoutSTARTTLS(t *testing.T) {
	f := &fakeSMTP{noSTARTTLS: true, user: "sinjal", pass: "hunter2-secret"}
	roots, port := f.start(t)
	err := sendEmail(context.Background(), smtpChannel(port, SecuritySTARTTLS), downMessage(), roots, failed)
	if err == nil || !strings.Contains(err.Error(), "does not offer STARTTLS") {
		t.Fatalf("err = %v, want a STARTTLS refusal", err)
	}
	cmds, mails := f.recorded()
	if len(mails) != 0 {
		t.Error("a message was sent")
	}
	for _, c := range cmds {
		if v, _, _ := strings.Cut(c.line, " "); v != "EHLO" && v != "QUIT" {
			t.Errorf("sent %q over plain text", c.line)
		}
	}
}

func TestSendEmailErrors(t *testing.T) {
	cases := []struct {
		name string
		f    *fakeSMTP
		edit func(*SMTP)
		want string
	}{
		{"wrong password", &fakeSMTP{user: "sinjal", pass: "other"}, nil,
			"smtp: authentication failed: 535 5.7.8 bad credentials"},
		{"recipient refused", &fakeSMTP{user: "sinjal", pass: "hunter2-secret", rejectRcpt: "oncall@example.org"}, nil,
			"smtp: recipient oncall@example.org refused: 550 5.1.1 no such user"},
		{"nothing listening", nil, func(c *SMTP) { c.Port = closedPort(t) }, "smtp: connect: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var roots *x509.CertPool
			port := 0
			if tc.f != nil {
				roots, port = tc.f.start(t)
			}
			c := smtpChannel(port, SecuritySTARTTLS)
			if tc.edit != nil {
				tc.edit(&c)
			}
			err := sendEmail(context.Background(), c, downMessage(), roots, failed)
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if s := fmt.Sprintf("%v %+v %#v", err, err, err); strings.Contains(s, "hunter2") {
				t.Errorf("error holds the password: %s", s)
			}
			if tc.f != nil {
				if _, mails := tc.f.recorded(); len(mails) != 0 {
					t.Error("a message was accepted")
				}
			}
		})
	}
}

func closedPort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func TestSendEmailUntrustedCertificate(t *testing.T) {
	for _, sec := range []string{SecuritySTARTTLS, SecurityTLS} {
		f := &fakeSMTP{implicitTLS: sec == SecurityTLS, user: "sinjal", pass: "hunter2-secret"}
		_, port := f.start(t)
		err := sendEmail(context.Background(), smtpChannel(port, sec), downMessage(), nil, failed)
		if err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Errorf("%s: err = %v, want a certificate error", sec, err)
		}
		if cmds, _ := f.recorded(); len(cmds) > 0 && cmds[len(cmds)-1].tls {
			t.Errorf("%s: talked over the untrusted link: %q", sec, cmds[len(cmds)-1].line)
		}
	}
}

// A server that accepts the connection and then says nothing holds the
// delivery only until the context ends.
func TestSendEmailBounded(t *testing.T) {
	f := &fakeSMTP{stall: true}
	roots, port := f.start(t)
	c := smtpChannel(port, SecuritySTARTTLS)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := sendEmail(ctx, c, downMessage(), roots, failed)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v, want a timeout", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v", d)
	}

	ctx, cancel = context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start = time.Now()
	err = sendEmail(ctx, c, downMessage(), roots, failed)
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("err = %v, want cancelled", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v after cancel", d)
	}
}

func TestBuildEmail(t *testing.T) {
	c := smtpChannel(25, SecuritySTARTTLS)
	e := baseEvent(KindDown)
	e.MonitorName = "Prüfung\r\nBcc: victim@example.net"
	e.Reason = strings.Repeat("ä", 150)
	m := Render(e, cest)
	m.Lines = append(m.Lines, ".", ".leading dot")
	data := buildEmail(c, m, failed)

	for line := range strings.SplitSeq(string(data), "\r\n") {
		if line == "" {
			break // end of the headers
		}
		if strings.HasPrefix(line, "Bcc") {
			t.Errorf("a name added a header: %q", line)
		}
		if len(line) > 998 {
			t.Errorf("header line of %d bytes", len(line))
		}
	}
	msg, body := parseMail(t, data)
	raw := msg.Header.Get("Subject")
	if !strings.HasPrefix(raw, "=?utf-8?q?") {
		t.Errorf("Subject not encoded: %q", raw)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(raw)
	if err != nil || subject != "Prüfung Bcc: victim@example.net is DOWN" {
		t.Errorf("Subject = %q (%v)", subject, err)
	}
	if _, want := m.Email(); strings.ReplaceAll(body, "\r\n", "\n") != want {
		t.Errorf("body = %q, want %q", body, want)
	}
	_, rawBody, _ := bytes.Cut(data, []byte("\r\n\r\n"))
	sc := bufio.NewScanner(bytes.NewReader(rawBody))
	for sc.Scan() {
		if len(sc.Text()) > 76 {
			t.Errorf("body line of %d bytes", len(sc.Text()))
		}
	}
}

// The dot lines of TestBuildEmail survive the DATA transfer (dot-stuffing
// by net/smtp, undone by the server).
func TestSendEmailDotLines(t *testing.T) {
	f := &fakeSMTP{}
	roots, port := f.start(t)
	c := smtpChannel(port, SecuritySTARTTLS)
	c.Username, c.Password = "", secret.String("")
	m := downMessage()
	m.Lines = append(m.Lines, ".", ".leading dot")
	if err := sendEmail(context.Background(), c, m, roots, failed); err != nil {
		t.Fatal(err)
	}
	_, mails := f.recorded()
	if len(mails) != 1 {
		t.Fatalf("got %d messages", len(mails))
	}
	_, body := parseMail(t, mails[0].data)
	if !strings.HasSuffix(body, "\n.\n.leading dot\n") {
		t.Errorf("body = %q", body)
	}
}

func TestSmtpErrorWithoutContext(t *testing.T) {
	err := smtpError(context.Background(), "data", errors.New("line one\r\nline two"))
	if err.Error() != "smtp: data: line one line two" {
		t.Errorf("err = %q", err)
	}
}
