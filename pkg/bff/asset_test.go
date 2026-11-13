package bff

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
)

// assetContributions renders the installed applications that ship a document:
// one package declaring two applications, which is how a package legitimately
// looks (KOSD-C-73).
func assetContributions() []apphost.Contribution {
	contribution := func(appID string) apphost.Contribution {
		manifest, _ := json.Marshal(map[string]any{
			"id": appID, "name": appID, "version": "2.0.0",
			"icon":  "https://" + appID + ".apps.example.test/icon.svg",
			"entry": "https://" + appID + ".apps.example.test/index.html",
			"window": map[string]any{
				"width": 800, "height": 600, "minWidth": 320, "minHeight": 240,
				"resizable": true, "mode": "single", "defaultState": "normal",
			},
			"permissions": []string{},
		})
		return apphost.Contribution{
			Extension: "files-ext", Component: appID, Phase: apphost.PhaseReady,
			Digest: "sha256:aaaa", Manifest: manifest,
		}
	}
	return []apphost.Contribution{contribution("files"), contribution("notes")}
}

// assetServer builds a front that serves the application asset origin.
func assetServer(t *testing.T) *Server {
	t.Helper()
	files := apphost.NewMemoryAssets()
	files_ := map[string][]byte{
		"index.html":     []byte("<html>v2</html>"),
		"icon.svg":       []byte("<svg/>"),
		"app.css":        []byte("body{color:red}"),
		"app.js":         []byte("main()"),
		"deep/theme.css": []byte("h1{color:blue}"),
	}
	if err := files.Publish("files-ext", "2.0.0", "sha256:aaaa", files_); err != nil {
		t.Fatalf("publish: %v", err)
	}
	server, err := New(Config{
		Upstream:      &url.URL{Scheme: "http", Host: "control.example.test"},
		AssetFiles:    files,
		ConsoleOrigin: "https://console.example.test",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return server
}

// installedEntry is the application the signed address belongs to.
func installedEntry(t *testing.T) apphost.Entry {
	t.Helper()
	registry := apphost.Load(assetContributions(), apphost.ValidationOptions{})
	entry, found := registry.App("files")
	if !found {
		t.Fatal("the installed application is missing from the registry")
	}
	return entry
}

// requestFor builds one asset request addressed to the named host.
func requestFor(address, host string) *http.Request {
	if strings.HasPrefix(address, "https://") {
		parsed, err := url.Parse(address)
		if err == nil {
			request := httptest.NewRequest(http.MethodGet, parsed.Path+"?"+parsed.RawQuery, nil)
			request.Host = host
			return request
		}
	}
	request := httptest.NewRequest(http.MethodGet, address, nil)
	request.Host = host
	return request
}

// requestWithGrant builds one asset request that carries the subresource grant.
func requestWithGrant(address, host, grant string) *http.Request {
	request := requestFor(address, host)
	if grant != "" {
		request.AddCookie(&http.Cookie{Name: apphost.AssetGrantCookie, Value: grant})
	}
	return request
}

// subresourceAddress rewrites one signed asset address to a sibling the way a
// browser resolves a relative reference: same directory, no query.
func subresourceAddress(t *testing.T, address, name string) string {
	t.Helper()
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatalf("parse %q: %v", address, err)
	}
	parsed.RawQuery = ""
	parsed.Path = parsed.Path[:strings.LastIndex(parsed.Path, "/")+1] + name
	return parsed.String()
}

// grantOf reads the narrow credential a served address left for subresources.
func grantOf(t *testing.T, served *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range served.Result().Cookies() {
		if cookie.Name == apphost.AssetGrantCookie {
			return cookie
		}
	}
	t.Fatalf("served address set no %s cookie, want the subresource grant", apphost.AssetGrantCookie)
	return nil
}

// serveAsset serves one asset request through the front.
func serveAsset(server *Server, request *http.Request) *httptest.ResponseRecorder {
	served := httptest.NewRecorder()
	server.appAsset(served, request)
	return served
}

// TestAssetServesTheVersionedAddress checks the asset route: the document is
// served from the application origin under an address naming the extension,
// application, version and digest, and a forged signature is refused
// (KOSD-C-73).
func TestAssetServesTheVersionedAddress(t *testing.T) {
	server := assetServer(t)
	address, reason := server.frameAddress(installedEntry(t))
	if address == "" {
		t.Fatalf("frame address = %q (%s), want a signed address", address, reason)
	}
	if !strings.HasPrefix(address,
		"https://files.apps.example.test/app-assets/files-ext/files/2.0.0/sha256:aaaa/index.html?") {
		t.Fatalf("address = %q, want the versioned application origin", address)
	}

	served := httptest.NewRecorder()
	server.appAsset(served, requestFor(address, "files.apps.example.test"))
	if served.Code != http.StatusOK || served.Body.String() != "<html>v2</html>" {
		t.Fatalf("asset = %d %q, want the shipped document", served.Code, served.Body.String())
	}
	if policy := served.Header().Get("Content-Security-Policy"); !strings.Contains(policy, "frame-ancestors https://console.example.test") {
		t.Fatalf("policy = %q, want only the console allowed to frame it (KOSD-C-75)", policy)
	}
}

// TestSignedEntryDocumentServesItsSameDigestSubresources checks that a served
// entry document loads its relative references: the answer leaves a narrow grant
// on the application origin, and the styles, scripts and icons of the same
// package digest are served without a query signature each (KOSD-C-73,
// DASH-C-30).
func TestSignedEntryDocumentServesItsSameDigestSubresources(t *testing.T) {
	server := assetServer(t)
	address, reason := server.frameAddress(installedEntry(t))
	if address == "" {
		t.Fatalf("frame address = %q (%s), want a signed address", address, reason)
	}
	entry := serveAsset(server, requestFor(address, "files.apps.example.test"))
	if entry.Code != http.StatusOK {
		t.Fatalf("entry document = %d %q, want it served", entry.Code, entry.Body.String())
	}
	grant := grantOf(t, entry)
	if grant.Path != apphost.AssetGrantPath || grant.Domain != "" || !grant.HttpOnly {
		t.Fatalf("grant = %+v, want an HttpOnly, host-only credential scoped to the asset routes", grant)
	}
	if grant.SameSite == http.SameSiteNoneMode && !grant.Secure {
		t.Fatalf("grant = %+v, want SameSite=None only together with Secure", grant)
	}
	if grant.MaxAge <= 0 {
		t.Fatalf("grant = %+v, want a bounded lifetime rather than a session cookie", grant)
	}

	for _, name := range []string{"app.css", "app.js", "icon.svg", "deep/theme.css"} {
		served := serveAsset(server, requestWithGrant(
			subresourceAddress(t, address, name), "files.apps.example.test", grant.Value))
		if served.Code != http.StatusOK {
			t.Fatalf("%s = %d %q, want the same-digest subresource served", name, served.Code, served.Body.String())
		}
		if served.Header().Get("X-Content-Type-Options") != "nosniff" ||
			served.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("%s headers = %v, want nosniff and no-referrer kept", name, served.Header())
		}
	}

	// A sibling address carrying the entry document's query signature is served
	// too: the signature covers the five bindings, not the path (KOSD-C-73).
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatalf("parse the signed address: %v", err)
	}
	signed := subresourceAddress(t, address, "app.css") + "?" + parsed.RawQuery
	if served := serveAsset(server, requestFor(signed, "files.apps.example.test")); served.Code != http.StatusOK {
		t.Fatalf("signed sibling = %d %q, want the document's signature to cover it", served.Code, served.Body.String())
	}

	// The policy stays as strict as on the document, and a subresource answer
	// hands out no further credential (KOSD-F-18, KOSD-C-75).
	stylesheet := serveAsset(server, requestWithGrant(
		subresourceAddress(t, address, "app.css"), "files.apps.example.test", grant.Value))
	policy := stylesheet.Header().Get("Content-Security-Policy")
	if strings.Contains(policy, "unsafe-inline") ||
		!strings.Contains(policy, "frame-ancestors https://console.example.test") {
		t.Fatalf("policy = %q, want the unchanged frame-ancestors and no unsafe-inline", policy)
	}
	if issued := stylesheet.Result().Cookies(); len(issued) != 0 {
		t.Fatalf("subresource answer issued cookies %v, want none", issued)
	}
}

