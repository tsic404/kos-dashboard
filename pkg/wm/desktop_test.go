package wm

import (
	"testing"
	"time"
)

// clock is a test clock the window set can advance.
type clock struct{ now time.Time }

func (c *clock) read() time.Time { return c.now }

func (c *clock) advance(d time.Duration) { c.now = c.now.Add(d) }

// newDesktop builds one window set on a fixed clock.
func newDesktop(size Size) (*Desktop, *clock) {
	c := &clock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	return NewWithClock(size, c.read), c
}

func TestWindowSetKeepsInstancesApart(t *testing.T) {
	desktop, _ := newDesktop(Size{Width: 800, Height: 600})
	first := desktop.Open("media")
	second := desktop.Open("media")
	if desktop.Count() != 2 || first.Instance != 1 || second.Instance != 2 {
		t.Fatalf("windows = %+v, want two instances", desktop.Windows())
	}
	if first.Bounds.Width != 800 || first.Bounds.Height != 600 || second.Bounds.X != cascadeOffset {
		t.Fatalf("windows = %+v, want the default size and cascaded second window", desktop.Windows())
	}
	if !desktop.Minimize(first.ID) || desktop.Windows()[0].State != StateMinimized || desktop.Count() != 2 {
		t.Fatalf("minimize = %+v, want a minimized window that is still open", desktop.Windows())
	}
	if !desktop.Close(first.ID) || desktop.Count() != 1 || desktop.Windows()[0].ID != second.ID {
		t.Fatalf("close = %+v, want the other instance left", desktop.Windows())
	}
	if desktop.Close(first.ID) || desktop.Minimize(first.ID) {
		t.Fatal("a closed window answered a later close or minimize")
	}
}

func TestReopeningDoesNotReuseAnOpenInstance(t *testing.T) {
	desktop, _ := newDesktop(DefaultSize)
	first := desktop.Open("media")
	second := desktop.Open("media")
	if !desktop.Close(first.ID) {
		t.Fatalf("close the first window: %+v", desktop.Windows())
	}
	third := desktop.Open("media")
	if third.Instance == second.Instance {
		t.Fatalf("reopened window instance = %d, want a number the open window %d does not use", third.Instance, second.Instance)
	}
	layout := desktop.Layout()
	if len(layout.Windows) != 2 || layout.Windows[0].Instance == layout.Windows[1].Instance {
		t.Fatalf("layout = %+v, want two placements with distinct instances", layout.Windows)
	}
	if notes := desktop.Open("notes"); notes.Instance != 1 {
		t.Fatalf("notes window instance = %d, want the first instance of its own application", notes.Instance)
	}
}

func TestCloseAllReportsTheWindowsItCloses(t *testing.T) {
	desktop, _ := newDesktop(DefaultSize)
	desktop.Open("media")
	desktop.Open("notes")
	if closed := desktop.CloseAll(); closed != 2 || desktop.Count() != 0 {
		t.Fatalf("close all = %d (left %d), want 2 closed", closed, desktop.Count())
	}
	if closed := desktop.CloseAll(); closed != 0 {
		t.Fatalf("close all on an empty desktop = %d, want 0", closed)
	}
	if desktop.Address() != "" {
		t.Fatalf("address after closing every window = %q, want the desktop", desktop.Address())
	}
}

func TestLayoutRoundTrip(t *testing.T) {
	desktop, _ := newDesktop(Size{Width: 640, Height: 480})
	desktop.Open("media")
	desktop.Open("media")
	desktop.Minimize(desktop.Windows()[0].ID)

	encoded, err := desktop.Layout().Encode()
	if err != nil {
		t.Fatalf("encode the layout: %v", err)
	}
	decoded, err := DecodeLayout(encoded)
	if err != nil {
		t.Fatalf("decode the layout: %v", err)
	}
	want := desktop.Layout()
	if len(decoded.Windows) != len(want.Windows) {
		t.Fatalf("decoded layout = %+v, want %+v", decoded, want)
	}
	for index := range want.Windows {
		if decoded.Windows[index] != want.Windows[index] {
			t.Fatalf("placement %d = %+v, want %+v", index, decoded.Windows[index], want.Windows[index])
		}
	}
	if _, err := DecodeLayout(""); err == nil {
		t.Fatal("an empty layout decoded")
	}
}

