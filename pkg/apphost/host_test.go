package apphost_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tsic404/kosd/pkg/controlplane"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
)

// recorder keeps the audit records the host writes.
type recorder struct {
	mu      sync.Mutex
	records []controlplane.AuditRecord
}

func (r *recorder) Record(_ context.Context, record controlplane.AuditRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, record)
	return nil
}

// countFor counts the records of one application.
func (r *recorder) countFor(app string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, record := range r.records {
		if record.Object.Name == app {
			count++
		}
	}
	return count
}

// clock returns a movable clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(delta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(delta)
}

// readyMessage renders the ready payload one frame posts.
func readyMessage(t *testing.T, window string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"type": apphost.MessageReady, "window": window})
	if err != nil {
		t.Fatalf("encode ready: %v", err)
	}
	return raw
}

// manifestWithPermissions renders the fixture manifest with two declared
// capabilities, so a case can revoke one and keep the other.
func manifestWithPermissions(t *testing.T, permissions ...string) map[string]any {
	t.Helper()
	document := manifestDoc(t)
	declared := make([]any, 0, len(permissions))
	for _, permission := range permissions {
		declared = append(declared, permission)
	}
	document["permissions"] = declared
	return document
}

// fixture is one host, its registry and the clock the host reads.
type fixture struct {
	host     *apphost.Host
	registry *apphost.Registry
	clock    *clock
	audit    *recorder
}

