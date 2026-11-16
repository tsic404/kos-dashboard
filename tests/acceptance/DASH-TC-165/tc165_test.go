// Package dashtc165 verifies DASH-TC-165: an application asset document's CSP
// names connect-src. A document with no declared backend carries exactly its own
// origin and the console origin; a backend application adds its backend origin;
// neither policy names another application or any wildcard. The document stays
// framed by the console alone (DASH-AC-73, DASH-C-76, KOSD-C-87).
package dashtc165

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
	"github.com/tsic404/kos-dashboard/tests/acceptance/front"
)

const (
	// digest is the package digest the front helper's contribution declares.
	digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	reader = "files"
	// notes is another installed application; its origin must never appear in
	// the two documents under test.
	notes = "notes"
	// backends is the one application that declares its own backend service.
	backends   = "reporting"
	backendURL = "https://files-backend.example.test/healthz"
)

// originOf renders one application origin.
func originOf(id string) string {
	return "https://" + id + ".apps.example.test"
}

// manifestOf renders one application document's manifest. An empty health
// address names no backend, so the two cases differ only by the declaration the
// CSP is judged against (DASH-C-76).
func manifestOf(id, health string) map[string]any {
	manifest := map[string]any{
		"id": id, "name": id, "version": "1.0.0",
		"icon":  originOf(id) + "/assets/icon.svg",
		"entry": originOf(id) + "/index.html",
		"window": map[string]any{
			"width": 1024, "height": 720, "minWidth": 320, "minHeight": 240,
			"resizable": true, "mode": "single", "defaultState": "normal",
		},
		"permissions": []string{},
	}
	if health != "" {
		manifest["backend"] = map[string]any{"type": "http", "health": health}
	}
	return manifest
}

// response is one asset answer the front gave.
type response struct {
	status int
	body   string
	header http.Header
}

// fetch reads one signed asset address on the application origin the
// deployment maps it to.
func fetch(t *testing.T, base, address, host string) response {
	t.Helper()
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatalf("parse the asset address: %v", err)
	}
	request, err := http.NewRequest(http.MethodGet, base+parsed.RequestURI(), nil)
	if err != nil {
		t.Fatalf("build the asset request: %v", err)
	}
	request.Host = host
	answer, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("read the asset: %v", err)
	}
	defer answer.Body.Close()
	payload, _ := io.ReadAll(answer.Body)
	return response{status: answer.StatusCode, body: string(payload), header: answer.Header}
}

// connectSources reads the origins one CSP's connect-src names.
func connectSources(t *testing.T, policy string) []string {
	t.Helper()
	for _, directive := range strings.Split(policy, ";") {
		trimmed := strings.TrimSpace(directive)
		if strings.HasPrefix(trimmed, "connect-src ") {
			return strings.Fields(strings.TrimPrefix(trimmed, "connect-src "))
		}
	}
	t.Fatalf("policy %q declares no connect-src", policy)
	return nil
}

// sameSources reports whether the two origin lists name exactly the same set.
func sameSources(t *testing.T, served, wanted []string) {
	t.Helper()
	if len(served) != len(wanted) {
		t.Fatalf("sources = %q, want exactly %q", served, wanted)
	}
	seen := map[string]bool{}
	for _, source := range served {
		seen[source] = true
	}
	for _, want := range wanted {
		if !seen[want] {
			t.Fatalf("sources = %q, want exactly %q", served, wanted)
		}
	}
}

// noWildcard reports whether a policy still carries a wildcard source.
func noWildcard(t *testing.T, policy string) {
	t.Helper()
	for _, directive := range strings.Split(policy, ";") {
		if strings.Contains(directive, "*") {
			t.Fatalf("policy = %q, want no wildcard source (KOSD-C-87)", policy)
		}
	}
}

// TestTC165ConnectSrcIsSelfConsoleAndTheDeclaredBackend is the case DASH-TC-165
// describes.
func TestTC165ConnectSrcIsSelfConsoleAndTheDeclaredBackend(t *testing.T) {
	assets := apphost.NewMemoryAssets()
	for _, application := range []string{reader, notes, backends} {
		if err := assets.Publish(application+"-ext", "1.0.0", digest, map[string][]byte{
			"index.html": []byte("<html>" + application + "</html>"),
		}); err != nil {
			t.Fatalf("publish %s: %v", application, err)
		}
	}
	browser := front.StartWithAssets(t, front.Stub(t), assets,
		front.Contribution(t, manifestOf(reader, "")),
		front.Contribution(t, manifestOf(notes, "")),
		front.Contribution(t, manifestOf(backends, backendURL)),
	)
	console := browser.Server.URL

	// Opening the application is what mints the signed document address the
	// shell mounts; the console origin is the one source both policies share.
	for _, application := range []string{reader, backends} {
		opened := browser.CallJSON(http.MethodPost, "/api/v1/apps/"+application+"/windows", nil)
		if opened.Status != http.StatusAccepted {
			t.Fatalf("open %s = %d %v, want %d", application, opened.Status, opened.Body, http.StatusAccepted)
		}
		frame, _ := opened.Decoded["frame"].(string)
		if frame == "" {
			t.Fatalf("open answer of %s carries no signed frame: %v", application, opened.Body)
		}
		served := fetch(t, browser.Server.URL, frame, originOf(application))
		if served.status != http.StatusOK || served.body != "<html>"+application+"</html>" {
			t.Fatalf("%s document = %d %q, want the packaged document", application, served.status, served.body)
		}
		policy := served.header.Get("Content-Security-Policy")
		if !strings.Contains(policy, "frame-ancestors "+console) {
			t.Fatalf("%s policy = %q, want only the console allowed to frame it (KOSD-C-87)", application, policy)
		}
		noWildcard(t, policy)
		wanted := []string{originOf(application), console}
		if application == backends {
			wanted = append(wanted, "https://files-backend.example.test")
		}
		sameSources(t, connectSources(t, policy), wanted)
	}
}
