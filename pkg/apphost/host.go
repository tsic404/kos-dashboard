package apphost

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tsic404/kosd/pkg/controlplane"
)

// The audit verbs the app host writes (KOSD-C-14): granting a capability,
// revoking it, and every attempt to use one. The object is the application, so a
// record answers which application a capability belonged to.
const (
	// VerbGrant records a capability the account granted.
	VerbGrant = "grant"
	// VerbRevoke records a capability the account revoked.
	VerbRevoke = "revoke"
	// VerbUse records one attempt to use a capability.
	VerbUse = "use"
)

// appObjectGroup names the application in audit records; it is the extension
// group the `App` contribution belongs to, not a kernel group.
const appObjectGroup = "apps.kos.tsic.top"

// Config wires the app host.
type Config struct {
	// Now supplies the clock; defaults to time.Now.
	Now func() time.Time
	// TokenTTL bounds one application token; zero uses DefaultTokenTTL.
	TokenTTL time.Duration
	// WindowTTL bounds one window registration; zero uses DefaultWindowTTL.
	WindowTTL time.Duration
	// Auditor records capability grants, revocations and uses (KOSD-C-14); nil
	// records nothing.
	Auditor controlplane.Auditor
}

// Host is the application host's session state: the window instances, the
// short-lived tokens bound to them and the account's capability grants
// (KOSD-C-75). It holds no cluster credential of its own.
type Host struct {
	mu        sync.Mutex
	now       func() time.Time
	tokenTTL  time.Duration
	windowTTL time.Duration
	auditor   controlplane.Auditor
	windows   map[string]*Window
	tokens    map[[32]byte]TokenBinding
	grants    map[string]map[string]map[string]bool
}

// New builds the application host.
func New(config Config) *Host {
	host := &Host{
		now:       config.Now,
		tokenTTL:  config.TokenTTL,
		windowTTL: config.WindowTTL,
		auditor:   config.Auditor,
		windows:   make(map[string]*Window),
		tokens:    make(map[[32]byte]TokenBinding),
		grants:    make(map[string]map[string]map[string]bool),
	}
	if host.now == nil {
		host.now = time.Now
	}
	if host.tokenTTL <= 0 {
		host.tokenTTL = DefaultTokenTTL
	}
	if host.windowTTL <= 0 {
		host.windowTTL = DefaultWindowTTL
	}
	return host
}

// Open registers a window instance for one application (DASH-C-30). A
// single-mode application answers its open instance instead of a second one, so
// the repeated open only focuses it; an application the caller cannot see is
// refused exactly as one that is not installed.
func (h *Host) Open(registry *Registry, account, appID string) (Window, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, installed := registryApp(registry, appID)
	if !installed {
		return Window{}, false, ErrAppUnavailable
	}
	now := h.now()
	if entry.Window.Mode == WindowModeSingle {
		for _, window := range h.windows {
			if window.Account != account || window.AppID != appID {
				continue
			}
			if window.State == InstanceStateClosing || window.State == InstanceStateError {
				continue
			}
			h.focusLocked(account, window.ID)
			return *window, false, nil
		}
	}
	window := &Window{
		ID:             newID(),
		AppID:          entry.ID,
		Account:        account,
		Origin:         entry.Origin(),
		State:          InstanceStateOpening,
		IsFocused:      true,
		DefaultWidth:   entry.Window.Width,
		DefaultHeight:  entry.Window.Height,
		DefaultState:   entry.Window.DefaultState,
		Mode:           entry.Window.Mode,
		OpenedAt:       now.UTC(),
		ExpiresAt:      now.Add(h.windowTTL).UTC(),
		HandshakeNonce: newID(),
	}
	for _, other := range h.windows {
		if other.Account == account {
			other.IsFocused = false
		}
	}
	h.windows[window.ID] = window
	return *window, true, nil
}