func TestSizeCodec(t *testing.T) {
	encoded, err := (Size{Width: 1024, Height: 640}).Encode()
	if err != nil {
		t.Fatalf("encode the size: %v", err)
	}
	if decoded, err := DecodeSize(encoded); err != nil || decoded != (Size{Width: 1024, Height: 640}) {
		t.Fatalf("decoded size = %+v (%v), want 1024x640", decoded, err)
	}
	if _, err := DecodeSize(`{"width":0,"height":0}`); err == nil {
		t.Fatal("a zero size decoded")
	}
}

func TestNewFallsBackToTheDefaultSize(t *testing.T) {
	desktop := New(Size{})
	window := desktop.Open("media")
	if desktop.DefaultSize() != DefaultSize || window.Bounds.Width != DefaultSize.Width {
		t.Fatalf("default size = %+v, want %+v", desktop.DefaultSize(), DefaultSize)
	}
}

func TestOpenSettlesToTheManifestDefault(t *testing.T) {
	desktop, _ := newDesktop(DefaultSize)
	for _, testCase := range []struct {
		name      string
		declared  string
		wantState string
	}{
		{name: "normal", declared: OpeningNormal, wantState: StateNormal},
		{name: "maximized", declared: OpeningMaximized, wantState: StateMaximized},
		{name: "fullscreen", declared: OpeningFullscreen, wantState: StateFullscreen},
		{name: "absent", declared: "", wantState: StateNormal},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			window, isNew := desktop.OpenWindow("app-"+testCase.name, OpenOptions{
				Mode: ModeMulti, State: testCase.declared, Width: 900, Height: 600,
			})
			if !isNew {
				t.Fatal("a fresh application open reported an existing window")
			}
			if window.State != StateOpening {
				t.Fatalf("state before settling = %q, want %q", window.State, StateOpening)
			}
			if !desktop.Settle(window.ID) {
				t.Fatalf("settle %s", window.ID)
			}
			settled, _ := desktop.Window(window.ID)
			if settled.State != testCase.wantState {
				t.Fatalf("settled state = %q, want %q", settled.State, testCase.wantState)
			}
			if settled.Restore.Width != 900 || settled.Restore.Height != 600 {
				t.Fatalf("restore = %+v, want the manifest's default size", settled.Restore)
			}
		})
	}
}

func TestStateMachineRestoresTheOriginalBounds(t *testing.T) {
	desktop, _ := newDesktop(DefaultSize)
	opened, _ := desktop.OpenWindow("media", OpenOptions{Mode: ModeMulti, Width: 800, Height: 600})
	desktop.Settle(opened.ID)
	original := opened.Bounds

	if !desktop.Minimize(opened.ID) || stateOf(t, desktop, opened.ID) != StateMinimized {
		t.Fatalf("minimize: %+v", desktop.Windows())
	}
	if !desktop.Restore(opened.ID) || stateOf(t, desktop, opened.ID) != StateNormal {
		t.Fatalf("restore from minimized: %+v", desktop.Windows())
	}
	if !desktop.Maximize(opened.ID) || stateOf(t, desktop, opened.ID) != StateMaximized {
		t.Fatalf("maximize: %+v", desktop.Windows())
	}
	if !desktop.Fullscreen(opened.ID) || stateOf(t, desktop, opened.ID) != StateFullscreen {
		t.Fatalf("fullscreen: %+v", desktop.Windows())
	}
	viewport := Bounds{Width: 1440, Height: 900}
	if !desktop.Snap(opened.ID, SnapLeft, viewport) || stateOf(t, desktop, opened.ID) != StateSnapped {
		t.Fatalf("snap: %+v", desktop.Windows())
	}
	snapped, _ := desktop.Window(opened.ID)
	if snapped.Bounds != (Bounds{X: 0, Y: 0, Width: 720, Height: 900}) {
		t.Fatalf("snapped bounds = %+v, want the left half", snapped.Bounds)
	}
	if !desktop.Restore(opened.ID) || stateOf(t, desktop, opened.ID) != StateNormal {
		t.Fatalf("restore from snapped: %+v", desktop.Windows())
	}
	restored, _ := desktop.Window(opened.ID)
	if restored.Bounds != original {
		t.Fatalf("bounds after restore = %+v, want the original %+v", restored.Bounds, original)
	}
	// DASH-C-74 only declares the normal window interchanging with minimized,
	// maximized, fullscreen and snapped, so a minimized window refuses the
	// other states until it is restored.
	if !desktop.Minimize(opened.ID) {
		t.Fatalf("minimize: %+v", desktop.Windows())
	}
	if desktop.Maximize(opened.ID) || desktop.Fullscreen(opened.ID) || desktop.Snap(opened.ID, SnapLeft, viewport) {
		t.Fatal("a minimized window accepted another state")
	}
}

