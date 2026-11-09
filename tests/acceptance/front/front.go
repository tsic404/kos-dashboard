// Package front starts the browser front for the window acceptance cases: the
// BFF over a control plane, with the installed applications handed in and one
// account signed in. A case reads the window state the browser session lives in
// (DASH-C-74) without reaching into the front's unexported types.
package front

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
	"github.com/tsic404/kos-dashboard/pkg/bff"
)

const (
	// Username and Password are the account the front signs in.
	Username = "owner"
	Password = "owner-password"
)

// Clock is a movable clock the front's window age judgement reads.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock starts at a fixed instant.
func NewClock() *Clock {
	return &Clock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
}

// Now reads the clock.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward.
func (c *Clock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

// Front is one running browser front with one signed-in session.
type Front struct {
	// Server is the front's address.
	Server *httptest.Server
	// Cookies carries the sealed session and the double-submit token.
	Cookies http.Header
	// Clock is the clock the front judges window heartbeats by.
	Clock *Clock

	t *testing.T
}

// Stub serves the control-plane routes a window session needs and nothing
// else: login trades a token, and the session read resolves the account.
func Stub(t *testing.T) *url.URL {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login":
			_, _ = w.Write([]byte(`{"token":"session-token","username":"` + Username + `","role":"Owner"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/session":
			_, _ = w.Write([]byte(`{"username":"` + Username + `"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"missing","stage":"read"}}`))
		}
	}))
	t.Cleanup(server.Close)
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse the stub control-plane URL: %v", err)
	}
	return base
}

// Start serves the front over the control plane with the installed
// applications and signs the owner in through it.
func Start(t *testing.T, upstream *url.URL, contributions ...apphost.Contribution) *Front {
	t.Helper()
	return StartWithAssets(t, upstream, nil, contributions...)
}

// StartWithAssets is Start with the application assets this process serves, so
// an open answer carries the signed frame address the shell mounts (KOSD-C-73).
func StartWithAssets(t *testing.T, upstream *url.URL, assets apphost.AssetStore, contributions ...apphost.Contribution) *Front {
	t.Helper()
	clock := NewClock()
	front := httptest.NewUnstartedServer(http.NotFoundHandler())
	server, err := bff.New(bff.Config{
		Upstream:      upstream,
		Apps:          apphost.MemorySource(contributions),
		Now:           clock.Now,
		ConsoleOrigin: "http://" + front.Listener.Addr().String(),
		AssetFiles:    assets,
	})
	if err != nil {
		t.Fatalf("build the browser front: %v", err)
	}
	front.Config.Handler = server.Handler()
	front.Start()
	t.Cleanup(front.Close)

	result := &Front{Server: front, Clock: clock, t: t}
	answer := result.Call(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`{"username":"`+Username+`","password":"`+Password+`"}`))
	if answer.Status != http.StatusOK {
		t.Fatalf("login through the front = %d %v", answer.Status, answer.Body)
	}
	cookies := http.Header{}
	pairs := make([]string, 0, 2)
	for _, cookie := range answer.Cookies {
		pairs = append(pairs, cookie.Name+"="+cookie.Value)
		if cookie.Name == "kos_bff_csrf" {
			cookies.Set("X-CSRF-Token", cookie.Value)
		}
	}
	cookies.Set("Cookie", strings.Join(pairs, "; "))
	result.Cookies = cookies
	return result
}

// Answer is one answer the front gave.
type Answer struct {
	// Status is the HTTP status.
	Status int
	// Body is the raw body.
	Body string
	// Decoded is the JSON body when it parsed.
	Decoded map[string]any
	// Cookies are the cookies the answer set.
	Cookies []*http.Cookie
}

// Call sends one request with the session's cookies and CSRF token.
func (f *Front) Call(method, path string, body io.Reader) Answer {
	f.t.Helper()
	request, err := http.NewRequest(method, f.Server.URL+path, body)
	if err != nil {
		f.t.Fatalf("build %s %s: %v", method, path, err)
	}
	request.Header.Set("Cookie", f.Cookies.Get("Cookie"))
	request.Header.Set("X-CSRF-Token", f.Cookies.Get("X-CSRF-Token"))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	answer, err := http.DefaultClient.Do(request)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer answer.Body.Close()
	payload, _ := io.ReadAll(answer.Body)
	decoded := map[string]any{}
	_ = json.Unmarshal(payload, &decoded)
	return Answer{Status: answer.StatusCode, Body: string(payload), Decoded: decoded, Cookies: answer.Cookies()}
}

// CallJSON sends one request with a JSON body.
func (f *Front) CallJSON(method, path string, body any) Answer {
	f.t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		f.t.Fatalf("encode %s %s: %v", method, path, err)
	}
	return f.Call(method, path, strings.NewReader(string(payload)))
}

// Window reads the desktop window out of one answer.
func Window(t *testing.T, answer Answer) map[string]any {
	t.Helper()
	window, _ := answer.Decoded["window"].(map[string]any)
	if window == nil {
		t.Fatalf("answer carries no window: %d %v", answer.Status, answer.Body)
	}
	return window
}

// Windows lists the desktop windows one answer carries.
func Windows(answer Answer) []map[string]any {
	raw, _ := answer.Decoded["windows"].([]any)
	windows := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		window, _ := item.(map[string]any)
		if window != nil {
			windows = append(windows, window)
		}
	}
	return windows
}

// Taskbar lists the applications the answer's taskbar carries, in order.
func Taskbar(answer Answer) string {
	raw, _ := answer.Decoded["taskbar"].([]any)
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

// ErrorCode reads the refusal code of one answer.
func ErrorCode(answer Answer) string {
	failure, _ := answer.Decoded["error"].(map[string]any)
	code, _ := failure["code"].(string)
	return code
}

// Manifest renders one installed application's manifest (KOSD-C-73).
func Manifest(id, mode, defaultState string, width, height int, deepLinks ...string) map[string]any {
	origin := "https://" + id + ".apps.example.test"
	return map[string]any{
		"id": id, "name": id, "version": "1.0.0",
		"icon":  origin + "/assets/icon.svg",
		"entry": origin + "/index.html",
		"window": map[string]any{
			"width": width, "height": height, "minWidth": 320, "minHeight": 240,
			"resizable": true, "mode": mode, "defaultState": defaultState,
		},
		"permissions": []string{},
		"deepLinks":   deepLinks,
	}
}

// Contribution wraps one manifest as an installed extension's contribution.
func Contribution(t *testing.T, manifest map[string]any) apphost.Contribution {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	id, _ := manifest["id"].(string)
	return apphost.Contribution{
		Extension: id + "-ext", Component: id, Phase: apphost.PhaseReady,
		Digest:   "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		Manifest: raw,
	}
}
