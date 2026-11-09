// Package dashtc146 verifies DASH-TC-146: a single-mode application answers
// two deep links with one window and the second only focuses it; a multi-mode
// application opens one window per deep link with distinct instance keys; and
// the address the session reports follows the focused window's deep link, so a
// back step lands on an address the open set still holds (DASH-AC-69,
// DASH-C-74).
package dashtc146

import (
	"net/http"
	"testing"

	"github.com/tsic404/kos-dashboard/tests/acceptance/front"
)

// open registers one window for a path.
func open(t *testing.T, desktop *front.Front, app, path string) front.Answer {
	t.Helper()
	answer := desktop.CallJSON(http.MethodPost, "/api/v1/desktop/windows", map[string]any{"app": app, "path": path})
	if answer.Status != http.StatusAccepted {
		t.Fatalf("open %s %s = %d %v", app, path, answer.Status, answer.Body)
	}
	return answer
}

// TestTC146SingleMultiAndDeepLinks is the case DASH-TC-146 describes.
func TestTC146SingleMultiAndDeepLinks(t *testing.T) {
	desktop := front.Start(t, front.Stub(t),
		front.Contribution(t, front.Manifest("files", "single", "normal", 900, 640, "/files")),
		front.Contribution(t, front.Manifest("notes", "multi", "normal", 800, 600, "/notes")),
	)

	// A single-mode application opens one window; the second deep link focuses
	// it and moves the address (DASH-C-74).
	first := open(t, desktop, "files", "/files/one")
	firstWindow := front.Window(t, first)
	if first.Decoded["isNew"].(bool) != true {
		t.Fatalf("first single open = %v, want a new window", first.Decoded)
	}
	second := open(t, desktop, "files", "/files/two")
	if second.Decoded["isNew"].(bool) || front.Window(t, second)["id"] != firstWindow["id"] {
		t.Fatalf("second single open = %v, want the first window focused", second.Decoded)
	}
	if listed := desktop.Call(http.MethodGet, "/api/v1/desktop/windows", nil); len(front.Windows(listed)) != 1 {
		t.Fatalf("session after two single deep links = %v, want one window", listed.Body)
	}
	if second.Decoded["address"] != "/files/two" {
		t.Fatalf("address after the second deep link = %v, want the newest path", second.Decoded["address"])
	}

	// A multi-mode application opens one window per new path, each with its own
	// instance key; opening the same path again only focuses its window.
	one := open(t, desktop, "notes", "/notes/a")
	two := open(t, desktop, "notes", "/notes/b")
	oneWindow, twoWindow := front.Window(t, one), front.Window(t, two)
	if oneWindow["id"] == twoWindow["id"] || oneWindow["instance"] == twoWindow["instance"] {
		t.Fatalf("multi windows = %v and %v, want distinct instance keys", oneWindow, twoWindow)
	}
	again := open(t, desktop, "notes", "/notes/a")
	if again.Decoded["isNew"].(bool) || front.Window(t, again)["id"] != oneWindow["id"] {
		t.Fatalf("reopening a deep link = %v, want its window focused", again.Decoded)
	}
	if len(front.Windows(desktop.Call(http.MethodGet, "/api/v1/desktop/windows", nil))) != 3 {
		t.Fatal("the open set changed when a deep link only focused its window")
	}

	// The address follows the focused window. A browser back step therefore
	// lands on an address the open set still holds (DASH-TC-146).
	if focused := desktop.Call(http.MethodPost, "/api/v1/desktop/windows/"+twoWindow["id"].(string)+"/focus", nil); focused.Status != http.StatusOK || focused.Decoded["address"] != "/notes/b" {
		t.Fatalf("focus the second multi window = %d %v, want its address", focused.Status, focused.Body)
	}
	back := desktop.Call(http.MethodPost, "/api/v1/desktop/windows/"+firstWindow["id"].(string)+"/focus", nil)
	if back.Status != http.StatusOK || back.Decoded["address"] != "/files/two" {
		t.Fatalf("back to the first window = %d %v, want the address it holds", back.Status, back.Body)
	}
	if windows := front.Windows(desktop.Call(http.MethodGet, "/api/v1/desktop/windows", nil)); len(windows) != 3 {
		t.Fatalf("open set after the back step = %v, want the three windows still open", windows)
	}

	// Closing the focused window moves the address to the window that takes the
	// focus, so the reported address never names a closed window.
	closed := desktop.Call(http.MethodDelete, "/api/v1/desktop/windows/"+firstWindow["id"].(string), nil)
	if closed.Status != http.StatusOK || closed.Decoded["address"] != "/notes/b" {
		t.Fatalf("close the focused window = %d %v, want the remaining window's address", closed.Status, closed.Body)
	}
}
