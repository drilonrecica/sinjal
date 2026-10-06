// Package notify holds what a notification channel is: its typed
// configuration (docs/11) and, in later files, how messages are worded and
// delivered. Channel configuration contains secrets (an SMTP password, a bot
// token, a webhook URL with a token in it), so every secret field is a
// secret.String and is stored only encrypted (docs/13 "Secret envelope").
package notify

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/drilonrecica/sinjal/internal/secret"
)

// Channel types, as stored in notification_channels.type.
const (
	TypeSMTP     = "smtp"
	TypeTelegram = "telegram"
	TypeDiscord  = "discord"
	TypeWebhook  = "webhook"
)

// Types lists the channel types in display order.
var Types = []string{TypeSMTP, TypeTelegram, TypeDiscord, TypeWebhook}

// SMTP connection security.
const (
	SecuritySTARTTLS = "starttls" // plain connection upgraded with STARTTLS
	SecurityTLS      = "tls"      // implicit TLS, usually port 465
)

// Bounds on what a form may hold.
const (
	MaxHostLen     = 253
	MaxRecipients  = 20
	MaxSecretLen   = 2048
	MaxFieldLen    = 512
	MaxHeaderName  = 100
	MaxHeaderValue = 1024
)

// Config is the typed configuration of one channel. Implementations are
// the value types SMTP, Telegram, Discord and Webhook. Field names (the
// keys of Validate's result, of Fields and of FromValues) are the names of
// the form inputs.
type Config interface {
	// Type is the channel type this configuration belongs to.
	Type() string
	// Validate returns one message per invalid field; nil means valid.
	Validate() map[string]string
	// WithSecretsFrom returns the configuration with every blank secret
	// field taken from old, so an edit form that leaves a secret empty
	// keeps it. old of another type is ignored.
	WithSecretsFrom(old Config) Config
	// Fields returns the non-secret values as shown in a form.
	Fields() map[string]string
	// SecretsSet reports which secret fields hold a value, so a form can
	// say "set" without ever showing it.
	SecretsSet() map[string]bool
}

// SMTP is an email channel.
type SMTP struct {
	Host     string
	Port     int
	Security string // SecuritySTARTTLS or SecurityTLS
	Username string
	Password secret.String
	From     string
	To       []string
}

// Telegram posts through the Bot API.
type Telegram struct {
	BotToken secret.String
	ChatID   string
}

// Discord posts to a channel webhook. The URL carries a token, so it is a
// secret.
type Discord struct {
	WebhookURL secret.String
}

// Webhook posts a JSON payload (docs/36) to a URL, optionally with one
// extra header, such as an Authorization header, whose value is a secret.
type Webhook struct {
	URL         string
	HeaderName  string
	HeaderValue secret.String
}

func (SMTP) Type() string     { return TypeSMTP }
func (Telegram) Type() string { return TypeTelegram }
func (Discord) Type() string  { return TypeDiscord }
func (Webhook) Type() string  { return TypeWebhook }

// errs collects field messages, keeping the first per field.
type errs map[string]string

func (e errs) add(field, msg string) {
	if _, ok := e[field]; !ok {
		e[field] = msg
	}
}

// result is nil when nothing is wrong.
func (e errs) result() map[string]string {
	if len(e) == 0 {
		return nil
	}
	return e
}

func (c SMTP) Validate() map[string]string {
	e := errs{}
	switch host := c.Host; {
	case host == "":
		e.add("host", "Enter the SMTP server.")
	case len(host) > MaxHostLen || strings.ContainsAny(host, " /:@?#\\"):
		e.add("host", "Enter a host name or address only, without a scheme or port.")
	}
	if c.Port < 1 || c.Port > 65535 {
		e.add("port", "Enter a port from 1 to 65535.")
	}
	if c.Security != SecuritySTARTTLS && c.Security != SecurityTLS {
		e.add("security", "Choose STARTTLS or TLS.")
	}
	if (c.Username == "") != (c.Password == "") {
		e.add("password", "Set both user name and password, or neither.")
	}
	if len(c.Username) > MaxFieldLen {
		e.add("username", fmt.Sprintf("Use at most %d characters.", MaxFieldLen))
	}
	if len(c.Password) > MaxSecretLen {
		e.add("password", fmt.Sprintf("Use at most %d characters.", MaxSecretLen))
	}
	if !validAddress(c.From) {
		e.add("from", "Enter an email address, such as sinjal@example.com.")
	}
	switch {
	case len(c.To) == 0:
		e.add("to", "Enter at least one recipient.")
	case len(c.To) > MaxRecipients:
		e.add("to", fmt.Sprintf("Use at most %d recipients.", MaxRecipients))
	}
	for _, to := range c.To {
		if !validAddress(to) {
			e.add("to", fmt.Sprintf("%q is not an email address.", to))
		}
	}
	return e.result()
}

