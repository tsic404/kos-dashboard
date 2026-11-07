package apphost

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The two message names the handshake uses (KOSD-C-75). Every other message is
// unknown and leaves no effect.
const (
	// MessageReady is what an application posts once its document loaded.
	MessageReady = "ready"
	// MessageInit is what the console posts back with the token.
	MessageInit = "init"
)

// FrameSandbox is the sandbox attribute a window frame is created with: scripts
// and the frame's own storage are allowed, while top navigation, the console
// DOM and shared cookies stay out of reach (KOSD-C-75).
const FrameSandbox = "allow-scripts allow-same-origin"

// DefaultTheme is the theme the console hands to a frame. Theme editing belongs
// to another stage; the handshake carries the default so a frame can render.
const DefaultTheme = "system"

// DefaultAssetTTL bounds one signed asset address (KOSD-C-73).
const DefaultAssetTTL = 10 * time.Minute

// Errors one signed asset address may answer with (KOSD-C-73).
var (
	// ErrAssetSignature refuses an address whose signature does not verify.
	ErrAssetSignature = errors.New("asset address signature does not verify")
	// ErrAssetExpired refuses an address past its expiry.
	ErrAssetExpired = errors.New("asset address expired")
)

// Asset is one UI asset address bound to the extension, application, version
// and digest it belongs to (KOSD-C-73). The digest is the package digest the
// control plane verified, so the address cannot be pointed at other bytes.
type Asset struct {
	// Extension is the extension identity.
	Extension string
	// AppID is the application identity.
	AppID string
	// Version is the application version.
	Version string
	// Digest is the package digest in sha256:<hex> form.
	Digest string
	// Path is the asset path inside the package.
	Path string
}

// SignedURL renders the short-lived address a frame loads the asset from. The
// signature binds the extension, application, version, digest and expiry, so an
// address cannot be replayed for another version or after it expired.
func SignedURL(secret []byte, origin string, asset Asset, expires time.Time) (string, error) {
	if origin == "" {
		return "", fmt.Errorf("apphost: asset origin is empty")
	}
	if !strings.HasPrefix(asset.Path, "/") {
		asset.Path = "/" + asset.Path
	}
	target, err := url.Parse(origin)
	if err != nil || target.Host == "" {
		return "", fmt.Errorf("apphost: asset origin %q is not absolute", origin)
	}
	target.Path = strings.TrimSuffix(target.Path, "/") + asset.Path
	query := target.Query()
	query.Set("kos-exp", strconv.FormatInt(expires.UTC().Unix(), 10))
	query.Set("kos-sig", SignAsset(secret, asset, expires))
	target.RawQuery = query.Encode()
	return target.String(), nil
}

// SignAsset renders the signature of one asset address and its expiry.
func SignAsset(secret []byte, asset Asset, expires time.Time) string {
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%s\n%s\n%s\n%s\n%d",
		asset.Extension, asset.AppID, asset.Version, asset.Digest, expires.UTC().Unix())
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyAsset judges one signed address. A digest that is not the installed
// one, a signature over other fields and an elapsed expiry all fail closed, so
// the frame does not load (KOSD-C-73).
func VerifyAsset(secret []byte, asset Asset, expires time.Time, signature string, now time.Time) error {
	if now.After(expires) {
		return ErrAssetExpired
	}
	expected := SignAsset(secret, asset, expires)
	if !sameSecret(expected, signature) {
		return ErrAssetSignature
	}
	return nil
}

// ParseExpiry reads the expiry parameter of one signed asset address.
func ParseExpiry(raw string) (time.Time, error) {
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("apphost: asset expiry %q is not a timestamp", raw)
	}
	return time.Unix(seconds, 0).UTC(), nil
}