// Apply settles one account's windows whose application went away: those windows
// enter the error state and their tokens stop working, while every other
// application's windows are untouched (DASH-C-30, KOSD-C-75). The registry is
// read as one caller's session (KOSD-C-59), so it only ever settles that
// caller's windows.
func (h *Host) Apply(registry *Registry, account string) {
	if registry == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, window := range h.windows {
		if window.Account != account {
			continue
		}
		if window.State == InstanceStateError || window.State == InstanceStateClosing {
			continue
		}
		if _, installed := registry.App(window.AppID); !installed {
			window.State = InstanceStateError
			h.revokeWindowLocked(window.ID)
		}
	}
}

// Windows lists the account's instances, newest first, with the state each is
// in. An instance whose application was uninstalled keeps its error state, so the
// shell can show the failure and refuse to reopen it (DASH-C-30).
func (h *Host) Windows(account string) []Window {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()
	windows := make([]Window, 0, len(h.windows))
	for _, window := range h.windows {
		if window.Account != account {
			continue
		}
		windows = append(windows, *window)
	}
	sort.Slice(windows, func(left, right int) bool {
		return windows[left].OpenedAt.After(windows[right].OpenedAt)
	})
	return windows
}

// Handshake runs the console side of the `ready` handshake (KOSD-C-75): the
// message origin must be the application origin the window was opened for, the
// nonce must be the one the open answer handed the shell, and the message must be
// a `ready` of this shape. Only then does it mint the token, and only once.
func (h *Host) Handshake(registry *Registry, account, windowID, origin, nonce string, message []byte) (Init, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()
	window, found := h.windows[windowID]
	if !found || window.Account != account {
		return Init{}, ErrWindowUnknown
	}
	if window.State == InstanceStateError || window.State == InstanceStateClosing {
		return Init{}, ErrAppUnavailable
	}
	entry, installed := registryApp(registry, window.AppID)
	if !installed {
		window.State = InstanceStateError
		h.revokeWindowLocked(window.ID)
		return Init{}, ErrAppUnavailable
	}
	if !sameOriginString(origin, window.Origin) {
		return Init{}, ErrOriginMismatch
	}
	if !sameSecret(nonce, window.HandshakeNonce) {
		return Init{}, ErrNonceMismatch
	}
	ready, err := DecodeReady(message)
	if err != nil {
		return Init{}, err
	}
	if ready.Window != "" && ready.Window != window.ID {
		return Init{}, ErrWindowUnknown
	}
	if window.hasToken {
		return Init{}, ErrHandshakeOnce
	}
	expires := h.now().Add(h.tokenTTL).UTC()
	binding := TokenBinding{
		AppID:       entry.ID,
		Account:     account,
		WindowID:    window.ID,
		Permissions: declaredPermissions(entry),
		ExpiresAt:   expires,
	}
	value, digest, err := Issue(binding)
	if err != nil {
		return Init{}, err
	}
	h.tokens[digest] = binding
	window.hasToken = true
	window.State = InstanceStateNormal
	return Init{
		Type:         MessageInit,
		Token:        value,
		Theme:        DefaultTheme,
		Capabilities: binding.Permissions,
		ExpiresAt:    expires,
	}, nil
}

// Authenticate resolves one application token. An expired or revoked token, a
// closed window and an uninstalled application all refuse alike: the application
// must be reopened (KOSD-C-75).
func (h *Host) Authenticate(value string) (TokenBinding, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()
	binding, found := h.tokens[DigestOf(value)]
	if !found {
		return TokenBinding{}, ErrTokenInvalid
	}
	return binding, nil
}

// Close closes one window and revokes its token (KOSD-C-75).
func (h *Host) Close(account, windowID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	window, found := h.windows[windowID]
	if !found || window.Account != account {
		return ErrWindowUnknown
	}
	window.State = InstanceStateClosing
	h.revokeWindowLocked(windowID)
	return nil
}

// RevokeAccount ends every window and token one account holds, which is what a
// sign-out does (KOSD-C-75).
func (h *Host) RevokeAccount(account string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, window := range h.windows {
		if window.Account == account {
			window.State = InstanceStateClosing
			h.revokeWindowLocked(window.ID)
		}
	}
	delete(h.grants, account)
}

