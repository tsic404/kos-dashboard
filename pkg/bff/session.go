package bff

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"
)

const (
	// sessionCookie carries the sealed control-plane session. It is HttpOnly:
	// no page script ever sees the bearer token inside it (KOSD-F-9).
	sessionCookie = "kos_bff_session"
	// csrfCookie is the double-submit token page scripts echo in the header.
	csrfCookie = "kos_bff_csrf"
	// csrfHeader is the header a state-changing request must repeat.
	csrfHeader = "X-CSRF-Token"
	// csrfTokenBytes is the entropy of one CSRF token.
	csrfTokenBytes = 32
)

// sealer seals the control-plane session into the cookie with AES-GCM, so the
// browser holds one opaque value and the process holds no session table.
type sealer struct {
	aead cipher.AEAD
}

// newSealer builds the cookie seal from a 32-byte key.
func newSealer(key []byte) (*sealer, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("bff: build the session cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("bff: build the session seal: %w", err)
	}
	return &sealer{aead: aead}, nil
}

// seal encrypts one token into the cookie value.
func (s *sealer) seal(token string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("bff: draw a session nonce: %w", err)
	}
	sealed := s.aead.Seal(nonce, nonce, []byte(token), nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// open reads the token back; tampering and truncation both fail closed.
func (s *sealer) open(value string) (string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) < s.aead.NonceSize() {
		return "", false
	}
	nonce, sealed := raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():]
	opened, err := s.aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", false
	}
	return string(opened), true
}

// login trades a login name and password for a session (KOSD-C-63). The
// control plane keeps the session; the front keeps its token out of the
// response and inside the sealed cookie.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeFailure(w, http.StatusBadRequest, "invalid_request", "登录请求体无法读取", "validate")
		return
	}
	status, contentType, payload, err := s.call(r.Context(), http.MethodPost, "/api/v1/auth/login", body)
	if err != nil {
		writeFailure(w, http.StatusBadGateway, "internal", "控制面不可达："+err.Error(), "read")
		return
	}
	if status < 200 || status > 299 {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write(payload)
		return
	}
	var answer map[string]any
	if err := decodeJSON(payload, &answer); err != nil {
		writeFailure(w, http.StatusBadGateway, "internal", "控制面的登录响应无法解码", "read")
		return
	}
	token, _ := answer["token"].(string)
	if token == "" {
		writeFailure(w, http.StatusBadGateway, "internal", "控制面的登录响应没有会话令牌", "read")
		return
	}
	delete(answer, "token")
	sealed, err := s.sealer.seal(token)
	if err != nil {
		writeFailure(w, http.StatusInternalServerError, "internal", "会话 Cookie 无法写入", "write")
		return
	}
	s.setCookie(w, sessionCookie, sealed, http.SameSiteStrictMode, true)
	csrf, err := s.rotateCSRF(w)
	if err != nil {
		writeFailure(w, http.StatusInternalServerError, "internal", "CSRF 令牌无法生成", "write")
		return
	}
	answer["csrfToken"] = csrf
	writeJSON(w, http.StatusOK, answer)
}

// logout revokes the control-plane session and drops the cookie (KOSD-C-63).
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	token, ok := s.sessionToken(r)
	if !ok {
		s.clearSession(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// The account is resolved before the session ends, because the control
	// plane refuses a read with a revoked token.
	account, accountErr := s.accountOf(r.Context(), token)
	status, contentType, payload, err := s.callWithToken(r.Context(), http.MethodPost, "/api/v1/auth/logout", nil, token)
	if err != nil {
		writeFailure(w, http.StatusBadGateway, "internal", "控制面不可达："+err.Error(), "read")
		return
	}
	if status >= 300 {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write(payload)
		return
	}
	// Signing out ends every application window and token the account holds
	// (KOSD-C-75).
	if accountErr == nil {
		s.host.RevokeAccount(account)
	}
	s.clearSession(w)
	w.WriteHeader(http.StatusNoContent)
}

// sessionToken reads the control-plane token out of the sealed cookie. The
// control plane remains the authority: an expired or revoked token fails there.
func (s *Server) sessionToken(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return "", false
	}
	return s.sealer.open(cookie.Value)
}

// setCookie writes one cookie with the attributes this front fixes.
func (s *Server) setCookie(w http.ResponseWriter, name, value string, sameSite http.SameSite, isHTTPOnly bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: isHTTPOnly,
		Secure:   s.secure,
		SameSite: sameSite,
		MaxAge:   int((12 * time.Hour).Seconds()),
	})
}

// clearSession drops the session cookie.
func (s *Server) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// rotateCSRF writes a fresh double-submit token and returns it.
func (s *Server) rotateCSRF(w http.ResponseWriter) (string, error) {
	raw := make([]byte, csrfTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	s.setCookie(w, csrfCookie, token, http.SameSiteStrictMode, false)
	return token, nil
}

// ensureCSRF writes a double-submit token when the browser holds none, so the
// first state-changing request after a reload already has one.
func (s *Server) ensureCSRF(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(csrfCookie); err == nil && cookie.Value != "" {
		return
	}
	_, _ = s.rotateCSRF(w)
}

// csrfOK judges a state-changing request: the header must repeat the cookie.
// Same-origin enforcement is the browser's job; this check is what keeps a
// cross-origin form post from riding the session cookie.
func (s *Server) csrfOK(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	return r.Header.Get(csrfHeader) == cookie.Value
}
