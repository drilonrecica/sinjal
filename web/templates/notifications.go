package templates

// ChannelListView is the Notifications page: the configured channels.
type ChannelListView struct {
	Admin bool
	Rows  []ChannelRow
}

// ChannelRow is one channel in the list. Status and Detail are text, so
// state never relies on colour alone.
type ChannelRow struct {
	ID        string
	Name      string
	TypeLabel string
	Status    string // "Healthy", "Failed", "Disabled", …
	Detail    string // "Last success 12:04 UTC", "Last failure …: reason"
}

// ChannelOption is one choice of a select field.
type ChannelOption struct{ Value, Label string }

// ChannelField describes one input of a channel form. Name is also the key
// of the form value, of the configuration's Fields and of its errors.
type ChannelField struct {
	Name        string
	Label       string
	Kind        string // "text", "number", "password" or "select"
	Hint        string
	Placeholder string
	Secret      bool // write-only: never shown back, blank keeps the stored value
	Options     []ChannelOption
}

// ChannelType is one choice of channel type.
type ChannelType struct{ Value, Label string }

// ChannelTypes lists the channel types in display order.
var ChannelTypes = []ChannelType{
	{"smtp", "Email (SMTP)"}, {"telegram", "Telegram"}, {"discord", "Discord"}, {"webhook", "Webhook"},
}

// ChannelTypeLabel is the display name of a channel type.
func ChannelTypeLabel(typ string) string {
	for _, t := range ChannelTypes {
		if t.Value == typ {
			return t.Label
		}
	}
	return typ
}

// ChannelFields lists the inputs of a channel type, in page order, after
// the name.
func ChannelFields(typ string) []ChannelField {
	switch typ {
	case "smtp":
		return []ChannelField{
			{Name: "host", Label: "SMTP server", Kind: "text", Placeholder: "smtp.example.com"},
			{Name: "port", Label: "Port", Kind: "number", Placeholder: "587"},
			{Name: "security", Label: "Connection security", Kind: "select",
				Options: []ChannelOption{{"starttls", "STARTTLS (usually port 587)"}, {"tls", "TLS (usually port 465)"}}},
			{Name: "username", Label: "User name", Kind: "text", Hint: "Leave both user name and password empty for a server without authentication."},
			{Name: "password", Label: "Password", Kind: "password", Secret: true},
			{Name: "from", Label: "From address", Kind: "text", Placeholder: "sinjal@example.com"},
			{Name: "to", Label: "Recipients", Kind: "text", Hint: "One or more addresses, separated by commas."},
		}
	case "telegram":
		return []ChannelField{
			{Name: "bot_token", Label: "Bot token", Kind: "password", Secret: true, Hint: "From @BotFather, like 123456:ABC-DEF…"},
			{Name: "chat_id", Label: "Chat id", Kind: "text", Hint: "A numeric id (groups start with -) or @channelname."},
		}
	case "discord":
		return []ChannelField{
			{Name: "webhook_url", Label: "Webhook URL", Kind: "password", Secret: true, Hint: "Channel settings → Integrations → Webhooks. The URL contains a token, so it is stored encrypted and not shown again."},
		}
	case "webhook":
		return []ChannelField{
			{Name: "url", Label: "URL", Kind: "text", Placeholder: "https://example.com/hooks/sinjal"},
			{Name: "header_name", Label: "Extra header name", Kind: "text", Hint: "Optional, such as Authorization."},
			{Name: "header_value", Label: "Extra header value", Kind: "password", Secret: true},
		}
	}
	return nil
}

// ChannelForm is the state of the create/edit channel page. Values are
// kept as typed so a rejected form is shown back as entered, except secrets,
// which are never shown: SecretSet says which are stored.
type ChannelForm struct {
	ID        string // "" while creating
	Type      string
	Name      string
	Enabled   bool
	Values    map[string]string
	SecretSet map[string]bool
	// Errors maps a field name to its message; "form" holds a failure not
	// tied to one field.
	Errors map[string]string
}

// Editing reports whether the form edits an existing channel.
func (f ChannelForm) Editing() bool { return f.ID != "" }

// Action is where the form posts.
func (f ChannelForm) Action() string {
	if f.Editing() {
		return "/notifications/channels/" + f.ID
	}
	return "/notifications/channels"
}

// summary lists the errors in page order.
func (f ChannelForm) summary() []summaryItem {
	var out []summaryItem
	if msg, ok := f.Errors["name"]; ok {
		out = append(out, summaryItem{"name", "Name", msg})
	}
	for _, fld := range ChannelFields(f.Type) {
		if msg, ok := f.Errors[fld.Name]; ok {
			out = append(out, summaryItem{fld.Name, fld.Label, msg})
		}
	}
	return out
}

// hintID is the id of a field's hint paragraph, "" when it has none.
func hintID(f ChannelField) string {
	if f.Hint == "" {
		return ""
	}
	return f.Name + "-hint"
}