// validAddress accepts a bare address (no display name, which would let a
// form smuggle in extra header text).
func validAddress(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && len(s) <= MaxFieldLen
}

var (
	botTokenRE = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)
	chatIDRE   = regexp.MustCompile(`^(-?[0-9]+|@[A-Za-z][A-Za-z0-9_]{3,})$`)
)

func (c Telegram) Validate() map[string]string {
	e := errs{}
	switch {
	case c.BotToken == "":
		e.add("bot_token", "Enter the bot token.")
	case !botTokenRE.MatchString(string(c.BotToken)) || len(c.BotToken) > MaxSecretLen:
		e.add("bot_token", "A bot token looks like 123456:ABC-DEF… as given by @BotFather.")
	}
	if !chatIDRE.MatchString(c.ChatID) {
		e.add("chat_id", "Enter a numeric chat id (groups start with -) or @channelname.")
	}
	return e.result()
}

func (c Discord) Validate() map[string]string {
	e := errs{}
	if c.WebhookURL == "" {
		e.add("webhook_url", "Enter the webhook URL.")
	} else if msg := checkURL(string(c.WebhookURL), []string{"https"}); msg != "" {
		e.add("webhook_url", msg)
	}
	return e.result()
}

func (c Webhook) Validate() map[string]string {
	e := errs{}
	if c.URL == "" {
		e.add("url", "Enter the URL.")
	} else if msg := checkURL(c.URL, []string{"http", "https"}); msg != "" {
		e.add("url", msg)
	}
	switch {
	case (c.HeaderName == "") != (c.HeaderValue == ""):
		e.add("header_value", "Set both header name and value, or neither.")
	case len(c.HeaderName) > MaxHeaderName || !validHeaderName(c.HeaderName):
		e.add("header_name", "Use letters, digits and hyphens only.")
	case len(c.HeaderValue) > MaxHeaderValue || strings.ContainsAny(string(c.HeaderValue), "\r\n"):
		e.add("header_value", "Use one line of at most 1024 characters.")
	}
	return e.result()
}

