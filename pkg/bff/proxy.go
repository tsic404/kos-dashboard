package bff

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/tsic404/kos-dashboard/pkg/dock"
)

// hopHeaders are the connection-scoped headers a proxy never forwards.
var hopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// proxy forwards one control-plane request. The upstream answer is copied
// unchanged, status and error envelope included, so a read through the front
// equals the same read made directly (KOSD-C-12).
func (s *Server) proxy(w http.ResponseWriter, r *http.Request) {
	if !isSafeMethod(r.Method) && !s.csrfOK(r) {
		writeFailure(w, http.StatusForbidden, "permission_denied", "缺少或错误的 CSRF 令牌", "authorize")
		return
	}
	token, ok := s.sessionToken(r)
	if !ok {
		writeFailure(w, http.StatusUnauthorized, "unauthenticated", "会话无效或已过期，请重新登录", "authorize")
		return
	}
	body, ok := s.judgePreferenceWrite(w, r)
	if !ok {
		return
	}
	request, err := s.upstreamRequest(r.Context(), r.Method, r.URL.Path, r.URL.RawQuery, body, token)
	if err != nil {
		writeFailure(w, http.StatusBadGateway, "internal", "控制面请求无法构造", "read")
		return
	}
	copyHeaders(request.Header, r.Header)
	request.Header.Del("Cookie")
	request.Header.Del("Authorization")
	request.Header.Set("Authorization", "Bearer "+token)

	response, err := s.client.Do(request)
	if err != nil {
		writeFailure(w, http.StatusBadGateway, "internal", "控制面不可达："+err.Error(), "read")
		return
	}
	defer response.Body.Close()
	if isSafeMethod(r.Method) {
		// A browser that reloaded holds the session cookie but may have lost
		// the double-submit token; a read hands it a fresh one.
		s.ensureCSRF(w, r)
	}
	copyHeaders(w.Header(), response.Header)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

// upstreamRequest builds one call to the control plane.
func (s *Server) upstreamRequest(ctx context.Context, method, path, query string, body io.Reader, token string) (*http.Request, error) {
	target := *s.upstream
	target.Path = singleJoiningSlash(s.upstream.Path, path)
	target.RawQuery = query
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return request, nil
}

// call performs one buffered upstream call and returns its status, content type
// and body. It is the shape the session routes need, where the front rewrites
// the payload.
func (s *Server) call(ctx context.Context, method, path string, body []byte) (int, string, []byte, error) {
	return s.callWithToken(ctx, method, path, body, "")
}

// callWithToken performs one buffered upstream call as one session.
func (s *Server) callWithToken(ctx context.Context, method, path string, body []byte, token string) (int, string, []byte, error) {
	request, err := s.upstreamRequest(ctx, method, path, "", bytes.NewReader(body), token)
	if err != nil {
		return 0, "", nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return 0, "", nil, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes))
	if err != nil {
		return 0, "", nil, err
	}
	return response.StatusCode, response.Header.Get("Content-Type"), payload, nil
}

// copyHeaders copies one header set, leaving out the connection-scoped ones.
func copyHeaders(target, source http.Header) {
	for _, name := range hopHeaders {
		target.Del(name)
	}
	for name, values := range source {
		if isHopHeader(name) {
			continue
		}
		for _, value := range values {
			target.Add(name, value)
		}
	}
}

// isHopHeader reports a connection-scoped header name.
func isHopHeader(name string) bool {
	for _, hop := range hopHeaders {
		if strings.EqualFold(hop, name) {
			return true
		}
	}
	return false
}

// isSafeMethod reports whether a method leaves the control plane unchanged, so
// it needs no CSRF token.
func isSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// singleJoiningSlash joins two URL paths with exactly one slash.
func singleJoiningSlash(left, right string) string {
	leftSlash := strings.HasSuffix(left, "/")
	rightSlash := strings.HasPrefix(right, "/")
	switch {
	case leftSlash && rightSlash:
		return left + right[1:]
	case !leftSlash && !rightSlash:
		return left + "/" + right
	default:
		return left + right
	}
}

// decodeJSON decodes one JSON body.
func decodeJSON(payload []byte, target any) error {
	return json.Unmarshal(payload, target)
}

// judgePreferenceWrite validates a write to this product's own preference
// domains before it reaches the control plane, so a value the desktop cannot
// read back never lands in the account's store (DASH-C-27). A body of another
// route is forwarded untouched.
func (s *Server) judgePreferenceWrite(w http.ResponseWriter, r *http.Request) (io.Reader, bool) {
	if r.Method != http.MethodPut {
		return r.Body, true
	}
	key, matched := productPreferenceKey(r.URL.Path)
	if !matched {
		return r.Body, true
	}
	body, err := readBody(r)
	if err != nil {
		writeFailure(w, http.StatusBadRequest, "invalid_request", "偏好值无法读取", "validate")
		return nil, false
	}
	var request struct {
		Value string `json:"value"`
	}
	if err := decodeJSON(body, &request); err != nil {
		writeFailure(w, http.StatusBadRequest, "invalid_request", "偏好请求体不是合法 JSON", "validate")
		return nil, false
	}
	if err := validateProductPreference(key, request.Value); err != nil {
		writeFailure(w, http.StatusBadRequest, "invalid_request", err.Error(), "validate")
		return nil, false
	}
	return bytes.NewReader(body), true
}

// productPreferenceKey names the key of one write to this product's preference
// domains. A path outside them matches nothing.
func productPreferenceKey(path string) (string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 5 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "preferences" {
		return "", false
	}
	if parts[3] != dock.Identifier {
		return "", false
	}
	return parts[4], true
}

// validateProductPreference judges one value against its key's declaration.
func validateProductPreference(key, value string) error {
	switch key {
	case dock.PreferenceLayout:
		_, err := dock.ParseDockLayout(value)
		return err
	case dock.PreferenceLauncherLayout:
		_, err := dock.ParseLauncherLayout(value)
		return err
	default:
		// Keys this product declares without a value schema carry opaque
		// values; the control plane already refuses undeclared keys.
		return nil
	}
}
