// Package dashtc164 verifies DASH-TC-164: a window reads the control plane
// through the BFF with its own token, and the answer matches a direct call's.
// The preflight needs neither cookie nor token and names only GET, HEAD and
// Authorization; inactive or undeclared origins get neither cross-origin
// permission nor protected content; a refused token stays readable by the
// correct origin; undecidable validity or scope answers service unavailable on
// both sides, never an empty success or another class (DASH-AC-72, DASH-C-75,
// KOSD-C-86, KOSD-C-87).
package dashtc164

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/tsic404/kos-dashboard/tests/acceptance/front"
)

const (
	// The declared applications: the one whose window is opened and therefore
	// active, another one that declares an entry origin but never opens a
	// window, and an origin nothing declares (KOSD-C-87).
	readerAppID   = "files"
	readerOrigin  = "https://files.apps.example.test"
	otherAppID    = "notes"
	otherOrigin   = "https://notes.apps.example.test"
	ghostOrigin   = "https://ghost.example.test"
	readPath      = "/api/v1/resources/Application"
	sessionToken  = "session-token"
	protectedBody = `{"items":[{"ref":{"kind":"Application","namespace":"default","name":"files"},` +
		`"observedAt":"2026-11-17T00:00:00Z","stale":false,` +
		`"object":{"metadata":{"name":"files"}}}]}`
	refusalBody = `{"error":{"code":"unauthenticated","message":"应用令牌无效或已过期",` +
		`"stage":"authorize","retryable":false,"requiresUserAction":false,"sideEffects":"none"}}`
	// unavailableBody is the control-plane answer when it cannot determine the
	// token's validity or authorization: the service-unavailable class, not one
	// of the other two (KOSD-C-86).
	unavailableBody = `{"error":{"code":"service_unavailable","message":"无法确定令牌有效性或授权",` +
		`"stage":"authorize","retryable":true,"requiresUserAction":false,"sideEffects":"none"}}`
)

// manifestOf renders one declared window application.
func manifestOf(id, origin string) map[string]any {
	return map[string]any{
		"id": id, "name": id, "version": "1.0.0",
		"icon":  origin + "/assets/icon.svg",
		"entry": origin + "/index.html",
		"window": map[string]any{
			"width": 1024, "height": 720, "minWidth": 320, "minHeight": 240,
			"resizable": true, "mode": "single", "defaultState": "normal",
		},
		"permissions": []string{},
	}
}

// upstreamCall is one request the control plane received.
type upstreamCall struct {
	method        string
	path          string
	authorization string
	origin        string
	cookie        string
}

// controlPlane is the controlled control-plane fixture: it answers the console
// login and session reads, records every read of the protected route and judges
// the application token exactly as KOSD-C-85 requires (KOSD-C-86).
type controlPlane struct {
	mu       sync.Mutex
	token    string
	degraded bool
	calls    []upstreamCall
	server   *httptest.Server
}

// newControlPlane starts the fixture.
func newControlPlane(t *testing.T) *controlPlane {
	t.Helper()
	control := &controlPlane{}
	control.server = httptest.NewServer(http.HandlerFunc(control.serve))
	t.Cleanup(control.server.Close)
	return control
}

// trust marks the one application token the control plane accepts.
func (c *controlPlane) trust(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = token
}

// degrade makes the control plane unable to determine token validity or
// authorization, so the protected read answers the service-unavailable class
// instead of a verdict (KOSD-C-86).
func (c *controlPlane) degrade() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.degraded = true
}

// url is the fixture's base address.
func (c *controlPlane) url(t *testing.T) *url.URL {
	t.Helper()
	parsed, err := url.Parse(c.server.URL)
	if err != nil {
		t.Fatalf("parse the control-plane URL: %v", err)
	}
	return parsed
}

// last returns the newest recorded call of one route.
func (c *controlPlane) last(path string) (upstreamCall, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := len(c.calls) - 1; index >= 0; index-- {
		if c.calls[index].path == path {
			return c.calls[index], true
		}
	}
	return upstreamCall{}, false
}

