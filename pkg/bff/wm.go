package bff

import (
	"net/http"
	"sync"
	"time"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
	"github.com/tsic404/kos-dashboard/pkg/wm"
)

// The window-session routes (DASH-C-74). A window's state, geometry, z-order,
// focus, workspace and display are the browser session's own state: the front
// keeps them in memory for the sealed session cookie and writes no cluster
// object and no preference key. The application registry supplies the manifest
// defaults an opening window settles to (KOSD-C-73). A session answer carries
// the whole window set, so the shell never assembles a second source of truth.
const (
	desktopWindowsPath         = "/api/v1/desktop/windows"
	desktopWindowPath          = "/api/v1/desktop/windows/{window}"
	desktopWindowFocusPath     = "/api/v1/desktop/windows/{window}/focus"
	desktopWindowStatePath     = "/api/v1/desktop/windows/{window}/state"
	desktopWindowBoundsPath    = "/api/v1/desktop/windows/{window}/bounds"
	desktopWindowWorkspacePath = "/api/v1/desktop/windows/{window}/workspace"
	desktopWindowUnsavedPath   = "/api/v1/desktop/windows/{window}/unsaved"
	desktopWindowHeartbeatPath = "/api/v1/desktop/windows/{window}/heartbeat"
	desktopWindowCrashPath     = "/api/v1/desktop/windows/{window}/crash"
	desktopWindowRestartPath   = "/api/v1/desktop/windows/{window}/restart"
	desktopWorkspacesPath      = "/api/v1/desktop/workspaces"
)

// windowSessionTTL is how long an unused browser session's window set is kept.
// The sealed cookie that names a session lives twelve hours (sessionCookie's
// MaxAge), so nothing can reach the entry after that.
const windowSessionTTL = 12 * time.Hour

// windowSessions holds one window manager per browser session. A session's
// windows die with it: a new browser, a new sign-in and a restarted process all
// start with no open set (DASH-C-74).
type windowSessions struct {
	mu       sync.Mutex
	now      func() time.Time
	sessions map[string]*windowSession
	// ttl is how long an unused session is kept; zero falls back to
	// windowSessionTTL.
	ttl time.Duration
	// lastPrune is when the store last swept its idle sessions; the sweep runs
	// at most once a minute so the hot path stays one map lookup.
	lastPrune time.Time
}

// windowSession is one browser session's window manager and the application
// instances its windows run in.
type windowSession struct {
	mu sync.Mutex
	// desktop is the session's window set (DASH-C-74).
	desktop *wm.Desktop
	// instances maps one desktop window to the application instance carrying
	// its token (KOSD-C-75). A restart replaces the instance while the desktop
	// window keeps its identity.
	instances map[string]string
	// lastSeen is when this session last answered a request; an idle session is
	// pruned so the map stays bounded by live browsers.
	lastSeen time.Time
}

// newWindowSessions builds the front's window session store.
func newWindowSessions(now func() time.Time) *windowSessions {
	return &windowSessions{now: now, sessions: make(map[string]*windowSession), ttl: windowSessionTTL}
}

// get resolves one browser session's window manager, building it on first use.
// An idle session is dropped first, so a browser that never signs out does not
// keep its window set for the life of the process.
func (s *windowSessions) get(conversation string) *windowSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneLocked(now)
	session, found := s.sessions[conversation]
	if !found {
		session = &windowSession{
			desktop:   wm.NewWithClock(wm.DefaultSize, s.now),
			instances: make(map[string]string),
		}
		s.sessions[conversation] = session
	}
	session.lastSeen = now
	return session
}

// pruneLocked drops every session that has gone unused for the TTL. It sweeps
// at most once a minute, so a request costs one lookup unless a minute passed.
func (s *windowSessions) pruneLocked(now time.Time) {
	if !s.lastPrune.IsZero() && now.Sub(s.lastPrune) < time.Minute {
		return
	}
	s.lastPrune = now
	ttl := s.ttl
	if ttl <= 0 {
		ttl = windowSessionTTL
	}
	for conversation, session := range s.sessions {
		if now.Sub(session.lastSeen) > ttl {
			delete(s.sessions, conversation)
		}
	}
}

// drop ends one browser session's windows, which is what a sign-out does.
func (s *windowSessions) drop(conversation string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, conversation)
}

