package apphost_test

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
)

// assetFixture is one signed address's binding.
func assetFixture() apphost.Asset {
	return apphost.Asset{
		Extension: "files-ext", AppID: "files", Version: "1.0.0",
		Digest: "sha256:abc", Path: "index.html",
	}
}

func TestSignedAssetAddressNamesTheApplicationAndItsPackage(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	expires := now.Add(5 * time.Minute)
	asset := assetFixture()

	address, err := apphost.SignedURL(secret, "https://files.apps.example.test", asset, expires)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// The address names the extension, application, version and digest, so two
	// applications of one package never share an address (KOSD-C-73).
	if !strings.HasPrefix(address,
		"https://files.apps.example.test/app-assets/files-ext/files/1.0.0/sha256:abc/index.html?") {
		t.Fatalf("address = %q, want the versioned application address", address)
	}
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	signature := parsed.Query().Get("kos-sig")
	if err := apphost.VerifyAsset(secret, asset, expires, signature, now); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// Another digest, another path, another application and another moment all
	// fail closed.
	other := asset
	other.Digest = "sha256:def"
	if err := apphost.VerifyAsset(secret, other, expires, signature, now); !errors.Is(err, apphost.ErrAssetSignature) {
		t.Fatalf("other digest = %v, want %v", err, apphost.ErrAssetSignature)
	}
	otherPath := asset
	otherPath.Path = "other.html"
	if err := apphost.VerifyAsset(secret, otherPath, expires, signature, now); !errors.Is(err, apphost.ErrAssetSignature) {
		t.Fatalf("other path = %v, want %v", err, apphost.ErrAssetSignature)
	}
	otherApp := asset
	otherApp.AppID = "notes"
	if err := apphost.VerifyAsset(secret, otherApp, expires, signature, now); !errors.Is(err, apphost.ErrAssetSignature) {
		t.Fatalf("other application = %v, want %v", err, apphost.ErrAssetSignature)
	}
	if err := apphost.VerifyAsset(secret, asset, expires, signature, expires.Add(time.Second)); !errors.Is(err, apphost.ErrAssetExpired) {
		t.Fatalf("expired address = %v, want %v", err, apphost.ErrAssetExpired)
	}
}

// TestTwoApplicationsOfOnePackageHaveTheirOwnAddresses checks that a package
// declaring two applications gives each a verifiable address: one application's
// signature never serves the other's address (KOSD-C-73).
func TestTwoApplicationsOfOnePackageHaveTheirOwnAddresses(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	expires := time.Date(2026, 11, 7, 12, 10, 0, 0, time.UTC)
	first := assetFixture()
	second := first
	second.AppID = "notes"

	firstAddress, err := apphost.SignedURL(secret, "https://files.apps.example.test", first, expires)
	if err != nil {
		t.Fatalf("sign the first: %v", err)
	}
	secondAddress, err := apphost.SignedURL(secret, "https://files.apps.example.test", second, expires)
	if err != nil {
		t.Fatalf("sign the second: %v", err)
	}
	if firstAddress == secondAddress {
		t.Fatal("two applications of one package share an address, want one each")
	}
	for _, candidate := range []struct {
		asset   apphost.Asset
		address string
	}{{first, firstAddress}, {second, secondAddress}} {
		parsed, err := url.Parse(candidate.address)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if err := apphost.VerifyAsset(secret, candidate.asset, expires,
			parsed.Query().Get("kos-sig"), expires.Add(-time.Minute)); err != nil {
			t.Fatalf("verify %s: %v", candidate.asset.AppID, err)
		}
	}
}

// TestDirAssetsServesOnlyThePublishedVersion checks the directory store: an
// address names the version and digest directory, a path outside it is not
// served, and a version the deployment did not unpack is unaddressed (KOSD-C-73).
func TestDirAssetsServesOnlyThePublishedVersion(t *testing.T) {
	root := t.TempDir()
	directory := root + "/files-ext/1.0.0/sha256:abc"
	if err := writeFile(directory, "index.html", "<html>v1</html>"); err != nil {
		t.Fatalf("write the package: %v", err)
	}
	store := &apphost.DirAssets{Root: root}
	asset := assetFixture()
	content, err := store.Read(t.Context(), asset)
	if err != nil || string(content) != "<html>v1</html>" {
		t.Fatalf("read = %q, %v, want the published document", content, err)
	}
	if _, err := store.Read(t.Context(), apphost.Asset{
		Extension: "files-ext", AppID: "files", Version: "2.0.0",
		Digest: "sha256:def", Path: "index.html",
	}); !errors.Is(err, apphost.ErrAssetUnaddressed) {
		t.Fatalf("read of an unpacked version = %v, want %v", err, apphost.ErrAssetUnaddressed)
	}
	if _, err := store.Read(t.Context(), apphost.Asset{
		Extension: "files-ext", AppID: "files", Version: "1.0.0",
		Digest: "sha256:abc", Path: "../../escape",
	}); !errors.Is(err, apphost.ErrAssetUnknown) {
		t.Fatalf("read of a path leaving the root = %v, want %v", err, apphost.ErrAssetUnknown)
	}
}

// writeFile stores one file of an unpacked package.
func writeFile(directory, name, content string) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, name), []byte(content), 0o644)
}
