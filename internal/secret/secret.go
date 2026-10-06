// Package secret provides a string type that never prints its value.
package secret

import "log/slog"

const redacted = "[REDACTED]"

// String holds a sensitive value. Every formatting, logging and encoding path
// yields "[REDACTED]"; the value is only available through Reveal.
type String string

func (String) String() string   { return redacted }
func (String) GoString() string { return `"` + redacted + `"` }

// LogValue implements slog.LogValuer.
func (String) LogValue() slog.Value { return slog.StringValue(redacted) }

func (String) MarshalText() ([]byte, error) { return []byte(redacted), nil }
func (String) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// Reveal returns the real value. Call it only where the value is deliberately used.
func (s String) Reveal() string { return string(s) }
