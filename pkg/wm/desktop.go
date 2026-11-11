// Package wm is the desktop window manager: the window state machine
// (DASH-C-74), the session window set with its geometry, z-order, focus,
// workspaces and displays, and the layout structures the account keeps
// (DASH-C-27).
//
// A window is session state: nothing here writes a cluster object or a
// preference (DASH-C-30, KOSD-F-19). Records is the layout domain's seam and
// Layout is the migrated value codec.
package wm

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// The window states DASH-C-74 fixes.
const (
	// StateOpening is a window registered before its default state applies.
	StateOpening = "opening"
	// StateNormal is a window at its own bounds.
	StateNormal = "normal"
	// StateMinimized is a window that stays open without a frame on screen.
	StateMinimized = "minimized"
	// StateMaximized is a window filling the display.
	StateMaximized = "maximized"
	// StateFullscreen is a window filling the display without chrome.
	StateFullscreen = "fullscreen"
	// StateSnapped is a window filling one half of the display.
	StateSnapped = "snapped"
	// StateClosing is a window whose close was accepted.
	StateClosing = "closing"
	// StateError is a window whose application crashed or stopped sending its
	// heartbeat; it offers restart and close (DASH-C-74).
	StateError = "error"
)

// The instance modes a manifest declares (KOSD-C-73, DASH-C-74).
const (
	// ModeSingle focuses the open window instead of opening another one.
	ModeSingle = "single"
	// ModeMulti opens one window per deep link.
	ModeMulti = "multi"
)

// The states an opening window may settle to (DASH-C-74).
const (
	// OpeningNormal opens at the manifest's default size.
	OpeningNormal = "normal"
	// OpeningMaximized opens maximized.
	OpeningMaximized = "maximized"
	// OpeningFullscreen opens fullscreen.
	OpeningFullscreen = "fullscreen"
)

// The edges a window may snap to (DASH-C-74).
const (
	// SnapLeft fills the left half of the viewport.
	SnapLeft = "left"
	// SnapRight fills the right half of the viewport.
	SnapRight = "right"
)

const (
	// WorkspaceDefault is the workspace a browser starts in. A new session
	// never restores the previous session's windows (DASH-C-74).
	WorkspaceDefault = "main"
	// cascadeOffset is how far each new window sits from the previous one, so
	// a second instance is visible instead of hidden behind the first.
	cascadeOffset = 32
	// cascadeWrap is the number of offsets a desktop reuses before starting
	// over.
	cascadeWrap = 8
	// DefaultHeartbeatTimeout is how long a window may stay silent before it
	// enters the error state (DASH-C-74).
	DefaultHeartbeatTimeout = 30 * time.Second
)

// ErrWindowUnknown refuses an instance this session does not hold.
var ErrWindowUnknown = errors.New("wm: window is unknown")

// ErrTransition refuses a state change the state machine does not allow.
var ErrTransition = errors.New("wm: state transition is not allowed")

// Bounds is one window rectangle. For a maximized, fullscreen or snapped window
// it is the rectangle the model knows; Restore always keeps the window's normal
// rectangle.
type Bounds struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Window is one window in one browser session (DASH-C-74). It carries the
// interaction state and geometry; it is not a cluster object.
type Window struct {
	// ID identifies the window while it is open.
	ID string `json:"id"`
	// Application is the application the window shows.
	Application string `json:"application"`
	// Instance numbers the window among the application's windows: a new one
	// takes the highest open number plus one, so two open windows never share
	// an instance (DASH-C-74).
	Instance int `json:"instance"`
	// State is the window state (DASH-C-74).
	State string `json:"state"`
	// Bounds is the window's current rectangle.
	Bounds Bounds `json:"bounds"`
	// Restore is the window's normal rectangle, the one it returns to from
	// maximized, fullscreen and snapped (DASH-C-74).
	Restore Bounds `json:"restore"`
	// Workspace is the session workspace the window belongs to.
	Workspace string `json:"workspace"`
	// Display is the display the window opened on; empty means the primary one.
	Display string `json:"display,omitempty"`
	// Z is the window's place in the session's z-order; focus brings a window
	// to the front.
	Z int `json:"z"`
	// IsFocused marks the window the session's address points at.
	IsFocused bool `json:"isFocused"`
	// Address is the deep link the window was opened or focused for; closing
	// it moves the address to the next window (DASH-C-74).
	Address string `json:"address,omitempty"`
	// ErrorReason says why the window entered the error state.
	ErrorReason string `json:"errorReason,omitempty"`
	// IsUnsaved reports the unsaved changes the application announced. It is
	// session state and never enters an object or a preference.
	IsUnsaved bool `json:"isUnsaved,omitempty"`
	// IsPinned marks the window kept in place. A pinned window is a layout
	// attribute and follows the account across browsers (DASH-C-27).
	IsPinned bool `json:"isPinned,omitempty"`

	// openingState is the state the manifest defaults this window to.
	openingState string
	// vetoUsed reports that the one close veto of this unsaved period was
	// spent (DASH-C-74).
	vetoUsed bool
	// hasHeartbeat reports that the window's frame proved liveness at least
	// once; only a watched window can time out.
	hasHeartbeat bool
	// lastSeen is when the window last reported its heartbeat.
	lastSeen time.Time
}

