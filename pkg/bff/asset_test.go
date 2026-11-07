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

// assetContributions renders one installed application that ships a document.
func assetContributions() []apphost.Contribution {
	manifest, _ := json.Marshal(map[string]any{
		"id": "files", "name": "文件", "version": "2.0.0",
		"icon":  "https://files.apps.example.test/icon.svg",
		"entry": "https://files.apps.example.test/index.html",
		"window": map[string]any{
			"width": 800, "height": 600, "minWidth": 320, "minHeight": 240,
			"resizable": true, "mode": "single", "defaultState": "normal",
		},
		"permissions": []string{},
	})
	return []apphost.Contribution{{
		Extension: "files-ext", Component: "files", Phase: apphost.PhaseReady,
		Digest: "sha256:aaaa", Manifest: manifest,
	}}
}

// assetServer builds a front that serves the application's asset origin.
func assetServer(t *testing.T) *Server {
	t.Helper()
	files := apphost.NewMemoryAssets()
	if err := files.Publish("files-ext", "2.0.0", map[string][]byte{"index.html": []byte("<html>v2</html>")}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	server, err := New(Config{
		Upstream:      &url.URL{Scheme: "http", Host: "control.example.test"},
		AssetApps:     apphost.MemorySource(assetContributions()),
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

// TestAssetServesTheVersionedAddress checks the asset route: the document is
// served from the application origin under an address naming the extension,
// version and digest, and a forged signature is refused (KOSD-C-73).
func TestAssetServesTheVersionedAddress(t *testing.T) {
	server := assetServer(t)
	address, reason := server.frameAddress(installedEntry(t))
	if address == "" {
		t.Fatalf("frame address = %q (%s), want a signed address", address, reason)
	}
	if !strings.HasPrefix(address, "https://files.apps.example.test/app-assets/files-ext/2.0.0/sha256:aaaa/index.html?") {
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

func TestAssetRefusesAnotherVersionAndAForgedSignature(t *testing.T) {
	server := assetServer(t)
	other := installedEntry(t)
	other.Version = "1.0.0"
	address, _ := server.frameAddress(other)
	if served := httptest.NewRecorder(); func() int {
		server.appAsset(served, requestFor(address, "files.apps.example.test"))
		return served.Code
	}() != http.StatusNotFound {
		t.Fatalf("address for another version = %d, want %d (KOSD-C-73)", served.Code, http.StatusNotFound)
	}

	valid, _ := server.frameAddress(installedEntry(t))
	base := valid[:strings.Index(valid, "kos-sig=")]
	forged := base + "kos-sig=forged"
	codes := httptest.NewRecorder()
	server.appAsset(codes, requestFor(forged, "files.apps.example.test"))
	if codes.Code != http.StatusForbidden {
		t.Fatalf("forged signature = %d, want %d (KOSD-C-73)", codes.Code, http.StatusForbidden)
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

// TestAssetExpiryIsJudged checks that an address past its expiry is refused
// rather than served (KOSD-C-73).
func TestAssetExpiryIsJudged(t *testing.T) {
	clock := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	files := apphost.NewMemoryAssets()
	_ = files.Publish("files-ext", "2.0.0", map[string][]byte{"index.html": []byte("x")})
	server, err := New(Config{
		Upstream:      &url.URL{Scheme: "http", Host: "control.example.test"},
		AssetApps:     apphost.MemorySource(assetContributions()),
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
