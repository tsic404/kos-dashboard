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

func TestOpenUsesManifestDefaultsAndSingleModeFocuses(t *testing.T) {
	fixture := newFixture(t)
	first, isNew, err := fixture.host.Open(fixture.registry, "owner", "files")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !isNew || first.DefaultWidth != 1024 || first.DefaultHeight != 720 || first.DefaultState != "normal" {
		t.Fatalf("window = %+v, want the manifest's default geometry", first)
	}
	second, isNew, err := fixture.host.Open(fixture.registry, "owner", "files")
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if isNew || second.ID != first.ID || !second.IsFocused {
		t.Fatalf("second open = %+v, want the same instance focused (DASH-TC-140)", second)
	}
}

func TestOpenRefusesAnUninstalledApplication(t *testing.T) {
	fixture := newFixture(t)
	if _, _, err := fixture.host.Open(fixture.registry, "owner", "absent"); !errors.Is(err, apphost.ErrAppUnavailable) {
		t.Fatalf("open of an uninstalled application = %v, want %v", err, apphost.ErrAppUnavailable)
	}
}

func TestHandshakeDeliversTheTokenOnce(t *testing.T) {
	fixture := newFixture(t)
	window, _, err := fixture.host.Open(fixture.registry, "owner", "files")
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

func TestHandshakeRefusesAnotherOriginAndAnotherNonce(t *testing.T) {
	fixture := newFixture(t)
	window, _, _ := fixture.host.Open(fixture.registry, "owner", "files")
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
	window, _, _ := fixture.host.Open(fixture.registry, "owner", "files")
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
	reopened, isNew, err := fixture.host.Open(fixture.registry, "owner", "files")
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
	window, _, _ := fixture.host.Open(fixture.registry, "owner", "files")
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
	windows := fixture.host.Windows("owner")
	if len(windows) != 1 || windows[0].State != apphost.InstanceStateError {
		t.Fatalf("windows = %+v, want the open window in its error state (DASH-C-30)", windows)
	}
	if _, _, err := fixture.host.Open(gone, "owner", "files"); !errors.Is(err, apphost.ErrAppUnavailable) {
		t.Fatalf("reopen after uninstall = %v, want %v", err, apphost.ErrAppUnavailable)
	}
}

func TestPermissionGrantUseAndRevoke(t *testing.T) {
	fixture := newFixture(t)
	window, _, _ := fixture.host.Open(fixture.registry, "owner", "files")
	init, err := fixture.host.Handshake(fixture.registry, "owner", window.ID,
		"https://files.apps.example.test", window.HandshakeNonce, readyMessage(t, window.ID))
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	ctx := context.Background()
	if _, err := fixture.host.Use(ctx, fixture.registry, "owner", window.ID, "notifications"); !errors.Is(err, apphost.ErrConsentRequired) {
		t.Fatalf("first use = %v, want %v (KOSD-C-75)", err, apphost.ErrConsentRequired)
	}
	if err := fixture.host.Grant(ctx, fixture.registry, "owner", "files", "notifications"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := fixture.host.Use(ctx, fixture.registry, "owner", window.ID, "notifications"); err != nil {
		t.Fatalf("use after grant: %v", err)
	}
	if err := fixture.host.Revoke(ctx, fixture.registry, "owner", "files", "notifications"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	// The token outlives the revocation, so the refusal is the capability and not
	// only the window: the window's own token is gone too (KOSD-C-75).
	if _, err := fixture.host.Authenticate(init.Token); !errors.Is(err, apphost.ErrTokenInvalid) {
		t.Fatalf("token after revoke = %v, want %v", err, apphost.ErrTokenInvalid)
	}
	if _, err := fixture.host.Use(ctx, fixture.registry, "owner", window.ID, "notifications"); !errors.Is(err, apphost.ErrTokenInvalid) {
		t.Fatalf("use after revoke = %v, want the window's capability gone at once (KOSD-C-75)", err)
	}
	if records := fixture.audit.countFor("files"); records < 4 {
		t.Fatalf("audit records = %d, want the grant, the uses and the revocation (KOSD-C-14)", records)
	}
	if err := fixture.host.Grant(ctx, fixture.registry, "owner", "files", "camera"); !errors.Is(err, apphost.ErrPermissionUnknown) {
		t.Fatalf("grant of a permission outside the fixed set = %v, want %v", err, apphost.ErrPermissionUnknown)
	}
}

// TestAnotherAccountDoesNotLoseItsWindows checks that installing for one account
// never settles another account's windows: the registry read is a deployment
// fact, so an install is not an uninstall (DASH-C-30).
func TestAnotherAccountDoesNotLoseItsWindows(t *testing.T) {
	fixture := newFixture(t)
	window, _, err := fixture.host.Open(fixture.registry, "owner", "files")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Another account's invisible set must not settle this account's windows: the
	// registry is read per session (KOSD-C-59), so only the caller's own windows
	// are judged by it.
	fixture.host.Apply(apphost.Load(nil, apphost.ValidationOptions{}), "guest")
	if windows := fixture.host.Windows("owner"); len(windows) != 1 || windows[0].State != apphost.InstanceStateOpening {
		t.Fatalf("owner windows = %+v, want the window untouched", windows)
	}
	if windows := fixture.host.Windows("guest"); len(windows) != 0 {
		t.Fatalf("guest windows = %+v, want none", windows)
	}
	// The owner's own read settles its window against its own registry.
	fixture.host.Apply(apphost.Load(nil, apphost.ValidationOptions{}), "owner")
	if windows := fixture.host.Windows("owner"); len(windows) != 1 || windows[0].State != apphost.InstanceStateError {
		t.Fatalf("owner windows after its own empty read = %+v, want the error state", windows)
	}
	_ = window
}
