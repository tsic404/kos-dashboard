// Package dashtc163 verifies DASH-TC-163: a page is no iframe entry — the
// navigation lists pages alone while an `App` contribution appears only on the
// launcher and the dock, no page answer carries a frame, an account without the
// page's permission sees no entry and its direct call answers exactly what any
// other unauthorized request gets, and a page-only extension never receives a
// window application (DASH-AC-71, DASH-C-22, KOSD-C-18, KOSD-C-59).
package dashtc163

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/tsic404/kosd/pkg/extension"
	"github.com/tsic404/kosd/pkg/write"
	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
	"github.com/tsic404/kos-dashboard/pkg/bff"
)

const (
	componentName = "reports"
	pageExtID     = "reports-ext"
	pageID        = "reports"
	actionID      = "mark-viewed"
	actionPath    = "/api/v1/preferences/" + pageExtID + "/view"
	appExtID      = "notes-ext"
	appComponent  = "notes"
	appID         = "notes"
	appName       = "笔记"
	appOrigin     = "https://notes.apps.example.test"
	ownerName     = "owner"
	ownerPassword = "owner-password"
	guestName     = "guest"
	guestPassword = "guest-password"
	digest        = "sha256:0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"
)

// pageDeclaration is the page-only extension: one page that requires Member,
// with one owner-only write action. It declares no `App` (KOSD-C-18).
func pageDeclaration() extension.Declaration {
	return extension.Declaration{
		Identifier: pageExtID,
		Version:    "1.0.0",
		Contributions: extension.Contributions{
			Pages: []extension.Page{{
				ID: pageID, Title: "报表", Location: "panel",
				RequiredRole: string(write.RoleMember),
				Items: []extension.PageItem{{
					ID: "reports", Label: "报表", Source: extension.PageSourceLocal,
					Store: "reports.view", Action: actionID,
				}},
				Actions: []extension.Action{{
					ID: actionID, Method: http.MethodPut,
					RequiredRole: string(write.RoleOwner), Path: actionPath,
				}},
			}},
		},
		Permissions: []extension.Permission{
			{Contribution: pageID, RequiredRole: string(write.RoleMember)},
			{Contribution: pageExtID, RequiredRole: string(write.RoleOwner)},
		},
	}
}

// appManifest is the `App` contribution of the other extension: a window
// application with its own origin, which is the one place a frame comes from
// (KOSD-C-18, KOSD-C-75).
func appManifest() map[string]any {
	return map[string]any{
		"id": appID, "name": appName, "version": "1.0.0",
		"icon":  appOrigin + "/assets/icon.svg",
		"entry": appOrigin + "/index.html",
		"window": map[string]any{
			"width": 900, "height": 700, "minWidth": 320, "minHeight": 240,
			"resizable": true, "mode": "single", "defaultState": "normal",
		},
		"permissions": []string{},
	}
}

// registry is the application registry the front reads; it holds the `App`
// contribution of the second extension.
type registry struct {
	mu            sync.Mutex
	contributions []apphost.Contribution
}

// append installs one contribution.
func (r *registry) append(contribution apphost.Contribution) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.contributions = append(r.contributions, contribution)
}

// items returns the installed contributions.
func (r *registry) items() []apphost.Contribution {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]apphost.Contribution(nil), r.contributions...)
}

