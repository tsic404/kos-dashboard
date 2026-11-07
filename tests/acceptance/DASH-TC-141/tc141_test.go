// Package dashtc141 verifies DASH-TC-141: an extension whose `App` manifest
// lacks a required field makes its component not ready and surfaces no entry; an
// extension that declares only a page contributes no launcher or dock entry
// while its page still appears (DASH-C-22); and a path no installed application
// declared resolves to nothing and creates no window (DASH-AC-67, DASH-C-30,
// KOSD-C-73).
package dashtc141

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
	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
	"github.com/tsic404/kos-dashboard/pkg/bff"
)

const (
	// The broken contribution names an application but declares no entry.
	brokenExtID  = "broken-ext"
	brokenAppID  = "broken"
	brokenOrigin = "https://broken.apps.example.test"
	// The page-only extension contributes a page and no `App`.
	pageExtID = "reports-ext"
	pageID    = "reports"
	// The installed application the deep-link check uses.
	notesExtID  = "notes-ext"
	notesAppID  = "notes"
	notesName   = "笔记"
	notesOrigin = "https://notes.apps.example.test"
	ownerName   = "owner"
	ownerPass   = "owner-password"
)

// brokenManifest declares an application without the required entry field.
func brokenManifest() map[string]any {
	return map[string]any{
		"id": brokenAppID, "name": "损坏", "version": "1.0.0",
		"icon": brokenOrigin + "/assets/icon.svg",
		"window": map[string]any{
			"width": 800, "height": 600, "minWidth": 320, "minHeight": 240,
			"resizable": true, "mode": "single", "defaultState": "normal",
		},
		"permissions": []string{},
	}
}

// notesManifest declares a valid application with one deep link.
func notesManifest() map[string]any {
	return map[string]any{
		"id": notesAppID, "name": notesName, "version": "1.0.0",
		"icon":  notesOrigin + "/assets/icon.svg",
		"entry": notesOrigin + "/index.html",
		"window": map[string]any{
			"width": 900, "height": 700, "minWidth": 320, "minHeight": 240,
			"resizable": true, "mode": "single", "defaultState": "normal",
		},
		"permissions": []string{},
		"deepLinks":   []string{"/notes"},
	}
}

// contributionOf wraps one manifest as an installed contribution.
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

// registry is the control-plane application registry the front reads.
type registry struct {
	mu            sync.Mutex
	contributions []apphost.Contribution
}

func (r *registry) append(contribution apphost.Contribution) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.contributions = append(r.contributions, contribution)
}

func (r *registry) items() []apphost.Contribution {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]apphost.Contribution(nil), r.contributions...)
}

// startControlPlane composes the harness control plane with the application
// registry route.
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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": apps.items()})
	})
	mux.Handle("/", proxy)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// startFront serves the browser front and logs the owner in through it.
func startFront(t *testing.T, control *httptest.Server) (*httptest.Server, http.Header) {
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
	return response{status: answer.StatusCode, body: string(payload), decoded: decoded, cookies: answer.Cookies(), header: answer.Header}
}

// appEntry is one launcher/dock item.
type appEntry struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Surfaces []string `json:"surfaces"`
}

