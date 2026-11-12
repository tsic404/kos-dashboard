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
	"net"
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
	fileAppID = "files"
	fileName  = "文件"
	noteAppID = "notes"
	noteName  = "笔记"
	fileExtID = "files-ext"
	noteExtID = "note-ext"
	// brokenExtID is a component whose manifest fails the required set.
	brokenExtID = "broken-ext"
	ownerName   = "owner"
	ownerPass   = "owner-password"
	fileOrigin  = "https://files.apps.example.test"
	noteOrigin  = "https://notes.apps.example.test"
	// The declared window geometry, so the test can tell the manifest's values
	// from a default the console guessed.
	fileWidth, fileHeight = 1024, 720
	// assetDigest is the package digest the stub control plane reports.
	assetDigest           = "sha256:0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"
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
		Digest:   assetDigest,
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
	assets := apphost.NewMemoryAssets()
	for extension, files := range map[string]map[string][]byte{
		fileExtID: {
			"index.html": []byte("<html>files</html>"),
			"app.css":    []byte("body{color:red}"),
			"app.js":     []byte("main()"),
		},
		noteExtID: {
			"index.html": []byte("<html>notes</html>"),
			"app.css":    []byte("body{color:blue}"),
		},
	} {
		if err := assets.Publish(extension, "1.0.0", assetDigest, files); err != nil {
			t.Fatalf("publish the assets of %s: %v", extension, err)
		}
	}
	return startFrontWith(t, control, audit, assets, nil)
}