// OpenOptions carries the manifest defaults and the place a window opens in.
type OpenOptions struct {
	// Mode is the instance mode (KOSD-C-73).
	Mode string
	// State is the state the manifest opens the window in; empty means normal.
	State string
	// Width and Height are the manifest's window defaults; zero falls back to
	// the desktop's default size.
	Width, Height int
	// Bounds is a stored placement to honour (DASH-C-27); nil cascades the
	// window from the previous one.
	Bounds *Bounds
	// Workspace names the workspace to open in; empty uses the current one.
	Workspace string
	// Display names the display the window belongs to.
	Display string
	// Address is the deep link the window was opened for.
	Address string
}

// CloseResult reports what one close request did.
type CloseResult struct {
	// Found reports that the session held the window.
	Found bool
	// Vetoed reports that the application's unsaved state refused this close
	// once; the window stays open.
	Vetoed bool
	// Closed reports that the window is gone and its frame is to be destroyed.
	Closed bool
}

// Record is one layout record the account keeps (DASH-C-27): the application,
// the instance key, the normal rectangle, the window state, the workspace and
// the display. Minimization is session-only and never appears here.
type Record struct {
	Application string `json:"application"`
	Instance    int    `json:"instance"`
	X           int    `json:"x"`
	Y           int    `json:"y"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	State       string `json:"state"`
	Workspace   string `json:"workspace"`
	Display     string `json:"display,omitempty"`
	// Pinned reports that the window is pinned; it is a layout attribute, not
	// session state (DASH-C-27).
	Pinned bool `json:"pinned,omitempty"`
}

// SetPinned marks one live window pinned or unpinned. Pinning is a layout
// attribute: the window stays open and the flag follows the account through the
// layout preference (DASH-C-27).
func (d *Desktop) SetPinned(id string, isPinned bool) bool {
	window := d.windowByID(id)
	if window == nil || !isLive(window.State) {
		return false
	}
	window.IsPinned = isPinned
	return true
}

// Desktop is the window set of one browser session (DASH-C-74). Open, Minimize,
// Close and CloseAll change this value and nothing else: the type holds no
// cluster client and no preference store, so window interaction cannot reach
// the application's lifecycle.
type Desktop struct {
	defaultSize Size
	now         func() time.Time
	windows     []Window
	opened      int
	workspaces  []string
	current     string
	focused     string
	z           int
}

// New builds a browser's window set. A size without positive width and height
// falls back to DefaultSize, so a stored preference that lost its meaning still
// opens a usable window.
func New(defaultSize Size) *Desktop {
	return NewWithClock(defaultSize, time.Now)
}

// NewWithClock builds a window set whose window age is judged by now.
func NewWithClock(defaultSize Size, now func() time.Time) *Desktop {
	if defaultSize.Width <= 0 || defaultSize.Height <= 0 {
		defaultSize = DefaultSize
	}
	if now == nil {
		now = time.Now
	}
	return &Desktop{
		defaultSize: defaultSize,
		now:         now,
		workspaces:  []string{WorkspaceDefault},
		current:     WorkspaceDefault,
	}
}

// Open adds one window of the application and returns it settled. Opening an
// application twice gives two instances with their own windows (DASH-C-74).
func (d *Desktop) Open(application string) Window {
	window, _ := d.OpenWindow(application, OpenOptions{Mode: ModeMulti})
	d.Settle(window.ID)
	settled, _ := d.Window(window.ID)
	return settled
}

// OpenWindow registers one window and returns it in the opening state. A
// deep link opens or focuses its instance: one already showing the same path is
// focused, and a single-mode application has one window however often it opens
// (DASH-C-74). The caller settles the window with Settle once the manifest's
// default state applies.
func (d *Desktop) OpenWindow(application string, options OpenOptions) (Window, bool) {
	for index := range d.windows {
		window := &d.windows[index]
		if window.Application != application || !isLive(window.State) {
			continue
		}
		// A multi-mode window answers newly opened deep links: two different
		// paths are two instances, the same path is one.
		if options.Mode == ModeMulti && (options.Address == "" || window.Address != options.Address) {
			continue
		}
		if options.Address != "" {
			window.Address = options.Address
		}
		d.focusLocked(window.ID)
		return *window, false
	}
	d.opened++
	instance := d.nextInstance(application)
	bounds := Bounds{Width: options.Width, Height: options.Height}
	if bounds.Width <= 0 || bounds.Height <= 0 {
		bounds.Width, bounds.Height = d.defaultSize.Width, d.defaultSize.Height
	}
	if options.Bounds != nil {
		bounds.X, bounds.Y = options.Bounds.X, options.Bounds.Y
		if options.Bounds.Width > 0 {
			bounds.Width = options.Bounds.Width
		}
		if options.Bounds.Height > 0 {
			bounds.Height = options.Bounds.Height
		}
	} else {
		offset := cascadeOffset * ((d.opened - 1) % cascadeWrap)
		bounds.X, bounds.Y = offset, offset
	}
	workspace := d.addWorkspaceLocked(options.Workspace)
	window := Window{
		ID:           fmt.Sprintf("window-%d", d.opened),
		Application:  application,
		Instance:     instance,
		State:        StateOpening,
		Bounds:       bounds,
		Restore:      bounds,
		Workspace:    workspace,
		Display:      options.Display,
		Address:      options.Address,
		openingState: openingStateOf(options.State),
	}
	d.windows = append(d.windows, window)
	d.focusLocked(window.ID)
	return d.windows[len(d.windows)-1], true
}

// Settle moves one opening window to the state its manifest defaults it to. The
// default size rectangle stays the window's restore rectangle (DASH-C-74).
func (d *Desktop) Settle(id string) bool {
	window := d.windowByID(id)
	if window == nil || window.State != StateOpening {
		return false
	}
	window.State = window.openingState
	if window.State == "" {
		window.State = StateNormal
	}
	if window.State != StateNormal {
		window.Restore = window.Bounds
	}
	return true
}

// Window reads one open window.
func (d *Desktop) Window(id string) (Window, bool) {
	window := d.windowByID(id)
	if window == nil {
		return Window{}, false
	}
	return *window, true
}

// Windows returns the open windows in the order they were opened.
func (d *Desktop) Windows() []Window {
	return append([]Window(nil), d.windows...)
}

// Count returns how many windows are open.
func (d *Desktop) Count() int { return len(d.windows) }

// DefaultSize returns the size new windows take in this browser.
func (d *Desktop) DefaultSize() Size { return d.defaultSize }

// Minimize marks one normal window minimized; it stays open and keeps its
// frame and its page state (DASH-C-74).
func (d *Desktop) Minimize(id string) bool {
	window := d.windowByID(id)
	if window == nil || window.State != StateNormal {
		return false
	}
	window.State = StateMinimized
	d.loseFocusLocked(window)
	return true
}

// Restore returns a minimized, maximized, fullscreen or snapped window to its
// normal state and to the rectangle it had before (DASH-C-74).
func (d *Desktop) Restore(id string) bool {
	window := d.windowByID(id)
	if window == nil {
		return false
	}
	switch window.State {
	case StateMinimized, StateMaximized, StateFullscreen, StateSnapped:
	default:
		return false
	}
	window.State = StateNormal
	window.Bounds = window.Restore
	d.focusLocked(id)
	return true
}

// Maximize fills the display with one normal window.
func (d *Desktop) Maximize(id string) bool {
	window := d.windowByID(id)
	if window == nil || window.State != StateNormal {
		return false
	}
	window.Restore = window.Bounds
	window.State = StateMaximized
	d.focusLocked(id)
	return true
}

// Fullscreen fills the display without chrome. A normal or maximized window may
// enter it; the restore rectangle stays the window's normal one.
func (d *Desktop) Fullscreen(id string) bool {
	window := d.windowByID(id)
	if window == nil {
		return false
	}
	switch window.State {
	case StateNormal:
		window.Restore = window.Bounds
	case StateMaximized:
	default:
		return false
	}
	window.State = StateFullscreen
	d.focusLocked(id)
	return true
}

// Snap fills one half of the viewport with a normal or fullscreen window and
// keeps the rectangle Restore returns to (DASH-C-74).
func (d *Desktop) Snap(id, edge string, viewport Bounds) bool {
	window := d.windowByID(id)
	if window == nil || viewport.Width <= 0 || viewport.Height <= 0 {
		return false
	}
	switch window.State {
	case StateNormal:
		window.Restore = window.Bounds
	case StateFullscreen:
	default:
		return false
	}
	if edge != SnapLeft && edge != SnapRight {
		return false
	}
	half := viewport.Width / 2
	window.Bounds = Bounds{X: viewport.X, Y: viewport.Y, Width: half, Height: viewport.Height}
	if edge == SnapRight {
		window.Bounds.X = viewport.X + half
		window.Bounds.Width = viewport.Width - half
	}
	window.State = StateSnapped
	d.focusLocked(id)
	return true
}

// SetBounds moves one normal window and keeps its restore rectangle in step.
func (d *Desktop) SetBounds(id string, bounds Bounds) bool {
	window := d.windowByID(id)
	if window == nil || bounds.Width <= 0 || bounds.Height <= 0 {
		return false
	}
	if window.State != StateNormal {
		return false
	}
	window.Bounds = bounds
	window.Restore = bounds
	return true
}

// SetWorkspace moves one window to a workspace, creating the workspace when the
// session does not know it yet.
func (d *Desktop) SetWorkspace(id, workspace string) bool {
	window := d.windowByID(id)
	name := strings.TrimSpace(workspace)
	if window == nil || name == "" {
		return false
	}
	window.Workspace = d.addWorkspaceLocked(name)
	if window.IsFocused {
		d.current = window.Workspace
	}
	return true
}

// SwitchWorkspace makes one workspace current and focuses the topmost window
// there; the taskbar lists only the current workspace's windows (DASH-C-74).
func (d *Desktop) SwitchWorkspace(workspace string) bool {
	name := strings.TrimSpace(workspace)
	if name == "" {
		return false
	}
	d.current = d.addWorkspaceLocked(name)
	if window := d.windowByID(d.focused); window == nil || window.Workspace != d.current {
		d.focusTopLocked()
	}
	return true
}

// Focus brings one window to the front and points the session's address at its
// deep link.
func (d *Desktop) Focus(id string) bool {
	if d.windowByID(id) == nil {
		return false
	}
	d.focusLocked(id)
	return true
}

// Focused returns the window the session's address points at.
func (d *Desktop) Focused() (Window, bool) {
	window := d.windowByID(d.focused)
	if window == nil {
		return Window{}, false
	}
	return *window, true
}

// SetUnsaved records the unsaved changes the application announced. A new
// unsaved period brings back the one close veto DASH-C-74 grants.
func (d *Desktop) SetUnsaved(id string, isUnsaved bool) bool {
	window := d.windowByID(id)
	if window == nil {
		return false
	}
	if !isUnsaved {
		window.vetoUsed = false
	} else if !window.IsUnsaved {
		window.vetoUsed = false
	}
	window.IsUnsaved = isUnsaved
	return true
}

// RequestClose closes one window, honouring the application's one close veto
// while it reports unsaved changes (DASH-C-74). A second request destroys the
// window and its frame.
func (d *Desktop) RequestClose(id string) CloseResult {
	index := d.indexOf(id)
	if index < 0 {
		return CloseResult{}
	}
	window := &d.windows[index]
	if window.State != StateError && window.IsUnsaved && !window.vetoUsed {
		window.vetoUsed = true
		return CloseResult{Found: true, Vetoed: true}
	}
	window.State = StateClosing
	d.removeLocked(index)
	return CloseResult{Found: true, Closed: true}
}

// Close removes one window and reports whether it was open. The instances of
// the application that were not closed stay.
func (d *Desktop) Close(id string) bool {
	index := d.indexOf(id)
	if index < 0 {
		return false
	}
	d.removeLocked(index)
	return true
}

// CloseAll removes every window and returns how many it closed.
func (d *Desktop) CloseAll() int {
	closed := len(d.windows)
	d.windows = nil
	d.focused = ""
	return closed
}

// Fail moves one window into its error state, where it offers restart and close
// (DASH-C-74). A crashed frame or a missed heartbeat both arrive here.
func (d *Desktop) Fail(id, reason string) bool {
	window := d.windowByID(id)
	if window == nil || window.State == StateError || window.State == StateClosing {
		return false
	}
	window.State = StateError
	window.ErrorReason = reason
	window.IsUnsaved = false
	window.vetoUsed = false
	d.loseFocusLocked(window)
	return true
}

// Restart moves one error window back through opening. The instance stays the
// same, so the window that recovers keeps its instance key (DASH-C-74).
func (d *Desktop) Restart(id string) bool {
	window := d.windowByID(id)
	if window == nil || window.State != StateError {
		return false
	}
	window.State = StateOpening
	window.ErrorReason = ""
	window.hasHeartbeat = false
	d.focusLocked(id)
	return true
}

// Heartbeat records that one window is alive and starts watching it: a window
// that stops reporting after its first heartbeat enters the error state.
func (d *Desktop) Heartbeat(id string) bool {
	window := d.windowByID(id)
	if window == nil {
		return false
	}
	window.hasHeartbeat = true
	window.lastSeen = d.now()
	return true
}

// Expire moves every window that stayed silent longer than timeout into its
// error state and returns them (DASH-C-74).
func (d *Desktop) Expire(now time.Time, timeout time.Duration) []Window {
	if timeout <= 0 {
		timeout = DefaultHeartbeatTimeout
	}
	expired := make([]Window, 0)
	lostFocus := false
	for index := range d.windows {
		window := &d.windows[index]
		if !isLive(window.State) || !window.hasHeartbeat || now.Sub(window.lastSeen) <= timeout {
			continue
		}
		window.State = StateError
		window.ErrorReason = "window heartbeat timed out"
		window.IsUnsaved = false
		window.vetoUsed = false
		if window.IsFocused {
			window.IsFocused = false
			lostFocus = true
		}
		expired = append(expired, *window)
	}
	if lostFocus {
		d.focused = ""
		d.focusTopLocked()
	}
	return expired
}

// Workspaces returns the session's workspaces in the order they were created.
func (d *Desktop) Workspaces() []string {
	return append([]string(nil), d.workspaces...)
}

// CurrentWorkspace names the workspace the shell shows.
func (d *Desktop) CurrentWorkspace() string { return d.current }

// Taskbar lists the current workspace's live windows in the order they were
// opened (DASH-C-74).
func (d *Desktop) Taskbar() []Window {
	taskbar := make([]Window, 0, len(d.windows))
	for _, window := range d.windows {
		if !isLive(window.State) || window.Workspace != d.current {
			continue
		}
		taskbar = append(taskbar, window)
	}
	return taskbar
}

// Address names the deep link the address bar shows: the focused window's, or
// nothing when the desktop itself is focused (DASH-C-74).
func (d *Desktop) Address() string {
	if window := d.windowByID(d.focused); window != nil {
		return window.Address
	}
	return ""
}

// Records renders the session's layout records: every live window with the
// rectangle it returns to, its state, its workspace and its display. A layout
// store persists these and nothing else; minimization stays session-only
// (DASH-C-27, DASH-C-74).
func (d *Desktop) Records() []Record {
	records := make([]Record, 0, len(d.windows))
	for _, window := range d.windows {
		if !isLive(window.State) {
			continue
		}
		state := window.State
		if state == StateMinimized || state == StateOpening {
			state = StateNormal
		}
		records = append(records, Record{
			Application: window.Application,
			Instance:    window.Instance,
			X:           window.Restore.X,
			Y:           window.Restore.Y,
			Width:       window.Restore.Width,
			Height:      window.Restore.Height,
			State:       state,
			Workspace:   window.Workspace,
			Display:     window.Display,
			Pinned:      window.IsPinned,
		})
	}
	return records
}

// WindowLayout renders the session's layout at the current version: the records
// a layout store persists and nothing else (DASH-C-27).
func (d *Desktop) WindowLayout() WindowLayout {
	return WindowLayout{SchemaVersion: WindowLayoutVersion, Records: d.Records()}
}

// Layout is the migrated session snapshot codec (DASH-C-27). It carries
// minimization and belongs to the sessions that already encoded it; the layout
// domain persists Records instead.
func (d *Desktop) Layout() Layout {
	layout := Layout{Windows: make([]Placement, 0, len(d.windows))}
	for _, window := range d.windows {
		layout.Windows = append(layout.Windows, Placement{
			Application: window.Application,
			Instance:    window.Instance,
			X:           window.Bounds.X,
			Y:           window.Bounds.Y,
			Width:       window.Bounds.Width,
			Height:      window.Bounds.Height,
			IsMinimized: window.State == StateMinimized,
		})
	}
	return layout
}

// nextInstance returns one above the highest instance of one application that
// is still open. Counting the open windows would hand a closed window's number
// to a new one while its sibling keeps it, and Records carries the instance as
// a window's only identity across browsers (DASH-C-27).
func (d *Desktop) nextInstance(application string) int {
	highest := 0
	for _, window := range d.windows {
		if window.Application == application && window.Instance > highest {
			highest = window.Instance
		}
	}
	return highest + 1
}

// focusLocked brings one window to the front, marks it focused and makes its
// workspace current, so the focused window is visible.
func (d *Desktop) focusLocked(id string) {
	index := d.indexOf(id)
	if index < 0 {
		return
	}
	for position := range d.windows {
		d.windows[position].IsFocused = false
	}
	d.z++
	d.windows[index].IsFocused = true
	d.windows[index].Z = d.z
	d.focused = id
	d.current = d.windows[index].Workspace
}

// focusTopLocked focuses the topmost visible window of the current workspace.
// A minimized window cannot take the focus: it is not on screen, so handing it
// the focus would point the address and the taskbar at a hidden window
// (DASH-C-74). None is focused when the workspace holds no visible window.
func (d *Desktop) focusTopLocked() {
	best := -1
	for index := range d.windows {
		window := &d.windows[index]
		if !canHoldFocus(window.State) || window.Workspace != d.current {
			continue
		}
		if best < 0 || window.Z > d.windows[best].Z {
			best = index
		}
	}
	for index := range d.windows {
		d.windows[index].IsFocused = index == best
	}
	if best < 0 {
		d.focused = ""
		return
	}
	d.focused = d.windows[best].ID
}

// loseFocusLocked drops the focus of a window that can no longer hold it and
// hands it to the next window of the current workspace.
func (d *Desktop) loseFocusLocked(window *Window) {
	if !window.IsFocused {
		return
	}
	window.IsFocused = false
	d.focused = ""
	d.focusTopLocked()
}

// removeLocked drops one window and settles the focus on what remains.
func (d *Desktop) removeLocked(index int) {
	removed := d.windows[index]
	d.windows = append(d.windows[:index], d.windows[index+1:]...)
	if d.focused != removed.ID {
		return
	}
	d.focused = ""
	d.focusTopLocked()
}

// addWorkspaceLocked adds one workspace name and returns the stored spelling.
// The default workspace is created first, so creation order keeps it ahead.
func (d *Desktop) addWorkspaceLocked(workspace string) string {
	name := strings.TrimSpace(workspace)
	if name == "" {
		return d.current
	}
	for _, known := range d.workspaces {
		if known == name {
			return known
		}
	}
	d.workspaces = append(d.workspaces, name)
	return name
}

// windowByID finds one open window.
func (d *Desktop) windowByID(id string) *Window {
	index := d.indexOf(id)
	if index < 0 {
		return nil
	}
	return &d.windows[index]
}

// indexOf finds one open window.
func (d *Desktop) indexOf(id string) int {
	for index := range d.windows {
		if d.windows[index].ID == id {
			return index
		}
	}
	return -1
}

// openingStateOf validates the state a manifest declares for opening.
func openingStateOf(state string) string {
	switch state {
	case OpeningMaximized, OpeningFullscreen:
		return state
	default:
		return OpeningNormal
	}
}

// isLive reports a state in which the window still exists and runs.
// canHoldFocus reports whether a window is on screen and may hold the focus. A
// minimized window stays live — it keeps its frame and its page state — but it
// is hidden, so the focus belongs to the next visible window (DASH-C-74).
func canHoldFocus(state string) bool {
	return state != StateMinimized && isLive(state)
}

func isLive(state string) bool {
	switch state {
	case StateOpening, StateNormal, StateMinimized, StateMaximized, StateFullscreen, StateSnapped:
		return true
	}
	return false
}
