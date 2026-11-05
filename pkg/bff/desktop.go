package bff

import (
	"net/http"

	"github.com/tsic404/kos-dashboard/pkg/desktop"
)

// desktop serves the desktop page DASH-C-52 declares: the entries, the
// installed applications, the browser-local recent list and the notification
// centre. Every server item is read through the control-plane API as the
// caller's session (KOSD-C-12), and the front refuses exactly what that API
// refuses.
func (s *Server) desktop(w http.ResponseWriter, r *http.Request) {
	token, ok := s.sessionToken(r)
	if !ok {
		writeFailure(w, http.StatusUnauthorized, "unauthenticated", "会话无效或已过期，请重新登录", "authorize")
		return
	}
	reader := desktop.New(desktop.Config{
		BaseURL: s.upstream,
		Token:   token,
		HTTP:    s.client,
		Now:     s.now,
	})
	page, err := reader.Page(r.Context())
	if err != nil {
		if failure, isFailure := err.(*desktop.Failure); isFailure && len(failure.Body) > 0 {
			// The control plane's own envelope travels unchanged, so the page
			// and the resource route refuse the same read the same way.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(failure.Status)
			_, _ = w.Write(failure.Body)
			return
		}
		writeFailure(w, http.StatusBadGateway, "internal", "桌面数据读取失败："+err.Error(), "read")
		return
	}
	writeJSON(w, http.StatusOK, page)
}
