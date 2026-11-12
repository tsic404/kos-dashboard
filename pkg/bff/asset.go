package bff

import (
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
)

// assetPathPrefix serves an installed application's UI assets. The address names
// the extension, the application, the version, the package digest and the asset
// path, and carries the short-lived signature the console issued when it opened
// the window (KOSD-C-73).
const assetPathPrefix = apphost.AssetAddressPrefix

// appAsset serves one UI asset of an installed application. It runs on the
// application origin the deployment mapped here, never on the console origin, so
// a frame stays cross-origin to the console and cannot reach its cookie or DOM
// (KOSD-F-18, KOSD-C-75).
func (s *Server) appAsset(w http.ResponseWriter, r *http.Request) {
	if s.assetFiles == nil {
		writeFailure(w, http.StatusNotFound, "not_found", "应用资产未在此进程分发", "authorize")
		return
	}
	if s.isConsoleOrigin(r) {
		// Serving application bytes on the console origin would put the frame in
		// the console's own origin, which is what KOSD-F-18 forbids.
		writeFailure(w, http.StatusNotFound, "not_found", "应用资产不在控制台 origin 上分发", "authorize")
		return
	}
	asset, ok := splitAssetAddress(r.URL.Path)
	if !ok {
		writeFailure(w, http.StatusNotFound, "not_found", "资产地址不是扩展、应用、版本与摘要的形式", "authorize")
		return
	}
	expires, isSignedAddress, err := s.assetExpiry(r, asset)
	if err != nil {
		// An address whose signature does not verify is not served, and the frame
		// shows its load error instead of a downgraded asset (KOSD-C-73).
		writeFailure(w, http.StatusForbidden, "permission_denied", "资产地址签名无效或已过期", "authorize")
		return
	}
	content, err := s.assetFiles.Read(r.Context(), asset)
	switch {
	case errors.Is(err, apphost.ErrAssetUnknown), errors.Is(err, apphost.ErrAssetUnaddressed):
		writeFailure(w, http.StatusNotFound, "not_found", "该版本没有这个资产", "authorize")
		return
	case err != nil:
		writeFailure(w, http.StatusBadGateway, "internal", "资产读取失败："+err.Error(), "read")
		return
	}
	if isSignedAddress {
		// A relative reference carries no query of its own, so a served signed
		// document hands its digest scope to the following requests (KOSD-C-73).
		s.setAssetGrant(w, asset, expires)
	}
	header := w.Header()
	header.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors "+s.frameAncestor())
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	// The address names the version and digest, so its bytes never change.
	header.Set("Cache-Control", "public, max-age=31536000, immutable")
	header.Set("Content-Type", contentTypeOf(asset.Path))
	_, _ = w.Write(content)
}

// assetExpiry judges the credential one asset request carries. A request that
// names an expiry or a signature is judged by its address alone, so a tampered
// query never falls back to the document grant. A relative reference carries
// neither and is judged by the grant the entry document left on this origin
// (KOSD-C-73).
func (s *Server) assetExpiry(r *http.Request, asset apphost.Asset) (time.Time, bool, error) {
	query := r.URL.Query()
	if query.Has("kos-exp") || query.Has("kos-sig") {
		expires, err := apphost.ParseExpiry(query.Get("kos-exp"))
		if err != nil {
			return time.Time{}, false, apphost.ErrAssetSignature
		}
		return expires, true, apphost.VerifyAsset(s.sealKey, asset, expires, query.Get("kos-sig"), s.now())
	}
	cookie, err := r.Cookie(apphost.AssetGrantCookie)
	if err != nil || cookie.Value == "" {
		return time.Time{}, false, apphost.ErrAssetSignature
	}
	return time.Time{}, false, apphost.VerifyAssetGrant(s.sealKey, asset, cookie.Value, s.now())
}

// setAssetGrant leaves the short-lived credential a document's relative
// references load with. A relative reference carries no query of its own, so the
// digest scope needs somewhere else to travel; it is host-only, HttpOnly,
// scoped to the asset routes and independent of the console session cookie
// (KOSD-C-73, KOSD-C-75).
func (s *Server) setAssetGrant(w http.ResponseWriter, asset apphost.Asset, expires time.Time) {
	remaining := int(expires.Sub(s.now()) / time.Second)
	if remaining < 1 {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     apphost.AssetGrantCookie,
		Value:    apphost.SignAssetGrant(s.sealKey, asset, expires),
		Path:     apphost.AssetGrantPath,
		MaxAge:   remaining,
		Expires:  expires,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: s.assetGrantSameSite(),
	})
}

// assetGrantSameSite keeps the grant usable inside a framed application. An
// application origin may be another site than the console, which only
// SameSite=None rides; browsers require it with Secure, so a front not behind
// TLS keeps Lax and serves same-site application origins (KOSD-C-73).
func (s *Server) assetGrantSameSite() http.SameSite {
	if s.secure {
		return http.SameSiteNoneMode
	}
	return http.SameSiteLaxMode
}

// splitAssetAddress reads the extension, application, version, digest and asset
// path off one address. Every segment is required, so a path naming no version is
// not served.
func splitAssetAddress(requestPath string) (apphost.Asset, bool) {
	remainder, found := strings.CutPrefix(requestPath, assetPathPrefix)
	if !found {
		return apphost.Asset{}, false
	}
	parts := strings.SplitN(remainder, "/", 5)
	if len(parts) != 5 {
		return apphost.Asset{}, false
	}
	for _, part := range parts[:4] {
		if part == "" {
			return apphost.Asset{}, false
		}
	}
	unescaped, err := url.PathUnescape(parts[4])
	if err != nil || unescaped == "" {
		return apphost.Asset{}, false
	}
	return apphost.Asset{
		Extension: parts[0], AppID: parts[1], Version: parts[2],
		Digest: parts[3], Path: unescaped,
	}, true
}

// isConsoleOrigin reports a request addressed to the console origin.
func (s *Server) isConsoleOrigin(r *http.Request) bool {
	if s.consoleHost == "" {
		return false
	}
	host := r.Host
	if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
		host = forwarded
	}
	return strings.EqualFold(host, s.consoleHost)
}

// frameAncestor names the origin allowed to frame an application document. Only
// the console may, so an application cannot be embedded elsewhere (KOSD-C-75).
func (s *Server) frameAncestor() string {
	if s.validation.ConsoleOrigin != "" {
		return s.validation.ConsoleOrigin
	}
	return "'self'"
}

// contentTypeOf names the media type one asset path is served with.
func contentTypeOf(assetPath string) string {
	switch path.Ext(assetPath) {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".woff2":
		return "font/woff2"
	default:
		return "application/octet-stream"
	}
}
