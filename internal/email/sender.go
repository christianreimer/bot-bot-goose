// Package email is the outbound-mail surface. Sender is a thin interface so
// the magic-link flow stays testable without a third-party provider and the
// production provider can be swapped (Resend → Postmark → SES) by changing
// one env var, not the call sites.
package email

import (
	"context"
	"fmt"
	"log/slog"
)

type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// LogSender writes the message to the logger instead of sending it. Used in
// dev so a developer working without API keys can copy the magic link out
// of stdout and click it. NEVER use in prod — the sender is silently a no-op
// from the user's perspective.
type LogSender struct {
	Log *slog.Logger
}

func (l *LogSender) Send(_ context.Context, msg Message) error {
	if l.Log == nil {
		l.Log = slog.Default()
	}
	l.Log.Info("EMAIL (LogSender — would have sent)", "to", msg.To, "subject", msg.Subject, "text", msg.Text)
	return nil
}

// AssertConfigured panics if a production sender was misconfigured (no API
// key, no from address). Call this at boot — surfacing the error there is
// kinder than failing the first magic-link request.
func AssertConfigured(s Sender) error {
	switch v := s.(type) {
	case *LogSender:
		return nil
	case *ResendSender:
		if v.APIKey == "" {
			return fmt.Errorf("email: resend sender missing api key")
		}
		if v.From == "" {
			return fmt.Errorf("email: resend sender missing from address")
		}
		return nil
	}
	return nil
}