func TestCloseVetoesOnceWhileUnsaved(t *testing.T) {
	desktop, _ := newDesktop(DefaultSize)
	window := desktop.Open("media")
	if !desktop.SetUnsaved(window.ID, true) {
		t.Fatal("record the unsaved changes")
	}
	if result := desktop.RequestClose(window.ID); !result.Found || !result.Vetoed || result.Closed {
		t.Fatalf("first close = %+v, want it vetoed", result)
	}
	if _, found := desktop.Window(window.ID); !found || desktop.Count() != 1 {
		t.Fatalf("windows after the veto = %+v, want the window still open", desktop.Windows())
	}
	if result := desktop.RequestClose(window.ID); !result.Closed || result.Vetoed {
		t.Fatalf("second close = %+v, want the window destroyed", result)
	}
	if desktop.Count() != 0 {
		t.Fatalf("windows after the second close = %+v, want none", desktop.Windows())
	}
}

func TestCrashAndRestartKeepTheInstanceKey(t *testing.T) {
	desktop, _ := newDesktop(DefaultSize)
	opened, _ := desktop.OpenWindow("media", OpenOptions{Mode: ModeMulti, Width: 800, Height: 600})
	desktop.Settle(opened.ID)
	if !desktop.Fail(opened.ID, "renderer crashed") {
		t.Fatal("crash the window")
	}
	crashed, _ := desktop.Window(opened.ID)
	if crashed.State != StateError || crashed.ErrorReason != "renderer crashed" {
		t.Fatalf("crashed window = %+v, want the error state", crashed)
	}
	if !desktop.Restart(opened.ID) {
		t.Fatal("restart the crashed window")
	}
	if stateOf(t, desktop, opened.ID) != StateOpening {
		t.Fatalf("restarted window state = %q, want %q", stateOf(t, desktop, opened.ID), StateOpening)
	}
	if !desktop.Settle(opened.ID) {
		t.Fatal("settle the restarted window")
	}
	restarted, _ := desktop.Window(opened.ID)
	if restarted.State != StateNormal || restarted.Instance != opened.Instance || restarted.ID != opened.ID {
		t.Fatalf("restarted window = %+v, want the same instance at normal", restarted)
	}
}

func TestHeartbeatTimeoutEntersError(t *testing.T) {
	desktop, clock := newDesktop(DefaultSize)
	window := desktop.Open("media")
	// A window that never proved liveness is still loading; only a watched
	// window times out.
	clock.advance(DefaultHeartbeatTimeout + time.Second)
	if expired := desktop.Expire(clock.read(), DefaultHeartbeatTimeout); len(expired) != 0 {
		t.Fatalf("expired = %+v, want no timeout before the first heartbeat", expired)
	}
	if !desktop.Heartbeat(window.ID) {
		t.Fatal("record the first heartbeat")
	}
	clock.advance(DefaultHeartbeatTimeout + time.Second)
	expired := desktop.Expire(clock.read(), DefaultHeartbeatTimeout)
	if len(expired) != 1 || expired[0].ID != window.ID || expired[0].State != StateError {
		t.Fatalf("expired = %+v, want the silent window in its error state", expired)
	}
	if again := desktop.Expire(clock.read(), DefaultHeartbeatTimeout); len(again) != 0 {
		t.Fatalf("expired again = %+v, want none for an error window", again)
	}
}

