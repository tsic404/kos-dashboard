// Package dashtc148 verifies DASH-TC-148: a layout written from one browser is
// read back unchanged in another browser of the same account; a shrunken
// viewport clamps the bounds without writing the clamp back, and a window on a
// display the second browser no longer has falls back to the primary display
// (DASH-AC-70, DASH-C-27, DASH-C-74).
package dashtc148

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/tsic404/kosd/pkg/extension"
	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
	"github.com/tsic404/kos-dashboard/pkg/dock"
	"github.com/tsic404/kos-dashboard/pkg/wm"
	"github.com/tsic404/kos-dashboard/tests/acceptance/front"
)

const (
	ownerName     = "owner"
	ownerPassword = "owner-password"
	applicationID = "media"
)

// layoutPath is where the shell writes the account's layout: the control
// plane's preference API, under the key DASH-C-27 names.
func layoutPath() string {
	return "/api/v1/preferences/" + dock.Identifier + "/" + dock.PreferenceWindowLayout
}

// preferencePath reads the whole preference domain.
func preferencePath() string {
	return "/api/v1/preferences/" + dock.Identifier
}

// contribution is the one window application every browser of the case has.
func contribution(t *testing.T) apphost.Contribution {
	t.Helper()
	return front.Contribution(t, front.Manifest(applicationID, "multi", "normal", 800, 600, "/media"))
}

// startControlPlane boots the real control plane with the desktop package
// installed, so the window-layout key is a declared preference key.
func startControlPlane(t *testing.T) (*harness.Shell, string, *url.URL) {
	t.Helper()
	cluster := harness.Start(t)
	cluster.InstallCRD(t, dock.APIGroup, dock.EntryVersion, dock.EntryKind, dock.EntryResource, true)
	installation := cluster.InstallPackage(t, "dashboard", dock.Declaration(), extension.MemoryArtifacts{})
	shell := cluster.StartShell(t)
	shell.Use(installation)
	owner := shell.SignInOwner(t, ownerName, ownerPassword)
	upstream, err := url.Parse(shell.Accounts.HTTP.URL)
	if err != nil {
		t.Fatalf("parse the control-plane URL: %v", err)
	}
	return shell, owner, upstream
}

// openWindow opens one window and answers its session window.
func openWindow(t *testing.T, desktop *front.Front, body map[string]any) map[string]any {
	t.Helper()
	answer := desktop.CallJSON(http.MethodPost, "/api/v1/desktop/windows", body)
	if answer.Status != http.StatusAccepted {
		t.Fatalf("open window %v = %d %v", body, answer.Status, answer.Body)
	}
	return front.Window(t, answer)
}

// setWindow sends one window-session write and answers its session answer.
func setWindow(t *testing.T, desktop *front.Front, window, suffix string, body map[string]any) front.Answer {
	t.Helper()
	answer := desktop.CallJSON(http.MethodPut, "/api/v1/desktop/windows/"+window+suffix, body)
	if answer.Status != http.StatusOK {
		t.Fatalf("write %s = %d %v", suffix, answer.Status, answer.Body)
	}
	return answer
}

// layoutOf reads the session's layout records.
func layoutOf(t *testing.T, answer front.Answer) wm.WindowLayout {
	t.Helper()
	raw, found := answer.Decoded["layout"]
	if !found {
		t.Fatalf("session answer carries no layout: %v", answer.Decoded)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("encode the session layout: %v", err)
	}
	var layout wm.WindowLayout
	if err := json.Unmarshal(encoded, &layout); err != nil {
		t.Fatalf("decode the session layout: %v", err)
	}
	return layout
}

