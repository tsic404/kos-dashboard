// Package dashtc140 verifies DASH-TC-140: installing an extension whose `App`
// contribution declares one window application makes the entry appear in both
// the launcher and the dock with the manifest's name, icon and default size;
// opening it registers one single-mode instance and a handshake delivers one
// token; uninstalling the extension withdraws both entries, puts the open
// window in its error state and refuses to reopen it, while another installed
// application is untouched (DASH-AC-67, DASH-C-30, KOSD-C-73, KOSD-C-75).
package dashtc140

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/tsic404/kosd/pkg/controlplane"
	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
	"github.com/tsic404/kos-dashboard/pkg/bff"
)

const (
	// The applications the scenario installs.
	fileAppID  = "files"
	fileName   = "文件"
	noteAppID  = "notes"
	noteName   = "笔记"
	fileExtID  = "files-ext"
	noteExtID  = "note-ext"
	ownerName  = "owner"
	ownerPass  = "owner-password"
	fileOrigin = "https://files.apps.example.test"
	noteOrigin = "https://notes.apps.example.test"
	// The declared window geometry, so the test can tell the manifest's values
	// from a default the console guessed.
	fileWidth, fileHeight = 1024, 720
	noteWidth, noteHeight = 800, 600
)

// manifestOf renders one `App` contribution with the fields DASH-TC-140 reads.
func manifestOf(id, name, origin string, width, height int, mode string) map[string]any {
	return map[string]any{
		"id": id, "name": name, "version": "1.0.0",
		"icon":  origin + "/assets/icon.svg",
		"entry": origin + "/index.html",
		"window": map[string]any{
			"width": width, "height": height, "minWidth": 320, "minHeight": 240,
			"resizable": true, "mode": mode, "defaultState": "normal",
		},
		"permissions": []string{apphost.PermissionNotifications},
		"deepLinks":   []string{"/" + id},
	}
}

// contributionOf wraps one manifest as an installed extension's contribution.
func contributionOf(t *testing.T, extension, component, phase string, manifest map[string]any) apphost.Contribution {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	return apphost.Contribution{
		Extension: extension, Component: component, Phase: phase,
		Digest:   "sha256:0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0",
		Manifest: raw,
	}
}

// registry is the control-plane application registry the front reads. It holds
// the contributions of the installed extensions; installing writes here and
// uninstalling removes from here, exactly as the control plane's answer would
// change (KOSD-C-73).
type registry struct {
	mu            sync.Mutex
	contributions []apphost.Contribution
}

func (r *registry) append(contribution apphost.Contribution) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.contributions = append(r.contributions, contribution)
}

func (r *registry) remove(extension string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := make([]apphost.Contribution, 0, len(r.contributions))
	for _, contribution := range r.contributions {
		if contribution.Extension != extension {
			kept = append(kept, contribution)
		}
	}
	r.contributions = kept
}

// items returns a copy of the installed contributions.
func (r *registry) items() []apphost.Contribution {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]apphost.Contribution(nil), r.contributions...)
}

// startControlPlane composes the harness control plane with the application
// registry route, so the front reads the registry over the same HTTP surface it
// uses in production.
func startControlPlane(t *testing.T, shell *harness.Shell, apps *registry) *httptest.Server {
	t.Helper()
	target, err := url.Parse(shell.Accounts.HTTP.URL)
	if err != nil {
		t.Fatalf("parse the control-plane URL: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/apps", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": apps.items()})
	})
	mux.Handle("/", proxy)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// writeJSON writes one JSON answer.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// recorder keeps the audit records the front writes (KOSD-C-14).
type recorder struct {
	mu      sync.Mutex
	records []controlplane.AuditRecord
}

func (r *recorder) Record(_ context.Context, record controlplane.AuditRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, record)
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.records)
}

