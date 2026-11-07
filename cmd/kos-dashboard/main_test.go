package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// validOptions is the least configuration the process can serve with.
func validOptions() options {
	return options{
		upstream:      "http://127.0.0.1:8081",
		consoleOrigin: "https://console.example.test",
		appAssetsRoot: "/var/lib/kos/assets",
	}
}

// TestFrameOriginRepeats checks that every -frame-origin the operator names is
// kept: the flag is documented as repeatable and the front frames each origin
// (KOSD-F-18).
func TestFrameOriginRepeats(t *testing.T) {
	parsed, err := parseOptions([]string{
		"-frame-origin", "https://a.example.test",
		"-frame-origin=https://b.example.test",
		"-console-origin", "https://console.example.test",
		"-app-assets-root", "/var/lib/kos/assets",
	})
	if err != nil {
		t.Fatalf("parse the options: %v", err)
	}
	if len(parsed.frameOrigins) != 2 ||
		parsed.frameOrigins[0] != "https://a.example.test" ||
		parsed.frameOrigins[1] != "https://b.example.test" {
		t.Fatalf("frame origins = %v, want both named origins", parsed.frameOrigins)
	}
}

// TestOptionsFrameNothingByDefault checks the front frames only the origins an
// operator named (KOSD-F-18).
func TestOptionsFrameNothingByDefault(t *testing.T) {
	parsed, err := parseOptions(nil)
	if err != nil {
		t.Fatalf("parse the options: %v", err)
	}
	if len(parsed.frameOrigins) != 0 {
		t.Fatalf("frame origins = %v, want none", parsed.frameOrigins)
	}
	if parsed.address != ":8080" || parsed.upstream != "http://127.0.0.1:8081" || parsed.secure {
		t.Fatalf("options = %+v, want the documented defaults", parsed)
	}
}

// TestFrameOriginRefusesAnEmptyOrigin checks the flag cannot silently admit a
// frameless entry an operator typed by mistake.
func TestFrameOriginRefusesAnEmptyOrigin(t *testing.T) {
	if _, err := parseOptions([]string{"-frame-origin", ""}); err == nil {
		t.Fatal("an empty -frame-origin is accepted")
	}
}

// TestValidateRequiresTheConsoleOrigin checks the process refuses to start
// without the origin that keeps application entries off the console (KOSD-F-18).
func TestValidateRequiresTheConsoleOrigin(t *testing.T) {
	config := validOptions()
	config.consoleOrigin = ""
	err := config.validate()
	if err == nil {
		t.Fatal("the process started without -console-origin")
	}

	config.consoleOrigin = "https://console.example.test/a/path"
	if err := config.validate(); err == nil {
		t.Fatal("a console origin with a path passed for an origin")
	}
}

// TestValidateRequiresTheAssetRoot checks the process refuses to start when it
// cannot serve the asset addresses it would sign (KOSD-C-73).
func TestValidateRequiresTheAssetRoot(t *testing.T) {
	config := validOptions()
	config.appAssetsRoot = ""
	if err := config.validate(); err == nil {
		t.Fatal("the process started without an asset root, so every window would open on an unserved address")
	}
}

// TestServerServesAssetsFromTheConfiguredRoot checks the binary wiring: the asset
// route resolves the configured directory, so a signed address's bytes are
// reachable from the process the flags describe (KOSD-C-73).
func TestServerServesAssetsFromTheConfiguredRoot(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "files-ext", "1.0.0", "sha256:aaaa")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("create the package directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte("<html>v1</html>"), 0o644); err != nil {
		t.Fatalf("write the document: %v", err)
	}
	config := validOptions()
	config.appAssetsRoot = root
	// An unset seal key draws a random one; the route still judges the address.
	server, _, err := newServer(config, nil)
	if err != nil {
		t.Fatalf("build the front: %v", err)
	}

	// A request without a valid signature is refused at the signature check, which
	// is only reached once the store is wired. A request to a console-origin host
	// is refused before that, so the asset origin host is used.
	request := httptest.NewRequest(http.MethodGet,
		"/app-assets/files-ext/files/1.0.0/sha256:aaaa/index.html?kos-exp=1&kos-sig=no", nil)
	request.Host = "files.apps.example.test"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("asset read = %d %s, want %d: the route did not reach the asset store",
			response.Code, response.Body.String(), http.StatusForbidden)
	}
}
