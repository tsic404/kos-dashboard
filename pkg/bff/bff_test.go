package bff

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// upstreamCall is one request the fake control plane received.
type upstreamCall struct {
	Method string
	Path   string
	Header http.Header
	Body   string
}

// fakeControlPlane answers the routes the front proxies. Handlers are keyed by
// "METHOD PATH"; an unlisted route answers 404 with the control-plane envelope.
type fakeControlPlane struct {
	calls    []upstreamCall
	handlers map[string]http.HandlerFunc
}

// newFakeControlPlane starts a control plane that records what it receives.
func newFakeControlPlane(t *testing.T, handlers map[string]http.HandlerFunc) (*fakeControlPlane, *httptest.Server) {
	t.Helper()
	fake := &fakeControlPlane{handlers: handlers}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fake.calls = append(fake.calls, upstreamCall{
			Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: string(body),
		})
		if handler, found := fake.handlers[r.Method+" "+r.URL.Path]; found {
			handler(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"missing","stage":"read","retryable":false,"requiresUserAction":false,"sideEffects":"none"}}`))
	}))
	t.Cleanup(server.Close)
	return fake, server
}

// newFront starts the browser front over one upstream.
func newFront(t *testing.T, upstream *httptest.Server) *httptest.Server {
	t.Helper()
	base, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse the upstream URL: %v", err)
	}
	server, err := New(Config{Upstream: base})
	if err != nil {
		t.Fatalf("build the front: %v", err)
	}
	front := httptest.NewServer(server.Handler())
	t.Cleanup(front.Close)
	return front
}

// signIn logs in through the front and returns the session cookies.
func signIn(t *testing.T, front *httptest.Server) http.Header {
	t.Helper()
	response, body := do(t, front, http.MethodPost, "/api/v1/auth/login", "", strings.NewReader(`{"username":"alice","password":"secret"}`))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login = %d %v", response.StatusCode, body)
	}
	if _, found := body["token"]; found {
		t.Fatalf("the login answer carries the control-plane token: %v", body)
	}
	header := http.Header{}
	for _, cookie := range response.Cookies() {
		header.Add("Cookie", cookie.Name+"="+cookie.Value)
	}
	if header.Get("Cookie") == "" {
		t.Fatal("login set no session cookie")
	}
	return header
}

// do sends one request to the front and decodes a JSON answer when it has one.
func do(t *testing.T, front *httptest.Server, method, path string, cookie string, body io.Reader) (*http.Response, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(method, front.URL+path, body)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	if cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
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

// loginHandlers is the control plane the session cases run against.
func loginHandlers() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"POST /api/v1/auth/login": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"session-token","username":"alice","role":"Owner","mustChangePassword":false}`))
		},
	}
}