// startFront serves the browser front over the composed control plane and logs
// the owner in through it.
func startFront(t *testing.T, control *httptest.Server, audit *recorder) (*httptest.Server, http.Header) {
	t.Helper()
	upstream, err := url.Parse(control.URL)
	if err != nil {
		t.Fatalf("parse the composed URL: %v", err)
	}
	server, err := bff.New(bff.Config{Upstream: upstream, Auditor: audit})
	if err != nil {
		t.Fatalf("build the browser front: %v", err)
	}
	front := httptest.NewServer(server.Handler())
	t.Cleanup(front.Close)
	response := call(t, front, http.MethodPost, "/api/v1/auth/login", "",
		strings.NewReader(`{"username":"`+ownerName+`","password":"`+ownerPass+`"}`), "")
	if response.status != http.StatusOK {
		t.Fatalf("login through the front = %d %v", response.status, response.body)
	}
	cookies := http.Header{}
	pairs := make([]string, 0, 2)
	for _, cookie := range response.cookies {
		pairs = append(pairs, cookie.Name+"="+cookie.Value)
	}
	cookies.Set("Cookie", strings.Join(pairs, "; "))
	assertSessionCookie(t, response.cookies)
	if csrf := csrfOf(response.cookies); csrf == "" {
		t.Fatal("login set no CSRF token")
	} else {
		cookies.Set("X-CSRF-Token", csrf)
	}
	return front, cookies
}

// csrfOf reads the double-submit token out of the login cookies.
func csrfOf(cookies []*http.Cookie) string {
	for _, cookie := range cookies {
		if cookie.Name == "kos_bff_csrf" {
			return cookie.Value
		}
	}
	return ""
}

// appEntry is the launcher/dock item the front reports.
type appEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Icon        string   `json:"icon"`
	Entry       string   `json:"entry"`
	Digest      string   `json:"digest"`
	Surfaces    []string `json:"surfaces"`
	Permissions []string `json:"permissions"`
	Window      struct {
		Width, Height, MinWidth, MinHeight int
		Resizable                          bool
		Mode, DefaultState                 string
	} `json:"window"`
}

// appsAnswer is the launcher/dock read.
type appsAnswer struct {
	Items   []appEntry `json:"items"`
	Refused []struct {
		Extension, Component, App, Reason string
	} `json:"refused"`
}

// readApps reads the launcher/dock list.
func readApps(t *testing.T, front *httptest.Server, cookies http.Header) appsAnswer {
	t.Helper()
	response := call(t, front, http.MethodGet, "/api/v1/apps", cookies.Get("Cookie"), nil, "")
	if response.status != http.StatusOK {
		t.Fatalf("read the applications = %d %v", response.status, response.body)
	}
	var answer appsAnswer
	if err := json.Unmarshal([]byte(response.body), &answer); err != nil {
		t.Fatalf("decode the applications: %v", err)
	}
	return answer
}

// window is one registered instance.
type window struct {
	ID             string `json:"id"`
	AppID          string `json:"appId"`
	State          string `json:"state"`
	HandshakeNonce string `json:"handshakeNonce"`
	DefaultWidth   int    `json:"defaultWidth"`
	DefaultHeight  int    `json:"defaultHeight"`
	DefaultState   string `json:"defaultState"`
	Mode           string `json:"mode"`
}

// openAnswer is one open answer.
type openAnswer struct {
	Window  window `json:"window"`
	IsNew   bool   `json:"isNew"`
	Frame   string `json:"frame"`
	Sandbox string `json:"sandbox"`
}

// openWindow opens one application window through the front.
func openWindow(t *testing.T, front *httptest.Server, cookies http.Header, appID string) openAnswer {
	t.Helper()
	response := call(t, front, http.MethodPost, "/api/v1/apps/"+appID+"/windows",
		cookies.Get("Cookie"), nil, cookies.Get("X-CSRF-Token"))
	if response.status != http.StatusAccepted {
		t.Fatalf("open %s = %d %v", appID, response.status, response.body)
	}
	var answer openAnswer
	if err := json.Unmarshal([]byte(response.body), &answer); err != nil {
		t.Fatalf("decode the open answer: %v", err)
	}
	return answer
}

// handshake posts one ready message and returns the answer status and body.
func handshake(t *testing.T, front *httptest.Server, cookies http.Header, windowID, origin, nonce string) response {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"origin":  origin,
		"nonce":   nonce,
		"message": []byte(`{"type":"ready"}`),
	})
	if err != nil {
		t.Fatalf("encode the handshake: %v", err)
	}
	return call(t, front, http.MethodPost, "/api/v1/windows/"+windowID+"/handshake",
		cookies.Get("Cookie"), bytes.NewReader(body), cookies.Get("X-CSRF-Token"))
}