// newFixture builds a host over one installed application.
func newFixture(t *testing.T) fixture {
	t.Helper()
	now := &clock{now: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
	audit := &recorder{}
	return fixture{
		host:     apphost.New(apphost.Config{Now: now.Now, Auditor: audit}),
		registry: loaded(t, contributionOf(t, "files-ext", "files", apphost.PhaseReady, manifestDoc(t))),
		clock:    now,
		audit:    audit,
	}
}

// handshake runs the whole open-handshake sequence for one browser session.
func handshake(t *testing.T, fixture fixture, conversation, account, appID string) apphost.Init {
	t.Helper()
	window, _, err := fixture.host.Open(fixture.registry, conversation, account, appID)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	init, err := fixture.host.Handshake(fixture.registry, account, window.ID,
		"https://files.apps.example.test", window.HandshakeNonce, readyMessage(t, window.ID))
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	return init
}

func TestOpenUsesManifestDefaultsAndSingleModeFocuses(t *testing.T) {
	fixture := newFixture(t)
	first, isNew, err := fixture.host.Open(fixture.registry, "browser-a", "owner", "files")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !isNew || first.DefaultWidth != 1024 || first.DefaultHeight != 720 || first.DefaultState != "normal" {
		t.Fatalf("window = %+v, want the manifest's default geometry", first)
	}
	second, isNew, err := fixture.host.Open(fixture.registry, "browser-a", "owner", "files")
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if isNew || second.ID != first.ID || !second.IsFocused {
		t.Fatalf("second open = %+v, want the same instance focused (DASH-TC-140)", second)
	}
}

func TestOpenRefusesAnUninstalledApplication(t *testing.T) {
	fixture := newFixture(t)
	if _, _, err := fixture.host.Open(fixture.registry, "browser-a", "owner", "absent"); !errors.Is(err, apphost.ErrAppUnavailable) {
		t.Fatalf("open of an uninstalled application = %v, want %v", err, apphost.ErrAppUnavailable)
	}
}

func TestHandshakeDeliversTheTokenOnce(t *testing.T) {
	fixture := newFixture(t)
	window, _, err := fixture.host.Open(fixture.registry, "browser-a", "owner", "files")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	init, err := fixture.host.Handshake(fixture.registry, "owner", window.ID,
		"https://files.apps.example.test", window.HandshakeNonce, readyMessage(t, window.ID))
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if init.Token == "" || init.Type != apphost.MessageInit {
		t.Fatalf("init = %+v, want an init message with a token", init)
	}
	if _, err := fixture.host.Handshake(fixture.registry, "owner", window.ID,
		"https://files.apps.example.test", window.HandshakeNonce, readyMessage(t, window.ID)); !errors.Is(err, apphost.ErrHandshakeOnce) {
		t.Fatalf("second handshake = %v, want %v (KOSD-C-75)", err, apphost.ErrHandshakeOnce)
	}
	if binding, err := fixture.host.Authenticate(init.Token); err != nil ||
		binding.AppID != "files" || binding.WindowID != window.ID {
		t.Fatalf("authenticate = %+v, %v, want the window binding", binding, err)
	}
}

func TestReopenAfterHandshakeAdvertisesTheDeliveredToken(t *testing.T) {
	fixture := newFixture(t)
	first, _, err := fixture.host.Open(fixture.registry, "browser-a", "owner", "files")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := fixture.host.Handshake(fixture.registry, "owner", first.ID,
		"https://files.apps.example.test", first.HandshakeNonce, readyMessage(t, first.ID)); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	// A shell that lost its own record (a reload) asks to open the same single
	// application again. The answer names the same instance and marks that its
	// token was already delivered, so the shell closes it and opens a fresh one
	// instead of handshaking a window that refuses a second token (KOSD-C-75).
	reused, isNew, err := fixture.host.Open(fixture.registry, "browser-a", "owner", "files")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if isNew || reused.ID != first.ID || !reused.HasToken {
		t.Fatalf("reopen = %+v, isNew=%v, want the same instance marked as delivered", reused, isNew)
	}
	if err := fixture.host.Close("owner", reused.ID); err != nil {
		t.Fatalf("close: %v", err)
	}
	fresh, isNew, err := fixture.host.Open(fixture.registry, "browser-a", "owner", "files")
	if err != nil {
		t.Fatalf("open after close: %v", err)
	}
	if !isNew || fresh.ID == reused.ID || fresh.HasToken {
		t.Fatalf("fresh open = %+v, isNew=%v, want a new instance without a token", fresh, isNew)
	}
	if _, err := fixture.host.Handshake(fixture.registry, "owner", fresh.ID,
		"https://files.apps.example.test", fresh.HandshakeNonce, readyMessage(t, fresh.ID)); err != nil {
		t.Fatalf("handshake after the reload recovery: %v", err)
	}
}

func TestSecondBrowserMountsItsOwnSingleInstance(t *testing.T) {
	fixture := newFixture(t)
	if handshake(t, fixture, "browser-a", "owner", "files").Token == "" {
		t.Fatal("the first browser got no token")
	}
	// The opening set is browser session state (DASH-C-74): the same account in a
	// second browser gets its own instance and mounts without disturbing the
	// first.
	init := handshake(t, fixture, "browser-b", "owner", "files")
	if init.Token == "" {
		t.Fatal("the second browser could not mount the same application")
	}
	first := fixture.host.Windows("browser-a", "owner")
	second := fixture.host.Windows("browser-b", "owner")
	if len(first) != 1 || len(second) != 1 || first[0].ID == second[0].ID {
		t.Fatalf("first = %+v, second = %+v, want one instance per browser session", first, second)
	}
	// The focus mark is session state as well: the second browser raising its own
	// window does not blur the window the first one holds (DASH-C-74).
	if !first[0].IsFocused || !second[0].IsFocused {
		t.Fatalf("focus after the second browser opened = %v/%v, want each session focused on its own window",
			first[0].IsFocused, second[0].IsFocused)
	}
}

func TestHandshakeRefusesAnotherOriginAndAnotherNonce(t *testing.T) {
	fixture := newFixture(t)
	window, _, _ := fixture.host.Open(fixture.registry, "browser-a", "owner", "files")
	if _, err := fixture.host.Handshake(fixture.registry, "owner", window.ID,
		"https://evil.example.test", window.HandshakeNonce, readyMessage(t, window.ID)); !errors.Is(err, apphost.ErrOriginMismatch) {
		t.Fatalf("handshake from another origin = %v, want %v", err, apphost.ErrOriginMismatch)
	}
	if _, err := fixture.host.Handshake(fixture.registry, "owner", window.ID,
		"https://files.apps.example.test", "other", readyMessage(t, window.ID)); !errors.Is(err, apphost.ErrNonceMismatch) {
		t.Fatalf("handshake with another nonce = %v, want %v", err, apphost.ErrNonceMismatch)
	}
	if _, err := fixture.host.Handshake(fixture.registry, "owner", window.ID,
		"https://files.apps.example.test", window.HandshakeNonce, []byte(`{"type":"unknown"}`)); err == nil {
		t.Fatal("unknown message accepted, want it ignored without a token (KOSD-C-75)")
	}
}

func TestTokenExpiresAndCloseRevokesIt(t *testing.T) {
	fixture := newFixture(t)
	window, _, _ := fixture.host.Open(fixture.registry, "browser-a", "owner", "files")
	init, err := fixture.host.Handshake(fixture.registry, "owner", window.ID,
		"https://files.apps.example.test", window.HandshakeNonce, readyMessage(t, window.ID))
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	fixture.clock.Advance(apphost.DefaultTokenTTL + time.Second)
	if _, err := fixture.host.Authenticate(init.Token); !errors.Is(err, apphost.ErrTokenInvalid) {
		t.Fatalf("expired token = %v, want %v", err, apphost.ErrTokenInvalid)
	}

	// An expired token requires reopening the window (KOSD-C-75).
	if err := fixture.host.Close("owner", window.ID); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, isNew, err := fixture.host.Open(fixture.registry, "browser-a", "owner", "files")
	if err != nil || !isNew || reopened.ID == window.ID {
		t.Fatalf("reopen = %+v, %v, %v, want a fresh instance", reopened, isNew, err)
	}
	fresh, err := fixture.host.Handshake(fixture.registry, "owner", reopened.ID,
		"https://files.apps.example.test", reopened.HandshakeNonce, readyMessage(t, reopened.ID))
	if err != nil {
		t.Fatalf("handshake after expiry: %v", err)
	}
	if _, err := fixture.host.Authenticate(fresh.Token); err != nil {
		t.Fatalf("fresh token: %v", err)
	}
	if err := fixture.host.Close("owner", reopened.ID); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := fixture.host.Authenticate(fresh.Token); !errors.Is(err, apphost.ErrTokenInvalid) {
		t.Fatalf("token after close = %v, want %v", err, apphost.ErrTokenInvalid)
	}
}

func TestUninstallEntersErrorStateAndRefusesReopen(t *testing.T) {
	fixture := newFixture(t)
	window, _, _ := fixture.host.Open(fixture.registry, "browser-a", "owner", "files")
	init, err := fixture.host.Handshake(fixture.registry, "owner", window.ID,
		"https://files.apps.example.test", window.HandshakeNonce, readyMessage(t, window.ID))
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	// The uninstall removes the contribution from the registry the front reads.
	gone := apphost.Load(nil, apphost.ValidationOptions{})
	fixture.host.Apply(gone, "owner")
	if _, err := fixture.host.Authenticate(init.Token); !errors.Is(err, apphost.ErrTokenInvalid) {
		t.Fatalf("token after uninstall = %v, want %v (KOSD-C-75)", err, apphost.ErrTokenInvalid)
	}
	windows := fixture.host.Windows("browser-a", "owner")
	if len(windows) != 1 || windows[0].State != apphost.InstanceStateError {
		t.Fatalf("windows = %+v, want the open window in its error state (DASH-C-30)", windows)
	}
	if _, _, err := fixture.host.Open(gone, "browser-a", "owner", "files"); !errors.Is(err, apphost.ErrAppUnavailable) {
		t.Fatalf("reopen after uninstall = %v, want %v", err, apphost.ErrAppUnavailable)
	}
}

func TestPermissionGrantUseAndRevoke(t *testing.T) {
	now := &clock{now: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
	audit := &recorder{}
	fixture := fixture{
		host: apphost.New(apphost.Config{Now: now.Now, Auditor: audit}),
		registry: loaded(t, contributionOf(t, "files-ext", "files", apphost.PhaseReady,
			manifestWithPermissions(t, apphost.PermissionNotifications, apphost.PermissionClipboardRead))),
		clock: now,
		audit: audit,
	}
	window, _, _ := fixture.host.Open(fixture.registry, "browser-a", "owner", "files")
	init, err := fixture.host.Handshake(fixture.registry, "owner", window.ID,
		"https://files.apps.example.test", window.HandshakeNonce, readyMessage(t, window.ID))
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	ctx := context.Background()
	if _, err := fixture.host.Use(ctx, fixture.registry, "owner", window.ID, apphost.PermissionNotifications); !errors.Is(err, apphost.ErrConsentRequired) {
		t.Fatalf("first use = %v, want %v (KOSD-C-75)", err, apphost.ErrConsentRequired)
	}
	if err := fixture.host.Grant(ctx, fixture.registry, "owner", "files", apphost.PermissionNotifications); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := fixture.host.Grant(ctx, fixture.registry, "owner", "files", apphost.PermissionClipboardRead); err != nil {
		t.Fatalf("grant the second capability: %v", err)
	}
	if _, err := fixture.host.Use(ctx, fixture.registry, "owner", window.ID, apphost.PermissionNotifications); err != nil {
		t.Fatalf("use after grant: %v", err)
	}
	if err := fixture.host.Revoke(ctx, fixture.registry, "owner", "files", apphost.PermissionNotifications); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	// Only the revoked capability stops: the window keeps its token and the
	// other confirmed capability (KOSD-C-75).
	if _, err := fixture.host.Use(ctx, fixture.registry, "owner", window.ID, apphost.PermissionNotifications); !errors.Is(err, apphost.ErrPermissionRevoked) {
		t.Fatalf("use after revoke = %v, want %v", err, apphost.ErrPermissionRevoked)
	}
	if _, err := fixture.host.Use(ctx, fixture.registry, "owner", window.ID, apphost.PermissionClipboardRead); err != nil {
		t.Fatalf("use of another confirmed capability after the revoke: %v", err)
	}
	if _, err := fixture.host.Authenticate(init.Token); err != nil {
		t.Fatalf("token after a revocation = %v, want the window still usable", err)
	}
	if err := fixture.host.Revoke(ctx, fixture.registry, "owner", "files", "camera"); !errors.Is(err, apphost.ErrPermissionUnknown) {
		t.Fatalf("revoke of a permission outside the fixed set = %v, want %v", err, apphost.ErrPermissionUnknown)
	}
	if records := fixture.audit.countFor("files"); records < 5 {
		t.Fatalf("audit records = %d, want the grants, the uses and the revocation (KOSD-C-14)", records)
	}
	if err := fixture.host.Grant(ctx, fixture.registry, "owner", "files", "camera"); !errors.Is(err, apphost.ErrPermissionUnknown) {
		t.Fatalf("grant of a permission outside the fixed set = %v, want %v", err, apphost.ErrPermissionUnknown)
	}
	// A capability inside the fixed set that this application never declared is
	// the real escalation path: both the grant and the withdrawal are refused, so
	// no state is written for it (KOSD-C-73, KOSD-C-75).
	before := fixture.audit.countFor("files")
	if err := fixture.host.Grant(ctx, fixture.registry, "owner", "files", apphost.PermissionFilePicker); !errors.Is(err, apphost.ErrPermissionUndeclared) {
		t.Fatalf("grant of an undeclared capability = %v, want %v", err, apphost.ErrPermissionUndeclared)
	}
	if err := fixture.host.Revoke(ctx, fixture.registry, "owner", "files", apphost.PermissionFilePicker); !errors.Is(err, apphost.ErrPermissionUndeclared) {
		t.Fatalf("revoke of an undeclared capability = %v, want %v", err, apphost.ErrPermissionUndeclared)
	}
	if state := fixture.host.PermissionState("owner", "files", apphost.PermissionFilePicker); state != apphost.PermissionPending {
		t.Fatalf("state of an undeclared capability = %q, want %q", state, apphost.PermissionPending)
	}
	if after := fixture.audit.countFor("files"); after != before {
		t.Fatalf("audit records after a refused capability = %d, want %d: a refusal is not an authorization (KOSD-C-14)", after, before)
	}
}

// TestAnotherAccountDoesNotLoseItsWindows checks that a registry read scoped to
// one caller settles only that caller's windows: the read is a session-scoped
// view (KOSD-C-59), so an account that cannot see an application keeps its
// window until its own read says otherwise.
func TestAnotherAccountDoesNotLoseItsWindows(t *testing.T) {
	fixture := newFixture(t)
	if _, _, err := fixture.host.Open(fixture.registry, "browser-a", "owner", "files"); err != nil {
		t.Fatalf("open: %v", err)
	}
	gone := apphost.Load(nil, apphost.ValidationOptions{})
	fixture.host.Apply(gone, "guest")
	if windows := fixture.host.Windows("browser-a", "owner"); len(windows) != 1 || windows[0].State != apphost.InstanceStateOpening {
		t.Fatalf("owner windows = %+v, want the window untouched", windows)
	}
	fixture.host.Apply(gone, "owner")
	if windows := fixture.host.Windows("browser-a", "owner"); len(windows) != 1 || windows[0].State != apphost.InstanceStateError {
		t.Fatalf("owner windows after its own empty read = %+v, want the error state", windows)
	}
}
