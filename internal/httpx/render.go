package httpx

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// renderHTML executes the named page template against its own private clone
// of the layout chain. Each page has its own template set so define-block
// shadowing across pages can't happen.
func (s *Server) renderHTML(w http.ResponseWriter, status int, name string, data any) {
	page, ok := s.templates[name]
	if !ok {
		s.cfg.Logger.Error("template missing", "name", name)
		http.Error(w, "template missing", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := page.ExecuteTemplate(&buf, name, data); err != nil {
		s.cfg.Logger.Error("template execute", "name", name, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "encode", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeJSONErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"code": code, "error": msg})
}
