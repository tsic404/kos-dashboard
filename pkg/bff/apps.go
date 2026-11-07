package bff

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
)

// The routes the application host serves (DASH-C-30, KOSD-C-73, KOSD-C-75).
// The list is the launcher and dock source; the window routes carry the handshake
// and the token; the permission routes are the account's settings surface for a
// capability.
const (
	appsPath           = "/api/v1/apps"
	appPath            = "/api/v1/apps/{id}"
	appWindowsPath     = "/api/v1/apps/{id}/windows"
	windowsPath        = "/api/v1/windows"
	windowPath         = "/api/v1/windows/{window}"
	windowHandshake    = "/api/v1/windows/{window}/handshake"
	capabilityUsePath  = "/api/v1/windows/{window}/capabilities/{permission}"
	appPermissionsPath = "/api/v1/apps/{id}/permissions"
	appPermissionPath  = "/api/v1/apps/{id}/permissions/{permission}"
	deepLinksPath      = "/api/v1/deep-links"
	// deepLinkQuery names the path to resolve.
	deepLinkQuery = "path"
)

// isOrigin keeps an origin to scheme and host, so a path, query or wildcard in
// the reported origin cannot widen the check (KOSD-C-75).
func isOrigin(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return false
	}
	return parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

// applications is the launcher and dock source. Both surfaces read one list: an
// installed application appears on both with the name, icon and window defaults
// its manifest declares, and one that is absent or invalid appears on neither
// (DASH-C-30).
func (s *Server) applications(w http.ResponseWriter, r *http.Request) {
	token, ok := s.sessionOrRefuse(w, r)
	if !ok {
		return
	}
	registry, ok := s.appsOrRefuse(w, r, token)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": registry.Apps(),
		// The refusals stay observable without surfacing an entry: a manifest
		// that does not satisfy KOSD-C-73 makes its component not ready.
		"notReady": registry.Refusals(),
	})
}

// application reads one loaded application.
func (s *Server) application(w http.ResponseWriter, r *http.Request) {
	token, ok := s.sessionOrRefuse(w, r)
	if !ok {
		return
	}
	registry, ok := s.appsOrRefuse(w, r, token)
	if !ok {
		return
	}
	entry, found := registry.App(r.PathValue("id"))
	if !found {
		writeFailure(w, http.StatusNotFound, "not_found", "应用未安装或声明无效", "authorize")
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// openWindow registers one window instance and answers the geometry the manifest
// defaults to, the sandbox the frame is created with and the signed frame
// address. The token is not part of this answer: it is delivered once, in the
// handshake (KOSD-C-75).
func (s *Server) openWindow(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.csrfOrRefuse(w, r); !ok {
		return
	}
	registry, account, ok := s.appsAndAccount(w, r)
	if !ok {
		return
	}
	window, isNew, err := s.host.Open(registry, account, r.PathValue("id"))
	if err != nil {
		writeFailure(w, http.StatusConflict, "failed_precondition", "应用未安装或已进入错误态，无法打开", "authorize")
		return
	}
	answer := map[string]any{"window": window, "isNew": isNew, "sandbox": apphost.FrameSandbox}
	if entry, found := registry.App(window.AppID); found {
		if frame, reason := s.frameAddress(entry); frame != "" {
			answer["frame"] = frame
		} else if reason != "" {
			answer["frameError"] = reason
		}
	}
	writeJSON(w, http.StatusAccepted, answer)
}

// listWindows reports the account's window instances with the state each is in,
// so the shell can show an uninstalled application's window in its error state
// (DASH-C-30).
func (s *Server) listWindows(w http.ResponseWriter, r *http.Request) {
	_, account, ok := s.appsAndAccount(w, r)
	if !ok {
		return
	}
	windows := s.host.Windows(account)
	writeJSON(w, http.StatusOK, map[string]any{"items": windows})
}

// handshake runs the console side of the `ready` handshake: it validates the
// origin, the window nonce and the message shape, then answers the `init` payload
// with the token exactly once (KOSD-C-75).
func (s *Server) handshake(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.csrfOrRefuse(w, r); !ok {
		return
	}
	var ready struct {
		Origin  string `json:"origin"`
		Nonce   string `json:"nonce"`
		Message []byte `json:"message"`
	}
	body, err := readBody(r)
	if err != nil {
		writeFailure(w, http.StatusBadRequest, "invalid_request", "握手请求体无法读取", "validate")
		return
	}
	if err := decodeJSON(body, &ready); err != nil {
		writeFailure(w, http.StatusBadRequest, "invalid_request", "握手请求体不是合法 JSON", "validate")
		return
	}
	if !isOrigin(ready.Origin) {
		// A wildcard or a path-bearing origin never identifies a frame.
		writeFailure(w, http.StatusBadRequest, "invalid_request", "握手来源不是合法 origin", "validate")
		return
	}
	registry, account, ok := s.appsAndAccount(w, r)
	if !ok {
		return
	}
	init, err := s.host.Handshake(registry, account, r.PathValue("window"), ready.Origin, ready.Nonce, ready.Message)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, init)
	case errors.Is(err, apphost.ErrOriginMismatch), errors.Is(err, apphost.ErrNonceMismatch),
		errors.Is(err, apphost.ErrHandshakeOnce), errors.Is(err, apphost.ErrWindowUnknown):
		// A message the console cannot attribute to this frame is ignored: it
		// yields no token and no state change (KOSD-C-75).
		writeFailure(w, http.StatusForbidden, "permission_denied", err.Error(), "authorize")
	default:
		writeFailure(w, http.StatusBadRequest, "invalid_request", err.Error(), "validate")
	}
}

