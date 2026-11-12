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

// AssetAddressPrefix is the path every application asset is served under.
const AssetAddressPrefix = "/app-assets/"

// Errors one signed asset address may answer with (KOSD-C-73).
var (
	// ErrAssetSignature refuses an address whose signature does not verify.
	ErrAssetSignature = errors.New("asset address signature does not verify")
	// ErrAssetExpired refuses an address past its expiry.
	ErrAssetExpired = errors.New("asset address expired")
)

// Asset is one UI asset address bound to the extension, application, version and
// digest it belongs to (KOSD-C-73). The digest is the package digest the control
// plane verified, so the address cannot be pointed at other bytes, and the
// application identity keeps two applications of one package apart.
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

// AddressPath renders the path one asset is served at: the extension, the
// application, the version and the digest all name the address, so two
// applications of one package each have their own (KOSD-C-73).
func (a Asset) AddressPath() string {
	return AssetAddressPrefix + a.Extension + "/" + a.AppID + "/" + a.Version + "/" + a.Digest +
		"/" + strings.TrimPrefix(a.Path, "/")
}

// SignedURL renders the short-lived address a frame loads the asset from. The
// signature binds the extension, application, version, digest and expiry, so an
// address cannot be replayed for another document or after it expired
// (KOSD-C-73). The path is not part of the signature: a document's relative
// references are served under the same grant.
func SignedURL(secret []byte, origin string, asset Asset, expires time.Time) (string, error) {
	if origin == "" {
		return "", fmt.Errorf("apphost: asset origin is empty")
	}
	target, err := url.Parse(origin)
	if err != nil || target.Host == "" {
		return "", fmt.Errorf("apphost: asset origin %q is not absolute", origin)
	}
	target.Path = strings.TrimSuffix(target.Path, "/") + asset.AddressPath()
	query := target.Query()
	query.Set("kos-exp", strconv.FormatInt(expires.UTC().Unix(), 10))
	query.Set("kos-sig", SignAsset(secret, asset, expires))
	target.RawQuery = query.Encode()
	return target.String(), nil
}

// AssetGrantCookie is the scoped credential a served signed address leaves on the
// application origin. A relative reference carries no query, so the document's
// grant rides a cookie scoped to the asset routes instead (KOSD-C-73).
const AssetGrantCookie = "kos_asset_grant"

// AssetGrantPath scopes AssetGrantCookie to the asset routes; no other
// application route receives it.
const AssetGrantPath = AssetAddressPrefix

// SignAsset renders the signature of one asset address and its expiry. The
// signed message is the extension, application, version and digest with the
// expiry, so neither the address nor the document it names can be swapped and
// every asset of one package version shares the grant (KOSD-C-73).
func SignAsset(secret []byte, asset Asset, expires time.Time) string {
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%s\n%s\n%s\n%s\n%d",
		asset.Extension, asset.AppID, asset.Version, asset.Digest,
		expires.UTC().Unix())
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyAsset judges one signed address. A signature over other fields and an
// elapsed expiry both fail closed, so the frame shows its load error instead of
// downgraded content (KOSD-C-73).
func VerifyAsset(secret []byte, asset Asset, expires time.Time, signature string, now time.Time) error {
	if now.After(expires) {
		return ErrAssetExpired
	}
	if !sameSecret(SignAsset(secret, asset, expires), signature) {
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

// SignAssetGrant renders the credential a served document leaves for its relative
// references: the document's five bindings with their expiry, in the same order
// the address carries them (KOSD-C-73).
func SignAssetGrant(secret []byte, asset Asset, expires time.Time) string {
	return strconv.FormatInt(expires.UTC().Unix(), 10) + "." + SignAsset(secret, asset, expires)
}

// VerifyAssetGrant judges one grant credential against the asset address it was
// presented for. A value that is not the document's expiry and signature, names
// another binding, or outlived its expiry fails closed (KOSD-C-73).
func VerifyAssetGrant(secret []byte, asset Asset, credential string, now time.Time) error {
	expiry, signature, found := strings.Cut(credential, ".")
	if !found || expiry == "" || signature == "" {
		return ErrAssetSignature
	}
	expires, err := ParseExpiry(expiry)
	if err != nil {
		return ErrAssetSignature
	}
	return VerifyAsset(secret, asset, expires, signature, now)
}