// response is one answer the front gave.
type response struct {
	status  int
	body    string
	decoded map[string]any
	cookies []*http.Cookie
	header  http.Header
}

// call sends one request to the front.
func call(t *testing.T, front *httptest.Server, method, path, cookie string, body io.Reader, csrf string) response {
	t.Helper()
	request, err := http.NewRequest(method, front.URL+path, body)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	if cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	answer, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer answer.Body.Close()
	payload, _ := io.ReadAll(answer.Body)
	decoded := map[string]any{}
	_ = json.Unmarshal(payload, &decoded)
	return response{
		status: answer.StatusCode, body: string(payload), decoded: decoded,
		cookies: answer.Cookies(), header: answer.Header,
	}
}

// TestTC140InstallsOpensAndUninstallsAWIndowApplication is the case DASH-TC-140
// describes.
func TestTC140InstallsOpensAndUninstallsAWIndowApplication(t *testing.T) {
	cluster := harness.Start(t)
	shell := cluster.StartShell(t)
	shell.SignInOwner(t, ownerName, ownerPass)

	apps := &registry{}
	control := startControlPlane(t, shell, apps)
	audit := &recorder{}
	front, cookies := startFront(t, control, audit)

	// Installing the extension publishes its `App` contribution: the entry
	// appears in the launcher and the dock with the manifest's name and icon
	// (DASH-AC-67, DASH-C-30).
	apps.append(contributionOf(t, fileExtID, "files", apphost.PhaseReady,
		manifestOf(fileAppID, fileName, fileOrigin, fileWidth, fileHeight, apphost.WindowModeSingle)))
	installed := readApps(t, front, cookies)
	if len(installed.Items) != 1 {
		t.Fatalf("applications = %+v, want the installed application", installed.Items)
	}
	entry := installed.Items[0]
	if entry.ID != fileAppID || entry.Name != fileName || !strings.HasSuffix(entry.Icon, "/assets/icon.svg") {
		t.Fatalf("entry = %+v, want the manifest's name and icon (DASH-TC-140)", entry)
	}
	if strings.Join(entry.Surfaces, ",") != apphost.SurfaceLauncher+","+apphost.SurfaceDock {
		t.Fatalf("surfaces = %v, want the launcher and the dock", entry.Surfaces)
	}
	if entry.Window.Width != fileWidth || entry.Window.Height != fileHeight || entry.Window.Mode != apphost.WindowModeSingle {
		t.Fatalf("window = %+v, want the manifest's default size and single mode", entry.Window)
	}

	// Opening from either surface registers one instance at the manifest's
	// default size; a second open of a single-mode application only focuses it.
	opened := openWindow(t, front, cookies, fileAppID)
	if opened.Window.DefaultWidth != fileWidth || opened.Window.DefaultHeight != fileHeight || !opened.IsNew {
		t.Fatalf("open = %+v, want a new window at the manifest's default size", opened)
	}
	again := openWindow(t, front, cookies, fileAppID)
	if again.IsNew || again.Window.ID != opened.Window.ID {
		t.Fatalf("second open = %+v, want the same single instance focused (DASH-TC-140)", again)
	}
	if opened.Sandbox != apphost.FrameSandbox {
		t.Fatalf("sandbox = %q, want scripts and own storage without top navigation (KOSD-C-75)", opened.Sandbox)
	}
	if opened.Frame == "" {
		t.Fatal("open answer carries no signed frame address (KOSD-C-73)")
	}
	if frame, err := url.Parse(opened.Frame); err != nil ||
		!strings.HasPrefix(frame.String(), fileOrigin+"/app-assets/"+fileExtID+"/1.0.0/") ||
		frame.Query().Get("kos-sig") == "" || frame.Query().Get("kos-exp") == "" {
		t.Fatalf("frame = %q, want a signed, versioned address on the application origin", opened.Frame)
	}

	// The handshake verifies origin and nonce, and delivers the token once
	// (KOSD-C-75).
	if wrong := handshake(t, front, cookies, opened.Window.ID, noteOrigin, opened.Window.HandshakeNonce); wrong.status != http.StatusForbidden {
		t.Fatalf("handshake from another origin = %d %v, want it ignored (KOSD-C-75)", wrong.status, wrong.body)
	}
	init := handshake(t, front, cookies, opened.Window.ID, fileOrigin, opened.Window.HandshakeNonce)
	if init.status != http.StatusOK || init.decoded["token"] == "" || init.decoded["type"] != "init" {
		t.Fatalf("handshake = %d %v, want the init payload with a token", init.status, init.body)
	}
	if repeated := handshake(t, front, cookies, opened.Window.ID, fileOrigin, opened.Window.HandshakeNonce); repeated.status != http.StatusForbidden {
		t.Fatalf("second handshake = %d, want the token delivered once (KOSD-C-75)", repeated.status)
	}

	// The console document frames only the installed application's origin, and
	// the session cookie stays a host-only, HttpOnly cookie that the application
	// origin cannot read (KOSD-F-18, KOSD-C-75).
	document := call(t, front, http.MethodGet, "/", cookies.Get("Cookie"), nil, "")
	if document.status != http.StatusOK {
		t.Fatalf("read the shell document = %d %v", document.status, document.body)
	}
	policy := document.header.Get("Content-Security-Policy")
	if !strings.Contains(policy, "frame-src 'self' "+fileOrigin) {
		t.Fatalf("content security policy = %q, want frame-src to list %s", policy, fileOrigin)
	}
	if !strings.Contains(policy, "frame-ancestors 'self'") || strings.Contains(policy, "frame-ancestors *") {
		t.Fatalf("content security policy = %q, want the frame ancestors restricted to the console", policy)
	}

	// A permission is confirmed and written to the audit trail; revoking it
	// takes effect on the open window at once (KOSD-C-75, KOSD-C-14).
	before := audit.count()
	assertPermissionFlow(t, front, cookies, opened.Window.ID)
	if recorded := audit.count() - before; recorded < 4 {
		t.Fatalf("audit records = %d, want the grant, both uses and the revocation (KOSD-C-14)", recorded)
	}

	// Uninstalling the extension withdraws both entries and puts the open
	// window in its error state; it cannot be reopened (DASH-C-30).
	apps.remove(fileExtID)
	after := readApps(t, front, cookies)
	if len(after.Items) != 0 {
		t.Fatalf("applications after the uninstall = %+v, want the entry gone", after.Items)
	}
	windows := call(t, front, http.MethodGet, "/api/v1/windows", cookies.Get("Cookie"), nil, "")
	var listed struct {
		Items []window `json:"items"`
	}
	if err := json.Unmarshal([]byte(windows.body), &listed); err != nil {
		t.Fatalf("decode the window list: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].State != apphost.InstanceStateError {
		t.Fatalf("windows after the uninstall = %+v, want the open window in its error state", listed.Items)
	}
	if reopen := call(t, front, http.MethodPost, "/api/v1/apps/"+fileAppID+"/windows",
		cookies.Get("Cookie"), nil, cookies.Get("X-CSRF-Token")); reopen.status != http.StatusConflict {
		t.Fatalf("reopen after the uninstall = %d %v, want %d", reopen.status, reopen.body, http.StatusConflict)
	}

	// Another installed application is unaffected.
	apps.append(contributionOf(t, noteExtID, "notes", apphost.PhaseReady,
		manifestOf(noteAppID, noteName, noteOrigin, noteWidth, noteHeight, apphost.WindowModeMulti)))
	remaining := readApps(t, front, cookies)
	if len(remaining.Items) != 1 || remaining.Items[0].ID != noteAppID {
		t.Fatalf("applications = %+v, want only the other application", remaining.Items)
	}
	if other := openWindow(t, front, cookies, noteAppID); !other.IsNew || other.Window.DefaultWidth != noteWidth {
		t.Fatalf("opening the other application = %+v, want its own defaults", other)
	}

	// The desktop page still renders and the browser received no cluster
	// credential (KOSD-F-9).
	assertNoCredentials(t, front, cookies)
}

// assertPermissionFlow grants and revokes one declared capability through the
// front's settings surface and checks the audit trail records both writes.
func assertPermissionFlow(t *testing.T, front *httptest.Server, cookies http.Header, windowID string) {
	t.Helper()
	listed := call(t, front, http.MethodGet, "/api/v1/apps/"+fileAppID+"/permissions", cookies.Get("Cookie"), nil, "")
	if listed.status != http.StatusOK {
		t.Fatalf("read the permissions = %d %v", listed.status, listed.body)
	}
	var answer struct {
		Items []struct{ Name, State string } `json:"items"`
	}
	if err := json.Unmarshal([]byte(listed.body), &answer); err != nil {
		t.Fatalf("decode the permissions: %v", err)
	}
	if len(answer.Items) != 1 || answer.Items[0].Name != apphost.PermissionNotifications ||
		answer.Items[0].State != string(apphost.PermissionPending) {
		t.Fatalf("permissions = %+v, want the declared capability pending confirmation", answer.Items)
	}
	useBefore := call(t, front, http.MethodPost, "/api/v1/windows/"+windowID+"/capabilities/"+apphost.PermissionNotifications,
		cookies.Get("Cookie"), nil, cookies.Get("X-CSRF-Token"))
	if useBefore.status != http.StatusForbidden {
		// Before the confirmation the capability is not usable (KOSD-C-75).
		t.Fatalf("use before the grant = %d %v, want %d", useBefore.status, useBefore.body, http.StatusForbidden)
	}
	grant := call(t, front, http.MethodPut, "/api/v1/apps/"+fileAppID+"/permissions/"+apphost.PermissionNotifications,
		cookies.Get("Cookie"), nil, cookies.Get("X-CSRF-Token"))
	if grant.status != http.StatusNoContent {
		t.Fatalf("grant = %d %v, want %d", grant.status, grant.body, http.StatusNoContent)
	}
	if use := call(t, front, http.MethodPost, "/api/v1/windows/"+windowID+"/capabilities/"+apphost.PermissionNotifications,
		cookies.Get("Cookie"), nil, cookies.Get("X-CSRF-Token")); use.status != http.StatusNoContent {
		t.Fatalf("use after the grant = %d %v, want %d", use.status, use.body, http.StatusNoContent)
	}
	revoke := call(t, front, http.MethodDelete, "/api/v1/apps/"+fileAppID+"/permissions/"+apphost.PermissionNotifications,
		cookies.Get("Cookie"), nil, cookies.Get("X-CSRF-Token"))
	if revoke.status != http.StatusNoContent {
		t.Fatalf("revoke = %d %v, want %d", revoke.status, revoke.body, http.StatusNoContent)
	}
	if use := call(t, front, http.MethodPost, "/api/v1/windows/"+windowID+"/capabilities/"+apphost.PermissionNotifications,
		cookies.Get("Cookie"), nil, cookies.Get("X-CSRF-Token")); use.status != http.StatusForbidden {
		// The revocation ends the open window's capability at once (KOSD-C-75).
		t.Fatalf("use after the revoke = %d %v, want %d", use.status, use.body, http.StatusForbidden)
	}
}

// assertNoCredentials checks that no answer the browser receives carries a
// kubeconfig or a service-account token (KOSD-F-9).
func assertNoCredentials(t *testing.T, front *httptest.Server, cookies http.Header) {
	t.Helper()
	for _, path := range []string{"/api/v1/apps", "/api/v1/windows", "/api/v1/auth/session"} {
		response := call(t, front, http.MethodGet, path, cookies.Get("Cookie"), nil, "")
		if response.status != http.StatusOK {
			t.Fatalf("read %s = %d %v", path, response.status, response.body)
		}
		for _, forbidden := range []string{"kubeconfig", "serviceAccountToken", "client-key-data"} {
			if strings.Contains(response.body, forbidden) {
				t.Fatalf("%s carries %q: the browser never receives a cluster credential (KOSD-F-9)", path, forbidden)
			}
		}
	}
}

// assertSessionCookie checks the console session cookie never leaves the console
// origin: no Domain attribute and HttpOnly, so an application frame cannot read
// it (KOSD-F-18, KOSD-C-75).
func assertSessionCookie(t *testing.T, cookies []*http.Cookie) {
	t.Helper()
	for _, cookie := range cookies {
		if cookie.Name != "kos_bff_session" {
			continue
		}
		if cookie.Domain != "" || !cookie.HttpOnly {
			t.Fatalf("session cookie = %+v, want a host-only HttpOnly cookie (KOSD-F-18)", cookie)
		}
		return
	}
	t.Fatal("login set no session cookie")
}