// startControlPlane composes the harness control plane with the application
// registry route the front reads.
func startControlPlane(t *testing.T, shell *harness.Shell, apps *registry) *httptest.Server {
	t.Helper()
	target, err := url.Parse(shell.Accounts.HTTP.URL)
	if err != nil {
		t.Fatalf("parse the control-plane URL: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/apps", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": apps.items(), "notReady": []any{}})
	})
	mux.Handle("/", proxy)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// startFront serves the browser front over the composed control plane and logs
// one account in through it.
func startFront(t *testing.T, control *httptest.Server, username, password string) (*httptest.Server, http.Header) {
	t.Helper()
	upstream, err := url.Parse(control.URL)
	if err != nil {
		t.Fatalf("parse the composed URL: %v", err)
	}
	server, err := bff.New(bff.Config{Upstream: upstream})
	if err != nil {
		t.Fatalf("build the browser front: %v", err)
	}
	front := httptest.NewServer(server.Handler())
	t.Cleanup(front.Close)
	response := call(t, front, http.MethodPost, "/api/v1/auth/login", http.Header{},
		strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`), "")
	if response.status != http.StatusOK {
		t.Fatalf("login through the front = %d %v", response.status, response.body)
	}
	cookies := http.Header{}
	pairs := make([]string, 0, 2)
	for _, cookie := range response.cookies {
		pairs = append(pairs, cookie.Name+"="+cookie.Value)
	}
	cookies.Set("Cookie", strings.Join(pairs, "; "))
	for _, cookie := range response.cookies {
		if cookie.Name == "kos_bff_csrf" {
			cookies.Set("X-CSRF-Token", cookie.Value)
		}
	}
	if cookies.Get("X-CSRF-Token") == "" {
		t.Fatal("login set no CSRF token")
	}
	return front, cookies
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
func call(t *testing.T, front *httptest.Server, method, path string, cookies http.Header, body io.Reader, csrf string) response {
	t.Helper()
	request, err := http.NewRequest(method, front.URL+path, body)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	if cookie := cookies.Get("Cookie"); cookie != "" {
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
	return response{status: answer.StatusCode, body: string(payload), decoded: decoded, cookies: answer.Cookies(), header: answer.Header}
}

// pageIDs reads the identities the page registry lists.
func pageIDs(t *testing.T, front *httptest.Server, cookies http.Header) []string {
	t.Helper()
	answer := call(t, front, http.MethodGet, "/api/v1/pages", cookies, nil, "")
	if answer.status != http.StatusOK {
		t.Fatalf("read the page registry = %d %v", answer.status, answer.body)
	}
	var listed struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(answer.body), &listed); err != nil {
		t.Fatalf("decode the page registry: %v", err)
	}
	ids := make([]string, 0, len(listed.Items))
	for _, item := range listed.Items {
		ids = append(ids, item.ID)
	}
	return ids
}

// appSurfaces reads the identities the launcher and the dock list together with
// every surface an entry claims.
func appSurfaces(t *testing.T, front *httptest.Server, cookies http.Header) ([]string, []string) {
	t.Helper()
	answer := call(t, front, http.MethodGet, "/api/v1/apps", cookies, nil, "")
	if answer.status != http.StatusOK {
		t.Fatalf("read the applications = %d %v", answer.status, answer.body)
	}
	var listed struct {
		Items []struct {
			ID       string   `json:"id"`
			Surfaces []string `json:"surfaces"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(answer.body), &listed); err != nil {
		t.Fatalf("decode the applications: %v", err)
	}
	ids := make([]string, 0, len(listed.Items))
	surfaces := make([]string, 0, len(listed.Items))
	for _, item := range listed.Items {
		ids = append(ids, item.ID)
		surfaces = append(surfaces, item.Surfaces...)
	}
	return ids, surfaces
}

// contains reports whether one list names the value.
func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

// TestTC163PageIsNoIframeEntryAndHidesFromTheUnauthorized is the case
// DASH-TC-163 describes.
func TestTC163PageIsNoIframeEntryAndHidesFromTheUnauthorized(t *testing.T) {
	cluster := harness.Start(t)
	installation := cluster.InstallPackage(t, componentName, pageDeclaration(), extension.MemoryArtifacts{})
	shell := cluster.StartShell(t)
	shell.Use(installation)
	owner := shell.SignInOwner(t, ownerName, ownerPassword)
	shell.Account(t, owner, guestName, guestPassword, write.RoleGuest)

	apps := &registry{}
	apps.append(contributionOf(t, appExtID, appComponent, apphost.PhaseReady, appManifest()))
	control := startControlPlane(t, shell, apps)
	front, ownerCookies := startFront(t, control, ownerName, ownerPassword)

	// `Page` and `App` are independent declarations: the page-only extension's
	// page is navigation, the `App` extension's application is not, and neither
	// substitutes for the other (KOSD-C-18, DASH-C-22).
	pages := pageIDs(t, front, ownerCookies)
	if !contains(pages, pageID) {
		t.Fatalf("pages = %v, want the page-only extension's page", pages)
	}
	if contains(pages, appID) {
		t.Fatalf("pages = %v, want no application in the page navigation (DASH-C-22)", pages)
	}
	applications, surfaces := appSurfaces(t, front, ownerCookies)
	if !contains(applications, appID) {
		t.Fatalf("applications = %v, want the `App` contribution", applications)
	}
	if contains(applications, pageID) {
		t.Fatalf("applications = %v, want no page in the application surfaces", applications)
	}
	for _, surface := range surfaces {
		if surface != apphost.SurfaceLauncher && surface != apphost.SurfaceDock {
			t.Fatalf("application surfaces = %v, want the launcher and the dock alone (DASH-C-22)", surfaces)
		}
	}

	// A page is no iframe entry: its projection carries no frame address and no
	// sandbox, and opening it as a window application is refused. The frame
	// belongs to the `App` alone (DASH-C-22, KOSD-C-18, KOSD-C-75).
	page := call(t, front, http.MethodGet, "/api/v1/pages/"+pageID, ownerCookies, nil, "")
	if page.status != http.StatusOK {
		t.Fatalf("read page %s = %d %v", pageID, page.status, page.body)
	}
	for _, field := range []string{"frame", "frameError", "sandbox", "iframe"} {
		if carriesKey(page.decoded, field) {
			t.Fatalf("page answer carries %q, want a page to create no iframe: %v", field, page.decoded)
		}
	}
	refused := call(t, front, http.MethodPost, "/api/v1/apps/"+pageID+"/windows", ownerCookies, nil, ownerCookies.Get("X-CSRF-Token"))
	if refused.status != http.StatusConflict {
		t.Fatalf("open page %s as a window application = %d %v, want %d",
			pageID, refused.status, refused.body, http.StatusConflict)
	}
	if carriesKey(refused.decoded, "frame") || carriesKey(refused.decoded, "sandbox") {
		t.Fatalf("page window answer carries a frame: %v", refused.decoded)
	}
	// The document frames the origins of installed applications and nothing a
	// page declares (KOSD-F-18).
	document := call(t, front, http.MethodGet, "/", ownerCookies, nil, "")
	policy := document.header.Get("Content-Security-Policy")
	if !strings.Contains(policy, "frame-src 'self' "+appOrigin) {
		t.Fatalf("frame policy = %q, want the installed application's origin", policy)
	}

	// An account without the page's permission sees no entry, and its direct
	// call answers exactly what the same account gets for a page that does not
	// exist (KOSD-C-59).
	guestFront, guestCookies := startFront(t, control, guestName, guestPassword)
	if pages := pageIDs(t, guestFront, guestCookies); contains(pages, pageID) {
		t.Fatalf("guest pages = %v, want no entry without the page's permission (KOSD-C-59)", pages)
	}
	hidden := call(t, guestFront, http.MethodGet, "/api/v1/pages/"+pageID, guestCookies, nil, "")
	absent := call(t, guestFront, http.MethodGet, "/api/v1/pages/no-such-page", guestCookies, nil, "")
	if hidden.status != absent.status || hidden.body != absent.body {
		t.Fatalf("refused page = %d %v, want the refusal of a page that does not exist %d %v",
			hidden.status, hidden.body, absent.status, absent.body)
	}

	// A direct call of the page's action is refused with the ordinary
	// unauthorized answer: the same status, code and stage any other write of
	// that account receives (KOSD-C-59).
	direct := call(t, guestFront, http.MethodPut, actionPath, guestCookies,
		strings.NewReader(`{"value":"viewed"}`), guestCookies.Get("X-CSRF-Token"))
	ordinary := call(t, guestFront, http.MethodPut, "/api/v1/preferences/notes-ext/view", guestCookies,
		strings.NewReader(`{"value":"viewed"}`), guestCookies.Get("X-CSRF-Token"))
	if direct.status != http.StatusForbidden {
		t.Fatalf("direct guest action = %d %v, want %d", direct.status, direct.body, http.StatusForbidden)
	}
	if direct.status != ordinary.status ||
		stringField(direct.decoded, "error", "code") != stringField(ordinary.decoded, "error", "code") ||
		stringField(direct.decoded, "error", "stage") != stringField(ordinary.decoded, "error", "stage") {
		t.Fatalf("direct guest action = %d %v, want the ordinary refusal %d %v",
			direct.status, direct.body, ordinary.status, ordinary.body)
	}
}

// contributionOf wraps one `App` manifest as an installed contribution.
func contributionOf(t *testing.T, extensionID, component, phase string, manifest map[string]any) apphost.Contribution {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	return apphost.Contribution{
		Extension: extensionID, Component: component, Phase: phase,
		Digest:   digest,
		Manifest: raw,
	}
}

// stringField reads one nested string out of a decoded answer.
func stringField(decoded map[string]any, keys ...string) string {
	current := any(decoded)
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = object[key]
	}
	value, _ := current.(string)
	return value
}

// carriesKey reports whether one decoded answer names a key anywhere.
func carriesKey(value any, key string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for name, nested := range typed {
			if name == key || carriesKey(nested, key) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if carriesKey(nested, key) {
				return true
			}
		}
	}
	return false
}
