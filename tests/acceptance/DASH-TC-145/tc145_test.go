// Package dashtc145 verifies DASH-TC-145: a window moves through minimized,
// normal, maximized, fullscreen, snapped and back to normal with the rectangle
// it had before; while the application reports unsaved changes the first close
// is vetoed and the second destroys the window (DASH-AC-69, DASH-C-74).
package dashtc145

import (
	"net/http"
	"testing"

	"github.com/tsic404/kos-dashboard/tests/acceptance/front"
)

// stateOf applies one state transition and returns the window it produced.
func stateOf(t *testing.T, desktop *front.Front, path string, body map[string]any) map[string]any {
	t.Helper()
	answer := desktop.CallJSON(http.MethodPut, path+"/state", body)
	if answer.Status != http.StatusOK {
		t.Fatalf("state %v = %d %v", body, answer.Status, answer.Body)
	}
	return front.Window(t, answer)
}

// boundsOf reports one window rectangle's numbers.
func boundsOf(t *testing.T, window map[string]any) (x, y, width, height int) {
	t.Helper()
	bounds, _ := window["bounds"].(map[string]any)
	return number(bounds["x"]), number(bounds["y"]), number(bounds["width"]), number(bounds["height"])
}

// number reads one JSON number.
func number(value any) int {
	if value, ok := value.(float64); ok {
		return int(value)
	}
	return 0
}