// PermissionState reports one account's grant state of one capability: granted,
// or pending when it was never resolved.
func (h *Host) PermissionState(account, appID, permission string) PermissionState {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.grants[account][appID][permission] {
		return PermissionGranted
	}
	return PermissionPending
}

// Grant records the user's confirmation of one capability and audits it
// (KOSD-C-75, KOSD-C-14). A capability outside the fixed set or undeclared by the
// application is refused.
func (h *Host) Grant(ctx context.Context, registry *Registry, account, appID, permission string) error {
	if !isPermission(permission) {
		return ErrPermissionUnknown
	}
	entry, installed := registryApp(registry, appID)
	if !installed {
		return ErrAppUnavailable
	}
	if !declares(entry, permission) {
		return ErrPermissionUndeclared
	}
	h.mu.Lock()
	if h.grants[account] == nil {
		h.grants[account] = make(map[string]map[string]bool)
	}
	if h.grants[account][appID] == nil {
		h.grants[account][appID] = make(map[string]bool)
	}
	h.grants[account][appID][permission] = true
	h.mu.Unlock()
	h.audit(ctx, account, entry, permission, VerbGrant, controlplane.ResultAccepted)
	return nil
}

// Revoke withdraws one capability, ends the tokens of that application's windows
// and audits it, so the running window loses the capability at once
// (KOSD-C-75, KOSD-C-14).
func (h *Host) Revoke(ctx context.Context, registry *Registry, account, appID, permission string) error {
	if !isPermission(permission) {
		return ErrPermissionUnknown
	}
	entry, installed := registryApp(registry, appID)
	if !installed {
		return ErrAppUnavailable
	}
	h.mu.Lock()
	delete(h.grants[account][appID], permission)
	for _, window := range h.windows {
		if window.Account == account && window.AppID == appID {
			h.revokeWindowLocked(window.ID)
		}
	}
	h.mu.Unlock()
	h.audit(ctx, account, entry, permission, VerbRevoke, controlplane.ResultAccepted)
	return nil
}

// Use judges one capability use of a window and audits the attempt (KOSD-C-14).
// A capability the account never confirmed is refused with ErrConsentRequired,
// so the console asks before the application may use it (KOSD-C-75).
func (h *Host) Use(ctx context.Context, registry *Registry, account, windowID, permission string) (TokenBinding, error) {
	binding, ok := h.bindingOfWindow(account, windowID)
	if !ok {
		return TokenBinding{}, ErrTokenInvalid
	}
	if !isPermission(permission) {
		return TokenBinding{}, ErrPermissionUnknown
	}
	entry, installed := registryApp(registry, binding.AppID)
	if !installed {
		return TokenBinding{}, ErrAppUnavailable
	}
	if !declares(entry, permission) {
		return TokenBinding{}, ErrPermissionUndeclared
	}
	if h.PermissionState(binding.Account, binding.AppID, permission) == PermissionGranted {
		h.audit(ctx, binding.Account, entry, permission, VerbUse, controlplane.ResultAccepted)
		return binding, nil
	}
	h.audit(ctx, binding.Account, entry, permission, VerbUse, controlplane.ResultRejected)
	return TokenBinding{}, ErrConsentRequired
}

// PermissionState is one account's answer to a capability request.
type PermissionState string

const (
	// PermissionPending is a capability the user never confirmed.
	PermissionPending PermissionState = "pending"
	// PermissionGranted is a capability the user confirmed.
	PermissionGranted PermissionState = "granted"
)

// focusLocked makes one window the account's focused instance.
func (h *Host) focusLocked(account, windowID string) {
	for id, window := range h.windows {
		if window.Account == account {
			window.IsFocused = id == windowID
		}
	}
}