func TestSingleModeFocusesAndMultiKeepsInstanceKeys(t *testing.T) {
	desktop, _ := newDesktop(DefaultSize)
	first, isNew := desktop.OpenWindow("files", OpenOptions{Mode: ModeSingle, Address: "/files/a"})
	if !isNew {
		t.Fatal("the first single-mode open reported an existing window")
	}
	desktop.Settle(first.ID)
	second, isNew := desktop.OpenWindow("files", OpenOptions{Mode: ModeSingle, Address: "/files/b"})
	if isNew || second.ID != first.ID {
		t.Fatalf("second single-mode open = %+v (new %v), want the first window focused", second, isNew)
	}
	if desktop.Count() != 1 {
		t.Fatalf("windows = %+v, want one single-mode window", desktop.Windows())
	}
	if second.Address != "/files/b" {
		t.Fatalf("address = %q, want the deep link the second open named", second.Address)
	}

	one, _ := desktop.OpenWindow("notes", OpenOptions{Mode: ModeMulti, Address: "/notes/a"})
	desktop.Settle(one.ID)
	two, _ := desktop.OpenWindow("notes", OpenOptions{Mode: ModeMulti, Address: "/notes/b"})
	desktop.Settle(two.ID)
	if one.Instance == two.Instance || one.ID == two.ID {
		t.Fatalf("multi-mode windows = %+v and %+v, want distinct instance keys", one, two)
	}
}

func TestAddressFollowsFocusAndClose(t *testing.T) {
	desktop, _ := newDesktop(DefaultSize)
	first, _ := desktop.OpenWindow("notes", OpenOptions{Mode: ModeMulti, Address: "/notes/a"})
	desktop.Settle(first.ID)
	second, _ := desktop.OpenWindow("notes", OpenOptions{Mode: ModeMulti, Address: "/notes/b"})
	desktop.Settle(second.ID)
	if desktop.Address() != "/notes/b" {
		t.Fatalf("address after opening = %q, want the newest deep link", desktop.Address())
	}
	if !desktop.Focus(first.ID) || desktop.Address() != "/notes/a" {
		t.Fatalf("address after focusing the first window = %q, want its deep link", desktop.Address())
	}
	if result := desktop.RequestClose(first.ID); !result.Closed {
		t.Fatalf("close the focused window = %+v", result)
	}
	if desktop.Address() != "/notes/b" {
		t.Fatalf("address after closing = %q, want the remaining window's deep link", desktop.Address())
	}
	desktop.Close(second.ID)
	if desktop.Address() != "" {
		t.Fatalf("address after closing every window = %q, want the desktop", desktop.Address())
	}
}

func TestTaskbarListsOnlyTheCurrentWorkspace(t *testing.T) {
	desktop, _ := newDesktop(DefaultSize)
	main, _ := desktop.OpenWindow("files", OpenOptions{Mode: ModeMulti})
	desktop.Settle(main.ID)
	other, _ := desktop.OpenWindow("notes", OpenOptions{Mode: ModeMulti, Workspace: "work"})
	desktop.Settle(other.ID)

	if names := taskbarNames(desktop); names != "notes" {
		t.Fatalf("taskbar = %q, want only the current workspace's window", names)
	}
	if workspaces := desktop.Workspaces(); len(workspaces) != 2 || workspaces[0] != WorkspaceDefault || workspaces[1] != "work" {
		t.Fatalf("workspaces = %v, want the default and the created one in order", workspaces)
	}
	if !desktop.SwitchWorkspace(WorkspaceDefault) {
		t.Fatal("switch to the default workspace")
	}
	if names := taskbarNames(desktop); names != "files" {
		t.Fatalf("taskbar = %q, want only the default workspace's window", names)
	}
}