// TestAssetGrantDoesNotCrossDigestApplicationVersionOrTampering checks that the
// grant is scoped to the five bindings it was signed for: another digest,
// application or version, a request that names only half of the address
// parameters, and a tampered grant all fail closed (KOSD-C-73).
func TestAssetGrantDoesNotCrossDigestApplicationVersionOrTampering(t *testing.T) {
	server := assetServer(t)
	address, reason := server.frameAddress(installedEntry(t))
	if address == "" {
		t.Fatalf("frame address = %q (%s), want a signed address", address, reason)
	}
	grant := grantOf(t, serveAsset(server, requestFor(address, "files.apps.example.test")))
	subresource := subresourceAddress(t, address, "app.css")

	if served := serveAsset(server, requestFor(subresource, "files.apps.example.test")); served.Code != http.StatusForbidden {
		t.Fatalf("subresource without a credential = %d, want %d (KOSD-C-73)", served.Code, http.StatusForbidden)
	}

	otherApplication := strings.Replace(subresource, "/files-ext/files/", "/files-ext/notes/", 1)
	for _, changed := range []string{
		strings.Replace(subresource, "sha256:aaaa", "sha256:bbbb", 1),
		otherApplication,
		strings.Replace(subresource, "/2.0.0/", "/3.0.0/", 1),
	} {
		if served := serveAsset(server, requestWithGrant(changed, "files.apps.example.test", grant.Value)); served.Code != http.StatusForbidden {
			t.Fatalf("%s = %d, want %d", changed, served.Code, http.StatusForbidden)
		}
	}
	if served := serveAsset(server, requestWithGrant(subresource, "files.apps.example.test", grant.Value+"x")); served.Code != http.StatusForbidden {
		t.Fatalf("tampered grant = %d, want %d", served.Code, http.StatusForbidden)
	}

	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatalf("parse the signed address: %v", err)
	}
	expiry := parsed.Query().Get("kos-exp")
	signature := parsed.Query().Get("kos-sig")
	// A query that states only one half of the binding never falls back to the
	// grant, and another application's signed address is not helped either
	// (KOSD-C-73).
	for _, query := range []string{
		"kos-exp=" + expiry,
		"kos-sig=" + signature,
		"kos-exp=" + expiry + "&kos-sig=forged",
		"kos-exp=&kos-sig=",
	} {
		if served := serveAsset(server, requestWithGrant(subresource+"?"+query, "files.apps.example.test", grant.Value)); served.Code != http.StatusForbidden {
			t.Fatalf("query %q = %d, want %d", query, served.Code, http.StatusForbidden)
		}
	}
	if served := serveAsset(server, requestFor(otherApplication+"?"+parsed.RawQuery, "files.apps.example.test")); served.Code != http.StatusForbidden {
		t.Fatalf("another application's signed address = %d, want %d", served.Code, http.StatusForbidden)
	}
}

