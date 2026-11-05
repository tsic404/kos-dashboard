package bff

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/tsic404/kos-dashboard/pkg/desktop"
)

// stream is the event stream the shell listens on (KOSD-C-13). The control
// plane reads events at request time, so the front repeats that read and emits
// only what the session did not receive yet. When the read fails the stream
// keeps the last observation and marks it stale instead of claiming there are
// no events (KOSD-C-64).
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	token, ok := s.sessionToken(r)
	if !ok {
		writeFailure(w, http.StatusUnauthorized, "unauthenticated", "会话无效或已过期，请重新登录", "authorize")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeFailure(w, http.StatusInternalServerError, "internal", "事件流不可用", "read")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	client := desktop.New(desktop.Config{
		BaseURL: s.upstream,
		Token:   token,
		HTTP:    s.client,
		Now:     s.now,
	})
	// seen holds the identities of the previous read, not of the whole
	// connection: each round rebuilds it, so a long-lived stream holds one
	// event window instead of growing with its uptime.
	seen := map[string]bool{}
	var lastObserved time.Time
	isFirst := true
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	for {
		read, err := client.Events(r.Context())
		switch {
		case err != nil:
			if !lastObserved.IsZero() {
				s.emit(w, flusher, "stale", map[string]any{
					"observedAt": lastObserved.UTC(),
					"stale":      s.now().Sub(lastObserved) > desktop.StaleThreshold,
					"reason":     "控制面事件读取失败：" + err.Error(),
				})
			} else {
				// Nothing was observed yet, so the stream says why it is empty
				// instead of staying silent (KOSD-C-64).
				s.emit(w, flusher, "stale", map[string]any{
					"observedAt": nil,
					"stale":      true,
					"reason":     "控制面事件读取失败：" + err.Error(),
				})
			}
		default:
			current := identitiesOf(read.Events)
			if isFirst || hasNew(current, seen) {
				isFirst = false
				s.emit(w, flusher, "events", read)
			}
			lastObserved = read.ObservedAt
			seen = current
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// emit writes one server-sent event.
func (s *Server) emit(w http.ResponseWriter, flusher http.Flusher, name string, body any) {
	payload, err := json.Marshal(body)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, payload)
	flusher.Flush()
}

// hasNew reports whether the read carries an event the previous read did not.
// Identity is the event's object reference, its last-seen time and its count,
// so a repeated occurrence of the same event still reaches the shell.
func hasNew(current, previous map[string]bool) bool {
	for identity := range current {
		if !previous[identity] {
			return true
		}
	}
	return false
}

// identitiesOf names every event in one read. The set is bounded by the read,
// so comparing two of them never grows with the connection's lifetime.
func identitiesOf(events []desktop.Event) map[string]bool {
	identities := make(map[string]bool, len(events))
	for _, event := range events {
		identities[event.Identity()] = true
	}
	return identities
}