func TestRecordsNeverCarrySessionOnlyState(t *testing.T) {
	desktop, _ := newDesktop(DefaultSize)
	window, _ := desktop.OpenWindow("files", OpenOptions{Mode: ModeMulti, Width: 800, Height: 600, Display: "hdmi-1", Workspace: "work"})
	desktop.Settle(window.ID)
	_ = desktop.SetUnsaved(window.ID, true)
	_ = desktop.Minimize(window.ID)
	_ = desktop.Focus(window.ID)

	records := desktop.Records()
	if len(records) != 1 {
		t.Fatalf("records = %+v, want the one live window", records)
	}
	record := records[0]
	if record.State != StateNormal {
		t.Fatalf("record state = %q, want normal: minimization is session-only", record.State)
	}
	if record.Workspace != "work" || record.Display != "hdmi-1" {
		t.Fatalf("record = %+v, want the window's workspace and display", record)
	}
	if record.X != window.Bounds.X || record.Width != 800 || record.Instance != window.Instance {
		t.Fatalf("record = %+v, want the window's normal rectangle and instance key", record)
	}
	for _, record := range records {
		if record.State == StateMinimized {
			t.Fatal("a record carries the minimized state")
		}
	}
}

func TestMinimizeKeepsThePagesState(t *testing.T) {
	desktop, _ := newDesktop(DefaultSize)
	window := desktop.Open("media")
	if !desktop.Minimize(window.ID) {
		t.Fatal("minimize the window")
	}
	kept, found := desktop.Window(window.ID)
	if !found || kept.State != StateMinimized {
		t.Fatalf("window after minimize = %+v (found %v), want it open", kept, found)
	}
	if desktop.Count() != 1 {
		t.Fatalf("count after minimize = %d, want the window still in the set", desktop.Count())
	}
}

// stateOf reads one window's state.
func stateOf(t *testing.T, desktop *Desktop, id string) string {
	t.Helper()
	window, found := desktop.Window(id)
	if !found {
		t.Fatalf("window %s is not open", id)
	}
	return window.State
}

// taskbarNames renders the applications the taskbar lists, in order.
func taskbarNames(desktop *Desktop) string {
	names := ""
	for _, window := range desktop.Taskbar() {
		if names != "" {
			names += ","
		}
		names += window.Application
	}
	return names
}

// TestMinimizeHandsTheFocusToAVisibleWindow checks that a minimized window
// cannot hold the focus: the address and the taskbar must not point at a window
// that is not on screen (DASH-C-74).
func TestMinimizeHandsTheFocusToAVisibleWindow(t *testing.T) {
	desktop, _ := newDesktop(DefaultSize)
	first, _ := desktop.OpenWindow("files", OpenOptions{Mode: ModeMulti, Address: "/files/a"})
	desktop.Settle(first.ID)
	second, _ := desktop.OpenWindow("notes", OpenOptions{Mode: ModeMulti, Address: "/notes/b"})
	desktop.Settle(second.ID)

	if focused, found := desktop.Focused(); !found || focused.ID != second.ID {
		t.Fatalf("focused window = %+v, want the last opened one", focused)
	}
	if !desktop.Minimize(second.ID) {
		t.Fatal("minimize the focused window")
	}
	focused, found := desktop.Focused()
	if !found || focused.ID != first.ID {
		t.Fatalf("focused window after the minimize = %+v, want the visible window", focused)
	}

	if !desktop.Minimize(first.ID) {
		t.Fatal("minimize the remaining visible window")
	}
	if focused, found := desktop.Focused(); found {
		t.Fatalf("focused window = %+v, want none while every window is minimized", focused)
	}
	if address := desktop.Address(); address != "" {
		t.Fatalf("address = %q, want none while the window is hidden", address)
	}

	// Restoring brings the window back with its focus.
	if !desktop.Restore(first.ID) {
		t.Fatal("restore the minimized window")
	}
	if focused, found := desktop.Focused(); !found || focused.ID != first.ID {
		t.Fatalf("focused window after the restore = %+v, want the restored one", focused)
	}
}
