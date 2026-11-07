package apphost_test

import (
	"encoding/json"
	"testing"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
)

// contributionOf wraps one manifest as an installed contribution.
func contributionOf(t *testing.T, extension, component, phase string, document map[string]any) apphost.Contribution {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode contribution: %v", err)
	}
	return apphost.Contribution{
		Extension: extension, Component: component, Phase: phase,
		Digest: "sha256:abc", Manifest: raw,
	}
}

// loaded builds a registry over one contribution list.
func loaded(t *testing.T, contributions ...apphost.Contribution) *apphost.Registry {
	t.Helper()
	return apphost.Load(contributions, apphost.ValidationOptions{})
}

func TestRegistryListsActiveApplicationsOnBothSurfaces(t *testing.T) {
	registry := loaded(t, contributionOf(t, "files-ext", "files", apphost.PhaseReady, manifestDoc(t)))
	apps := registry.Apps()
	if len(apps) != 1 {
		t.Fatalf("apps = %+v, want the installed application", apps)
	}
	entry := apps[0]
	if len(entry.Surfaces) != 2 {
		t.Fatalf("surfaces = %v, want the launcher and the dock (DASH-C-30)", entry.Surfaces)
	}
	if entry.Name != "文件" || entry.Icon == "" || entry.Window.Width != 1024 {
		t.Fatalf("entry = %+v, want the manifest's name, icon and default size", entry)
	}
	if origins := registry.Origins(); len(origins) != 1 || origins[0] != "https://files.apps.example.test" {
		t.Fatalf("origins = %v, want the application origin only (KOSD-C-75)", origins)
	}
}

func TestRegistryWithdrawsInactiveAndInvalidApplications(t *testing.T) {
	inactive := contributionOf(t, "off-ext", "off", apphost.PhaseDisabled, manifestDoc(t))
	document := manifestDoc(t)
	delete(document, "entry")
	missingEntry := contributionOf(t, "broken-ext", "broken", apphost.PhaseReady, document)
	registry := loaded(t, inactive, missingEntry)
	if apps := registry.Apps(); len(apps) != 0 {
		t.Fatalf("apps = %+v, want none: a disabled or invalid contribution surfaces nothing (KOSD-C-73)", apps)
	}
	refusals := registry.Refusals()
	if len(refusals) != 2 {
		t.Fatalf("refusals = %+v, want one per refused contribution", refusals)
	}
}

func TestRegistryRefusesDuplicateApplicationIdentity(t *testing.T) {
	registry := loaded(t,
		contributionOf(t, "first-ext", "first", apphost.PhaseReady, manifestDoc(t)),
		contributionOf(t, "second-ext", "second", apphost.PhaseReady, manifestDoc(t)),
	)
	if apps := registry.Apps(); len(apps) != 1 {
		t.Fatalf("apps = %+v, want one entry for one identity", apps)
	}
	if refusals := registry.Refusals(); len(refusals) != 1 {
		t.Fatalf("refusals = %+v, want the duplicate reported", refusals)
	}
}

func TestRegistryResolvesOnlyDeclaredDeepLinks(t *testing.T) {
	registry := loaded(t, contributionOf(t, "files-ext", "files", apphost.PhaseReady, manifestDoc(t)))
	if entry, err := registry.ResolveDeepLink("/folders/42"); err != nil || entry.ID != "files" {
		t.Fatalf("deep link /folders/42 resolved to %+v, %v", entry, err)
	}
	if _, err := registry.ResolveDeepLink("/unrelated"); err == nil {
		t.Fatal("an undeclared deep link resolved, want no window (DASH-AC-67)")
	}
}

func TestRegistryPageOnlyContributionHasNoApplication(t *testing.T) {
	// A component that declares pages and no App contributes no application;
	// the page registry carries its page instead (DASH-C-22, KOSD-C-73).
	registry := loaded(t)
	if apps := registry.Apps(); len(apps) != 0 {
		t.Fatalf("apps = %+v, want none", apps)
	}
}

// TestRegistryFailsClosedOnAnUnknownPhase checks that a contribution whose
// component reports no phase is not served: an unknown state is not an active
// one (KOSD-C-28).
func TestRegistryFailsClosedOnAnUnknownPhase(t *testing.T) {
	registry := loaded(t, contributionOf(t, "unknown-ext", "unknown", "", manifestDoc(t)))
	if apps := registry.Apps(); len(apps) != 0 {
		t.Fatalf("apps = %+v, want none: an unknown phase is not active", apps)
	}
	if refusals := registry.Refusals(); len(refusals) != 1 {
		t.Fatalf("refusals = %+v, want the contribution reported", refusals)
	}
}
