package apphost

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// The window and token lifetimes (KOSD-C-75). The token is short-lived: it
// outlives one sitting of the window and never a day.
const (
	// DefaultTokenTTL is how long an application token stays usable.
	DefaultTokenTTL = 15 * time.Minute
	// DefaultWindowTTL is how long a window instance stays registered.
	DefaultWindowTTL = 12 * time.Hour
	// tokenBytes is the entropy of one application token.
	tokenBytes = 32
)

// The window states the app host reports (DASH-C-74).
const (
	// InstanceStateOpening is a registered instance whose handshake did not run
	// yet.
	InstanceStateOpening = "opening"
	// InstanceStateNormal is an instance whose handshake completed.
	InstanceStateNormal = "normal"
	// InstanceStateError is an instance whose application went away; it cannot be
	// reopened (DASH-C-30).
	InstanceStateError = "error"
	// InstanceStateClosing is an instance whose close was requested.
	InstanceStateClosing = "closing"
)

// Errors the app host answers with. They are refusals of the console, not of
// the control plane, so the BFF renders them in its own envelope.
var (
	// ErrAppUnavailable refuses an application missing from the registry, or
	// one whose window is in the error state: it cannot be reopened after its
	// extension was uninstalled (DASH-C-30).
	ErrAppUnavailable = errors.New("application is not installed")
	// ErrWindowUnknown refuses an instance the host does not hold.
	ErrWindowUnknown = errors.New("window instance is unknown or expired")
	// ErrHandshakeOnce refuses a second handshake: the token was delivered once
	// (KOSD-C-75).
	ErrHandshakeOnce = errors.New("the handshake of this window already delivered its token")
	// ErrOriginMismatch refuses a message whose origin is not the application
	// origin (KOSD-C-75).
	ErrOriginMismatch = errors.New("message origin is not the application origin")
	// ErrNonceMismatch refuses a message that does not carry the nonce the host
	// issued for this window.
	ErrNonceMismatch = errors.New("message does not carry this window's handshake nonce")
	// ErrTokenInvalid refuses a token that expired, was revoked, or belongs to
	// a window that is gone.
	ErrTokenInvalid = errors.New("application token is expired or revoked")
	// ErrPermissionUnknown refuses a capability outside the fixed set
	// (KOSD-C-75).
	ErrPermissionUnknown = errors.New("permission is not in the fixed set")
	// ErrPermissionUndeclared refuses a capability the application did not
	// declare in its manifest.
	ErrPermissionUndeclared = errors.New("application did not declare this permission")
	// ErrConsentRequired answers a capability whose first use awaits the user's
	// confirmation (KOSD-C-75).
	ErrConsentRequired = errors.New("permission awaits the user's confirmation")
)

// Window is one window instance an application runs in (KOSD-C-75). It binds
// the account, the application, the handshake nonce and the token it received.
// Geometry and z-order stay in the browser (DASH-C-74); the host keeps only
// what it must to judge a token.
type Window struct {
	// ID is the instance identity the browser names when it closes the window.
	ID string `json:"id"`
	// AppID is the application the instance runs.
	AppID string `json:"appId"`
	// Account is the account that opened the instance.
	Account string `json:"account"`
	// Origin is the application origin the instance was opened for.
	Origin string `json:"origin"`
	// State is the instance state (DASH-C-74).
	State string `json:"state"`
	// IsFocused marks the instance a single-mode application focuses.
	IsFocused bool `json:"isFocused"`
	// DefaultWidth and DefaultHeight are the manifest's window defaults
	// (KOSD-C-73); the shell opens the window at this size.
	DefaultWidth  int `json:"defaultWidth"`
	DefaultHeight int `json:"defaultHeight"`
	// DefaultState is the state the manifest opens in (DASH-C-74).
	DefaultState string `json:"defaultState"`
	// Mode is the instance mode the manifest declares.
	Mode string `json:"mode"`
	// OpenedAt is when the instance was registered.
	OpenedAt time.Time `json:"openedAt"`
	// ExpiresAt bounds the instance registration.
	ExpiresAt time.Time `json:"expiresAt"`

	// HandshakeNonce is the challenge the shell passes to the frame and posts
	// back with the `ready` message; it binds one handshake to this window
	// (KOSD-C-75).
	HandshakeNonce string `json:"handshakeNonce"`
	// hasToken marks that the handshake delivered a token; a second handshake
	// is refused (KOSD-C-75).
	hasToken bool
}

// TokenBinding is what one application token authorises: the account, the
// application, the window instance, the declared capabilities and the expiry
// (KOSD-C-75).
type TokenBinding struct {
	// AppID is the application the token belongs to.
	AppID string
	// Account is the account that opened the window.
	Account string
	// WindowID is the window instance the token is bound to.
	WindowID string
	// Permissions are the capabilities the application declared.
	Permissions []string
	// ExpiresAt is the moment the token stops being usable.
	ExpiresAt time.Time
}

// Issue mints one token for a binding. It returns the value to deliver and the
// digest to keep; the caller stores only the digest (KOSD-C-14).
func Issue(binding TokenBinding) (value string, digest [sha256.Size]byte, err error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", digest, fmt.Errorf("apphost: draw a token: %w", err)
	}
	value = base64.RawURLEncoding.EncodeToString(raw)
	return value, sha256.Sum256([]byte(value)), nil
}

// DigestOf names the stored form of one token value.
func DigestOf(value string) [sha256.Size]byte {
	return sha256.Sum256([]byte(value))
}

// sameSecret compares two secrets without leaking where they differ.
func sameSecret(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