// openDesktopWindow registers one window in the caller's session and answers
// the application instance that mounts it. A single-mode application answers
// its live window instead of opening a second one; a deep link answers its own
// window; a multi-mode application takes a fresh instance key per new path
// (DASH-C-74).
func (s *Server) openDesktopWindow(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.csrfOrRefuse(w, r); !ok {
		return
	}
	registry, account, ok := s.appsAndAccount(w, r)
	if !ok {
		return
	}
	var request struct {
		App       string `json:"app"`
		Path      string `json:"path"`
		Workspace string `json:"workspace"`
		Display   string `json:"display"`
	}
	body, err := readBody(r)
	if err != nil || decodeJSON(body, &request) != nil || request.App == "" {
		writeFailure(w, http.StatusBadRequest, "invalid_request", "窗口打开请求缺少应用标识", "validate")
		return
	}
	entry, found := registry.App(request.App)
	if !found {
		writeFailure(w, http.StatusNotFound, "not_found", "应用未安装或声明无效", "authorize")
		return
	}
	if request.Path != "" {
		// A deep link opens or focuses its own application; a path another
		// application declared (or none) creates no window here (DASH-C-74).
		resolved, err := registry.ResolveDeepLink(request.Path)
		if err != nil || resolved.ID != entry.ID {
			writeFailure(w, http.StatusNotFound, "not_found", "没有已安装应用声明这个深链", "authorize")
			return
		}
	}
	session := s.windows.get(s.conversationOf(r))
	session.mu.Lock()
	defer session.mu.Unlock()
	session.desktop.Expire(s.now(), wm.DefaultHeartbeatTimeout)
	window, isNew := session.desktop.OpenWindow(entry.ID, wm.OpenOptions{
		Mode:      entry.Window.Mode,
		State:     entry.Window.DefaultState,
		Width:     entry.Window.Width,
		Height:    entry.Window.Height,
		Workspace: request.Workspace,
		Display:   request.Display,
		Address:   request.Path,
	})
	instanceID := session.instances[window.ID]
	if !isNew && instanceID != "" {
		// A deep link that only focused its window keeps the application
		// instance it already runs in; registering another one would leave an
		// instance no window shows.
		if instance, held := s.instanceOf(s.conversationOf(r), account, instanceID); held {
			s.writeOpenAnswer(w, session, window.ID, entry, instance, false)
			return
		}
	}
	instance, _, err := s.host.Open(registry, s.conversationOf(r), account, entry.ID)
	if err != nil {
		if isNew {
			session.desktop.Close(window.ID)
		}
		writeFailure(w, http.StatusConflict, "failed_precondition", "应用未安装或已进入错误态，无法打开", "authorize")
		return
	}
	session.instances[window.ID] = instance.ID
	s.writeOpenAnswer(w, session, window.ID, entry, instance, isNew)
}

// writeOpenAnswer writes one open answer: the desktop window, the application
// instance that mounts it and the signed frame address (KOSD-C-73).
func (s *Server) writeOpenAnswer(w http.ResponseWriter, session *windowSession, window string, entry apphost.Entry, instance apphost.Window, isNew bool) {
	session.desktop.Settle(window)
	settled, _ := session.desktop.Window(window)
	answer := s.sessionAnswer(session, settled.ID)
	answer["instance"] = instance
	answer["sandbox"] = apphost.FrameSandbox
	answer["isNew"] = isNew
	if frame, reason := s.frameAddress(entry); frame != "" {
		answer["frame"] = frame
	} else if reason != "" {
		answer["frameError"] = reason
	}
	writeJSON(w, http.StatusAccepted, answer)
}

// instanceOf resolves one application instance the session holds.
func (s *Server) instanceOf(conversation, account, id string) (apphost.Window, bool) {
	for _, window := range s.host.Windows(conversation, account) {
		if window.ID == id {
			return window, true
		}
	}
	return apphost.Window{}, false
}

// listDesktopWindows reports the session's window set.
func (s *Server) listDesktopWindows(w http.ResponseWriter, r *http.Request) {
	registry, _, ok := s.appsAndAccount(w, r)
	if !ok {
		return
	}
	session := s.windows.get(s.conversationOf(r))
	session.mu.Lock()
	defer session.mu.Unlock()
	syncInstalled(session.desktop, registry)
	session.desktop.Expire(s.now(), wm.DefaultHeartbeatTimeout)
	writeJSON(w, http.StatusOK, s.sessionAnswer(session, ""))
}