// TestTC145StateTransitionsAndCloseVeto is the case DASH-TC-145 describes.
func TestTC145StateTransitionsAndCloseVeto(t *testing.T) {
	desktop := front.Start(t, front.Stub(t),
		front.Contribution(t, front.Manifest("files", "single", "normal", 800, 600, "/files")))
	opened := desktop.CallJSON(http.MethodPost, "/api/v1/desktop/windows", map[string]any{"app": "files", "path": "/files"})
	if opened.Status != http.StatusAccepted {
		t.Fatalf("open = %d %v", opened.Status, opened.Body)
	}
	window := front.Window(t, opened)
	if window["state"] != "normal" {
		t.Fatalf("opened state = %v, want normal", window["state"])
	}
	path := "/api/v1/desktop/windows/" + window["id"].(string)
	originalX, originalY, originalWidth, originalHeight := boundsOf(t, window)

	// The state transitions DASH-C-74 declares, each with its restore rectangle.
	minimized := stateOf(t, desktop, path, map[string]any{"state": "minimized"})
	if minimized["state"] != "minimized" {
		t.Fatalf("minimized window = %v, want minimized", minimized)
	}
	if x, y, width, height := boundsOf(t, minimized); x != originalX || y != originalY || width != originalWidth || height != originalHeight {
		t.Fatalf("minimized bounds = %d,%d %dx%d, want the original rectangle", x, y, width, height)
	}
	listed := desktop.Call(http.MethodGet, "/api/v1/desktop/windows", nil)
	if len(front.Windows(listed)) != 1 {
		t.Fatalf("session after minimize = %v, want the window still open", listed.Body)
	}
	restored := stateOf(t, desktop, path, map[string]any{"state": "normal"})
	if restored["state"] != "normal" {
		t.Fatalf("restored window = %v, want normal", restored)
	}
	if x, y, width, height := boundsOf(t, restored); x != originalX || y != originalY || width != originalWidth || height != originalHeight {
		t.Fatalf("restored bounds = %d,%d %dx%d, want the original rectangle", x, y, width, height)
	}
	if maximized := stateOf(t, desktop, path, map[string]any{"state": "maximized"}); maximized["state"] != "maximized" {
		t.Fatalf("maximized window = %v, want maximized", maximized)
	}
	if fullscreen := stateOf(t, desktop, path, map[string]any{"state": "fullscreen"}); fullscreen["state"] != "fullscreen" {
		t.Fatalf("fullscreen window = %v, want fullscreen", fullscreen)
	}
	snapped := stateOf(t, desktop, path, map[string]any{
		"state": "snapped", "edge": "left",
		"viewport": map[string]any{"x": 0, "y": 0, "width": 1440, "height": 900},
	})
	if snapped["state"] != "snapped" {
		t.Fatalf("snapped window = %v, want snapped", snapped)
	}
	if x, y, width, height := boundsOf(t, snapped); x != 0 || y != 0 || width != 720 || height != 900 {
		t.Fatalf("snapped bounds = %d,%d %dx%d, want the left half of the viewport", x, y, width, height)
	}
	restored = stateOf(t, desktop, path, map[string]any{"state": "normal"})
	if x, y, width, height := boundsOf(t, restored); restored["state"] != "normal" ||
		x != originalX || y != originalY || width != originalWidth || height != originalHeight {
		t.Fatalf("restored window = %v, want the original rectangle", restored)
	}

	// A minimized window refuses another state until it is restored.
	stateOf(t, desktop, path, map[string]any{"state": "minimized"})
	refused := desktop.CallJSON(http.MethodPut, path+"/state", map[string]any{"state": "maximized"})
	if refused.Status != http.StatusConflict {
		t.Fatalf("maximize a minimized window = %d %v, want %d", refused.Status, refused.Body, http.StatusConflict)
	}
	stateOf(t, desktop, path, map[string]any{"state": "normal"})

	// A window that never reported unsaved changes closes the first time, and so
	// does one reported clean: the veto belongs to unsaved work (DASH-C-74).
	removed := desktop.Call(http.MethodDelete, path, nil)
	if removed.Status != http.StatusOK {
		t.Fatalf("close a window that never reported unsaved = %d %v, want the first close to remove it", removed.Status, removed.Body)
	}
	window = front.Window(t, desktop.CallJSON(http.MethodPost, "/api/v1/desktop/windows",
		map[string]any{"app": "files", "path": "/files"}))
	path = "/api/v1/desktop/windows/" + window["id"].(string)
	if cleared := desktop.CallJSON(http.MethodPut, path+"/unsaved", map[string]any{"unsaved": false}); cleared.Status != http.StatusOK {
		t.Fatalf("record a clean window = %d %v", cleared.Status, cleared.Body)
	}
	if removed = desktop.Call(http.MethodDelete, path, nil); removed.Status != http.StatusOK {
		t.Fatalf("close a window reporting unsaved=false = %d %v, want the first close to remove it", removed.Status, removed.Body)
	}
	// Reopen for the veto: the one close a period of unsaved work earns.
	window = front.Window(t, desktop.CallJSON(http.MethodPost, "/api/v1/desktop/windows",
		map[string]any{"app": "files", "path": "/files"}))
	path = "/api/v1/desktop/windows/" + window["id"].(string)

	// Unsaved changes veto one close; the second destroys the window.
	unsaved := desktop.CallJSON(http.MethodPut, path+"/unsaved", map[string]any{"unsaved": true})
	if unsaved.Status != http.StatusOK {
		t.Fatalf("record unsaved changes = %d %v", unsaved.Status, unsaved.Body)
	}
	vetoed := desktop.Call(http.MethodDelete, path, nil)
	if vetoed.Status != http.StatusConflict || front.ErrorCode(vetoed) != "close_vetoed" {
		t.Fatalf("first close = %d %v, want the one veto", vetoed.Status, vetoed.Body)
	}
	listed = desktop.Call(http.MethodGet, "/api/v1/desktop/windows", nil)
	if len(front.Windows(listed)) != 1 {
		t.Fatalf("session after the veto = %v, want the window still open", listed.Body)
	}
	closed := desktop.Call(http.MethodDelete, path, nil)
	if closed.Status != http.StatusOK || len(front.Windows(closed)) != 0 {
		t.Fatalf("second close = %d %v, want the window destroyed", closed.Status, closed.Body)
	}
}
