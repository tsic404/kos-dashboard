package bff

import (
	"net/http"
	"net/url"
	"strings"
)

// The cross-origin face of one window read (DASH-C-75, KOSD-C-87). A preflight
// names the two read methods and the Authorization header; every answer names
// one exact source or none, never a wildcard and never a credential grant.
const (
	// applicationMethods is what a window preflight permits.
	applicationMethods = "GET, HEAD"
	// applicationHeaders is what a window preflight permits.
	applicationHeaders = "Authorization"
)

// applicationToken reads the token a window submitted. The console session
// rides the sealed cookie and never Authorization, so a bearer here belongs to
// an application (KOSD-C-86).
func applicationToken(r *http.Request) (string, bool) {
	scheme, value, found := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	value = strings.TrimSpace(value)
	return value, value != ""
}

// requestOrigin reads the exact source one browser request came from. A missing
// or opaque origin names none, so it gains no permission (KOSD-C-87).
func requestOrigin(r *http.Request) string {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" || !isOrigin(origin) {
		return ""
	}
	return origin
}

// sameOrigins reports whether two origins name the same source: scheme and host
// compared case-insensitively, with no other component in play.
func sameOrigins(left, right string) bool {
	parsed, err := url.Parse(left)
	if err != nil || parsed.Host == "" {
		return false
	}
	other, err := url.Parse(right)
	if err != nil || other.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Scheme, other.Scheme) && strings.EqualFold(parsed.Host, other.Host)
}

// activeOrigin reports whether one origin is the declared entry origin of an
// application whose window this host holds (KOSD-C-87).
func (s *Server) activeOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	for _, declared := range s.host.Origins() {
		if sameOrigins(declared, origin) {
			return true
		}
	}
	return false
}

// permittedOrigin reports whether one answer may name the request's source. A
// token the host resolves is pinned to the application it belongs to; a token it
// no longer holds keeps the refusal readable for the applications it still runs
// (KOSD-C-87).
func (s *Server) permittedOrigin(token, origin string) bool {
	if origin == "" {
		return false
	}
	if declared, found := s.host.OriginOf(token); found {
		return sameOrigins(declared, origin)
	}
	return s.activeOrigin(origin)
}

// preflight answers one cross-origin preflight of an application read. It needs
// neither a console cookie nor a token, and it authorises no actual request
// (KOSD-C-87).
func (s *Server) preflight(w http.ResponseWriter, r *http.Request) {
	origin := requestOrigin(r)
	header := w.Header()
	header.Add("Vary", "Origin")
	if !s.activeOrigin(origin) {
		writeFailure(w, http.StatusForbidden, "permission_denied", "请求来源不是已激活应用的入口 origin", "authorize")
		return
	}
	header.Set("Access-Control-Allow-Origin", origin)
	header.Set("Access-Control-Allow-Methods", applicationMethods)
	header.Set("Access-Control-Allow-Headers", applicationHeaders)
	w.WriteHeader(http.StatusNoContent)
}

// allowOrigin names the one source permitted to read an answer. The headers a
// control plane sent for itself never reach the browser, so the foreign answer
// can neither widen the source nor grant credentials (KOSD-C-87).
func allowOrigin(header http.Header, origin string) {
	for name := range header {
		if strings.HasPrefix(strings.ToLower(name), "access-control-") {
			header.Del(name)
		}
	}
	header.Add("Vary", "Origin")
	if origin != "" {
		header.Set("Access-Control-Allow-Origin", origin)
	}
}