// focusDesktopWindow raises one window to the front; the address follows it
// (DASH-C-74).
func (s *Server) focusDesktopWindow(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.csrfOrRefuse(w, r); !ok {
		return
	}
	session, window, ok := s.sessionWindow(w, r)
	if !ok {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if !session.desktop.Focus(window) {
		writeFailure(w, http.StatusNotFound, "not_found", wm.ErrWindowUnknown.Error(), "authorize")
		return
	}
	writeJSON(w, http.StatusOK, s.sessionAnswer(session, window))
}

// stateDesktopWindow applies one state transition: minimized, normal,
// maximized, fullscreen or snapped (DASH-C-74). Snapping carries the viewport
// the window fills half of.
func (s *Server) stateDesktopWindow(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.csrfOrRefuse(w, r); !ok {
		return
	}
	session, window, ok := s.sessionWindow(w, r)
	if !ok {
		return
	}
	var request struct {
		State    string     `json:"state"`
		Edge     string     `json:"edge"`
		Viewport *wm.Bounds `json:"viewport"`
	}
	body, err := readBody(r)
	if err != nil || decodeJSON(body, &request) != nil {
		writeFailure(w, http.StatusBadRequest, "invalid_request", "状态请求体不是合法 JSON", "validate")
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	var applied bool
	switch request.State {
	case wm.StateMinimized:
		applied = session.desktop.Minimize(window)
	case wm.StateNormal:
		applied = session.desktop.Restore(window)
	case wm.StateMaximized:
		applied = session.desktop.Maximize(window)
	case wm.StateFullscreen:
		applied = session.desktop.Fullscreen(window)
	case wm.StateSnapped:
		if request.Viewport == nil {
			writeFailure(w, http.StatusBadRequest, "invalid_request", "吸附请求缺少视口", "validate")
			return
		}
		applied = session.desktop.Snap(window, request.Edge, *request.Viewport)
	default:
		writeFailure(w, http.StatusBadRequest, "invalid_request", "未知的窗口状态", "validate")
		return
	}
	if !applied {
		writeFailure(w, http.StatusConflict, "failed_precondition", wm.ErrTransition.Error(), "authorize")
		return
	}
	writeJSON(w, http.StatusOK, s.sessionAnswer(session, window))
}

// boundsDesktopWindow moves or resizes one normal window. The rectangle is
// session geometry; the layout domain persists it separately (DASH-C-27).
func (s *Server) boundsDesktopWindow(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.csrfOrRefuse(w, r); !ok {
		return
	}
	session, window, ok := s.sessionWindow(w, r)
	if !ok {
		return
	}
	var bounds wm.Bounds
	body, err := readBody(r)
	if err != nil || decodeJSON(body, &bounds) != nil {
		writeFailure(w, http.StatusBadRequest, "invalid_request", "窗口边界不是合法 JSON", "validate")
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if !session.desktop.SetBounds(window, bounds) {
		writeFailure(w, http.StatusConflict, "failed_precondition", "只有普通状态的窗口可以移动或缩放", "authorize")
		return
	}
	writeJSON(w, http.StatusOK, s.sessionAnswer(session, window))
}

// workspaceDesktopWindow moves one window to a workspace; the taskbar lists
// only the current workspace's windows (DASH-C-74).
func (s *Server) workspaceDesktopWindow(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.csrfOrRefuse(w, r); !ok {
		return
	}
	session, window, ok := s.sessionWindow(w, r)
	if !ok {
		return
	}
	var request struct {
		Workspace string `json:"workspace"`
	}
	body, err := readBody(r)
	if err != nil || decodeJSON(body, &request) != nil || request.Workspace == "" {
		writeFailure(w, http.StatusBadRequest, "invalid_request", "工作区请求缺少名称", "validate")
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if !session.desktop.SetWorkspace(window, request.Workspace) {
		writeFailure(w, http.StatusNotFound, "not_found", wm.ErrWindowUnknown.Error(), "authorize")
		return
	}
	writeJSON(w, http.StatusOK, s.sessionAnswer(session, window))
}

// unsavedDesktopWindow records the unsaved changes the application announced;
// while they last, one close may be vetoed (DASH-C-74).
func (s *Server) unsavedDesktopWindow(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.csrfOrRefuse(w, r); !ok {
		return
	}
	session, window, ok := s.sessionWindow(w, r)
	if !ok {
		return
	}
	var request struct {
		Unsaved bool `json:"unsaved"`
	}
	body, err := readBody(r)
	if err != nil || decodeJSON(body, &request) != nil {
		writeFailure(w, http.StatusBadRequest, "invalid_request", "未保存状态请求体不是合法 JSON", "validate")
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if !session.desktop.SetUnsaved(window, request.Unsaved) {
		writeFailure(w, http.StatusNotFound, "not_found", wm.ErrWindowUnknown.Error(), "authorize")
		return
	}
	writeJSON(w, http.StatusOK, s.sessionAnswer(session, window))
}

// heartbeatDesktopWindow records that one window is alive. A window that stops
// sending heartbeats enters the error state (DASH-C-74).
func (s *Server) heartbeatDesktopWindow(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.csrfOrRefuse(w, r); !ok {
		return
	}
	session, window, ok := s.sessionWindow(w, r)
	if !ok {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	// The timeout is settled before the beat is recorded: a beat that arrives
	// after the deadline does not revive a window that already stopped running
	// — only a restart does (DASH-C-74).
	session.desktop.Expire(s.now(), wm.DefaultHeartbeatTimeout)
	if !session.desktop.Heartbeat(window) {
		writeFailure(w, http.StatusNotFound, "not_found", wm.ErrWindowUnknown.Error(), "authorize")
		return
	}
	writeJSON(w, http.StatusOK, s.sessionAnswer(session, window))
}

// crashDesktopWindow moves one window into its error state, where it offers
// restart and close (DASH-C-74).
func (s *Server) crashDesktopWindow(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.csrfOrRefuse(w, r); !ok {
		return
	}
	session, window, ok := s.sessionWindow(w, r)
	if !ok {
		return
	}
	var request struct {
		Reason string `json:"reason"`
	}
	body, err := readBody(r)
	if err != nil || decodeJSON(body, &request) != nil {
		writeFailure(w, http.StatusBadRequest, "invalid_request", "崩溃请求体不是合法 JSON", "validate")
		return
	}
	if request.Reason == "" {
		request.Reason = "应用崩溃"
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if !session.desktop.Fail(window, request.Reason) {
		writeFailure(w, http.StatusConflict, "failed_precondition", "窗口已处于错误或关闭状态", "authorize")
		return
	}
	writeJSON(w, http.StatusOK, s.sessionAnswer(session, window))
}

// restartDesktopWindow recovers one error window. The desktop window keeps its
// instance key; a fresh application instance carries its new token (DASH-C-74).
func (s *Server) restartDesktopWindow(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.csrfOrRefuse(w, r); !ok {
		return
	}
	registry, account, ok := s.appsAndAccount(w, r)
	if !ok {
		return
	}
	session, window, ok := s.sessionWindow(w, r)
	if !ok {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	current, found := session.desktop.Window(window)
	if !found {
		writeFailure(w, http.StatusNotFound, "not_found", wm.ErrWindowUnknown.Error(), "authorize")
		return
	}
	if !session.desktop.Restart(window) {
		writeFailure(w, http.StatusConflict, "failed_precondition", "只有错误态的窗口可以重启", "authorize")
		return
	}
	if previous := session.instances[window]; previous != "" {
		_ = s.host.Close(account, previous)
	}
	instance, _, err := s.host.Open(registry, s.conversationOf(r), account, current.Application)
	if err != nil {
		session.desktop.Fail(window, "应用未安装或已进入错误态，无法重启")
		writeFailure(w, http.StatusConflict, "failed_precondition", "应用未安装或已进入错误态，无法重启", "authorize")
		return
	}
	session.instances[window] = instance.ID
	session.desktop.Settle(window)
	answer := s.sessionAnswer(session, window)
	answer["instance"] = instance
	answer["sandbox"] = apphost.FrameSandbox
	answer["isNew"] = true
	if entry, installed := registry.App(current.Application); installed {
		if frame, reason := s.frameAddress(entry); frame != "" {
			answer["frame"] = frame
		} else if reason != "" {
			answer["frameError"] = reason
		}
	}
	writeJSON(w, http.StatusAccepted, answer)
}

// closeDesktopWindow requests one close. While the application reports unsaved
// changes the first request is vetoed and the window stays; a later request
// destroys it and its frame (DASH-C-74).
func (s *Server) closeDesktopWindow(w http.ResponseWriter, r *http.Request) {
	token, ok := s.sessionOrRefuse(w, r)
	if !ok {
		return
	}
	if !s.csrfOK(r) {
		writeFailure(w, http.StatusForbidden, "permission_denied", "缺少或错误的 CSRF 令牌", "authorize")
		return
	}
	account, ok := s.accountOrRefuse(w, r, token)
	if !ok {
		return
	}
	session := s.windows.get(s.conversationOf(r))
	session.mu.Lock()
	defer session.mu.Unlock()
	window := r.PathValue("window")
	result := session.desktop.RequestClose(window)
	switch {
	case !result.Found:
		writeFailure(w, http.StatusNotFound, "not_found", wm.ErrWindowUnknown.Error(), "authorize")
	case result.Vetoed:
		writeFailure(w, http.StatusConflict, "close_vetoed", "应用有未保存的更改，关闭已被否决", "authorize")
	case result.Closed:
		if instance := session.instances[window]; instance != "" {
			_ = s.host.Close(account, instance)
			delete(session.instances, window)
		}
		writeJSON(w, http.StatusOK, s.sessionAnswer(session, ""))
	}
}

// listDesktopWorkspaces reports the session's workspaces and which one is
// current.
func (s *Server) listDesktopWorkspaces(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.appsAndAccount(w, r); !ok {
		return
	}
	session := s.windows.get(s.conversationOf(r))
	session.mu.Lock()
	defer session.mu.Unlock()
	writeJSON(w, http.StatusOK, s.sessionAnswer(session, ""))
}

// switchDesktopWorkspace makes one workspace current; the taskbar lists its
// windows only (DASH-C-74).
func (s *Server) switchDesktopWorkspace(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.csrfOrRefuse(w, r); !ok {
		return
	}
	if _, _, ok := s.appsAndAccount(w, r); !ok {
		return
	}
	var request struct {
		Workspace string `json:"workspace"`
	}
	body, err := readBody(r)
	if err != nil || decodeJSON(body, &request) != nil || request.Workspace == "" {
		writeFailure(w, http.StatusBadRequest, "invalid_request", "工作区请求缺少名称", "validate")
		return
	}
	session := s.windows.get(s.conversationOf(r))
	session.mu.Lock()
	defer session.mu.Unlock()
	if !session.desktop.SwitchWorkspace(request.Workspace) {
		writeFailure(w, http.StatusBadRequest, "invalid_request", "工作区名称不能为空", "validate")
		return
	}
	writeJSON(w, http.StatusOK, s.sessionAnswer(session, ""))
}

// sessionWindow resolves the caller's session and the window the path names.
func (s *Server) sessionWindow(w http.ResponseWriter, r *http.Request) (*windowSession, string, bool) {
	if _, _, ok := s.appsAndAccount(w, r); !ok {
		return nil, "", false
	}
	return s.windows.get(s.conversationOf(r)), r.PathValue("window"), true
}

// sessionAnswer renders one browser session's window state. changed names the
// window a transition touched; an empty name answers the set alone. Every answer
// settles the heartbeats that timed out first, so a silent window shows its
// error state in the answer that observed it (DASH-C-74).
func (s *Server) sessionAnswer(session *windowSession, changed string) map[string]any {
	session.desktop.Expire(s.now(), wm.DefaultHeartbeatTimeout)
	instances := make(map[string]string, len(session.instances))
	for window, instance := range session.instances {
		instances[window] = instance
	}
	answer := map[string]any{
		"windows":          session.desktop.Windows(),
		"workspaces":       session.desktop.Workspaces(),
		"currentWorkspace": session.desktop.CurrentWorkspace(),
		"taskbar":          session.desktop.Taskbar(),
		"address":          session.desktop.Address(),
		"appInstances":     instances,
	}
	if changed != "" {
		if window, found := session.desktop.Window(changed); found {
			answer["window"] = window
		}
	}
	return answer
}

// syncInstalled moves the session's windows of uninstalled applications into
// their error state (DASH-C-30): the window stays observable and cannot run.
func syncInstalled(desktop *wm.Desktop, registry *apphost.Registry) {
	for _, window := range desktop.Windows() {
		if _, installed := registry.App(window.Application); installed {
			continue
		}
		desktop.Fail(window.ID, "应用已卸载，窗口已进入错误态")
	}
}