// TestTC148CrossBrowserLayoutAndClamp is the case DASH-TC-148 describes.
func TestTC148CrossBrowserLayoutAndClamp(t *testing.T) {
	shell, owner, upstream := startControlPlane(t)
	browserA := front.Start(t, upstream, contribution(t))
	browserB := front.Start(t, upstream, contribution(t))

	// Browser A opens two windows and chooses their bounds, states, workspaces
	// and pinned flags through the session routes (DASH-C-74).
	first := openWindow(t, browserA, map[string]any{"app": applicationID, "path": "/media/a"})
	second := openWindow(t, browserA, map[string]any{"app": applicationID, "path": "/media/b", "display": "left"})
	firstID, secondID := first["id"].(string), second["id"].(string)
	setWindow(t, browserA, firstID, "/bounds", map[string]any{"x": 40, "y": 24, "width": 800, "height": 600})
	setWindow(t, browserA, firstID, "/pinned", map[string]any{"pinned": true})
	setWindow(t, browserA, secondID, "/bounds", map[string]any{"x": 500, "y": 100, "width": 700, "height": 500})
	setWindow(t, browserA, secondID, "/state", map[string]any{"state": "maximized"})
	setWindow(t, browserA, secondID, "/workspace", map[string]any{"workspace": "work"})

	// The shell persists what it sees: the session's layout projection, written
	// through the control-plane preference API (KOSD-C-12).
	session := browserA.Call(http.MethodGet, "/api/v1/desktop/windows", nil)
	if session.Status != http.StatusOK {
		t.Fatalf("read browser A's session = %d %v", session.Status, session.Body)
	}
	chosen := layoutOf(t, session)
	if len(chosen.Records) != 2 {
		t.Fatalf("session layout = %+v, want both windows", chosen)
	}
	encoded, err := chosen.Encode()
	if err != nil {
		t.Fatalf("encode the layout: %v", err)
	}
	if stored := browserA.CallJSON(http.MethodPut, layoutPath(), map[string]any{"value": encoded}); stored.Status != http.StatusNoContent {
		t.Fatalf("write the layout = %d %v", stored.Status, stored.Body)
	}

	// Browser B is another browser of the same account: it reads the same
	// layout without opening a window (DASH-C-27).
	read := func(viewport, displays string) wm.WindowLayout {
		t.Helper()
		path := "/api/v1/desktop/layout?viewport=" + viewport + displays
		answer := browserB.Call(http.MethodGet, path, nil)
		if answer.Status != http.StatusOK {
			t.Fatalf("read the layout at %s = %d %v", path, answer.Status, answer.Body)
		}
		raw, err := json.Marshal(answer.Decoded["records"])
		if err != nil {
			t.Fatalf("encode records: %v", err)
		}
		var layout wm.WindowLayout
		if err := json.Unmarshal(raw, &layout.Records); err != nil {
			t.Fatalf("decode records: %v", err)
		}
		layout.SchemaVersion = int(answer.Decoded["schemaVersion"].(float64))
		return layout
	}

	same := read("1440x900", "&display=left")
	if len(same.Records) != len(chosen.Records) {
		t.Fatalf("browser B read %+v, want browser A's two records", same.Records)
	}
	for index := range chosen.Records {
		if same.Records[index] != chosen.Records[index] {
			t.Fatalf("browser B record %d = %+v, want %+v", index, same.Records[index], chosen.Records[index])
		}
	}

	// A shrunken viewport clamps the bounds into it without writing the clamp
	// back to the preference (DASH-C-27).
	clamped := read("800x600", "&display=left")
	firstRecord := recordOf(t, clamped, applicationID, 1)
	if firstRecord.X != 0 || firstRecord.Y != 0 || firstRecord.Width != 800 || firstRecord.Height != 600 {
		t.Fatalf("first clamped record = %+v, want it fitted into 800x600", firstRecord)
	}
	secondRecord := recordOf(t, clamped, applicationID, 2)
	if secondRecord.X != 100 || secondRecord.Y != 100 || secondRecord.Width != 700 || secondRecord.Height != 500 {
		t.Fatalf("second clamped record = %+v, want it fitted into 800x600", secondRecord)
	}
	status, stored := shell.Call(t, http.MethodGet, preferencePath(), nil, owner)
	if status != http.StatusOK {
		t.Fatalf("read the stored preference = %d %v", status, stored)
	}
	items, _ := stored["items"].(map[string]any)
	if items[dock.PreferenceWindowLayout] != encoded {
		t.Fatalf("stored layout changed by a clamped read: %v", items)
	}

	// The removed display takes its window back to the primary one.
	fallen := read("800x600", "&display=")
	fallenRecord := recordOf(t, fallen, applicationID, 2)
	if fallenRecord.Display != "" {
		t.Fatalf("record on the removed display = %+v, want it on the primary display", fallenRecord)
	}

	// Windows themselves are session state: browser B has none open, so it
	// restores no open set (DASH-C-74).
	if open := browserB.Call(http.MethodGet, "/api/v1/desktop/windows", nil); len(front.Windows(open)) != 0 {
		t.Fatalf("browser B session = %v, want no restored window", open.Body)
	}
}

// recordOf finds one record by its application and instance key.
func recordOf(t *testing.T, layout wm.WindowLayout, application string, instance int) wm.Record {
	t.Helper()
	for _, record := range layout.Records {
		if record.Application == application && record.Instance == instance {
			return record
		}
	}
	t.Fatalf("layout %+v carries no record %s#%d", layout.Records, application, instance)
	return wm.Record{}
}
