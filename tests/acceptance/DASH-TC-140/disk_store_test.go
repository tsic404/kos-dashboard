// Package dashtc140 also covers the deployment-shaped asset store: a deployment
// unpacks the verified packages on disk, and the front must serve a signed
// address out of the directory that address names (DASH-TC-140, KOSD-C-73).
package dashtc140

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
)

// diskSealKey is the front's seal key, so the case can mint the expired address
// a deployment's clock would refuse.
var diskSealKey = []byte("0123456789abcdef0123456789abcdef")

// TestSignedEntryDocumentServesTheDeploymentAssetRoot drives the directory store
// under the documented layout instead of the in-memory one: the signed entry
// document and its same-digest subresources come off disk, while a cross-digest
// address and an expired address are refused (DASH-TC-140, KOSD-C-73).
//
// The in-memory store keys packages by extension and version, so it cannot catch
// a served address whose segments the disk store lays out differently; this case
// is the one that binds the two.
func TestSignedEntryDocumentServesTheDeploymentAssetRoot(t *testing.T) {
	cluster := harness.Start(t)
	shell := cluster.StartShell(t)
	shell.SignInOwner(t, ownerName, ownerPass)

	apps := &registry{}
	control := startControlPlane(t, shell, apps)
	audit := &recorder{}

	packaged := map[string]string{
		"index.html": "<html>files from disk</html>",
		"app.css":    "body{color:red}",
		"app.js":     "main()",
		"icon.svg":   "<svg></svg>",
	}
	root := t.TempDir()
	for name, content := range packaged {
		writeDiskAsset(t, root, fileExtID, fileAppID, "1.0.0", assetDigest, name, content)
	}
	front, cookies := startFrontWith(t, control, audit, &apphost.DirAssets{Root: root}, diskSealKey)

	apps.append(contributionOf(t, fileExtID, "files", apphost.PhaseReady,
		manifestOf(fileAppID, fileName, fileOrigin, fileWidth, fileHeight, apphost.WindowModeSingle)))
	opened := openWindow(t, front, cookies, fileAppID)
	// The address the front signs names the same segments the store reads, the
	// application included (KOSD-C-73).
	wantPrefix := fileOrigin + "/app-assets/" + fileExtID + "/" + fileAppID + "/1.0.0/" + assetDigest + "/"
	if !strings.HasPrefix(opened.Frame, wantPrefix) {
		t.Fatalf("frame = %q, want an address under %s", opened.Frame, wantPrefix)
	}

	document := fetchAsset(t, front, opened.Frame, "files.apps.example.test")
	if document.status != http.StatusOK || document.body != packaged["index.html"] {
		t.Fatalf("entry document = %d %q, want the document unpacked on disk", document.status, document.body)
	}
	grant := grantOf(t, document)

	stylesheet := siblingAddress(t, opened.Frame, "app.css")
	if served := fetchAssetWithGrant(t, front, stylesheet, "files.apps.example.test", grant); served.status != http.StatusOK ||
		served.body != packaged["app.css"] {
		t.Fatalf("same-digest subresource = %d %q, want the stylesheet unpacked on disk", served.status, served.body)
	}

	// The grant covers one digest only: the same path under another digest is
	// refused even while the entry document's grant is present (KOSD-C-73).
	otherDigest := "sha256:" + strings.Repeat("f", 64)
	if otherDigest == assetDigest {
		t.Fatal("the case needs a digest other than the packaged one")
	}
	crossDigest := strings.Replace(stylesheet, assetDigest, otherDigest, 1)
	if served := fetchAssetWithGrant(t, front, crossDigest, "files.apps.example.test", grant); served.status != http.StatusForbidden {
		t.Fatalf("cross-digest subresource = %d, want %d", served.status, http.StatusForbidden)
	}

	// An address whose expiry elapsed is refused, so an open window cannot keep
	// loading a document long after the console signed it (KOSD-C-73).
	expired, err := apphost.SignedURL(diskSealKey, fileOrigin, apphost.Asset{
		Extension: fileExtID, AppID: fileAppID, Version: "1.0.0",
		Digest: assetDigest, Path: "index.html",
	}, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("sign the expired address: %v", err)
	}
	if served := fetchAsset(t, front, expired, "files.apps.example.test"); served.status != http.StatusForbidden {
		t.Fatalf("expired address = %d, want %d", served.status, http.StatusForbidden)
	}
}

// writeDiskAsset lays one unpacked package file out the way -app-assets-root
// documents: <extension>/<application>/<version>/<digest>/<asset>.
func writeDiskAsset(t *testing.T, root, extension, application, version, digest, name, content string) {
	t.Helper()
	directory := filepath.Join(root, extension, application, version, digest)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("create the %s package directory: %v", application, err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