func validHeaderName(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// checkURL returns a message unless raw is an absolute URL with one of
// the schemes, a host and no user info (credentials belong in the
// dedicated secret fields).
func checkURL(raw string, schemes []string) string {
	if len(raw) > MaxSecretLen {
		return fmt.Sprintf("Use at most %d characters.", MaxSecretLen)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "Enter a full URL starting with " + strings.Join(schemes, ":// or ") + "://."
	}
	ok := false
	for _, s := range schemes {
		ok = ok || u.Scheme == s
	}
	if !ok {
		return "Enter a full URL starting with " + strings.Join(schemes, ":// or ") + "://."
	}
	if u.User != nil {
		return "Remove the user name and password from the URL."
	}
	return ""
}

func (c SMTP) WithSecretsFrom(old Config) Config {
	if o, ok := old.(SMTP); ok && c.Password == "" && c.Username == o.Username {
		// A password belongs to its user name: changing the user means
		// entering the password again.
		c.Password = o.Password
	}
	return c
}

func (c Telegram) WithSecretsFrom(old Config) Config {
	if o, ok := old.(Telegram); ok && c.BotToken == "" {
		c.BotToken = o.BotToken
	}
	return c
}

func (c Discord) WithSecretsFrom(old Config) Config {
	if o, ok := old.(Discord); ok && c.WebhookURL == "" {
		c.WebhookURL = o.WebhookURL
	}
	return c
}

func (c Webhook) WithSecretsFrom(old Config) Config {
	if o, ok := old.(Webhook); ok && c.HeaderValue == "" && c.HeaderName == o.HeaderName {
		c.HeaderValue = o.HeaderValue
	}
	return c
}

func (c SMTP) Fields() map[string]string {
	port := ""
	if c.Port != 0 {
		port = strconv.Itoa(c.Port)
	}
	return map[string]string{"host": c.Host, "port": port, "security": c.Security,
		"username": c.Username, "from": c.From, "to": strings.Join(c.To, ", ")}
}

func (c Telegram) Fields() map[string]string { return map[string]string{"chat_id": c.ChatID} }
func (c Discord) Fields() map[string]string  { return map[string]string{} }
func (c Webhook) Fields() map[string]string {
	return map[string]string{"url": c.URL, "header_name": c.HeaderName}
}

func (c SMTP) SecretsSet() map[string]bool     { return map[string]bool{"password": c.Password != ""} }
func (c Telegram) SecretsSet() map[string]bool { return map[string]bool{"bot_token": c.BotToken != ""} }
func (c Discord) SecretsSet() map[string]bool {
	return map[string]bool{"webhook_url": c.WebhookURL != ""}
}
func (c Webhook) SecretsSet() map[string]bool {
	return map[string]bool{"header_value": c.HeaderValue != ""}
}

// FromValues builds the configuration of a channel type from form values,
// trimmed; get returns "" for a missing one. It does not validate, and a
// type that is not one of Types gives nil.
func FromValues(typ string, get func(name string) string) Config {
	v := func(name string) string { return strings.TrimSpace(get(name)) }
	switch typ {
	case TypeSMTP:
		port, _ := strconv.Atoi(v("port")) // 0 when not a number, which Validate refuses
		var to []string
		for _, s := range strings.FieldsFunc(get("to"), func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' }) {
			if s = strings.TrimSpace(s); s != "" {
				to = append(to, s)
			}
		}
		return SMTP{Host: v("host"), Port: port, Security: v("security"), Username: v("username"),
			Password: secret.String(get("password")), From: v("from"), To: to}
	case TypeTelegram:
		return Telegram{BotToken: secret.String(v("bot_token")), ChatID: v("chat_id")}
	case TypeDiscord:
		return Discord{WebhookURL: secret.String(v("webhook_url"))}
	case TypeWebhook:
		return Webhook{URL: v("url"), HeaderName: v("header_name"), HeaderValue: secret.String(get("header_value"))}
	}
	return nil
}

// The stored form is plain JSON with the real secret values; it exists
// only inside the encrypted notification_channels.config_enc.
type (
	smtpWire struct {
		Host     string   `json:"host"`
		Port     int      `json:"port"`
		Security string   `json:"security"`
		Username string   `json:"username,omitempty"`
		Password string   `json:"password,omitempty"`
		From     string   `json:"from"`
		To       []string `json:"to"`
	}
	telegramWire struct {
		BotToken string `json:"bot_token"`
		ChatID   string `json:"chat_id"`
	}
	discordWire struct {
		WebhookURL string `json:"webhook_url"`
	}
	webhookWire struct {
		URL         string `json:"url"`
		HeaderName  string `json:"header_name,omitempty"`
		HeaderValue string `json:"header_value,omitempty"`
	}
)

// Marshal is the plaintext that gets encrypted into config_enc. Never log
// or store it unencrypted.
func Marshal(c Config) ([]byte, error) {
	switch c := c.(type) {
	case SMTP:
		return json.Marshal(smtpWire{c.Host, c.Port, c.Security, c.Username, c.Password.Reveal(), c.From, c.To})
	case Telegram:
		return json.Marshal(telegramWire{c.BotToken.Reveal(), c.ChatID})
	case Discord:
		return json.Marshal(discordWire{c.WebhookURL.Reveal()})
	case Webhook:
		return json.Marshal(webhookWire{c.URL, c.HeaderName, c.HeaderValue.Reveal()})
	}
	return nil, fmt.Errorf("notify: cannot store a %T", c)
}

// Unmarshal is the inverse of Marshal for a channel of the given type.
func Unmarshal(typ string, b []byte) (Config, error) {
	switch typ {
	case TypeSMTP:
		var w smtpWire
		err := json.Unmarshal(b, &w)
		return SMTP{w.Host, w.Port, w.Security, w.Username, secret.String(w.Password), w.From, w.To}, err
	case TypeTelegram:
		var w telegramWire
		err := json.Unmarshal(b, &w)
		return Telegram{secret.String(w.BotToken), w.ChatID}, err
	case TypeDiscord:
		var w discordWire
		err := json.Unmarshal(b, &w)
		return Discord{secret.String(w.WebhookURL)}, err
	case TypeWebhook:
		var w webhookWire
		err := json.Unmarshal(b, &w)
		return Webhook{w.URL, w.HeaderName, secret.String(w.HeaderValue)}, err
	}
	return nil, fmt.Errorf("notify: unknown channel type %q", typ)
}
