// Package bff serves the browser front of the desktop (DASH-C-22, KOSD-C-12).
//
// The browser holds one same-origin session cookie and never sees the
// control-plane bearer token, a kubeconfig or a service-account token
// (KOSD-F-9). Every control-plane route is proxied unchanged, so a read through
// the BFF answers exactly what the control-plane API answers (KOSD-C-12); the
// desktop page is the one aggregate this front assembles (DASH-C-52), and the
// event stream is the one read it repeats (KOSD-C-13).
package bff

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/tsic404/kosd/pkg/controlplane"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
)

const (
	// maxBodyBytes bounds one request body the front forwards.
	maxBodyBytes = 1 << 20
	// defaultPollInterval is how often the event stream re-reads the control
	// plane when the caller names no interval.
	defaultPollInterval = 5 * time.Second
	// upstreamTimeout bounds one proxied call. The control plane answers reads
	// from its projection, so a longer wait means the control plane is wedged.
	upstreamTimeout = 30 * time.Second
	// sessionKeySize is the AES-256 key size the session seal uses.
	sessionKeySize = 32
)

// Config wires the browser front to its control plane.
type Config struct {
	// Upstream is the control-plane API base URL; required.
	Upstream *url.URL
	// SealKey seals the session cookie; 32 bytes. An empty key is generated at
	// start, which ends every session when the process restarts.
	SealKey []byte
	// Assets carries the browser entry. Nil serves the embedded placeholder.
	Assets fs.FS
	// Now supplies the clock the stale marker compares against; defaults to
	// time.Now.
	Now func() time.Time
	// PollInterval is the event-stream read interval; defaults to five seconds.
	PollInterval time.Duration
	// FrameOrigins lists the application origins a window may frame
	// (KOSD-F-18); installed applications add their own origins at request time.
	FrameOrigins []string
	// ConsoleOrigin is the origin this front is served from; an application
	// entry may never share it (KOSD-F-18).
	ConsoleOrigin string
	// AllowInsecureEntries admits plain-HTTP application entries on a loopback
	// host, which is how a local deployment serves an application; any other
	// entry must be HTTPS (KOSD-C-73).
	AllowInsecureEntries bool
	// Apps is the installed `App` contribution source. Nil reads the control
	// plane's application registry as the caller's session.
	Apps apphost.Source
	// AppHost is the application session state. Nil builds one over the
	// registry.
	AppHost *apphost.Host
	// Auditor records capability grants, revocations and uses (KOSD-C-14); nil
	// records nothing.
	Auditor controlplane.Auditor

	// AssetFiles holds the UI assets this process serves.
	AssetFiles apphost.AssetStore
	// AssetTTL bounds one signed asset address; defaults to
	// apphost.DefaultAssetTTL.
	AssetTTL time.Duration
	// SecureCookies marks the session and asset-grant cookies Secure; set behind
	// TLS.
	SecureCookies bool
	// HTTP is the client every upstream call uses; defaults to a bounded one.
	HTTP *http.Client
}

// Server is the browser front.
type Server struct {
	upstream          *url.URL
	client            *http.Client
	sealer            *sealer
	sealKey           []byte
	now               func() time.Time
	poll              time.Duration
	csp               string
	secure            bool
	assets            fs.FS
	apps              apphost.Source
	host              *apphost.Host
	windows           *windowSessions
	assetTTL          time.Duration
	validation        apphost.ValidationOptions
	configuredOrigins []string

	assetFiles  apphost.AssetStore
	consoleHost string
}

// New builds the browser front.
func New(config Config) (*Server, error) {
	if config.Upstream == nil {
		return nil, fmt.Errorf("bff: an upstream control plane is required")
	}
	key := config.SealKey
	if len(key) == 0 {
		key = make([]byte, sessionKeySize)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("bff: generate the session key: %w", err)
		}
	}
	if len(key) != sessionKeySize {
		return nil, fmt.Errorf("bff: the session key must be %d bytes, got %d", sessionKeySize, len(key))
	}
	box, err := newSealer(key)
	if err != nil {
		return nil, err
	}
	server := &Server{
		upstream:          config.Upstream,
		client:            config.HTTP,
		sealer:            box,
		sealKey:           append([]byte(nil), key...),
		now:               config.Now,
		poll:              config.PollInterval,
		secure:            config.SecureCookies,
		assetFiles:        config.AssetFiles,
		apps:              config.Apps,
		assetTTL:          config.AssetTTL,
		configuredOrigins: config.FrameOrigins,

		validation: apphost.ValidationOptions{
			ConsoleOrigin:        config.ConsoleOrigin,
			AllowInsecureEntries: config.AllowInsecureEntries,
		},
	}
	if server.client == nil {
		server.client = &http.Client{Timeout: upstreamTimeout}
	}
	if server.now == nil {
		server.now = time.Now
	}
	if server.poll <= 0 {
		server.poll = defaultPollInterval
	}
	if server.assetTTL <= 0 {
		server.assetTTL = apphost.DefaultAssetTTL
	}
	if server.assets == nil {
		server.assets = embeddedAssets()
	}
	if host := hostOf(config.ConsoleOrigin); host != "" {
		server.consoleHost = host
	}
	server.host = config.AppHost
	if server.host == nil {
		server.host = apphost.New(apphost.Config{Now: server.now, Auditor: config.Auditor})
	}
	server.windows = newWindowSessions(server.now)
	server.csp = contentSecurityPolicy(config.FrameOrigins, nil)
	return server, nil
}