// TestExpiredAssetGrantIsRefused checks that the grant and the address behind it
// both stop working once their expiry passes (KOSD-C-73).
func TestExpiredAssetGrantIsRefused(t *testing.T) {
	clock := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	files := apphost.NewMemoryAssets()
	_ = files.Publish("files-ext", "2.0.0", "sha256:aaaa", map[string][]byte{
		"index.html": []byte("x"), "app.css": []byte("body{}"),
	})
	server, err := New(Config{
		Upstream:      &url.URL{Scheme: "http", Host: "control.example.test"},
		AssetFiles:    files,
		ConsoleOrigin: "https://console.example.test",
		AssetTTL:      time.Minute,
		Now:           func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	address, reason := server.frameAddress(installedEntry(t))
	if address == "" {
		t.Fatalf("frame address = %q (%s), want a signed address", address, reason)
	}
	grant := grantOf(t, serveAsset(server, requestFor(address, "files.apps.example.test")))
	subresource := subresourceAddress(t, address, "app.css")

	server.now = func() time.Time { return clock.Add(2 * time.Minute) }
	if served := serveAsset(server, requestWithGrant(subresource, "files.apps.example.test", grant.Value)); served.Code != http.StatusForbidden {
		t.Fatalf("expired grant = %d, want %d", served.Code, http.StatusForbidden)
	}
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatalf("parse the signed address: %v", err)
	}
	if served := serveAsset(server, requestFor(subresource+"?"+parsed.RawQuery, "files.apps.example.test")); served.Code != http.StatusForbidden {
		t.Fatalf("expired signature on a subresource = %d, want %d", served.Code, http.StatusForbidden)
	}
}

// TestTwoApplicationsOfOnePackageAreBothServed checks that the second application a
// package declares loads its own addressed asset instead of being refused for
// reusing the first one's signature (KOSD-C-73).
func TestTwoApplicationsOfOnePackageAreBothServed(t *testing.T) {
	server := assetServer(t)
	registry := apphost.Load(assetContributions(), apphost.ValidationOptions{})
	for _, appID := range []string{"files", "notes"} {
		entry, found := registry.App(appID)
		if !found {
			t.Fatalf("%s is missing from the registry", appID)
		}
		address, reason := server.frameAddress(entry)
		if address == "" {
			t.Fatalf("frame address of %s = %q (%s), want a signed address", appID, address, reason)
		}
		served := httptest.NewRecorder()
		server.appAsset(served, requestFor(address, appID+".apps.example.test"))
		if served.Code != http.StatusOK || served.Body.String() != "<html>v2</html>" {
			t.Fatalf("asset of %s = %d %q, want the shipped document", appID, served.Code, served.Body.String())
		}
	}
}

func TestAssetRefusesAnotherVersionAndAForgedSignature(t *testing.T) {
	server := assetServer(t)
	other := installedEntry(t)
	other.Version = "1.0.0"
	address, _ := server.frameAddress(other)
	served := httptest.NewRecorder()
	server.appAsset(served, requestFor(address, "files.apps.example.test"))
	if served.Code != http.StatusNotFound {
		t.Fatalf("address for another version = %d, want %d (KOSD-C-73)", served.Code, http.StatusNotFound)
	}

	valid, _ := server.frameAddress(installedEntry(t))
	base := valid[:strings.Index(valid, "kos-sig=")]
	forged := httptest.NewRecorder()
	server.appAsset(forged, requestFor(base+"kos-sig=forged", "files.apps.example.test"))
	if forged.Code != http.StatusForbidden {
		t.Fatalf("forged signature = %d, want %d (KOSD-C-73)", forged.Code, http.StatusForbidden)
	}
}

// TestAssetIsNotServedOnTheConsoleOrigin checks the origin separation: even a
// correctly signed address is not served on the console host (KOSD-F-18).
func TestAssetIsNotServedOnTheConsoleOrigin(t *testing.T) {
	server := assetServer(t)
	address, _ := server.frameAddress(installedEntry(t))
	response := httptest.NewRecorder()
	server.appAsset(response, requestFor(address, "console.example.test"))
	if response.Code != http.StatusNotFound {
		t.Fatalf("console-origin asset read = %d, want %d", response.Code, http.StatusNotFound)
	}
}

// TestAssetExpiryIsJudged checks that an address past its expiry is refused rather
// than served (KOSD-C-73).
func TestAssetExpiryIsJudged(t *testing.T) {
	clock := time.Now().UTC()
	files := apphost.NewMemoryAssets()
	_ = files.Publish("files-ext", "2.0.0", "sha256:aaaa", map[string][]byte{"index.html": []byte("x")})
	server, err := New(Config{
		Upstream:      &url.URL{Scheme: "http", Host: "control.example.test"},
		AssetFiles:    files,
		ConsoleOrigin: "https://console.example.test",
		AssetTTL:      time.Minute,
		Now:           func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	address, _ := server.frameAddress(installedEntry(t))
	later := httptest.NewRecorder()
	server.now = func() time.Time { return clock.Add(2 * time.Minute) }
	server.appAsset(later, requestFor(address, "files.apps.example.test"))
	if later.Code != http.StatusForbidden {
		t.Fatalf("expired address = %d, want %d", later.Code, http.StatusForbidden)
	}
}

// TestOpenWindowRefusesToSignWithoutAssetStore checks that an open answer never
// carries a frame address this process cannot serve (KOSD-C-73).
func TestOpenWindowRefusesToSignWithoutAssetStore(t *testing.T) {
	server := assetServer(t)
	server.assetFiles = nil
	frame, reason := server.frameAddress(installedEntry(t))
	if frame != "" || reason == "" {
		t.Fatalf("frame address without a store = %q, %q, want the reason", frame, reason)
	}
}
