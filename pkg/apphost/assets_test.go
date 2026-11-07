package apphost_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
)

// assetFixture is one signed address's binding.
func assetFixture() apphost.Asset {
	return apphost.Asset{
		Extension: "files-ext", AppID: "files", Version: "1.0.0",
		Digest: "sha256:abc", Path: "/assets/index.html",
	}
}

func TestSignedAssetAddressIsBoundToItsDigestAndExpiry(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	expires := now.Add(5 * time.Minute)
	asset := assetFixture()

	address, err := apphost.SignedURL(secret, "https://files.apps.example.test", asset, expires)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !strings.HasPrefix(address, "https://files.apps.example.test/assets/index.html?") {
		t.Fatalf("address = %q, want a path on the application origin (KOSD-C-73)", address)
	}
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	signature := parsed.Query().Get("kos-sig")
	if err := apphost.VerifyAsset(secret, asset, expires, signature, now); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// Another digest, another version and another moment all fail closed.
	other := asset
	other.Digest = "sha256:def"
	if err := apphost.VerifyAsset(secret, other, expires, signature, now); !errors.Is(err, apphost.ErrAssetSignature) {
		t.Fatalf("other digest = %v, want %v", err, apphost.ErrAssetSignature)
	}
	if err := apphost.VerifyAsset(secret, asset, expires, signature, expires.Add(time.Second)); !errors.Is(err, apphost.ErrAssetExpired) {
		t.Fatalf("expired address = %v, want %v", err, apphost.ErrAssetExpired)
	}
}
