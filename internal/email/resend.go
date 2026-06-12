package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ResendSender posts to https://resend.com's REST API. We bypass the official
// SDK to keep deps minimal — the API surface we use is one POST.
type ResendSender struct {
	APIKey string
	From   string // e.g. "Bot Bot Goose <noreply@botbotgoose.app>"
	Client *http.Client
}

func (r *ResendSender) Send(ctx context.Context, msg Message) error {
	if r.Client == nil {
		r.Client = &http.Client{Timeout: 15 * time.Second}
	}
	body := map[string]any{
		"from":    r.From,
		"to":      []string{msg.To},
		"subject": msg.Subject,
		"text":    msg.Text,
	}
	if msg.HTML != "" {
		body["html"] = msg.HTML
	}
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(buf))
	req.Header.Set("Authorization", "Bearer "+r.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.Client.Do(req)
	if err != nil {
		return fmt.Errorf("resend send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("resend %d: %s", resp.StatusCode, string(raw))
	}
	return nil
}