// cookieValue reads one cookie off a response.
func cookieValue(response *http.Response, name string) string {
	for _, cookie := range response.Cookies() {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

// TestLoginKeepsTheControlPlaneTokenOutOfTheBrowser checks that the browser
// receives a session it cannot read and never the bearer token itself
// (KOSD-C-63, KOSD-F-9).
func TestLoginKeepsTheControlPlaneTokenOutOfTheBrowser(t *testing.T) {
	_, upstream := newFakeControlPlane(t, loginHandlers())
	front := newFront(t, upstream)

	response, body := do(t, front, http.MethodPost, "/api/v1/auth/login", "", strings.NewReader(`{"username":"alice","password":"secret"}`))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login = %d %v", response.StatusCode, body)
	}
	if _, found := body["token"]; found {
		t.Fatalf("the login answer carries the control-plane token: %v", body)
	}
	if body["username"] != "alice" || body["role"] != "Owner" {
		t.Fatalf("login answer = %v, want the session fields of the control plane", body)
	}
	if _, found := body["csrfToken"]; !found {
		t.Fatal("login answer carries no CSRF token")
	}
	session := cookieValue(response, sessionCookie)
	if session == "" {
		t.Fatal("login set no session cookie")
	}
	if strings.Contains(session, "session-token") {
		t.Fatal("the session cookie carries the control-plane token in the clear")
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == sessionCookie && !cookie.HttpOnly {
			t.Fatal("the session cookie is readable by page scripts")
		}
	}
}

// TestProxyAnswersWhatTheControlPlaneAnswers checks that one read through the
// front equals the same read made directly, error envelope included
// (KOSD-C-12).
func TestProxyAnswersWhatTheControlPlaneAnswers(t *testing.T) {
	fake, upstream := newFakeControlPlane(t, map[string]http.HandlerFunc{
		"POST /api/v1/auth/login": loginHandlers()["POST /api/v1/auth/login"],
		"GET /api/v1/resources/Application": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[{"ref":{"kind":"Application","namespace":"default","name":"media"},"observedAt":"2026-03-01T12:00:00Z","stale":false,"object":{"metadata":{"name":"media"}}}]}`))
		},
		"GET /api/v1/resources/Hidden": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"permission_denied","message":"hidden","stage":"authorize","retryable":false,"requiresUserAction":false,"sideEffects":"none"}}`))
		},
	})
	front := newFront(t, upstream)
	cookies := signIn(t, front)

	response, body := do(t, front, http.MethodGet, "/api/v1/resources/Application", cookies.Get("Cookie"), nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("proxied read = %d %v", response.StatusCode, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("proxied read items = %v, want the one the control plane served", body["items"])
	}
	if got := lastCall(fake, "GET /api/v1/resources/Application").Header.Get("Authorization"); got != "Bearer session-token" {
		t.Fatalf("upstream authorization = %q, want the session's bearer token", got)
	}

	response, body = do(t, front, http.MethodGet, "/api/v1/resources/Hidden", cookies.Get("Cookie"), nil)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("proxied refusal = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
	failure, _ := body["error"].(map[string]any)
	if failure["code"] != "permission_denied" || failure["stage"] != "authorize" {
		t.Fatalf("proxied refusal envelope = %v, want the control plane's own", body)
	}
}

// TestDesktopPageSurvivesAnUnavailableReadState checks the desktop route answers
// 200 with all four items when the control plane cannot serve the account's read
// state: the preference store is auxiliary, so its refusal costs only the
// notification read state (KOSD-C-61, DASH-C-27).
func TestDesktopPageSurvivesAnUnavailableReadState(t *testing.T) {
	list := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}
	_, upstream := newFakeControlPlane(t, map[string]http.HandlerFunc{
		"POST /api/v1/auth/login":            loginHandlers()["POST /api/v1/auth/login"],
		"GET /api/v1/resources/DesktopEntry": list,
		"GET /api/v1/resources/Application":  list,
		"GET /api/v1/resources/Endpoint":     list,
		"GET /api/v1/events": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"observedAt":"2026-03-01T12:00:00Z","window":"1h","events":[{"name":"media.1","namespace":"default","type":"Normal","reason":"Created","message":"created","count":1,"lastSeen":"2026-03-01T11:59:00Z","involvedObject":{"kind":"Application","namespace":"default","name":"media"}}]}`))
		},
		"GET /api/v1/preferences/kos-dashboard": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"database_unavailable","message":"控制面数据库暂不可用，请稍后重试","stage":"apply","retryable":true,"requiresUserAction":false,"sideEffects":"none"}}`))
		},
	})
	front := newFront(t, upstream)
	cookies := signIn(t, front)

	response, body := do(t, front, http.MethodGet, "/api/v1/desktop", cookies.Get("Cookie"), nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("desktop page = %d %v, want %d although the read state is unavailable",
			response.StatusCode, body, http.StatusOK)
	}
	items, _ := body["items"].([]any)
	if len(items) != 4 {
		t.Fatalf("desktop items = %v, want the four DASH-C-52 declares", body["items"])
	}
	notifications := map[string]any{}
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item["id"] == "notifications" {
			notifications = item
		}
	}
	value, _ := notifications["value"].(map[string]any)
	if value["readStateUnavailable"] != true || value["readStateReason"] != "控制面数据库暂不可用，请稍后重试" {
		t.Fatalf("notification item = %v, want the read state marked unavailable", notifications)
	}
	for _, absent := range []string{"readThrough", "unread"} {
		if _, found := value[absent]; found {
			t.Fatalf("notification item carries %q although the read state is unavailable: %v", absent, value)
		}
	}
	if reason, _ := notifications["reason"].(string); reason == "" {
		t.Fatalf("notification item = %v, want why it carries no read state", notifications)
	}
}

