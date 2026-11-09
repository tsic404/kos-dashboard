// The window session client (DASH-C-74). Every operation goes through the
// front's window-session routes, and the session's own answer is the only state
// this module keeps: the shell never assembles a second source of truth. The
// window set, z-order, focus, workspaces and the address are browser session
// state and are never written to a preference or a cluster object (KOSD-F-19).

import { Failure } from "./api.js";

// The routes the window session reads and writes.
const WINDOWS_PATH = "/api/v1/desktop/windows";
const WORKSPACES_PATH = "/api/v1/desktop/workspaces";

// The states DASH-C-74 fixes. A window in one of the live states is running;
// the error state offers restart and close.
export const STATES = Object.freeze([
  "opening",
  "normal",
  "minimized",
  "maximized",
  "fullscreen",
  "snapped",
  "closing",
  "error",
]);

// LIVE_STATES are the states a running window may carry.
const LIVE_STATES = new Set(["opening", "normal", "minimized", "maximized", "fullscreen", "snapped"]);

// EMPTY_SESSION is the answer a browser that never opened a window holds.
export function emptySession() {
  return {
    windows: [],
    workspaces: ["main"],
    currentWorkspace: "main",
    taskbar: [],
    address: "",
    appInstances: {},
  };
}

// windowsOf lists the session's windows.
export function windowsOf(session) {
  const windows = session && Array.isArray(session.windows) ? session.windows : [];
  return windows.filter((window) => window && typeof window.id === "string");
}

// windowById resolves one window of the session.
export function windowById(session, id) {
  return windowsOf(session).find((window) => window.id === id) || null;
}

// taskbarOf lists the current workspace's running windows. The front answers the
// taskbar of the current workspace; the filter here is only the guard that keeps
// a stale answer from listing another workspace's window (DASH-C-74).
export function taskbarOf(session) {
  const current = session && typeof session.currentWorkspace === "string" ? session.currentWorkspace : "";
  const taskbar = session && Array.isArray(session.taskbar) ? session.taskbar : [];
  return taskbar.filter((window) => window && window.workspace === current && LIVE_STATES.has(window.state));
}

// addressOf names the deep link the focused window points at; the desktop itself
// answers nothing (DASH-C-74).
export function addressOf(session) {
  return session && typeof session.address === "string" ? session.address : "";
}

