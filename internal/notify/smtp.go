package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// smtpTimeout bounds one delivery, connection to QUIT, whatever deadline
// the caller gives.
const smtpTimeout = 30 * time.Second

// SendEmail delivers a message through an SMTP channel: implicit TLS or a
// STARTTLS upgrade the server must offer (never plain text), AUTH PLAIN when
// a user name is set, one plain-text part. The error says which step
// failed and what the server answered; it never holds the password.
func SendEmail(ctx context.Context, c SMTP, m Message) error {
	return sendEmail(ctx, c, m, nil, time.Now())
}

// sendEmail is SendEmail with the trusted roots (nil: the system's) and
// the time the message is dated.
func sendEmail(ctx context.Context, c SMTP, m Message, roots *x509.CertPool, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, smtpTimeout)
	defer cancel()
	tlsCfg := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12, RootCAs: roots}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))

	var conn net.Conn
	var err error
	if c.Security == SecurityTLS {
		conn, err = (&tls.Dialer{Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return smtpError(ctx, "connect", err)
	}
	// net/smtp knows no context: closing the connection when the context
	// ends keeps a server that stops answering from holding the delivery.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return smtpError(ctx, "greeting", err)
	}
	defer cl.Close()

	if c.Security != SecurityTLS {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			if ctx.Err() != nil {
				return smtpError(ctx, "hello", ctx.Err())
			}
			return errors.New("smtp: the server does not offer STARTTLS")
		}
		if err := cl.StartTLS(tlsCfg); err != nil {
			return smtpError(ctx, "starttls", err)
		}
	}
	if c.Username != "" {
		if err := cl.Auth(smtp.PlainAuth("", c.Username, c.Password.Reveal(), c.Host)); err != nil {
			return smtpError(ctx, "authentication failed", err)
		}
	}
	if err := cl.Mail(c.From); err != nil {
		return smtpError(ctx, "sender refused", err)
	}
	for _, to := range c.To {
		if err := cl.Rcpt(to); err != nil {
			return smtpError(ctx, "recipient "+to+" refused", err)
		}
	}
	w, err := cl.Data()
	if err != nil {
		return smtpError(ctx, "data", err)
	}
	if _, err := w.Write(buildEmail(c, m, now)); err != nil {
		return smtpError(ctx, "data", err)
	}
	if err := w.Close(); err != nil {
		return smtpError(ctx, "message refused", err)
	}
	// The message is accepted; a failed QUIT changes nothing.
	_ = cl.Quit()
	return nil
}

// smtpError names the failed step and gives the server's answer on one
// short line.
func smtpError(ctx context.Context, step string, err error) error {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("smtp: %s: timed out", step)
		}
		return fmt.Errorf("smtp: %s: cancelled", step)
	}
	var te *textproto.Error
	if errors.As(err, &te) {
		return fmt.Errorf("smtp: %s: %d %s", step, te.Code, oneLine(te.Msg, maxReasonRunes))
	}
	return fmt.Errorf("smtp: %s: %s", step, oneLine(err.Error(), maxReasonRunes))
}

// buildEmail is the message as sent after DATA: headers and one
// quoted-printable UTF-8 text part. The subject is one line already
// (Render), so it cannot add a header; it is encoded when not ASCII.
func buildEmail(c SMTP, m Message, now time.Time) []byte {
	subject, body := m.Email()
	domain := c.From[strings.LastIndexByte(c.From, '@')+1:]
	var b bytes.Buffer
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", c.From)
	header("To", strings.Join(c.To, ", "))
	header("Subject", mime.QEncoding.Encode("utf-8", subject))
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", "<"+rand.Text()+"@"+domain+">")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	_, _ = qp.Write([]byte(body))
	_ = qp.Close()
	return b.Bytes()
}