// TestWriteNeedsTheCSRFToken checks that the session cookie alone cannot carry
// a state-changing request (KOSD-C-22).
func TestWriteNeedsTheCSRFToken(t *testing.T) {
	_, upstream := newFakeControlPlane(t, map[string]http.HandlerFunc{
		"POST /api/v1/auth/login": loginHandlers()["POST /api/v1/auth/login"],
		"POST /api/v1/action-requests": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"accepted":true}`))
		},
	})
	front := newFront(t, upstream)
	response, body := do(t, front, http.MethodPost, "/api/v1/auth/login", "", strings.NewReader(`{"username":"alice","password":"secret"}`))
	csrf := cookieValue(response, csrfCookie)
	session := cookieValue(response, sessionCookie)
	if csrf == "" || session == "" {
		t.Fatalf("login cookies = %v", response.Cookies())
	}
	cookies := sessionCookie + "=" + session + "; " + csrfCookie + "=" + csrf

	response, body = do(t, front, http.MethodPost, "/api/v1/action-requests", cookies, strings.NewReader(`{}`))
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("write without the CSRF header = %d %v, want %d", response.StatusCode, body, http.StatusForbidden)
	}

	request, _ := http.NewRequest(http.MethodPost, front.URL+"/api/v1/action-requests", strings.NewReader(`{}`))
	request.Header.Set("Cookie", cookies)
	request.Header.Set(csrfHeader, csrf)
	allowed, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("write with the CSRF header: %v", err)
	}
	defer allowed.Body.Close()
	if allowed.StatusCode != http.StatusAccepted {
		t.Fatalf("write with the CSRF header = %d, want %d", allowed.StatusCode, http.StatusAccepted)
	}
}

// TestShellCarriesThePolicyAndRoutesUnknownPaths checks the document policy and
// that the shell routes its own paths (DASH-C-22, KOSD-F-18).
func TestShellCarriesThePolicyAndRoutesUnknownPaths(t *testing.T) {
	_, upstream := newFakeControlPlane(t, nil)
	front := newFront(t, upstream)

	response, err := http.Get(front.URL + "/")
	if err != nil {
		t.Fatalf("read the shell: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("shell = %d, want %d", response.StatusCode, http.StatusOK)
	}
	policy := response.Header.Get("Content-Security-Policy")
	for _, fragment := range []string{"default-src 'self'", "frame-ancestors 'self'", "object-src 'none'"} {
		if !strings.Contains(policy, fragment) {
			t.Fatalf("policy = %q, want it to carry %q", policy, fragment)
		}
	}
	if response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("the shell is served without nosniff")
	}

	unknown, err := http.Get(front.URL + "/desktop/apps/1")
	if err != nil {
		t.Fatalf("read an unknown shell path: %v", err)
	}
	defer unknown.Body.Close()
	if unknown.StatusCode != http.StatusOK {
		t.Fatalf("unknown shell path = %d, want the entry document", unknown.StatusCode)
	}
}

// TestShellServesItsModulesAndRefusesMissingFiles checks that the browser
// entry is served as the document, its modules as JavaScript, and that a
// missing module answers 404 instead of the entry document.
func TestShellServesItsModulesAndRefusesMissingFiles(t *testing.T) {
	_, upstream := newFakeControlPlane(t, nil)
	front := newFront(t, upstream)

	entry, err := http.Get(front.URL + "/")
	if err != nil {
		t.Fatalf("read the entry document: %v", err)
	}
	defer entry.Body.Close()
	document, _ := io.ReadAll(entry.Body)
	if !strings.Contains(string(document), `/shell/main.js`) {
		t.Fatalf("entry document = %s, want it to load the shell module", document)
	}

	module, err := http.Get(front.URL + "/shell/main.js")
	if err != nil {
		t.Fatalf("read the shell module: %v", err)
	}
	defer module.Body.Close()
	if module.StatusCode != http.StatusOK {
		t.Fatalf("shell module = %d, want %d", module.StatusCode, http.StatusOK)
	}
	if contentType := module.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/javascript") {
		t.Fatalf("shell module content type = %q, want a JavaScript type", contentType)
	}

	missing, err := http.Get(front.URL + "/shell/missing.js")
	if err != nil {
		t.Fatalf("read a missing module: %v", err)
	}
	defer missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing module = %d, want %d", missing.StatusCode, http.StatusNotFound)
	}
}

// TestStreamEmitsReadEvents checks that the stream delivers the read's events
// and their observation time (KOSD-C-13).
func TestStreamEmitsReadEvents(t *testing.T) {
	_, upstream := newFakeControlPlane(t, map[string]http.HandlerFunc{
		"POST /api/v1/auth/login": loginHandlers()["POST /api/v1/auth/login"],
		"GET /api/v1/events": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"observedAt":"2026-03-01T12:00:00Z","window":"1h","events":[{"name":"media.1","namespace":"default","type":"Normal","reason":"Created","message":"created","count":1,"lastSeen":"2026-03-01T11:59:00Z","involvedObject":{"kind":"Application","namespace":"default","name":"media"}}]}`))
		},
	})
	front := newFront(t, upstream)
	cookies := signIn(t, front)

	request, _ := http.NewRequest(http.MethodGet, front.URL+"/api/v1/stream", nil)
	request.Header.Set("Cookie", cookies.Get("Cookie"))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("open the stream: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("stream = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("stream content type = %q", contentType)
	}
	frame := readFrame(t, response.Body)
	if !strings.Contains(frame, "event: events") || !strings.Contains(frame, `"name":"media.1"`) {
		t.Fatalf("first frame = %q, want the read's event", frame)
	}
	if !strings.Contains(frame, `"observedAt":"2026-03-01T12:00:00Z"`) {
		t.Fatalf("first frame = %q, want the read's observation time", frame)
	}
}