// createWindowSession binds the window layer to one front. Each operation asks
// the front and adopts its answer as the whole session, so two layers can never
// disagree about the open set.
export function createWindowSession({ api }) {
  let session = emptySession();

  // adopt replaces the session with one answer.
  function adopt(answer) {
    session = { ...emptySession(), ...(answer || {}) };
    return session;
  }

  // load reads the session's window set.
  async function load() {
    return adopt(await api.get(WINDOWS_PATH));
  }

  // current reads the last answer the front gave.
  function current() {
    return session;
  }

  // open asks the front to open an application's window. A path is a deep link:
  // the front opens or focuses the window that shows it (DASH-C-74).
  async function open(appId, { path = "", workspace = "", display = "" } = {}) {
    const body = { app: appId };
    if (path) body.path = path;
    if (workspace) body.workspace = workspace;
    if (display) body.display = display;
    return adopt(await api.write("POST", WINDOWS_PATH, body));
  }

  // openDeepLink is open by path; the front resolves the application the path
  // belongs to before it opens the window.
  async function openDeepLink(path, options = {}) {
    const resolved = await api.get(`/api/v1/deep-links?path=${encodeURIComponent(path)}`);
    const appId = resolved && resolved.app;
    if (!appId) {
      throw new Failure(404, { error: { code: "not_found", message: "没有已安装应用声明这个深链", stage: "authorize" } });
    }
    return open(appId, { ...options, path });
  }

  // focus raises one window and moves the address with it.
  async function focus(windowId) {
    return adopt(await api.write("POST", `${WINDOWS_PATH}/${encodeURIComponent(windowId)}/focus`));
  }

  // setState applies one state transition. Snapping names the edge and the
  // viewport the window fills half of (DASH-C-74).
  async function setState(windowId, state, { edge = "", viewport = null } = {}) {
    const body = { state };
    if (edge) body.edge = edge;
    if (viewport) body.viewport = viewport;
    return adopt(await api.write("PUT", `${WINDOWS_PATH}/${encodeURIComponent(windowId)}/state`, body));
  }

  // minimize, restore, maximize, fullscreen and snap are the named transitions.
  const minimize = (windowId) => setState(windowId, "minimized");
  const restore = (windowId) => setState(windowId, "normal");
  const maximize = (windowId) => setState(windowId, "maximized");
  const fullscreen = (windowId) => setState(windowId, "fullscreen");
  const snap = (windowId, edge, viewport) => setState(windowId, "snapped", { edge, viewport });

  // setBounds moves or resizes a normal window.
  async function setBounds(windowId, bounds) {
    return adopt(await api.write("PUT", `${WINDOWS_PATH}/${encodeURIComponent(windowId)}/bounds`, bounds));
  }

  // moveToWorkspace moves one window into a workspace.
  async function moveToWorkspace(windowId, workspace) {
    return adopt(await api.write("PUT", `${WINDOWS_PATH}/${encodeURIComponent(windowId)}/workspace`, { workspace }));
  }

  // markUnsaved records the unsaved changes the application announced, which
  // lets one close be vetoed (DASH-C-74).
  async function markUnsaved(windowId, unsaved) {
    return adopt(await api.write("PUT", `${WINDOWS_PATH}/${encodeURIComponent(windowId)}/unsaved`, { unsaved }));
  }

  // heartbeat reports that one window is alive.
  async function heartbeat(windowId) {
    return adopt(await api.write("POST", `${WINDOWS_PATH}/${encodeURIComponent(windowId)}/heartbeat`));
  }

  // crash moves one window into its error state, where it offers restart and
  // close (DASH-C-74).
  async function crash(windowId, reason = "") {
    return adopt(await api.write("POST", `${WINDOWS_PATH}/${encodeURIComponent(windowId)}/crash`, { reason }));
  }

  // restart recovers one error window. The instance key stays the same, so the
  // layout record that names it still applies (DASH-C-74).
  async function restart(windowId) {
    return adopt(await api.write("POST", `${WINDOWS_PATH}/${encodeURIComponent(windowId)}/restart`));
  }

  // close requests one close. The front vetoes the first request while the
  // application reports unsaved changes; the answer then keeps the window open
  // and reports the veto (DASH-C-74).
  async function close(windowId) {
    try {
      const answer = await api.write("DELETE", `${WINDOWS_PATH}/${encodeURIComponent(windowId)}`);
      adopt(answer);
      return { vetoed: false, session };
    } catch (failure) {
      if (failure instanceof Failure && failure.status === 409 && failure.code === "close_vetoed") {
        return { vetoed: true, session };
      }
      throw failure;
    }
  }

  // switchWorkspace makes one workspace current; the taskbar lists its windows
  // only (DASH-C-74).
  async function switchWorkspace(workspace) {
    return adopt(await api.write("POST", WORKSPACES_PATH, { workspace }));
  }

  // syncAddress puts the focused window's deep link into the browser address.
  // Closing a window moves the address to the window that takes the focus, so
  // the address never names a closed window (DASH-C-74).
  function syncAddress(location, history) {
    const address = addressOf(session);
    if (!location || !history || typeof history.pushState !== "function") return address;
    const path = address || "/";
    if (location.pathname === path && !location.search) return path;
    history.pushState({ kosAddress: path }, "", path);
    return path;
  }

  return {
    current,
    load,
    open,
    openDeepLink,
    focus,
    setState,
    minimize,
    restore,
    maximize,
    fullscreen,
    snap,
    setBounds,
    moveToWorkspace,
    markUnsaved,
    heartbeat,
    crash,
    restart,
    close,
    switchWorkspace,
    syncAddress,
  };
}
