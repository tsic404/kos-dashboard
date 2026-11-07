package bff

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
)

// assetPathPrefix serves an installed application's UI assets. The address names
// the extension, version, package digest and asset path, and carries the
// short-lived signature the console issued when it opened the window
// (KOSD-C-73).
const assetPathPrefix = "/app-assets/"

// appAsset serves one UI asset of an installed application. It runs on the
// application origin the deployment mapped here, never on the console origin, so
// a frame stays cross-origin to the console and cannot reach its cookie or DOM
// (KOSD-F-18, KOSD-C-75).
func (s *Server) appAsset(w http.ResponseWriter, r *http.Request) {
	if s.assetApps == nil || s.assetFiles == nil {
		writeFailure(w, http.StatusNotFound, "not_found", "应用资产未在此进程分发", "authorize")
		return
	}
	if s.isConsoleOrigin(r) {
		// Serving application bytes on the console origin would put the frame in
		// the console's own origin, which is what KOSD-F-18 forbids.
		writeFailure(w, http.StatusNotFound, "not_found", "应用资产不在控制台 origin 上分发", "authorize")
		return
	}
	extension, version, digest, assetPath, ok := splitAssetAddress(r.URL.Path)
	if !ok {
		writeFailure(w, http.StatusNotFound, "not_found", "资产地址不是版本、摘要与路径的形式", "authorize")
		return
	}
	registry, err := apphost.LoadFrom(r.Context(), s.assetApps, s.validation)
	if err != nil {
		writeFailure(w, http.StatusBadGateway, "internal", "应用清单读取失败："+err.Error(), "read")
		return
	}
	asset, found := installedAsset(registry, extension, version, digest, assetPath)
	if !found {
		writeFailure(w, http.StatusNotFound, "not_found", "没有已安装版本提供该资产", "authorize")
		return
	}
	expires, err := apphost.ParseExpiry(r.URL.Query().Get("kos-exp"))
	if err != nil || apphost.VerifyAsset(s.sealKey, asset, expires, r.URL.Query().Get("kos-sig"), s.now()) != nil {
		// An address whose signature does not verify is not served, and the frame
		// shows its load error instead of a downgraded asset (KOSD-C-73).
		writeFailure(w, http.StatusForbidden, "permission_denied", "资产地址签名无效或已过期", "authorize")
		return
	}
	content, err := s.assetFiles.Read(r.Context(), extension, version, assetPath)
	switch {
	case errors.Is(err, apphost.ErrAssetUnknown), errors.Is(err, apphost.ErrAssetUnaddressed):
		writeFailure(w, http.StatusNotFound, "not_found", "该版本没有这个资产", "authorize")
		return
	case err != nil:
		writeFailure(w, http.StatusBadGateway, "internal", "资产读取失败："+err.Error(), "read")
		return
	}
	header := w.Header()
	header.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors "+s.frameAncestor())
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	// The address names the version and digest, so its bytes never change.
	header.Set("Cache-Control", "public, max-age=31536000, immutable")
	header.Set("Content-Type", contentTypeOf(assetPath))
	_, _ = w.Write(content)
}

// splitAssetAddress reads the extension, version, digest and asset path off one
// address. Every segment is required, so a path naming no version is not served.
func splitAssetAddress(requestPath string) (extension, version, digest, assetPath string, ok bool) {
	remainder, found := strings.CutPrefix(requestPath, assetPathPrefix)
	if !found {
		return "", "", "", "", false
	}
	parts := strings.SplitN(remainder, "/", 4)
	if len(parts) != 4 {
		return "", "", "", "", false
	}
	for _, part := range parts[:3] {
		if part == "" {
			return "", "", "", "", false
		}
	}
	unescaped, err := url.PathUnescape(parts[3])
	if err != nil || unescaped == "" {
		return "", "", "", "", false
	}
	return parts[0], parts[1], parts[2], unescaped, true
}

// installedAsset resolves one asset address against the installed applications.
// The extension, version and digest must be the installed triple: an address
// naming another version, or another digest of the same version, serves nothing
// (KOSD-C-73).
func installedAsset(registry *apphost.Registry, extension, version, digest, assetPath string) (apphost.Asset, bool) {
	for _, entry := range registry.Apps() {
		if entry.Extension != extension || entry.Version != version || entry.Digest != digest {
			continue
		}
		return apphost.Asset{
			Extension: extension, AppID: entry.ID, Version: version, Digest: digest,
			Path: assetPathPrefix + extension + "/" + version + "/" + digest + "/" + assetPath,
		}, true
	}
	return apphost.Asset{}, false
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
	switch {
	case strings.HasSuffix(assetPath, ".html"), strings.HasSuffix(assetPath, ".htm"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(assetPath, ".js"), strings.HasSuffix(assetPath, ".mjs"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(assetPath, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(assetPath, ".json"):
		return "application/json"
	case strings.HasSuffix(assetPath, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(assetPath, ".png"):
		return "image/png"
	case strings.HasSuffix(assetPath, ".woff2"):
		return "font/woff2"
	default:
		return "application/octet-stream"
	}
}