// Handler serves the browser front: the session routes, the proxied
// control-plane API, the desktop aggregate, the event stream and the static
// shell.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)
	mux.HandleFunc("GET /api/v1/desktop", s.desktop)
	mux.HandleFunc("GET /api/v1/stream", s.stream)
	mux.HandleFunc("GET "+appsPath, s.applications)

	mux.HandleFunc("GET "+windowsPath, s.listWindows)
	mux.HandleFunc("POST "+appWindowsPath, s.openWindow)
	mux.HandleFunc("GET "+appPath, s.application)
	mux.HandleFunc("GET "+appPermissionsPath, s.listPermissions)
	mux.HandleFunc("PUT "+appPermissionPath, s.grantPermission)
	mux.HandleFunc("DELETE "+appPermissionPath, s.revokePermission)
	mux.HandleFunc("POST "+windowHandshake, s.handshake)
	mux.HandleFunc("GET "+deepLinksPath, s.resolveDeepLink)
	mux.HandleFunc("GET "+assetPathPrefix+"{extension}/{app}/{version}/{digest}/{path...}", s.appAsset)
	mux.HandleFunc("POST "+capabilityUsePath, s.useCapability)
	mux.HandleFunc("GET "+dockLayoutPath, s.dockLayout)
	mux.HandleFunc("GET "+launcherLayoutPath, s.launcherLayout)
	mux.HandleFunc("GET "+desktopLayoutPath, s.desktopLayout)
	mux.HandleFunc("GET "+desktopWindowsPath, s.listDesktopWindows)
	mux.HandleFunc("POST "+desktopWindowsPath, s.openDesktopWindow)
	mux.HandleFunc("DELETE "+desktopWindowPath, s.closeDesktopWindow)
	mux.HandleFunc("POST "+desktopWindowFocusPath, s.focusDesktopWindow)
	mux.HandleFunc("PUT "+desktopWindowStatePath, s.stateDesktopWindow)
	mux.HandleFunc("PUT "+desktopWindowBoundsPath, s.boundsDesktopWindow)
	mux.HandleFunc("PUT "+desktopWindowWorkspacePath, s.workspaceDesktopWindow)
	mux.HandleFunc("PUT "+desktopWindowUnsavedPath, s.unsavedDesktopWindow)
	mux.HandleFunc("PUT "+desktopWindowPinnedPath, s.setDesktopWindowPinned)
	mux.HandleFunc("POST "+desktopWindowHeartbeatPath, s.heartbeatDesktopWindow)
	mux.HandleFunc("POST "+desktopWindowCrashPath, s.crashDesktopWindow)
	mux.HandleFunc("POST "+desktopWindowRestartPath, s.restartDesktopWindow)
	mux.HandleFunc("GET "+desktopWorkspacesPath, s.listDesktopWorkspaces)
	mux.HandleFunc("POST "+desktopWorkspacesPath, s.switchDesktopWorkspace)
	mux.HandleFunc("DELETE "+windowPath, s.closeWindow)
	mux.HandleFunc("/api/v1/", s.proxy)
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("/", s.static)
	return s.headers(mux)
}

// headers applies the response headers every browser response carries. The
// shell is same-origin only and frames nothing but the origins an installed
// application declares (KOSD-F-18, KOSD-C-75).
func (s *Server) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy", s.policyFor(r))
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "SAMEORIGIN")
		if len(r.URL.Path) >= len("/api/") && r.URL.Path[:len("/api/")] == "/api/" {
			// A proxied read answers the caller's own projection; no
			// intermediary may reuse it for another session.
			header.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// policyFor renders the policy of one response. Only a document frames an
// application, so only a document read consults the installed set; every other
// answer keeps the configured origins.
func (s *Server) policyFor(r *http.Request) string {
	if !isDocumentRequest(r) {
		return s.csp
	}
	token, ok := s.sessionToken(r)
	if !ok {
		return s.csp
	}
	return contentSecurityPolicy(s.configuredOrigins, s.appOrigins(r.Context(), token))
}

// isDocumentRequest reports a request the shell answers with a document, which
// is the one response a frame policy belongs to.
func isDocumentRequest(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/healthz" {
		return false
	}
	extension := path.Ext(strings.TrimSuffix(path.Clean("/"+r.URL.Path), "/"))
	return extension == "" || extension == ".html"
}

// appOrigins lists the installed applications' origins as the caller sees them.
func (s *Server) appOrigins(ctx context.Context, token string) []string {
	registry, err := s.registryFor(ctx, token)
	if err != nil {
		// A registry the front cannot read frames nothing new: only the
		// configured origins stay, so the answer fails closed (KOSD-F-18).
		return nil
	}
	return registry.Origins()
}

// health reports the front itself. It carries no control-plane read, so the
// observation is the moment it answered (KOSD-C-13).
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"observedAt": s.now().UTC(),
		"stale":      false,
	})
}

// failure is the control-plane error envelope this front answers its own
// refusals with (KOSD-C-12), so a caller reads one shape from both sides.
type failure struct {
	Code               string `json:"code"`
	Message            string `json:"message"`
	Stage              string `json:"stage"`
	Retryable          bool   `json:"retryable"`
	RequiresUserAction bool   `json:"requiresUserAction"`
	SideEffects        string `json:"sideEffects"`
}

// writeFailure writes one refusal in the control-plane envelope.
func writeFailure(w http.ResponseWriter, status int, code, message, stage string) {
	writeJSON(w, status, map[string]any{"error": failure{
		Code: code, Message: message, Stage: stage, SideEffects: "none",
	}})
}

// writeJSON writes one JSON body.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// readBody reads one bounded request body.
func readBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		return nil, err
	}
	return body, nil
}

// hostOf reads the host of one absolute origin.
func hostOf(origin string) string {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return ""
	}
	return parsed.Host
}
