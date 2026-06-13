package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
)

// emitJSON writes v as pretty JSON to stdout. Adds a trailing newline.
func emitJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// errorEnvelope is the structured shape we write to stderr on failure. The
// agent looks for {error, code, details}.
type errorEnvelope struct {
	Error   string `json:"error"`
	Code    string `json:"code"`
	Details any    `json:"details,omitempty"`
}

// emitError writes an error envelope to stderr (NOT stdout, so JSON consumers
// piping stdout don't see error garbage) and returns a sentinel error that
// causes the wrapping mustRun to exit with a non-zero code.
func emitError(code, msg string, details any) error {
	env := errorEnvelope{Error: redactURL(msg), Code: code, Details: details}
	enc := json.NewEncoder(os.Stderr)
	enc.SetIndent("", "  ")
	_ = enc.Encode(env)
	return errorEmitted
}

// errorEmitted signals "we already wrote the error envelope; just exit". The
// outer mustRun checks for this and skips re-printing.
var errorEmitted = fmt.Errorf("error already emitted to stderr")

// table writes columnar output to stdout via tabwriter. headers is the first
// row; rows is the rest. Every cell is converted with fmt.Sprint.
func emitTable(headers []string, rows [][]any) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(headers, "\t"))
	for _, r := range rows {
		cells := make([]string, len(r))
		for i, c := range r {
			cells[i] = fmt.Sprint(c)
		}
		fmt.Fprintln(w, strings.Join(cells, "\t"))
	}
	return w.Flush()
}

// emitOK is the standard "operation succeeded" stdout shape. Useful for
// non-list mutators (create/edit/delete/review) so the agent gets a stable
// confirmation envelope it can parse.
func emitOK(action string, payload map[string]any) error {
	out := map[string]any{"ok": true, "action": action}
	for k, v := range payload {
		out[k] = v
	}
	return emitJSON(out)
}

// redactURL strips userinfo (user:password@) from any postgres:// URL found
// inside s, so passwords don't leak into logs or error messages.
func redactURL(s string) string {
	out := s
	// Look for protocol prefixes and substring-redact each occurrence.
	for _, prefix := range []string{"postgres://", "postgresql://"} {
		for {
			idx := strings.Index(out, prefix)
			if idx < 0 {
				break
			}
			// Find the end of the URL (whitespace, quote, or end-of-string).
			end := len(out)
			for i := idx + len(prefix); i < len(out); i++ {
				ch := out[i]
				if ch == ' ' || ch == '\t' || ch == '\n' || ch == '"' || ch == '\'' {
					end = i
					break
				}
			}
			candidate := out[idx:end]
			u, err := url.Parse(candidate)
			if err != nil || u.User == nil {
				// Skip past this prefix to avoid infinite loop.
				out = out[:idx] + strings.Replace(out[idx:], prefix, "REDACTED_SCHEME_", 1)
				continue
			}
			u.User = url.User(u.User.Username())
			out = out[:idx] + u.String() + out[end:]
		}
		out = strings.ReplaceAll(out, "REDACTED_SCHEME_", prefix)
	}
	return out
}

// safeIOWriter wraps a writer to redact URLs in everything written through
// it. Used to filter slog output.
type safeIOWriter struct{ w io.Writer }

func (s safeIOWriter) Write(p []byte) (int, error) {
	red := redactURL(string(p))
	if _, err := s.w.Write([]byte(red)); err != nil {
		return 0, err
	}
	return len(p), nil
}