// pruneLocked drops the windows whose lifetime ended or whose close was
// requested, and every token that expired or lost its window.
func (h *Host) pruneLocked() {
	now := h.now()
	for id, window := range h.windows {
		if now.After(window.ExpiresAt) || window.State == InstanceStateClosing {
			delete(h.windows, id)
		}
	}
	for digest, binding := range h.tokens {
		if now.After(binding.ExpiresAt) {
			delete(h.tokens, digest)
			continue
		}
		if _, found := h.windows[binding.WindowID]; !found {
			delete(h.tokens, digest)
		}
	}
}

// revokeWindowLocked deletes every token bound to one window.
func (h *Host) revokeWindowLocked(windowID string) {
	for digest, binding := range h.tokens {
		if binding.WindowID == windowID {
			delete(h.tokens, digest)
		}
	}
}

// audit appends one capability record. A nil auditor has no trail to append to.
func (h *Host) audit(ctx context.Context, account string, entry Entry, permission, verb, result string) {
	if h.auditor == nil {
		return
	}
	_ = h.auditor.Record(ctx, controlplane.AuditRecord{
		Actor: account,
		Object: controlplane.ObjectRef{
			Group: appObjectGroup, Kind: "App", Name: entry.ID,
		},
		Verb:       verb + ":" + permission,
		Result:     result,
		Source:     "kos-dashboard/app-host",
		Extension:  entry.Extension,
		OccurredAt: h.now().UTC(),
	})
}

// declaredPermissions lists the capabilities one manifest declares.
func declaredPermissions(entry Entry) []string {
	permissions := make([]string, 0, len(entry.Permissions))
	for _, permission := range entry.Permissions {
		if isPermission(permission) {
			permissions = append(permissions, permission)
		}
	}
	sort.Strings(permissions)
	return permissions
}

// declares reports whether one manifest declared a capability.
func declares(entry Entry, permission string) bool {
	for _, declared := range entry.Permissions {
		if declared == permission {
			return true
		}
	}
	return false
}

// registryApp resolves one application in the caller's registry.
func registryApp(registry *Registry, appID string) (Entry, bool) {
	if registry == nil {
		return Entry{}, false
	}
	return registry.App(appID)
}

// Init is the console's answer to a `ready` message: the token, the theme and the
// capabilities the application may use (KOSD-C-75). The token is delivered once
// and never read back (KOSD-C-14).
type Init struct {
	// Type is the message name the shell posts.
	Type string `json:"type"`
	// Token is the short-lived application token.
	Token string `json:"token"`
	// Theme is the console's active theme, so the frame renders consistently.
	Theme string `json:"theme"`
	// Capabilities are the capabilities the application declared.
	Capabilities []string `json:"capabilities"`
	// ExpiresAt is when the token stops being usable.
	ExpiresAt time.Time `json:"expiresAt"`
}

// Ready is one `ready` message an application posts (KOSD-C-75).
type Ready struct {
	// Type is the message name; it must be "ready".
	Type string `json:"type"`
	// Window optionally names the instance; a mismatch is refused.
	Window string `json:"window,omitempty"`
}

// DecodeReady validates the shape of one `ready` message. Unknown fields are
// refused, so a message the console does not understand has no effect.
func DecodeReady(message []byte) (Ready, error) {
	var ready Ready
	decoder := json.NewDecoder(bytes.NewReader(message))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ready); err != nil {
		return Ready{}, fmt.Errorf("app host: read ready message: %w", err)
	}
	if ready.Type != MessageReady {
		return Ready{}, fmt.Errorf("app host: message type %q is not %q", ready.Type, MessageReady)
	}
	return ready, nil
}

// sameOriginString compares two origins case-insensitively.
func sameOriginString(left, right string) bool {
	return left != "" && right != "" && strings.EqualFold(left, right)
}

// newID draws one URL-safe identity.
func newID() string {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// bindingOfWindow resolves the token binding of one window the account holds.
func (h *Host) bindingOfWindow(account, windowID string) (TokenBinding, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked()
	window, found := h.windows[windowID]
	if !found || window.Account != account {
		return TokenBinding{}, false
	}
	for _, binding := range h.tokens {
		if binding.WindowID == windowID {
			return binding, true
		}
	}
	return TokenBinding{}, false
}