// closeWindow closes one instance and revokes its token (KOSD-C-75).
func (s *Server) closeWindow(w http.ResponseWriter, r *http.Request) {
	token, ok := s.sessionOrRefuse(w, r)
	if !ok {
		return
	}
	if !s.csrfOK(r) {
		writeFailure(w, http.StatusForbidden, "permission_denied", "缺少或错误的 CSRF 令牌", "authorize")
		return
	}
	account, ok := s.accountOrRefuse(w, r, token)
	if !ok {
		return
	}
	if err := s.host.Close(account, r.PathValue("window")); err != nil {
		writeFailure(w, http.StatusNotFound, "not_found", err.Error(), "authorize")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listPermissions reports the capabilities one application declares and the
// account's state for each. A capability outside the fixed set is never listed.
func (s *Server) listPermissions(w http.ResponseWriter, r *http.Request) {
	registry, account, ok := s.appsAndAccount(w, r)
	if !ok {
		return
	}
	entry, found := registry.App(r.PathValue("id"))
	if !found {
		writeFailure(w, http.StatusNotFound, "not_found", "应用未安装或声明无效", "authorize")
		return
	}
	items := make([]map[string]any, 0, len(entry.Permissions))
	for _, permission := range entry.Permissions {
		items = append(items, map[string]any{
			"name":  permission,
			"state": s.host.PermissionState(account, entry.ID, permission),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// grantPermission records the user's confirmation of one capability and audits
// it (KOSD-C-75, KOSD-C-14).
func (s *Server) grantPermission(w http.ResponseWriter, r *http.Request) {
	s.decidePermission(w, r, true)
}

// revokePermission withdraws one capability; the application's open windows lose
// it immediately (KOSD-C-75).
func (s *Server) revokePermission(w http.ResponseWriter, r *http.Request) {
	s.decidePermission(w, r, false)
}

// decidePermission applies one grant or revocation.
func (s *Server) decidePermission(w http.ResponseWriter, r *http.Request, isGrant bool) {
	registry, account, ok := s.appsAndAccount(w, r)
	if !ok {
		return
	}
	if !s.csrfOK(r) {
		writeFailure(w, http.StatusForbidden, "permission_denied", "缺少或错误的 CSRF 令牌", "authorize")
		return
	}
	appID, permission := r.PathValue("id"), r.PathValue("permission")
	var err error
	if isGrant {
		err = s.host.Grant(r.Context(), registry, account, appID, permission)
	} else {
		err = s.host.Revoke(r.Context(), registry, account, appID, permission)
	}
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, apphost.ErrPermissionUnknown), errors.Is(err, apphost.ErrPermissionUndeclared):
		writeFailure(w, http.StatusBadRequest, "invalid_request", err.Error(), "validate")
	case errors.Is(err, apphost.ErrAppUnavailable):
		writeFailure(w, http.StatusNotFound, "not_found", err.Error(), "authorize")
	default:
		writeFailure(w, http.StatusInternalServerError, "internal", err.Error(), "write")
	}
}

// useCapability judges one capability use of a window and audits the attempt
// (KOSD-C-75, KOSD-C-14). A capability the account did not confirm answers
// consent_required, so the shell asks before the application sees it.
func (s *Server) useCapability(w http.ResponseWriter, r *http.Request) {
	registry, account, ok := s.appsAndAccount(w, r)
	if !ok {
		return
	}
	if !s.csrfOK(r) {
		writeFailure(w, http.StatusForbidden, "permission_denied", "缺少或错误的 CSRF 令牌", "authorize")
		return
	}
	_, err := s.host.Use(r.Context(), registry, account, r.PathValue("window"), r.PathValue("permission"))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, apphost.ErrConsentRequired):
		writeFailure(w, http.StatusForbidden, "consent_required", "该能力尚未由用户确认", "authorize")
	case errors.Is(err, apphost.ErrTokenInvalid), errors.Is(err, apphost.ErrAppUnavailable):
		// A revoked or expired window has no usable capability left.
		writeFailure(w, http.StatusForbidden, "permission_denied", "窗口的令牌已失效，请重新打开", "authorize")
	default:
		writeFailure(w, http.StatusBadRequest, "invalid_request", err.Error(), "validate")
	}
}

// resolveDeepLink answers the application a path belongs to. A path no installed
// application declared resolves to nothing and creates no window (DASH-C-30).
func (s *Server) resolveDeepLink(w http.ResponseWriter, r *http.Request) {
	token, ok := s.sessionOrRefuse(w, r)
	if !ok {
		return
	}
	registry, ok := s.appsOrRefuse(w, r, token)
	if !ok {
		return
	}
	entry, err := registry.ResolveDeepLink(r.URL.Query().Get(deepLinkQuery))
	if err != nil {
		writeFailure(w, http.StatusNotFound, "not_found", "没有已安装应用声明这个深链", "authorize")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"app": entry.ID})
}

// sessionOrRefuse reads the caller's session or answers the refusal every
// application route gives an ended session.
func (s *Server) sessionOrRefuse(w http.ResponseWriter, r *http.Request) (string, bool) {
	token, ok := s.sessionToken(r)
	if !ok {
		writeFailure(w, http.StatusUnauthorized, "unauthenticated", "会话无效或已过期，请重新登录", "authorize")
		return "", false
	}
	return token, true
}

// csrfOrRefuse judges the double-submit token of a state-changing route.
func (s *Server) csrfOrRefuse(w http.ResponseWriter, r *http.Request) (string, bool) {
	token, ok := s.sessionOrRefuse(w, r)
	if !ok {
		return "", false
	}
	if !s.csrfOK(r) {
		writeFailure(w, http.StatusForbidden, "permission_denied", "缺少或错误的 CSRF 令牌", "authorize")
		return "", false
	}
	return token, true
}

// appsOrRefuse loads the installed applications as the caller's session.
func (s *Server) appsOrRefuse(w http.ResponseWriter, r *http.Request, token string) (*apphost.Registry, bool) {
	registry, err := s.registryFor(r.Context(), token)
	if err != nil {
		writeFailure(w, http.StatusBadGateway, "internal", "应用清单读取失败："+err.Error(), "read")
		return nil, false
	}
	return registry, true
}

// accountOrRefuse resolves the account the app host scopes its state to.
func (s *Server) accountOrRefuse(w http.ResponseWriter, r *http.Request, token string) (string, bool) {
	account, err := s.accountOf(r.Context(), token)
	if err != nil {
		writeFailure(w, http.StatusUnauthorized, "unauthenticated", "会话无效或已过期，请重新登录", "authorize")
		return "", false
	}
	return account, true
}

// appsAndAccount resolves the registry and the account of one request, then
// settles that account's windows against the registry: a window whose application
// this session can no longer see enters its error state (DASH-C-30).
func (s *Server) appsAndAccount(w http.ResponseWriter, r *http.Request) (*apphost.Registry, string, bool) {
	token, ok := s.sessionOrRefuse(w, r)
	if !ok {
		return nil, "", false
	}
	registry, ok := s.appsOrRefuse(w, r, token)
	if !ok {
		return nil, "", false
	}
	account, ok := s.accountOrRefuse(w, r, token)
	if !ok {
		return nil, "", false
	}
	s.host.Apply(registry, account)
	return registry, account, true
}

// registryFor loads the installed applications as the caller's session.
func (s *Server) registryFor(ctx context.Context, token string) (*apphost.Registry, error) {
	source := s.apps
	if source == nil {
		source = &apphost.ControlPlaneSource{BaseURL: s.upstream, Token: token, HTTP: s.client}
	}
	return apphost.LoadFrom(ctx, source, s.validation)
}

// frameAddress renders the signed frame address of one application. A manifest
// without a package digest has no verifiable asset address, so the shell gets the
// reason instead of an unverifiable URL (KOSD-C-73).
func (s *Server) frameAddress(entry apphost.Entry) (string, string) {
	if entry.Digest == "" {
		return "", "应用未声明包摘要，无法签发可校验的资产地址"
	}
	origin := entry.Origin()
	if origin == "" {
		return "", "应用入口不是绝对地址"
	}
	assetPath := "/"
	if parsed, err := url.Parse(entry.Entry); err == nil && parsed.Path != "" {
		assetPath = parsed.Path
	}
	// The address names the extension, version and digest the control plane
	// verified, so it can never be pointed at other bytes (KOSD-C-73).
	address := assetPathPrefix + entry.Extension + "/" + entry.Version + "/" + entry.Digest +
		"/" + strings.TrimPrefix(assetPath, "/")
	asset := apphost.Asset{
		Extension: entry.Extension, AppID: entry.ID, Version: entry.Version,
		Digest: entry.Digest, Path: address,
	}
	signed, err := apphost.SignedURL(s.sealKey, origin, asset, s.now().Add(s.assetTTL).UTC())
	if err != nil {
		return "", err.Error()
	}
	return signed, ""
}

// accountOf resolves the signed-in account from the control plane's own session
// read, so the app host scopes windows and grants to the account the control plane
// vouches for (KOSD-C-63).
func (s *Server) accountOf(ctx context.Context, token string) (string, error) {
	status, _, payload, err := s.callWithToken(ctx, http.MethodGet, "/api/v1/auth/session", nil, token)
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", errors.New("session read refused")
	}
	var answer struct {
		Username string `json:"username"`
	}
	if err := decodeJSON(payload, &answer); err != nil || answer.Username == "" {
		return "", errors.New("session read carries no account")
	}
	return answer.Username, nil
}