// startFrontWith serves the front over one asset store with one seal key, so a
// case can drive the disk store a deployment unpacks and mint its own signed
// addresses. An empty key is generated at start, which ends every session on a
// restart.
func startFrontWith(t *testing.T, control *httptest.Server, audit *recorder,
	files apphost.AssetStore, sealKey []byte) (*httptest.Server, http.Header) {
	t.Helper()
	upstream, err := url.Parse(control.URL)
	if err != nil {
		t.Fatalf("parse the composed URL: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve the front address: %v", err)
	}
	// The front knows its own origin, which is what tells the F-18 guard which
	// requests are console traffic (KOSD-F-18).
	server, err := bff.New(bff.Config{
		Upstream:      upstream,
		Auditor:       audit,
		SealKey:       sealKey,
		AssetFiles:    files,
		ConsoleOrigin: "http://" + listener.Addr().String(),
	})
	if err != nil {
		t.Fatalf("build the browser front with its origin: %v", err)
	}
	front := httptest.NewUnstartedServer(server.Handler())
	front.Listener.Close()
	front.Listener = listener
	front.Start()
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

// refusal is one contribution that produced no application.
type refusal struct {
	Extension string `json:"extension"`
	Component string `json:"component"`
	App       string `json:"app"`
	Reason    string `json:"reason"`
}

// appsAnswer is the launcher/dock read: the entries plus the contributions that
// produced none, which is how a manifest failing KOSD-C-73 stays observable.
type appsAnswer struct {
	Items   []appEntry `json:"items"`
	Refused []refusal  `json:"notReady"`
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
	HasToken       bool   `json:"hasToken"`
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
	// A manifest that fails KOSD-C-73 makes its component not ready and surfaces
	// no entry, while the valid application is unaffected (KOSD-C-73). It is
	// removed again so the rest of the case runs over one installed application.
	apps.append(contributionOf(t, brokenExtID, "broken", apphost.PhaseReady,
		map[string]any{
			"id": "broken", "name": "损坏", "version": "1.0.0",
			"icon": "https://broken.apps.example.test/icon.svg",
			"window": map[string]any{
				"width": 800, "height": 600, "minWidth": 320, "minHeight": 240,
				"resizable": true, "mode": "single", "defaultState": "normal",
			},
			"permissions": []string{},
		}))
	withBroken := readApps(t, front, cookies)
	if len(withBroken.Refused) != 1 || withBroken.Refused[0].Extension != brokenExtID ||
		!strings.Contains(withBroken.Refused[0].Reason, "entry") {
		t.Fatalf("not ready = %+v, want the manifest missing its entry reported", withBroken.Refused)
	}
	apps.remove(brokenExtID)
	installed := readApps(t, front, cookies)
	if len(installed.Refused) != 0 {
		t.Fatalf("not ready = %+v, want the valid manifest admitted", installed.Refused)
	}
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

	// The signed address actually serves the packaged document from the
	// application origin, and the console origin never serves application bytes
	// (KOSD-C-73, KOSD-F-18).
	document := fetchAsset(t, front, opened.Frame, "files.apps.example.test")
	if document.status != http.StatusOK || document.body != "<html>files</html>" {
		t.Fatalf("frame asset = %d %q, want the packaged document", document.status, document.body)
	}
	// The served entry document loads its relative references: its answer leaves
	// a narrow grant on the application origin, the same-digest stylesheet is
	// served without a query signature of its own, a request without the grant is
	// refused, and an address whose signature was tampered with stays refused
	// even while the grant is present (KOSD-C-73, DASH-C-30).
	grant := grantOf(t, document)
	stylesheet := siblingAddress(t, opened.Frame, "app.css")
	if served := fetchAssetWithGrant(t, front, stylesheet, "files.apps.example.test", grant); served.status != http.StatusOK ||
		served.body != "body{color:red}" {
		t.Fatalf("subresource = %d %q, want the packaged stylesheet", served.status, served.body)
	}
	if served := fetchAsset(t, front, stylesheet, "files.apps.example.test"); served.status != http.StatusForbidden {
		t.Fatalf("subresource without the grant = %d, want %d", served.status, http.StatusForbidden)
	}
	tampered := strings.Replace(opened.Frame, "kos-sig=", "kos-sig=forged-", 1)
	if served := fetchAssetWithGrant(t, front, tampered, "files.apps.example.test", grant); served.status != http.StatusForbidden {
		t.Fatalf("tampered signature = %d, want %d", served.status, http.StatusForbidden)
	}
	consoleOrigin, _ := url.Parse(front.URL)
	if console := fetchAsset(t, front, opened.Frame, consoleOrigin.Host); console.status != http.StatusNotFound {
		t.Fatalf("asset on the console origin = %d, want %d", console.status, http.StatusNotFound)
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
	// A reload loses the shell's own record: reopening the single instance
	// answers the delivered token, so the shell closes it and opens a fresh one
	// that can handshake again (KOSD-C-75).
	reused := openWindow(t, front, cookies, fileAppID)
	if reused.IsNew || reused.Window.ID != opened.Window.ID || !reused.Window.HasToken {
		t.Fatalf("reopen after the handshake = %+v, want the delivered instance marked", reused)
	}
	if closed := call(t, front, http.MethodDelete, "/api/v1/windows/"+reused.Window.ID,
		cookies.Get("Cookie"), nil, cookies.Get("X-CSRF-Token")); closed.status != http.StatusNoContent {
		t.Fatalf("close the delivered instance = %d %v, want %d", closed.status, closed.body, http.StatusNoContent)
	}
	fresh := openWindow(t, front, cookies, fileAppID)
	if !fresh.IsNew || fresh.Window.ID == reused.Window.ID || fresh.Window.HasToken {
		t.Fatalf("fresh open = %+v, want a new instance without a token", fresh)
	}
	if reloaded := handshake(t, front, cookies, fresh.Window.ID, fileOrigin, fresh.Window.HandshakeNonce); reloaded.status != http.StatusOK {
		t.Fatalf("handshake after the reload recovery = %d %v, want the token", reloaded.status, reloaded.body)
	}

	// A second browser of the same account owns its own instance and mounts the
	// same application without disturbing the first (DASH-C-74).
	_, secondBrowser := loginAgain(t, front)
	second := openWindow(t, front, secondBrowser, fileAppID)
	if !second.IsNew || second.Window.ID == fresh.Window.ID {
		t.Fatalf("second browser open = %+v, want its own instance", second)
	}
	if mounted := handshake(t, front, secondBrowser, second.Window.ID, fileOrigin, second.Window.HandshakeNonce); mounted.status != http.StatusOK {
		t.Fatalf("second browser handshake = %d %v, want the token", mounted.status, mounted.body)
	}
	if frame, err := url.Parse(opened.Frame); err != nil ||
		!strings.HasPrefix(frame.String(), fileOrigin+"/app-assets/"+fileExtID+"/"+fileAppID+"/1.0.0/") ||
		frame.Query().Get("kos-sig") == "" || frame.Query().Get("kos-exp") == "" {
		t.Fatalf("frame = %q, want a signed, versioned address on the application origin", opened.Frame)
	}

	// The console document frames only the installed application's origin, and
	// the session cookie stays a host-only, HttpOnly cookie that the application
	// origin cannot read (KOSD-F-18, KOSD-C-75).
	shellDocument := call(t, front, http.MethodGet, "/", cookies.Get("Cookie"), nil, "")
	if shellDocument.status != http.StatusOK {
		t.Fatalf("read the shell document = %d %v", shellDocument.status, shellDocument.body)
	}
	policy := shellDocument.header.Get("Content-Security-Policy")
	if !strings.Contains(policy, "frame-src 'self' "+fileOrigin) {
		t.Fatalf("content security policy = %q, want frame-src to list %s", policy, fileOrigin)
	}
	if !strings.Contains(policy, "frame-ancestors 'self'") || strings.Contains(policy, "frame-ancestors *") {
		t.Fatalf("content security policy = %q, want the frame ancestors restricted to the console", policy)
	}

	// A permission is confirmed and written to the audit trail; revoking it
	// takes effect on the open window at once (KOSD-C-75, KOSD-C-14).
	before := audit.count()
	assertPermissionFlow(t, front, cookies, fresh.Window.ID)
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

// fetchAsset reads one signed asset address through the front, addressed to the
// application origin the deployment maps to it.
func fetchAsset(t *testing.T, front *httptest.Server, address, host string) response {
	t.Helper()
	return fetchAssetWithGrant(t, front, address, host, "")
}

// fetchAssetWithGrant reads one asset address with the subresource grant a served
// entry document left on the application origin.
func fetchAssetWithGrant(t *testing.T, front *httptest.Server, address, host, grant string) response {
	t.Helper()
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatalf("parse the asset address: %v", err)
	}
	request, err := http.NewRequest(http.MethodGet, front.URL+parsed.RequestURI(), nil)
	if err != nil {
		t.Fatalf("build the asset request: %v", err)
	}
	request.Host = host
	if grant != "" {
		request.AddCookie(&http.Cookie{Name: apphost.AssetGrantCookie, Value: grant})
	}
	answer, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("read the asset: %v", err)
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

// grantOf reads the subresource grant a served asset answer set.
func grantOf(t *testing.T, served response) string {
	t.Helper()
	for _, cookie := range served.cookies {
		if cookie.Name == apphost.AssetGrantCookie {
			return cookie.Value
		}
	}
	t.Fatalf("asset answer set no %s cookie, want the subresource grant", apphost.AssetGrantCookie)
	return ""
}

// siblingAddress rewrites one signed address to a sibling in the same directory,
// which is how a browser resolves a relative reference in the document.
func siblingAddress(t *testing.T, address, name string) string {
	t.Helper()
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatalf("parse the signed address: %v", err)
	}
	parsed.RawQuery = ""
	parsed.Path = parsed.Path[:strings.LastIndex(parsed.Path, "/")+1] + name
	return parsed.String()
}

// loginAgain signs the same account in through the same front, which is a second
// browser session of that account.
func loginAgain(t *testing.T, front *httptest.Server) (response, http.Header) {
	t.Helper()
	response := call(t, front, http.MethodPost, "/api/v1/auth/login", "",
		strings.NewReader(`{"username":"`+ownerName+`","password":"`+ownerPass+`"}`), "")
	if response.status != http.StatusOK {
		t.Fatalf("second login = %d %v", response.status, response.body)
	}
	pairs := make([]string, 0, 2)
	for _, cookie := range response.cookies {
		pairs = append(pairs, cookie.Name+"="+cookie.Value)
	}
	cookies := http.Header{}
	cookies.Set("Cookie", strings.Join(pairs, "; "))
	if csrf := csrfOf(response.cookies); csrf != "" {
		cookies.Set("X-CSRF-Token", csrf)
	}
	return response, cookies
}
