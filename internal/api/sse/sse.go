// Package sse writes Server-Sent Events with ids so clients can resume
// with Last-Event-ID.
package sse

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Writer streams events to one client.
type Writer struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

// New prepares the response for SSE.
func New(w http.ResponseWriter) *Writer {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	sw := &Writer{w: w, rc: http.NewResponseController(w)}
	_ = sw.rc.SetWriteDeadline(time.Time{})
	sw.flush()
	return sw
}

func (s *Writer) flush() { _ = s.rc.Flush() }

// Event writes one event; data is JSON-encoded unless it is a string.
func (s *Writer) Event(id, event string, data any) error {
	var b strings.Builder
	if id != "" {
		fmt.Fprintf(&b, "id: %s\n", id)
	}
	if event != "" {
		fmt.Fprintf(&b, "event: %s\n", event)
	}
	var payload string
	switch v := data.(type) {
	case string:
		payload = v
	case []byte:
		payload = string(v)
	default:
		j, err := json.Marshal(v)
		if err != nil {
			return err
		}
		payload = string(j)
	}
	for _, line := range strings.Split(payload, "\n") {
		fmt.Fprintf(&b, "data: %s\n", line)
	}
	b.WriteString("\n")
	if _, err := s.w.Write([]byte(b.String())); err != nil {
		return err
	}
	s.flush()
	return nil
}

// Comment writes a keepalive comment.
func (s *Writer) Comment(text string) error {
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return err
	}
	s.flush()
	return nil
}

// LastEventID parses the resume position from the header or ?after=.
func LastEventID(r *http.Request) int64 {
	v := r.Header.Get("Last-Event-ID")
	if v == "" {
		v = r.URL.Query().Get("after")
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}