// appsAnswer is the launcher/dock read.
type appsAnswer struct {
	Items    []appEntry `json:"items"`
	NotReady []struct {
		Extension string `json:"extension"`
		Component string `json:"component"`
		App       string `json:"app"`
		Reason    string `json:"reason"`
	} `json:"notReady"`
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

// TestTC141UndeclaredUninstalledAndIncompleteManifests is the case DASH-TC-141
// describes.
func TestTC141UndeclaredUninstalledAndIncompleteManifests(t *testing.T) {
	cluster := harness.Start(t)
	shell := cluster.StartShell(t)
	shell.SignInOwner(t, ownerName, ownerPass)

	// The page-only extension is installed through the real control plane, so
	// the page registry serves its page (DASH-C-22).
	declaration := extension.Declaration{
		Identifier: pageExtID, Version: "1.0.0",
		Contributions: extension.Contributions{
			Pages: []extension.Page{{ID: pageID, Title: "报表", Location: "reports", Items: []extension.PageItem{{
				ID: "reports-recent", Label: "最近报表", Source: extension.PageSourceLocal, Store: "reports.recent",
				Reason: "报表列表只存在于浏览器",
			}}}},
		},
	}
	installation := cluster.InstallPackage(t, "reports", declaration, extension.MemoryArtifacts{})
	shell.Use(installation)

	apps := &registry{}
	control := startControlPlane(t, shell, apps)
	front, cookies := startFront(t, control)

	// An extension whose `App` manifest lacks the required entry contributes no
	// application: the component is not ready and neither the launcher nor the
	// dock shows an entry (KOSD-C-73).
	apps.append(contributionOf(t, brokenExtID, "broken", apphost.PhaseReady, brokenManifest()))
	broken := readApps(t, front, cookies)
	if len(broken.Items) != 0 {
		t.Fatalf("applications = %+v, want none: the manifest is invalid", broken.Items)
	}
	if len(broken.NotReady) != 1 || broken.NotReady[0].Extension != brokenExtID {
		t.Fatalf("refusals = %+v, want the incomplete manifest reported", broken.NotReady)
	}
	if !strings.Contains(broken.NotReady[0].Reason, "entry") {
		t.Fatalf("refusal = %+v, want it to name the missing field", broken.NotReady[0])
	}

	// The page-only extension contributes no window application, while its page
	// still appears in the page navigation (DASH-C-22).
	pages := call(t, front, http.MethodGet, "/api/v1/pages", cookies.Get("Cookie"), nil, "")
	if pages.status != http.StatusOK {
		t.Fatalf("read the pages = %d %v", pages.status, pages.body)
	}
	var listed struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(pages.body), &listed); err != nil {
		t.Fatalf("decode the pages: %v", err)
	}
	if !hasPage(listed.Items, pageID) {
		t.Fatalf("pages = %+v, want the page-only extension's page (DASH-C-22)", listed.Items)
	}
	if hasPage(listed.Items, brokenAppID) {
		t.Fatalf("pages = %+v, want no entry for an application (DASH-C-22)", listed.Items)
	}

	// A deep link no installed application declared resolves to nothing and
	// creates no window (DASH-C-30).
	undeclared := call(t, front, http.MethodGet, "/api/v1/deep-links?path=%2Fnotes", cookies.Get("Cookie"), nil, "")
	if undeclared.status != http.StatusNotFound {
		t.Fatalf("undeclared deep link = %d %v, want %d", undeclared.status, undeclared.body, http.StatusNotFound)
	}
	windows := call(t, front, http.MethodGet, "/api/v1/windows", cookies.Get("Cookie"), nil, "")
	var open struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(windows.body), &open); err != nil {
		t.Fatalf("decode the windows: %v", err)
	}
	if len(open.Items) != 0 {
		t.Fatalf("windows = %+v, want none: a resolved-nowhere deep link creates no window", open.Items)
	}

	// Installing an application that declares the deep link resolves it, and a
	// path it did not declare still resolves to nothing.
	apps.append(contributionOf(t, notesExtID, "notes", apphost.PhaseReady, notesManifest()))
	installed := readApps(t, front, cookies)
	if len(installed.Items) != 1 || installed.Items[0].ID != notesAppID {
		t.Fatalf("applications = %+v, want the installed application", installed.Items)
	}
	resolved := call(t, front, http.MethodGet, "/api/v1/deep-links?path=%2Fnotes%2F42", cookies.Get("Cookie"), nil, "")
	if resolved.status != http.StatusOK || resolved.decoded["app"] != notesAppID {
		t.Fatalf("declared deep link = %d %v, want it resolved to %s", resolved.status, resolved.body, notesAppID)
	}
	if unknown := call(t, front, http.MethodGet, "/api/v1/deep-links?path=%2Funknown", cookies.Get("Cookie"), nil, ""); unknown.status != http.StatusNotFound {
		t.Fatalf("undeclared path on an installed application = %d, want %d", unknown.status, http.StatusNotFound)
	}

	// An uninstalled application is absent from the launcher, the dock and the
	// deep-link resolver alike (DASH-AC-67).
	apps.mu.Lock()
	apps.contributions = nil
	apps.mu.Unlock()
	if after := readApps(t, front, cookies); len(after.Items) != 0 {
		t.Fatalf("applications after the uninstall = %+v, want none", after.Items)
	}
	if gone := call(t, front, http.MethodGet, "/api/v1/deep-links?path=%2Fnotes", cookies.Get("Cookie"), nil, ""); gone.status != http.StatusNotFound {
		t.Fatalf("deep link after the uninstall = %d, want %d", gone.status, http.StatusNotFound)
	}
}

// hasPage reports whether the page list names one page.
func hasPage(items []struct {
	ID string `json:"id"`
}, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}
