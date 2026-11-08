package bff

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
)

// windowClock is a test clock the window manager's age judgement reads.
type windowClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *windowClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *windowClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// windowContribution renders one installed application as the registry reports
// it (KOSD-C-73).
func windowContribution(t *testing.T, id, mode, defaultState string, width, height int, deepLinks ...string) apphost.Contribution {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id": id, "name": id, "version": "1.0.0",
		"icon":  "https://" + id + ".apps.example.test/icon.svg",
		"entry": "https://" + id + ".apps.example.test/index.html",
		"window": map[string]any{
			"width": width, "height": height, "minWidth": 320, "minHeight": 240,
			"resizable": true, "mode": mode, "defaultState": defaultState,
		},
		"permissions": []string{},
		"deepLinks":   deepLinks,
	})
	if err != nil {
		t.Fatalf("encode the manifest: %v", err)
	}
	return apphost.Contribution{
		Extension: id + "-ext", Component: id, Phase: apphost.PhaseReady,
		Digest:   "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		Manifest: raw,
	}
}

// startWindowFront serves the front over a stub control plane and the given
// installed applications, and signs one account in through it.
func startWindowFront(t *testing.T, clock *windowClock, contributions ...apphost.Contribution) (*httptest.Server, http.Header, *fakeControlPlane) {
	t.Helper()
	fake, upstream := newFakeControlPlane(t, map[string]http.HandlerFunc{
		"POST /api/v1/auth/login": loginHandlers()["POST /api/v1/auth/login"],
		"GET /api/v1/auth/session": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"username":"alice"}`))
		},
	})
	base, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse the upstream URL: %v", err)
	}
	server, err := New(Config{
		Upstream:      base,
		Apps:          apphost.MemorySource(contributions),
		Now:           clock.read,
		ConsoleOrigin: "https://console.example.test",
	})
	if err != nil {
		t.Fatalf("build the front: %v", err)
	}
	front := httptest.NewServer(server.Handler())
	t.Cleanup(front.Close)

	response, body := do(t, front, http.MethodPost, "/api/v1/auth/login", "", strings.NewReader(`{"username":"alice","password":"secret"}`))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login = %d %v", response.StatusCode, body)
	}
	pairs := make([]string, 0, 2)
	csrf := ""
	for _, cookie := range response.Cookies() {
		pairs = append(pairs, cookie.Name+"="+cookie.Value)
		if cookie.Name == csrfCookie {
			csrf = cookie.Value
		}
	}
	if csrf == "" {
		t.Fatal("login set no CSRF token")
	}
	cookies := http.Header{}
	cookies.Set("Cookie", strings.Join(pairs, "; "))
	cookies.Set(csrfHeader, csrf)
	return front, cookies, fake
}

// windowRequest sends one window-session request and decodes its answer.
func windowRequest(t *testing.T, front *httptest.Server, cookies http.Header, method, path string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode the request: %v", err)
		}
		reader = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequest(method, front.URL+path, reader)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	request.Header.Set("Cookie", cookies.Get("Cookie"))
	request.Header.Set("X-CSRF-Token", cookies.Get(csrfHeader))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	decoded := map[string]any{}
	_ = json.Unmarshal(payload, &decoded)
	return response, decoded
}

// openDesktopWindow opens one application's window through the session route.
func openDesktopWindow(t *testing.T, front *httptest.Server, cookies http.Header, body map[string]any) map[string]any {
	t.Helper()
	response, answer := windowRequest(t, front, cookies, http.MethodPost, desktopWindowsPath, body)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("open %v = %d %v", body, response.StatusCode, answer)
	}
	return answer
}

// desktopWindowOf reads one desktop window from a session answer.
func desktopWindowOf(t *testing.T, answer map[string]any) map[string]any {
	t.Helper()
	window, _ := answer["window"].(map[string]any)
	if window == nil {
		t.Fatalf("answer carries no desktop window: %v", answer)
	}
	return window
}

// windowsOfAnswer lists the desktop windows a session answer carries.
func windowsOfAnswer(t *testing.T, answer map[string]any) []map[string]any {
	t.Helper()
	raw, _ := answer["windows"].([]any)
	windows := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		window, _ := item.(map[string]any)
		if window != nil {
			windows = append(windows, window)
		}
	}
	return windows
}

// TestDesktopWindowsKeepInstanceSemantics checks that a single-mode
// application answers one focused window, that a deep link focuses its own
// window, and that two different deep links of a multi-mode application are two
// instances (DASH-C-74, DASH-TC-146).
func TestDesktopWindowsKeepInstanceSemantics(t *testing.T) {
	clock := &windowClock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	front, cookies, _ := startWindowFront(t, clock,
		windowContribution(t, "files", apphost.WindowModeSingle, apphost.DefaultStateNormal, 900, 640, "/files"),
		windowContribution(t, "notes", apphost.WindowModeMulti, apphost.DefaultStateNormal, 800, 600, "/notes"),
	)

	first := openDesktopWindow(t, front, cookies, map[string]any{"app": "files", "path": "/files/one"})
	if !first["isNew"].(bool) {
		t.Fatalf("first open = %v, want a new single-mode window", first)
	}
	firstWindow := desktopWindowOf(t, first)
	if firstWindow["state"] != "normal" || firstWindow["instance"].(float64) != 1 {
		t.Fatalf("opened window = %v, want normal at instance 1", firstWindow)
	}
	second := openDesktopWindow(t, front, cookies, map[string]any{"app": "files", "path": "/files/two"})
	if second["isNew"].(bool) || desktopWindowOf(t, second)["id"] != firstWindow["id"] {
		t.Fatalf("second single-mode open = %v, want the first window focused", second)
	}
	if len(windowsOfAnswer(t, second)) != 1 || second["address"] != "/files/two" {
		t.Fatalf("session after the second open = %v, want one window at the new deep link", second)
	}

	one := openDesktopWindow(t, front, cookies, map[string]any{"app": "notes", "path": "/notes/a"})
	two := openDesktopWindow(t, front, cookies, map[string]any{"app": "notes", "path": "/notes/b"})
	oneWindow, twoWindow := desktopWindowOf(t, one), desktopWindowOf(t, two)
	if !two["isNew"].(bool) || oneWindow["id"] == twoWindow["id"] || oneWindow["instance"] == twoWindow["instance"] {
		t.Fatalf("multi-mode windows = %v and %v, want distinct instances", oneWindow, twoWindow)
	}
	if len(windowsOfAnswer(t, two)) != 3 || two["address"] != "/notes/b" {
		t.Fatalf("session after two deep links = %v, want three windows addressed at the newest", two["windows"])
	}

	// Focusing the first deep link moves the address with it.
	focused, answer := windowRequest(t, front, cookies, http.MethodPost,
		desktopWindowPathOf(oneWindow["id"].(string))+"/focus", nil)
	if focused.StatusCode != http.StatusOK || answer["address"] != "/notes/a" {
		t.Fatalf("focus = %d %v, want the address of the focused window", focused.StatusCode, answer)
	}

	// Closing the focused window moves the address to the window that takes the
	// focus (DASH-C-74).
	closed, answer := windowRequest(t, front, cookies, http.MethodDelete,
		desktopWindowPathOf(oneWindow["id"].(string)), nil)
	if closed.StatusCode != http.StatusOK || answer["address"] != "/notes/b" {
		t.Fatalf("close = %d %v, want the remaining focused window's address", closed.StatusCode, answer)
	}
	if len(windowsOfAnswer(t, answer)) != 2 {
		t.Fatalf("session after close = %v, want two windows left", answer["windows"])
	}
}

// TestDesktopWindowStateTransitions checks the DASH-C-74 transitions and the
// bounds a restore returns to (DASH-TC-145).
func TestDesktopWindowStateTransitions(t *testing.T) {
	clock := &windowClock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	front, cookies, _ := startWindowFront(t, clock,
		windowContribution(t, "files", apphost.WindowModeMulti, apphost.DefaultStateNormal, 800, 600),
	)
	opened := openDesktopWindow(t, front, cookies, map[string]any{"app": "files"})
	windowID := desktopWindowOf(t, opened)["id"].(string)
	original := desktopWindowOf(t, opened)["bounds"]

	state := func(state string, extra map[string]any) map[string]any {
		t.Helper()
		body := map[string]any{"state": state}
		for key, value := range extra {
			body[key] = value
		}
		response, answer := windowRequest(t, front, cookies, http.MethodPut,
			desktopWindowPathOf(windowID)+"/state", body)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("state %s = %d %v", state, response.StatusCode, answer)
		}
		return desktopWindowOf(t, answer)
	}

	if window := state("minimized", nil); window["state"] != "minimized" {
		t.Fatalf("minimize = %v, want the minimized state", window)
	}
	if window := state("normal", nil); window["state"] != "normal" {
		t.Fatalf("restore from minimized = %v, want normal", window)
	}
	if window := state("maximized", nil); window["state"] != "maximized" {
		t.Fatalf("maximize = %v, want maximized", window)
	}
	if window := state("fullscreen", nil); window["state"] != "fullscreen" {
		t.Fatalf("fullscreen = %v, want fullscreen", window)
	}
	viewport := map[string]any{"x": 0, "y": 0, "width": 1440, "height": 900}
	if window := state("snapped", map[string]any{"edge": "left", "viewport": viewport}); window["state"] != "snapped" {
		t.Fatalf("snap = %v, want snapped", window)
	}
	restored := state("normal", nil)
	if restored["state"] != "normal" {
		t.Fatalf("restore from snapped = %v, want normal", restored)
	}
	if !sameBounds(restored["bounds"], original) {
		t.Fatalf("bounds after restore = %v, want the original %v", restored["bounds"], original)
	}

	// A minimized window does not accept another state until it is restored.
	if !sameState(state("minimized", nil), "minimized") {
		t.Fatal("minimize the normal window")
	}
	refused, answer := windowRequest(t, front, cookies, http.MethodPut, desktopWindowPathOf(windowID)+"/state",
		map[string]any{"state": "maximized"})
	if refused.StatusCode != http.StatusConflict {
		t.Fatalf("maximize a minimized window = %d %v, want %d", refused.StatusCode, answer, http.StatusConflict)
	}
}

// TestDesktopWindowCloseVetoesOnce checks that unsaved changes veto one close
// and the second close destroys the window (DASH-TC-145).
func TestDesktopWindowCloseVetoesOnce(t *testing.T) {
	clock := &windowClock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	front, cookies, _ := startWindowFront(t, clock,
		windowContribution(t, "files", apphost.WindowModeSingle, apphost.DefaultStateNormal, 900, 640),
	)
	opened := openDesktopWindow(t, front, cookies, map[string]any{"app": "files"})
	windowID := desktopWindowOf(t, opened)["id"].(string)
	if _, answer := windowRequest(t, front, cookies, http.MethodPut, desktopWindowPathOf(windowID)+"/unsaved",
		map[string]any{"unsaved": true}); answer["window"] == nil {
		t.Fatalf("record the unsaved changes: %v", answer)
	}
	vetoed, answer := windowRequest(t, front, cookies, http.MethodDelete, desktopWindowPathOf(windowID), nil)
	failure, _ := answer["error"].(map[string]any)
	if vetoed.StatusCode != http.StatusConflict || failure["code"] != "close_vetoed" {
		t.Fatalf("first close = %d %v, want the veto", vetoed.StatusCode, answer)
	}
	_, listed := windowRequest(t, front, cookies, http.MethodGet, desktopWindowsPath, nil)
	if len(windowsOfAnswer(t, listed)) != 1 {
		t.Fatalf("session after the veto = %v, want the window still open", listed["windows"])
	}
	closed, answer := windowRequest(t, front, cookies, http.MethodDelete, desktopWindowPathOf(windowID), nil)
	if closed.StatusCode != http.StatusOK || len(windowsOfAnswer(t, answer)) != 0 {
		t.Fatalf("second close = %d %v, want the window destroyed", closed.StatusCode, answer)
	}
}

// TestDesktopWindowCrashAndRestartKeepTheInstanceKey checks that a crashed
// window offers restart and that a restart keeps its instance key while
// renewing the application instance (DASH-TC-147).
func TestDesktopWindowCrashAndRestartKeepTheInstanceKey(t *testing.T) {
	clock := &windowClock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	front, cookies, _ := startWindowFront(t, clock,
		windowContribution(t, "files", apphost.WindowModeMulti, apphost.DefaultStateNormal, 800, 600, "/files"),
	)
	opened := openDesktopWindow(t, front, cookies, map[string]any{"app": "files", "path": "/files/a"})
	window := desktopWindowOf(t, opened)
	windowID := window["id"].(string)
	instanceOne := opened["instance"].(map[string]any)["id"].(string)

	crashed, answer := windowRequest(t, front, cookies, http.MethodPost, desktopWindowPathOf(windowID)+"/crash",
		map[string]any{"reason": "renderer crashed"})
	if crashed.StatusCode != http.StatusOK {
		t.Fatalf("crash = %d %v", crashed.StatusCode, answer)
	}
	crashedWindow := desktopWindowOf(t, answer)
	if crashedWindow["state"] != "error" || crashedWindow["errorReason"] != "renderer crashed" {
		t.Fatalf("crashed window = %v, want the error state with its reason", crashedWindow)
	}

	restarted, answer := windowRequest(t, front, cookies, http.MethodPost, desktopWindowPathOf(windowID)+"/restart", nil)
	if restarted.StatusCode != http.StatusAccepted {
		t.Fatalf("restart = %d %v", restarted.StatusCode, answer)
	}
	restartedWindow := desktopWindowOf(t, answer)
	if restartedWindow["state"] != "normal" || restartedWindow["id"] != windowID ||
		restartedWindow["instance"] != window["instance"] {
		t.Fatalf("restarted window = %v, want the same instance key at normal", restartedWindow)
	}
	instanceTwo := answer["instance"].(map[string]any)["id"].(string)
	if instanceTwo == "" || instanceTwo == instanceOne {
		t.Fatalf("application instance after restart = %q, want a fresh one", instanceTwo)
	}
}

// TestDesktopWindowHeartbeatTimeout checks that a window that stops sending
// heartbeats enters the error state (DASH-C-74).
func TestDesktopWindowHeartbeatTimeout(t *testing.T) {
	clock := &windowClock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	front, cookies, _ := startWindowFront(t, clock,
		windowContribution(t, "files", apphost.WindowModeMulti, apphost.DefaultStateNormal, 800, 600),
	)
	opened := openDesktopWindow(t, front, cookies, map[string]any{"app": "files"})
	windowID := desktopWindowOf(t, opened)["id"].(string)

	// A window that never reported is still loading; only a watched one expires.
	clock.advance(31 * time.Second)
	response, answer := windowRequest(t, front, cookies, http.MethodGet, desktopWindowsPath, nil)
	if response.StatusCode != http.StatusOK || len(windowsOfAnswer(t, answer)) != 1 {
		t.Fatalf("list = %d %v", response.StatusCode, answer)
	}
	if beat, answer := windowRequest(t, front, cookies, http.MethodPost, desktopWindowPathOf(windowID)+"/heartbeat", nil); beat.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat = %d %v", beat.StatusCode, answer)
	}
	clock.advance(31 * time.Second)
	response, answer = windowRequest(t, front, cookies, http.MethodGet, desktopWindowsPath, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list = %d %v", response.StatusCode, answer)
	}
	windows := windowsOfAnswer(t, answer)
	if len(windows) != 1 || windows[0]["state"] != "error" || windows[0]["errorReason"] == "" {
		t.Fatalf("windows after the timeout = %v, want the silent window in its error state", windows)
	}
}

// TestDesktopWindowWorkspaceTaskbar checks that the taskbar lists only the
// current workspace's windows (DASH-TC-147).
func TestDesktopWindowWorkspaceTaskbar(t *testing.T) {
	clock := &windowClock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	front, cookies, _ := startWindowFront(t, clock,
		windowContribution(t, "files", apphost.WindowModeMulti, apphost.DefaultStateNormal, 800, 600),
		windowContribution(t, "notes", apphost.WindowModeMulti, apphost.DefaultStateNormal, 800, 600),
	)
	files := openDesktopWindow(t, front, cookies, map[string]any{"app": "files"})
	if taskbar := taskbarApplications(t, files); taskbar != "files" {
		t.Fatalf("taskbar = %q, want the default workspace's window", taskbar)
	}
	notes := openDesktopWindow(t, front, cookies, map[string]any{"app": "notes", "workspace": "work"})
	notesID := desktopWindowOf(t, notes)["id"].(string)

	if taskbar := taskbarApplications(t, notes); taskbar != "notes" {
		t.Fatalf("taskbar = %q, want only the window opened in the work workspace", taskbar)
	}
	moved, answer := windowRequest(t, front, cookies, http.MethodPut, desktopWindowPathOf(notesID)+"/workspace",
		map[string]any{"workspace": "main"})
	if moved.StatusCode != http.StatusOK || taskbarApplications(t, answer) != "files,notes" {
		// The focused window moved to main, so main's taskbar holds both.
		t.Fatalf("move to the default workspace = %d %v", moved.StatusCode, answer)
	}
	switched, answer := windowRequest(t, front, cookies, http.MethodPost, desktopWorkspacesPath,
		map[string]any{"workspace": "work"})
	if switched.StatusCode != http.StatusOK || taskbarApplications(t, answer) != "" {
		t.Fatalf("switch workspace = %d %v, want an empty taskbar in the vacated workspace", switched.StatusCode, answer)
	}
	if answer["currentWorkspace"] != "work" {
		t.Fatalf("current workspace = %v, want work", answer["currentWorkspace"])
	}
}

// TestDesktopWindowsWriteNoObjectsOrPreferences checks the negative criterion:
// window interaction leaves the control plane untouched (DASH-C-30,
// KOSD-F-19).
func TestDesktopWindowsWriteNoObjectsOrPreferences(t *testing.T) {
	clock := &windowClock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	// Only the login may be a write: everything the window interaction sends is
	// a read or a session operation, and no preference or object route is ever
	// touched (DASH-C-30, KOSD-F-19).
	front, cookies, fake := startWindowFront(t, clock,
		windowContribution(t, "files", apphost.WindowModeMulti, apphost.DefaultStateNormal, 800, 600, "/files"),
	)
	opened := openDesktopWindow(t, front, cookies, map[string]any{"app": "files", "path": "/files/a"})
	windowID := desktopWindowOf(t, opened)["id"].(string)
	windowRequest(t, front, cookies, http.MethodPut, desktopWindowPathOf(windowID)+"/state", map[string]any{"state": "minimized"})
	windowRequest(t, front, cookies, http.MethodPut, desktopWindowPathOf(windowID)+"/state", map[string]any{"state": "normal"})
	windowRequest(t, front, cookies, http.MethodPost, desktopWindowPathOf(windowID)+"/focus", nil)
	windowRequest(t, front, cookies, http.MethodPut, desktopWindowPathOf(windowID)+"/unsaved", map[string]any{"unsaved": true})
	windowRequest(t, front, cookies, http.MethodPost, desktopWorkspacesPath, map[string]any{"workspace": "work"})
	windowRequest(t, front, cookies, http.MethodDelete, desktopWindowPathOf(windowID), nil)

	for _, call := range fake.calls {
		if call.Method == http.MethodGet {
			continue
		}
		if call.Method == http.MethodPost && call.Path == "/api/v1/auth/login" {
			continue
		}
		t.Fatalf("window interaction wrote the control plane: %s %s", call.Method, call.Path)
	}
}

// desktopWindowPathOf addresses one desktop window.
func desktopWindowPathOf(window string) string {
	return "/api/v1/desktop/windows/" + window
}

// sameBounds compares two JSON-encoded rectangles.
func sameBounds(left, right any) bool {
	leftRaw, _ := json.Marshal(left)
	rightRaw, _ := json.Marshal(right)
	return string(leftRaw) == string(rightRaw)
}

// sameState reports whether one desktop window carries a state.
func sameState(window map[string]any, state string) bool {
	return window["state"] == state
}

// taskbarApplications renders the applications a session answer's taskbar
// lists, in order.
func taskbarApplications(t *testing.T, answer map[string]any) string {
	t.Helper()
	raw, _ := answer["taskbar"].([]any)
	names := ""
	for _, item := range raw {
		window, _ := item.(map[string]any)
		if window == nil {
			continue
		}
		if names != "" {
			names += ","
		}
		names += window["application"].(string)
	}
	return names
}

// TestDesktopWindowHeartbeatAfterTimeoutKeepsTheErrorState checks that a beat
// arriving after the deadline does not revive a window: only a restart does
// (DASH-C-74). The answer itself carries the error, so the timeout is visible
// through a real BFF response.
func TestDesktopWindowHeartbeatAfterTimeoutKeepsTheErrorState(t *testing.T) {
	clock := &windowClock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	front, cookies, _ := startWindowFront(t, clock,
		windowContribution(t, "files", apphost.WindowModeMulti, apphost.DefaultStateNormal, 800, 600),
	)
	windowID := desktopWindowOf(t, openDesktopWindow(t, front, cookies, map[string]any{"app": "files"}))["id"].(string)
	if beat, answer := windowRequest(t, front, cookies, http.MethodPost, desktopWindowPathOf(windowID)+"/heartbeat", nil); beat.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat = %d %v", beat.StatusCode, answer)
	}

	clock.advance(31 * time.Second)
	late, answer := windowRequest(t, front, cookies, http.MethodPost, desktopWindowPathOf(windowID)+"/heartbeat", nil)
	if late.StatusCode != http.StatusOK {
		t.Fatalf("late heartbeat = %d %v", late.StatusCode, answer)
	}
	window := desktopWindowOf(t, answer)
	if window["state"] != "error" || window["errorReason"] == "" {
		t.Fatalf("window after the late heartbeat = %v, want its error state", window)
	}
}

// TestDesktopWindowHeartbeatSettlesASilentSibling checks that the timeout of one
// window is settled by another window's beat, so a silent window is observable
// in a real answer (DASH-C-74).
func TestDesktopWindowHeartbeatSettlesASilentSibling(t *testing.T) {
	clock := &windowClock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	front, cookies, _ := startWindowFront(t, clock,
		windowContribution(t, "files", apphost.WindowModeMulti, apphost.DefaultStateNormal, 800, 600),
	)
	first := desktopWindowOf(t, openDesktopWindow(t, front, cookies, map[string]any{"app": "files"}))["id"].(string)
	second := desktopWindowOf(t, openDesktopWindow(t, front, cookies, map[string]any{"app": "files"}))["id"].(string)
	for _, windowID := range []string{first, second} {
		if beat, answer := windowRequest(t, front, cookies, http.MethodPost, desktopWindowPathOf(windowID)+"/heartbeat", nil); beat.StatusCode != http.StatusOK {
			t.Fatalf("heartbeat %s = %d %v", windowID, beat.StatusCode, answer)
		}
	}

	// Only the second window keeps reporting; the first goes silent.
	clock.advance(31 * time.Second)
	_, answer := windowRequest(t, front, cookies, http.MethodPost, desktopWindowPathOf(second)+"/heartbeat", nil)
	found := false
	for _, window := range windowsOfAnswer(t, answer) {
		if window["id"] == first {
			found = true
			if window["state"] != "error" {
				t.Fatalf("silent window = %v, want the error state", window)
			}
		}
	}
	if !found {
		t.Fatalf("answer = %v, want the silent window in its error state", answer)
	}
}

// TestWindowSessionsPruneIdleSessions checks that an unused browser session's
// window set is dropped instead of living for the process lifetime.
func TestWindowSessionsPruneIdleSessions(t *testing.T) {
	clock := &windowClock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	sessions := newWindowSessions(clock.read)
	sessions.get("first").desktop.Open("files")

	clock.advance(windowSessionTTL + time.Minute)
	sessions.get("second")

	if _, found := sessions.sessions["first"]; found {
		t.Fatal("an idle session survived the TTL")
	}
	if _, found := sessions.sessions["second"]; !found {
		t.Fatal("the active session was pruned")
	}
}