// serve answers one control-plane request.
func (c *controlPlane) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login":
		_, _ = w.Write([]byte(`{"token":"` + sessionToken + `","username":"` + front.Username + `","role":"Owner"}`))
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/session":
		_, _ = w.Write([]byte(`{"username":"` + front.Username + `"}`))
	case r.URL.Path == readPath:
		c.mu.Lock()
		c.calls = append(c.calls, upstreamCall{
			method: r.Method, path: r.URL.Path,
			authorization: r.Header.Get("Authorization"),
			origin:        r.Header.Get("Origin"),
			cookie:        r.Header.Get("Cookie"),
		})
		trusted := "Bearer " + c.token
		degraded := c.degraded
		c.mu.Unlock()
		if degraded {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(unavailableBody))
			return
		}
		if r.Header.Get("Authorization") != trusted {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(refusalBody))
			return
		}
		_, _ = w.Write([]byte(protectedBody))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"missing","stage":"read"}}`))
	}
}

// appToken opens one application window through the console session and runs
// the handshake, which is the only delivery of a window's application token
// (KOSD-C-75).
func appToken(t *testing.T, browser *front.Front, appID, origin string) string {
	t.Helper()
	opened := browser.CallJSON(http.MethodPost, "/api/v1/apps/"+appID+"/windows", nil)
	if opened.Status != http.StatusAccepted {
		t.Fatalf("open %s = %d %v, want %d", appID, opened.Status, opened.Body, http.StatusAccepted)
	}
	window, _ := opened.Decoded["window"].(map[string]any)
	id, _ := window["id"].(string)
	nonce, _ := window["handshakeNonce"].(string)
	if id == "" || nonce == "" {
		t.Fatalf("open answer carries no window instance: %v", opened.Decoded)
	}
	handshake := browser.CallJSON(http.MethodPost, "/api/v1/windows/"+id+"/handshake", map[string]any{
		"origin": origin, "nonce": nonce, "message": []byte(`{"type":"ready"}`),
	})
	if handshake.Status != http.StatusOK {
		t.Fatalf("handshake of %s = %d %v, want %d", appID, handshake.Status, handshake.Body, http.StatusOK)
	}
	token, _ := handshake.Decoded["token"].(string)
	if token == "" {
		t.Fatalf("handshake of %s carried no token: %v", appID, handshake.Decoded)
	}
	return token
}

// answer is one answer a caller read.
type answer struct {
	status  int
	body    string
	decoded map[string]any
	header  http.Header
}

// call sends one request with exactly the headers a browser or a direct caller
// sends: a window read carries no console Cookie and no CSRF token.
func call(t *testing.T, base, method, path string, header http.Header) answer {
	t.Helper()
	request, err := http.NewRequest(method, base+path, nil)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	for name, values := range header {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	decoded := map[string]any{}
	_ = json.Unmarshal(payload, &decoded)
	return answer{status: response.StatusCode, body: string(payload), decoded: decoded, header: response.Header}
}

// allows reports whether one answer carries the exact cross-origin permission.
func allows(response answer, origin string) bool {
	return response.header.Get("Access-Control-Allow-Origin") == origin
}

// varyHasOrigin reports whether the answer keeps the sources in separate caches.
func varyHasOrigin(header http.Header) bool {
	for _, value := range header.Values("Vary") {
		for _, item := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(item), "Origin") {
				return true
			}
		}
	}
	return false
}

// errorCode reads the stable code of one error envelope.
func errorCode(decoded map[string]any) string {
	failure, _ := decoded["error"].(map[string]any)
	code, _ := failure["code"].(string)
	return code
}

// names reports whether one header names exactly the wanted values.
func names(header http.Header, name string, wanted ...string) bool {
	found := map[string]bool{}
	for _, value := range header.Values(name) {
		for _, item := range strings.Split(value, ",") {
			trimmed := strings.TrimSpace(item)
			if trimmed != "" {
				found[strings.ToUpper(trimmed)] = true
			}
		}
	}
	if len(found) != len(wanted) {
		return false
	}
	for _, want := range wanted {
		if !found[strings.ToUpper(want)] {
			return false
		}
	}
	return true
}

// TestTC164ForwardsTheApplicationTokenAndPermitsOnlyItsOwnOrigin is the case
// DASH-TC-164 describes.
func TestTC164ForwardsTheApplicationTokenAndPermitsOnlyItsOwnOrigin(t *testing.T) {
	control := newControlPlane(t)
	browser := front.StartWithAssets(t, control.url(t), nil,
		front.Contribution(t, manifestOf(readerAppID, readerOrigin)),
		front.Contribution(t, manifestOf(otherAppID, otherOrigin)),
	)
	token := appToken(t, browser, readerAppID, readerOrigin)
	control.trust(token)

	// The preflight names the one source the application declared and the two
	// read methods with the Authorization header; it needs neither a console
	// cookie nor an application token (KOSD-C-87).
	preflight := call(t, browser.Server.URL, http.MethodOptions, readPath, http.Header{
		"Origin":                         {readerOrigin},
		"Access-Control-Request-Method":  {http.MethodGet},
		"Access-Control-Request-Headers": {"authorization"},
	})
	if preflight.status != http.StatusNoContent {
		t.Fatalf("preflight = %d %v, want %d", preflight.status, preflight.body, http.StatusNoContent)
	}
	if !allows(preflight, readerOrigin) {
		t.Fatalf("preflight origin = %q, want exactly %q", preflight.header.Get("Access-Control-Allow-Origin"), readerOrigin)
	}
	if !names(preflight.header, "Access-Control-Allow-Methods", http.MethodGet, http.MethodHead) {
		t.Fatalf("preflight methods = %q, want exactly GET and HEAD",
			preflight.header.Get("Access-Control-Allow-Methods"))
	}
	if !names(preflight.header, "Access-Control-Allow-Headers", "Authorization") {
		t.Fatalf("preflight headers = %q, want exactly Authorization",
			preflight.header.Get("Access-Control-Allow-Headers"))
	}
	if preflight.header.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("the preflight allows credentialed cross-origin access")
	}
	if !varyHasOrigin(preflight.header) {
		t.Fatalf("preflight Vary = %v, want the source kept in the cache key", preflight.header.Values("Vary"))
	}

	// A preflight grants nothing: the actual request is judged on its own and a
	// read without a token is refused (KOSD-C-87).
	unauthenticated := call(t, browser.Server.URL, http.MethodGet, readPath, http.Header{
		"Origin": {readerOrigin},
	})
	if unauthenticated.status != http.StatusUnauthorized {
		t.Fatalf("read without a token = %d %v, want %d",
			unauthenticated.status, unauthenticated.body, http.StatusUnauthorized)
	}
	if _, carries := unauthenticated.decoded["items"]; carries {
		t.Fatalf("read without a token carries protected content: %v", unauthenticated.decoded)
	}

	// The actual read carries no console Cookie: the application token is the
	// identity, and the answer names the exact source (DASH-C-75, KOSD-C-86).
	read := call(t, browser.Server.URL, http.MethodGet, readPath, http.Header{
		"Origin":        {readerOrigin},
		"Authorization": {"Bearer " + token},
	})
	if read.status != http.StatusOK || read.body != protectedBody {
		t.Fatalf("application read = %d %v, want the control-plane answer %s", read.status, read.body, protectedBody)
	}
	if !allows(read, readerOrigin) {
		t.Fatalf("application read origin = %q, want exactly %q",
			read.header.Get("Access-Control-Allow-Origin"), readerOrigin)
	}
	if read.header.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("the application read allows credentialed cross-origin access")
	}
	if !varyHasOrigin(read.header) {
		t.Fatalf("application read Vary = %v, want the source kept in the cache key", read.header.Values("Vary"))
	}
	// The control plane received the application token itself and the request
	// source, never a console session (DASH-C-75).
	received, found := control.last(readPath)
	if !found {
		t.Fatal("the application read never reached the control plane")
	}
	if received.authorization != "Bearer "+token {
		t.Fatalf("upstream Authorization = %q, want the application token unchanged", received.authorization)
	}
	if received.origin != readerOrigin {
		t.Fatalf("upstream Origin = %q, want the request source unchanged", received.origin)
	}
	if received.cookie != "" {
		t.Fatalf("upstream Cookie = %q, want no console cookie", received.cookie)
	}
	// A direct call with the same identity answers the same data (DASH-C-75).
	direct := call(t, control.server.URL, http.MethodGet, readPath, http.Header{
		"Origin": {readerOrigin}, "Authorization": {"Bearer " + token},
	})
	if direct.status != read.status || direct.body != read.body {
		t.Fatalf("direct read = %d %v, want the BFF answer %d %v", direct.status, direct.body, read.status, read.body)
	}

	// Another application's origin, declared but not activated, and an origin
	// nothing declares get no cross-origin permission and no protected content
	// (KOSD-C-87).
	for _, origin := range []string{otherOrigin, ghostOrigin} {
		foreignPreflight := call(t, browser.Server.URL, http.MethodOptions, readPath, http.Header{
			"Origin":                        {origin},
			"Access-Control-Request-Method": {http.MethodGet},
		})
		if foreignPreflight.header.Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("preflight from %s = %q, want no cross-origin permission",
				origin, foreignPreflight.header.Get("Access-Control-Allow-Origin"))
		}
		foreignRead := call(t, browser.Server.URL, http.MethodGet, readPath, http.Header{
			"Origin": {origin}, "Authorization": {"Bearer " + token},
		})
		if foreignRead.header.Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("read from %s = %q, want no cross-origin permission",
				origin, foreignRead.header.Get("Access-Control-Allow-Origin"))
		}
		if foreignRead.status == http.StatusOK || strings.Contains(foreignRead.body, `"name":"files"`) {
			t.Fatalf("read from %s = %d %v, want no protected content", origin, foreignRead.status, foreignRead.body)
		}
	}

	// A token the control plane refuses stays readable by the correct source,
	// with the control plane's own envelope fields unchanged (DASH-C-75,
	// KOSD-C-86).
	forged := call(t, browser.Server.URL, http.MethodGet, readPath, http.Header{
		"Origin": {readerOrigin}, "Authorization": {"Bearer forged-token"},
	})
	directForged := call(t, control.server.URL, http.MethodGet, readPath, http.Header{
		"Origin": {readerOrigin}, "Authorization": {"Bearer forged-token"},
	})
	if forged.status != http.StatusUnauthorized {
		t.Fatalf("refused token = %d %v, want %d", forged.status, forged.body, http.StatusUnauthorized)
	}
	if forged.body != directForged.body {
		t.Fatalf("refused token = %v, want the control-plane envelope unchanged %v", forged.body, directForged.body)
	}
	if !allows(forged, readerOrigin) {
		t.Fatalf("refused token origin = %q, want %q so the correct source can read it",
			forged.header.Get("Access-Control-Allow-Origin"), readerOrigin)
	}
	if forged.header.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("the refused-token answer allows credentialed cross-origin access")
	}

	// When the control plane cannot determine token validity or authorization,
	// the BFF forwards that service-unavailable answer unchanged: the read is
	// neither let through nor turned into a successful empty result, and the
	// answer is not downgraded to one of the other failure classes (DASH-AC-72,
	// DASH-TC-164, KOSD-C-86).
	control.degrade()
	unavailable := call(t, browser.Server.URL, http.MethodGet, readPath, http.Header{
		"Origin": {readerOrigin}, "Authorization": {"Bearer " + token},
	})
	directUnavailable := call(t, control.server.URL, http.MethodGet, readPath, http.Header{
		"Origin": {readerOrigin}, "Authorization": {"Bearer " + token},
	})
	if unavailable.status != http.StatusServiceUnavailable || directUnavailable.status != http.StatusServiceUnavailable {
		t.Fatalf("unavailable read = %d (direct %d), want %d on both sides",
			unavailable.status, directUnavailable.status, http.StatusServiceUnavailable)
	}
	if unavailable.body != directUnavailable.body {
		t.Fatalf("unavailable read = %v, want the control-plane envelope unchanged %v",
			unavailable.body, directUnavailable.body)
	}
	if _, carries := unavailable.decoded["items"]; carries {
		t.Fatalf("unavailable read carries an item list: %v", unavailable.decoded)
	}
	if code := errorCode(unavailable.decoded); code == "" {
		t.Fatalf("unavailable read carries no stable error code: %v", unavailable.decoded)
	} else if code == "unauthenticated" || code == "permission_denied" {
		t.Fatalf("unavailable read was downgraded to %q", code)
	}
	if !allows(unavailable, readerOrigin) {
		t.Fatalf("unavailable read origin = %q, want %q so the correct source can read it",
			unavailable.header.Get("Access-Control-Allow-Origin"), readerOrigin)
	}
	if unavailable.header.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("the unavailable answer allows credentialed cross-origin access")
	}

	// The console keeps its own session path: a session read still needs the
	// cookie, and an application token is no substitute for it (KOSD-C-86).
	if supplied := call(t, browser.Server.URL, http.MethodGet, "/api/v1/dock", http.Header{}); supplied.status != http.StatusUnauthorized {
		t.Fatalf("session read without a cookie = %d %v, want %d", supplied.status, supplied.body, http.StatusUnauthorized)
	}
}