// TestStreamComparesEachReadWithThePreviousOne checks that the stream rebuilds
// its de-duplication from every read: the set follows the event window instead
// of growing with the connection, and an event that left the window and came
// back reaches the shell again (KOSD-C-13).
func TestStreamComparesEachReadWithThePreviousOne(t *testing.T) {
	// Round five loses media.2 and round seven brings it back; only the first,
	// third and seventh reads carry something the read before did not.
	rounds := [][]string{
		{"media.1"},
		{"media.1"},
		{"media.1", "media.2"},
		{"media.1", "media.2"},
		{"media.1"},
		{"media.1"},
		{"media.1", "media.2"},
	}
	var round atomic.Int32
	_, upstream := newFakeControlPlane(t, map[string]http.HandlerFunc{
		"POST /api/v1/auth/login": loginHandlers()["POST /api/v1/auth/login"],
		"GET /api/v1/events": func(w http.ResponseWriter, _ *http.Request) {
			index := min(int(round.Add(1))-1, len(rounds)-1)
			events := make([]string, 0, len(rounds[index]))
			for _, name := range rounds[index] {
				events = append(events, fmt.Sprintf(`{"name":%q,"namespace":"default","type":"Normal","reason":"Created","message":"created","count":1,"lastSeen":"2026-03-01T11:59:00Z","involvedObject":{"kind":"Application","namespace":"default","name":"media"}}`, name))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"observedAt":"2026-03-01T12:00:00Z","window":"1h","events":[` + strings.Join(events, ",") + `]}`))
		},
	})
	base, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse the upstream URL: %v", err)
	}
	server, err := New(Config{Upstream: base, PollInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("build the front: %v", err)
	}
	front := httptest.NewServer(server.Handler())
	t.Cleanup(front.Close)
	cookies := signIn(t, front)

	request, _ := http.NewRequest(http.MethodGet, front.URL+"/api/v1/stream", nil)
	request.Header.Set("Cookie", cookies.Get("Cookie"))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("open the stream: %v", err)
	}
	defer response.Body.Close()

	if frame := readFrame(t, response.Body); !strings.Contains(frame, "media.1") || strings.Contains(frame, "media.2") {
		t.Fatalf("first frame = %q, want the first read alone", frame)
	}
	if frame := readFrame(t, response.Body); !strings.Contains(frame, "media.2") {
		t.Fatalf("second frame = %q, want the read that added an event", frame)
	}
	// media.2 left the window and returned; a set accumulated for the life of
	// the connection would call it seen and send nothing.
	if frame := readFrame(t, response.Body); !strings.Contains(frame, "media.2") {
		t.Fatalf("third frame = %q, want the event that returned to the window", frame)
	}
}

// readFrame reads one server-sent event from a stream.
func readFrame(t *testing.T, body io.Reader) string {
	t.Helper()
	buffer := make([]byte, 4096)
	collected := strings.Builder{}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		count, err := body.Read(buffer)
		collected.Write(buffer[:count])
		if strings.Contains(collected.String(), "\n\n") {
			return collected.String()
		}
		if err != nil {
			t.Fatalf("read the stream: %v", err)
		}
	}
	t.Fatalf("no event frame within the deadline: %q", collected.String())
	return ""
}

// lastCall returns the last request the fake control plane received for one
// method and path.
func lastCall(fake *fakeControlPlane, route string) upstreamCall {
	for index := len(fake.calls) - 1; index >= 0; index-- {
		if fake.calls[index].Method+" "+fake.calls[index].Path == route {
			return fake.calls[index]
		}
	}
	return upstreamCall{}
}
