// The shell: it signs in, renders the pages the registry lists, and keeps the
// desktop surfaces current — the dock, the launcher and the application windows
// (DASH-C-22, DASH-C-30). Visibility is never decided here: the registry answer
// already carries only the pages this session may see (KOSD-C-59), and the
// application list already omits an undeclared or invalid `App` (KOSD-C-73).

import { Failure } from "./api.js";
import { appsOf, findApp, refusalsOf } from "./apps.js";
import {
  dockLayoutOf,
  launcherLayoutOf,
  readDock,
  readLauncher,
  saveDock,
  saveLauncher,
  withAutoHide,
  withDockPosition,
  withPinned,
  withSort,
} from "./preferences.js";
import { RECENT_STORE, readRecent, recordRecent } from "./recent.js";
import { renderBoot, renderLogin, renderShell } from "./render.js";
import { subscribeStream } from "./stream.js";
import { DESKTOP_PAGE_ID, findPage, firstPage, sourceOf } from "./registry.js";
import { createWindowSession, emptySession, windowById, windowsOf as windowSet } from "./windows.js";
import { createWindowLayer } from "./windowlayer.js";
import { createAppHost } from "./apphost.js";

// NOTIFICATIONS_ITEM is the desktop item the event stream refreshes.
const NOTIFICATIONS_ITEM = "notifications";
const REGISTRY_PATH = "/api/v1/pages";
const APPS_PATH = "/api/v1/apps";
// WATCH_MS is how often the shell re-reads the window session. The front expires
// a window whose application stopped proving it runs, and that time out is
// visible only in an answer (DASH-C-74).
const WATCH_MS = 10000;

// createApp wires the shell to one root element. Every dependency is injected
// so the behaviour can be exercised without a browser; the window layer is
// absent when there is no document to mount frames into.
export function createApp({
  api,
  root,
  storage,
  EventSource,
  document: doc = null,
  container = null,
  window: hostWindow = null,
  location = null,
  // liveness overrides the frame host's wait for the handshake and for the
  // application's next `alive`; the defaults are the contract's own (DASH-C-74).
  liveness = {},
}) {
  const windows = createWindowSession({ api });
  const layer = container && doc
    ? createWindowLayer({
        document: doc,
        container,
        host: createAppHost({
          api,
          document: doc,
          window: hostWindow,
          consoleOrigin: originOf(location),
          loadTimeout: liveness.loadTimeout,
          aliveTimeout: liveness.aliveTimeout,
          // The application proves it runs with `alive`; a frame that stops
          // reporting is a window that stopped (DASH-C-74).
          onAlive: (windowId) => {
            void reportAlive(windowId);
          },
          onUnsaved: (windowId, unsaved) => {
            void reportUnsaved(windowId, unsaved);
          },
          onFailure: (windowId, reason) => {
            void crashWindow(windowId, reason);
          },
        }),
      })
    : null;
  const state = {
    session: null,
    registry: { items: [] },
    requested: "",
    pageId: "",
    page: null,
    error: null,
    apps: [],
    refusals: [],
    dock: null,
    launcher: null,
    windows: [],
    windowSession: emptySession(),
    recent: [],
    launcherOpen: false,
    launcherQuery: "",
    dockMenu: null,
    surfaceError: null,
    pending: null,
    answer: null,
  };
  let closeStream = null;
  let isReadingSurfaces = false;
  let watch = null;
  // stoppedWindows names the windows whose frame this page saw stop but whose
  // crash report the front did not take. Their frame is gone either way, so the
  // page keeps showing the error and the way back until the front records it, the
  // window is restarted, or it is closed (DASH-C-74).
  const stoppedWindows = new Map();

  function render() {
    if (!state.session) {
      root.innerHTML = renderLogin({ error: state.error });
      return;
    }
    state.recent = readRecent(storage, RECENT_STORE);
    root.innerHTML = renderShell(state);
  }

  // start resolves the session, then the surfaces and the page registry.
  // requested names the page the address asked for; a page the registry does
  // not list falls back to the default route.
  async function start(requested = "") {
    root.innerHTML = renderBoot();
    state.requested = requested;
    try {
      state.session = await api.session();
    } catch (failure) {
      state.session = null;
      state.error = failure.status === 401 ? null : failure;
      render();
      return;
    }
    state.error = null;
    await loadSurfaces();
    if (!state.session) {
      render();
      return;
    }
    await openRegistry(state.requested);
    await openDeepLink();
  }

  // openRegistry reads the pages this session may see and opens one of them.
  // A page the registry omits has no entry and no route: that is how a
  // disabled component disappears (DASH-C-22).
  async function openRegistry(requested = "") {
    if (!state.session) {
      render();
      return;
    }
    try {
      state.registry = await api.get(REGISTRY_PATH);
    } catch (failure) {
      state.error = failure;
      if (failure.status === 401) forgetSession();
      render();
      return;
    }
    state.error = null;
    openStream();
    const page = (requested && findPage(state.registry, requested)) || firstPage(state.registry);
    if (!page) {
      state.pageId = "";
      state.page = null;
      render();
      return;
    }
    await navigate(page.id);
  }

  // openDeepLink opens or focuses the window that shows the address the browser
  // loaded. A path no installed application declares resolves to nothing, so no
  // window appears (DASH-C-30, DASH-C-74).
  async function openDeepLink() {
    const path = deepLinkPathOf(location);
    if (path === "" || !layer) return;
    let answer = null;
    try {
      answer = await mountAnswer(await windows.openDeepLink(path), null, () => windows.openDeepLink(path));
    } catch (failure) {
      if (failure.status === 401) forgetSession();
      else if (failure.status !== 404) state.surfaceError = failure;
      render();
      return;
    }
    if (!answer) return;
    state.surfaceError = null;
    render();
  }

  // navigate renders one listed page.
  async function navigate(pageId) {
    if (!state.session) return;
    state.pageId = pageId;
    state.error = null;
    state.page = null;
    state.answer = null;
    if (!findPage(state.registry, pageId)) {
      state.error = pageNotFound();
      render();
      return;
    }
    render();
    try {
      state.page = await readPage(pageId);
    } catch (failure) {
      state.page = null;
      state.error = failure;
      if (failure.status === 401) forgetSession();
    }
    render();
  }

  // readPage reads one page projection. The control plane serves it already
  // trimmed to the session role and refuses a page the role may not see with
  // the same answer an absent page gives (DASH-C-22, KOSD-C-59).
  async function readPage(pageId) {
    const page = await api.get(sourceOf(pageId));
    return withLocalSources(page);
  }

  // pageNotFound is the one answer for a page that is absent or withdrawn with
  // its component.
  function pageNotFound() {
    return new Failure(404, {
      error: { code: "page_not_found", message: "页面不存在或所属组件已停用", stage: "read" },
    });
  }

  // forgetSession drops everything the ended session owned — the stream, the
  // windows the browser mounted and the surfaces read for it — so the shell
  // returns to the sign-in form clean.
  function forgetSession() {
    if (closeStream) {
      closeStream();
      closeStream = null;
    }
    stopWatch();
    if (layer) layer.closeAll();
    stoppedWindows.clear();
    state.session = null;
    state.registry = { items: [] };
    state.pageId = "";
    state.page = null;
    state.apps = [];
    state.refusals = [];
    state.dock = null;
    state.launcher = null;
    state.windows = [];
    state.windowSession = emptySession();
    state.launcherOpen = false;
    state.launcherQuery = "";
    state.dockMenu = null;
    state.surfaceError = null;
    state.pending = null;
    state.answer = null;
  }

  // loadSurfaces reads the installed applications, both layouts and the
  // browser session's window set. A surface that fails keeps what the shell
  // already shows and reports the refusal without replacing the current page.
  async function loadSurfaces() {
    if (isReadingSurfaces) return;
    isReadingSurfaces = true;
    try {
      const [appsRead, dockRead, launcherRead, windowsRead] = await Promise.allSettled([
        api.get(APPS_PATH),
        readDock(api),
        readLauncher(api),
        windows.load(),
      ]);
      state.surfaceError = null;
      if (appsRead.status === "fulfilled") {
        state.apps = appsOf(appsRead.value);
        state.refusals = refusalsOf(appsRead.value);
      } else {
        absorbSurfaceFailure(appsRead.reason);
      }
      if (dockRead.status === "fulfilled") state.dock = dockRead.value;
      else absorbSurfaceFailure(dockRead.reason);
      if (launcherRead.status === "fulfilled") state.launcher = launcherRead.value;
      else absorbSurfaceFailure(launcherRead.reason);
      if (windowsRead.status === "fulfilled") {
        // The window set is browser session state (DASH-C-74): the front's
        // answer settles the nodes this page mounted and never opens one by
        // itself, so a page load restores no open set.
        syncWindows();
      } else {
        absorbSurfaceFailure(windowsRead.reason);
      }
    } finally {
      isReadingSurfaces = false;
    }
  }

  // syncWindows adopts the last window-session answer and settles the mounted
  // nodes against it. A window the front no longer holds is gone, and every
  // other takes the state, geometry and focus the front answered. A window this
  // page saw stop is shown in its error state even while the front still lists it
  // as running (DASH-C-74).
  function syncWindows() {
    state.windowSession = withStoppedWindows(windows.current());
    state.windows = layer ? layer.sync(state.windowSession) : windowSet(state.windowSession);
    return state.windows;
  }

  // withStoppedWindows lists the windows this page stopped but the front did not
  // record in their error state, so neither the node nor the taskbar presents them
  // as running. An answer that already has the state is the front's and wins.
  function withStoppedWindows(session) {
    if (stoppedWindows.size === 0 || !session) return session;
    const stopped = (window) => {
      if (!window || !stoppedWindows.has(window.id)) return window;
      if (window.state === "error" || window.state === "closing") return window;
      return { ...window, state: "error", errorReason: stoppedWindows.get(window.id) };
    };
    return {
      ...session,
      windows: (Array.isArray(session.windows) ? session.windows : []).map(stopped),
      taskbar: (Array.isArray(session.taskbar) ? session.taskbar : []).map(stopped),
    };
  }

  // syncAddress puts the focused window's deep link into the browser address, so
  // opening, focusing and closing keep the address and the open set together
  // (DASH-C-74).
  function syncAddress() {
    const history = hostWindow && hostWindow.history ? hostWindow.history : null;
    return windows.syncAddress(location, history);
  }

  // startWatch re-reads the window session while a window is mounted, so a
  // timeout the front settled shows up without a user action. Only a change of
  // state redraws; the address still follows the focus (DASH-C-74).
  function startWatch() {
    if (watch || typeof setInterval !== "function") return;
    watch = setInterval(async () => {
      if (!state.session || !state.windows.length) return;
      const before = statesOf(state.windows);
      try {
        await windows.load();
      } catch (failure) {
        if (failure && failure.status === 401) forgetSession();
        return;
      }
      syncWindows();
      syncAddress();
      if (statesOf(state.windows) !== before) render();
    }, WATCH_MS);
    if (watch && typeof watch.unref === "function") watch.unref();
  }

  // statesOf names the mounted windows and their states, so the watch can tell
  // whether the front changed one of them.
  function statesOf(list) {
    return list.map((window) => `${window.id}:${window.state}`).join(",");
  }

  function stopWatch() {
    if (!watch) return;
    clearInterval(watch);
    watch = null;
  }

  // reportAlive forwards the application's own liveness proof to the window
  // session: the heartbeat says the frame runs, not the page (DASH-C-74). The
  // front expires a window that stops reporting, and its answer then carries the
  // window in its error state.
  async function reportAlive(windowId) {
    if (!layer || !windowId) return;
    const before = statesOf(state.windows);
    try {
      await windows.heartbeat(windowId);
    } catch (failure) {
      if (failure && failure.status === 401) forgetSession();
      return;
    }
    syncWindows();
    if (statesOf(state.windows) !== before) render();
  }

  // reportUnsaved records the unsaved changes the application announced, which
  // lets one close be vetoed (DASH-C-74).
  async function reportUnsaved(windowId, unsaved) {
    if (!layer || !windowId) return;
    try {
      await windows.markUnsaved(windowId, unsaved);
      syncWindows();
      state.surfaceError = null;
    } catch (failure) {
      if (failure && failure.status === 401) forgetSession();
    }
    render();
  }

  // absorbSurfaceFailure records why one surface could not be read. An ended
  // session returns to the sign-in form instead of showing stale entries.
  function absorbSurfaceFailure(failure) {
    if (failure && failure.status === 401) {
      forgetSession();
      return;
    }
    state.surfaceError = state.surfaceError || failure;
  }

  // refreshSurfaces re-reads the surfaces and the page the shell shows after
  // the event stream reports a change: an uninstalled application leaves the
  // dock and the launcher and its open window enters the error state
  // (DASH-C-30), and a page action's progress is read from the object state and
  // conditions its page read answers (KOSD-C-12).
  async function refreshSurfaces() {
    if (!state.session) return;
    await loadSurfaces();
    if (!state.session) {
      render();
      return;
    }
    if (!state.pageId) {
      render();
      return;
    }
    await refresh(state.pageId);
  }

  // withLocalSources fills every local item from browser storage; a local item
  // is never read from the control plane (KOSD-C-53).
  function withLocalSources(page) {
    const items = (page.items || []).map((item) => {
      if (item.source !== "local") return item;
      return { ...item, value: readRecent(storage, item.store) };
    });
    return { ...page, items };
  }

  // login trades a login name and password for the session the BFF seals into
  // its cookie (KOSD-C-63).
  async function login(username, password) {
    state.error = null;
    try {
      state.session = await api.login(username, password);
    } catch (failure) {
      state.error = failure;
      render();
      return;
    }
    await loadSurfaces();
    await openRegistry(state.requested);
  }

  // logout revokes the session and returns to the sign-in form.
  async function logout() {
    try {
      await api.logout();
    } catch (failure) {
      state.error = failure;
      render();
      return;
    }
    forgetSession();
    state.error = null;
    render();
  }

  // launch opens one application from the launcher or the dock. The front
  // answers the window its manifest mode allows — a single-mode reopen focuses
  // the live window instead of adding one — and the layer mounts its frame
  // (DASH-C-74, KOSD-C-75); the recent list records the open locally
  // (KOSD-C-53).
  async function launch(appId) {
    const app = findApp(state.apps, appId);
    if (!app) {
      state.surfaceError = new Failure(404, {
        error: { code: "app_unavailable", message: "应用未安装或声明无效", stage: "authorize" },
      });
      state.launcherOpen = false;
      render();
      return;
    }
    state.launcherOpen = false;
    state.dockMenu = null;
    if (!layer) {
      render();
      return;
    }
    try {
      await mountApp(app);
      recordRecent(storage, { id: app.id, name: app.name, openedAt: new Date().toISOString() });
      state.surfaceError = null;
    } catch (failure) {
      if (failure && failure.status === 401) forgetSession();
      else state.surfaceError = failure;
    }
    render();
  }

  // mountApp asks the front to open one application's window and mounts the
  // frame its answer carries.
  function mountApp(app) {
    const workspace = state.windowSession.currentWorkspace;
    return mountAnswer(windows.open(app.id, { workspace }), app, () => windows.open(app.id, { workspace }));
  }

  // mountAnswer mounts the window one open answer carries and adopts the session
  // it answered. An instance this page holds no record of whose token was
  // already delivered can never handshake again, so it is closed and a fresh one
  // registered for the same window (KOSD-C-75).
  async function mountAnswer(pending, app, reopen) {
    let answer = await pending;
    if (answer && answer.isNew === false && answer.instance && answer.instance.hasToken && answer.window && !layer.holds(answer.window.id)) {
      await api.write("DELETE", `/api/v1/windows/${encodeURIComponent(answer.instance.id)}`).catch((failure) => {
        // An instance the front already pruned is as gone as one it just closed.
        if (!failure || failure.status !== 404) throw failure;
      });
      answer = await reopen();
    }
    if (!answer || !answer.window) return null;
    const windowId = answer.window.id;
    // An answer that names the instance whose token was already spent (a focus on
    // the window this page saw stop) cannot mount a frame that would handshake.
    // The error and its reason stay until a fresh instance runs or a close
    // settles the window (KOSD-C-75, DASH-C-74).
    if (stoppedWindows.has(windowId) && (!answer.instance || answer.instance.hasToken)) {
      syncWindows();
      syncAddress();
      return answer;
    }
    // A fresh instance runs, so an error this page kept for the window is over.
    if (answer.instance) stoppedWindows.delete(windowId);
    layer.mount(answer, app || findApp(state.apps, answer.window.application));
    startWatch();
    syncWindows();
    syncAddress();
    return answer;
  }

  // closeWindow requests one close. While the application reports unsaved
  // changes the front vetoes the first request and the window stays; a later
  // request destroys it and its frame (DASH-C-74).
  async function closeWindow(windowId) {
    if (!layer || !windowId) return;
    try {
      const result = await windows.close(windowId);
      if (result.vetoed) {
        state.surfaceError = closeVetoed();
      } else {
        state.surfaceError = null;
        stoppedWindows.delete(windowId);
        syncAddress();
      }
      syncWindows();
    } catch (failure) {
      if (failure && failure.status === 401) forgetSession();
      else state.surfaceError = failure;
    }
    render();
  }

  // closeAppWindows closes every window one application owns. A window whose
  // close the application vetoes stays where it is, including an error this page
  // kept for it (DASH-C-74).
  async function closeAppWindows(appId) {
    if (!layer || !appId) return;
    state.dockMenu = null;
    let vetoed = false;
    try {
      for (const window of windowSet(windows.current()).filter((entry) => entry.application === appId)) {
        const result = await windows.close(window.id);
        if (result.vetoed) {
          vetoed = true;
          continue;
        }
        stoppedWindows.delete(window.id);
      }
      syncWindows();
      syncAddress();
      state.surfaceError = vetoed ? closeVetoed() : null;
    } catch (failure) {
      if (failure && failure.status === 401) forgetSession();
      else state.surfaceError = failure;
    }
    render();
  }

  // closeVetoed is the answer a vetoed close shows: the window stays because the
  // application has unsaved changes (DASH-C-74).
  function closeVetoed() {
    return new Failure(409, {
      error: { code: "close_vetoed", message: "应用有未保存的更改，关闭已被否决", stage: "authorize" },
    });
  }

  // focusWindow raises one window and moves the address with it (DASH-C-74). A
  // minimized window comes back when its taskbar entry is chosen: its frame was
  // never destroyed, so restoring shows the page state it kept.
  async function focusWindow(windowId) {
    if (!layer || !windowId) return;
    try {
      // A minimized window cannot hold the focus; restoring it brings it back
      // and focuses it in one step, so the address never points at a hidden
      // window (DASH-C-74).
      if (windowById(windows.current(), windowId)?.state === "minimized") await windows.restore(windowId);
      else await windows.focus(windowId);
      syncWindows();
      syncAddress();
      state.surfaceError = null;
    } catch (failure) {
      if (failure && failure.status === 401) forgetSession();
      else state.surfaceError = failure;
    }
    render();
  }

  // transition applies one state change DASH-C-74 allows. Snapping fills half of
  // the viewport the browser reports.
  async function transition(windowId, command, edge = "") {
    if (!layer || !windowId) return;
    try {
      if (command === "window-minimize") await windows.minimize(windowId);
      else if (command === "window-maximize") await windows.maximize(windowId);
      else if (command === "window-fullscreen") await windows.fullscreen(windowId);
      else if (command === "window-snap") await windows.snap(windowId, edge || "left", viewport());
      else await windows.restore(windowId);
      syncWindows();
      syncAddress();
      state.surfaceError = null;
    } catch (failure) {
      if (failure && failure.status === 401) forgetSession();
      else state.surfaceError = failure;
    }
    render();
  }

  // restartWindow recovers an error window. The front keeps the instance key and
  // answers a fresh application instance, whose frame replaces the destroyed one
  // (DASH-C-74). A window whose crash report the front never took is reported again
  // first, so the front and this page agree before the restart.
  async function restartWindow(windowId) {
    if (!layer || !windowId) return;
    try {
      const stopped = stoppedWindows.get(windowId);
      if (stopped) {
        try {
          await windows.crash(windowId, stopped);
        } catch (failure) {
          // 409: the front already holds the window in its error state, which is
          // what the restart needs. Any other refusal keeps the error here.
          if (!failure || failure.status !== 409) throw failure;
        }
      }
      const answer = await windows.restart(windowId);
      // The frame is running again, so the error this page kept is over.
      stoppedWindows.delete(windowId);
      const application = answer && answer.window ? answer.window.application : "";
      layer.mount(answer, findApp(state.apps, application));
      startWatch();
      syncWindows();
      syncAddress();
      state.surfaceError = null;
    } catch (failure) {
      if (failure && failure.status === 401) forgetSession();
      else state.surfaceError = failure;
    }
    render();
  }

  // crashWindow reports a frame the layer could not keep running, so the window
  // enters its error state and offers restart and close (DASH-C-74). The report is
  // retried once; when the front still does not take it the window keeps that
  // error here, because the frame it lost is not coming back.
  async function crashWindow(windowId, reason) {
    if (!windowId) return;
    const why = reason || "应用崩溃";
    let refused = null;
    for (let attempt = 0; attempt < 2; attempt += 1) {
      try {
        await windows.crash(windowId, why);
        refused = null;
        break;
      } catch (failure) {
        if (failure && failure.status === 401) {
          forgetSession();
          render();
          return;
        }
        refused = failure;
        // 409 says the front already holds the window stopped. Read its answer
        // rather than keeping a second copy of what it settled.
        if (failure && failure.status === 409) {
          await windows.load().catch(() => {});
          if (windowById(windows.current(), windowId)?.state === "error") refused = null;
          break;
        }
      }
    }
    if (refused) {
      // The front never took the report. The frame is gone either way, so this
      // page keeps the window in its error state and the way back until a restart
      // or a close settles it (DASH-C-74).
      stoppedWindows.set(windowId, why);
      if (refused.status !== 409) state.surfaceError = refused;
    } else {
      stoppedWindows.delete(windowId);
    }
    syncWindows();
    syncAddress();
    render();
  }

  // createWorkspace makes a fresh workspace current. The windows already open
  // stay where they are; the new workspace is empty until a window joins it
  // (DASH-C-74).
  async function createWorkspace() {
    const taken = new Set(state.windowSession.workspaces || []);
    let index = taken.size + 1;
    while (taken.has(`工作区 ${index}`)) index += 1;
    return switchWorkspace(`工作区 ${index}`);
  }

  // moveWindow groups one window into another workspace; the taskbar lists only
  // the current workspace, so the window leaves the bar until the user switches
  // to its workspace (DASH-C-74).
  async function moveWindow(windowId, workspace) {
    if (!layer || !windowId || !workspace) return;
    try {
      await windows.moveToWorkspace(windowId, workspace);
      syncWindows();
      syncAddress();
      state.surfaceError = null;
    } catch (failure) {
      if (failure && failure.status === 401) forgetSession();
      else state.surfaceError = failure;
    }
    render();
  }

  // switchWorkspace changes which workspace the taskbar lists; the windows
  // themselves stay open where they are (DASH-C-74).
  async function switchWorkspace(workspace) {
    if (!workspace) return undefined;
    try {
      await windows.switchWorkspace(workspace);
      syncWindows();
      syncAddress();
      state.surfaceError = null;
    } catch (failure) {
      if (failure && failure.status === 401) forgetSession();
      else state.surfaceError = failure;
    }
    render();
    return undefined;
  }

  // addressChanged follows a back or forward step: the window showing the
  // address the browser returned to takes the focus, so the address and the open
  // set agree (DASH-C-74).
  async function addressChanged(pathname = "") {
    const target = windowSet(windows.current()).find((window) => window.address && window.address === pathname);
    if (!target) return;
    await focusWindow(target.id);
  }

  // viewport is the browser rectangle a snapped window fills half of.
  function viewport() {
    return {
      x: 0,
      y: 0,
      width: Number(hostWindow && hostWindow.innerWidth) || 0,
      height: Number(hostWindow && hostWindow.innerHeight) || 0,
    };
  }

  // setPinned stores one application in the dock layout. The layout belongs to
  // the account, so the write goes through the preference domain (DASH-C-27).
  function setPinned(appId, isPinned) {
    if (!state.dock) return undefined;
    return writeDock(withPinned(dockLayoutOf(state.dock), appId, isPinned));
  }

  function setDockPosition(position) {
    if (!state.dock) return undefined;
    return writeDock(withDockPosition(dockLayoutOf(state.dock), position));
  }

  function setAutoHide(autoHide) {
    if (!state.dock) return undefined;
    return writeDock(withAutoHide(dockLayoutOf(state.dock), autoHide));
  }

  function setLauncherSort(sort) {
    if (!state.launcher) return undefined;
    return writeLauncher(withSort(launcherLayoutOf(state.launcher), sort));
  }

  // writeDock stores one dock layout and answers the front's read of it; a
  // browser copy is never created.
  async function writeDock(layout) {
    state.dockMenu = null;
    try {
      state.dock = await saveDock(api, layout);
      state.surfaceError = null;
    } catch (failure) {
      if (failure && failure.status === 401) forgetSession();
      else state.surfaceError = failure;
    }
    render();
  }

  // writeLauncher stores one launcher layout and answers the front's read.
  async function writeLauncher(layout) {
    try {
      state.launcher = await saveLauncher(api, layout);
      state.surfaceError = null;
    } catch (failure) {
      if (failure && failure.status === 401) forgetSession();
      else state.surfaceError = failure;
    }
    render();
  }

  // searchLauncher filters the launcher list; the term is interaction state and
  // never enters a preference (DASH-C-27). The input keeps focus across the
  // re-render a keystroke causes.
  function searchLauncher(query) {
    state.launcherQuery = String(query ?? "");
    render();
    if (!state.launcherOpen || typeof root.querySelector !== "function") return;
    const input = root.querySelector("[data-launcher-search]");
    if (!input) return;
    if (typeof input.focus === "function") input.focus();
    if (typeof input.setSelectionRange === "function") {
      const end = String(input.value ?? "").length;
      input.setSelectionRange(end, end);
    }
  }

  // closeOverlays closes the launcher and the dock menu.
  function closeOverlays() {
    if (!state.launcherOpen && !state.dockMenu) return;
    state.launcherOpen = false;
    state.dockMenu = null;
    render();
  }

  // submitAction submits one action the page declares. The entry, not the
  // shell, decides the write (DASH-C-22); the answer is the acceptance and the
  // object it references, and the page is re-read so progress and the terminal
  // state come from that object's status and conditions (KOSD-C-12).
  async function submitAction(actionId, target = {}) {
    const page = state.page;
    if (!page) return;
    if (state.pending && state.pending.pageId === page.id) return;
    if (page.readOnly) {
      state.error = new Failure(400, {
        error: { code: "action_unavailable", message: "只读页面不提供写操作", stage: "validate" },
      });
      render();
      return;
    }
    const action = (page.actions || []).find((candidate) => candidate.id === actionId);
    if (!action || !action.entry) {
      state.error = new Failure(400, {
        error: { code: "action_unavailable", message: "页面没有声明该动作", stage: "validate" },
      });
      render();
      return;
    }
    let path = "";
    try {
      path = fillPath(action.entry.path, target);
    } catch (failure) {
      state.error = failure;
      render();
      return;
    }
    state.error = null;
    state.answer = null;
    // The ticket names the page it belongs to: a navigation during the write
    // neither inherits the busy state nor loses the answer it produced.
    const ticket = { pageId: page.id, actionId };
    state.pending = ticket;
    render();
    let answer = null;
    try {
      // The entry's body travels verbatim: the page declares the write,
      // including the watermark a mark-read carries (DASH-C-22, DASH-C-27).
      answer = await api.write(action.entry.method, path, action.entry.body);
    } catch (failure) {
      if (state.pending === ticket) state.pending = null;
      // A refusal belongs to the page that submitted it: a navigation during
      // the write must not show the old page's failure above the new one.
      if (failure && failure.status === 401) forgetSession();
      else if (state.pageId === page.id) state.error = failure;
      render();
      return;
    }
    if (state.pending === ticket) state.pending = null;
    if (state.pageId !== page.id) {
      render();
      return;
    }
    if (answer && answer.accepted) {
      state.answer = { actionId, idempotent: Boolean(answer.idempotent), ref: answer.ref || null };
    }
    await refresh(page.id);
  }

  // retry repeats the step that failed: the session, the surfaces or the page.
  function retry() {
    if (!state.session) return start();
    if (!state.pageId) return refreshSurfaces();
    return navigate(state.pageId);
  }

  // handleClick maps one click to an action: a navigation entry, a page action,
  // a launch, a surface command or a shell command. It returns the promise the
  // action settles with.
  function handleClick(event) {
    const navigation = closest(event.target, "data-nav");
    if (navigation) {
      return navigate(navigation.getAttribute("data-nav"));
    }
    const action = closest(event.target, "data-action-id");
    if (action) {
      return submitAction(action.getAttribute("data-action-id"), {
        name: action.getAttribute("data-name") || "",
        namespace: action.getAttribute("data-namespace") || "",
      });
    }
    const opener = closest(event.target, "data-open-app");
    if (opener) {
      return launch(opener.getAttribute("data-open-app"));
    }
    const command = closest(event.target, "data-command");
    if (command) {
      return runCommand(command);
    }
    const windowNode = closest(event.target, "data-window");
    if (windowNode && layer) {
      return focusWindow(windowNode.getAttribute("data-window"));
    }
    const named = closest(event.target, "data-action");
    if (!named) return undefined;
    switch (named.getAttribute("data-action")) {
      case "logout":
        return logout();
      case "retry":
        return retry();
      case "launcher":
        state.launcherOpen = !state.launcherOpen;
        state.launcherQuery = "";
        render();
        return undefined;
      case "launcher-close":
        state.launcherOpen = false;
        render();
        return undefined;
      default:
        return undefined;
    }
  }

  // runCommand runs one dock or launcher command. Every command maps either to
  // the preference domain or to a window operation (DASH-C-27, DASH-C-30).
  function runCommand(element) {
    const name = element.getAttribute("data-command");
    const appId = element.getAttribute("data-app");
    switch (name) {
      case "menu-close":
        state.dockMenu = null;
        render();
        return undefined;
      case "open":
        return launch(appId);
      case "pin":
        return setPinned(appId, true);
      case "unpin":
        return setPinned(appId, false);
      case "position":
        return setDockPosition(element.getAttribute("data-position"));
      case "autohide":
        return setAutoHide(!(state.dock && state.dock.autoHide));
      case "close-windows":
        return closeAppWindows(appId);
      case "launcher-sort":
        return setLauncherSort(element.getAttribute("data-sort"));
      case "window-close":
        return closeWindow(element.getAttribute("data-window"));
      case "window-minimize":
      case "window-maximize":
      case "window-fullscreen":
      case "window-restore":
        return transition(element.getAttribute("data-window"), name);
      case "window-snap":
        return transition(element.getAttribute("data-window"), name, element.getAttribute("data-edge"));
      case "window-focus":
        return focusWindow(element.getAttribute("data-window"));
      case "window-restart":
        return restartWindow(element.getAttribute("data-window"));
      case "workspace":
        return switchWorkspace(element.getAttribute("data-workspace"));
      case "workspace-create":
        return createWorkspace();
      default:
        return undefined;
    }
  }

  // handleChange maps one select to its command: moving a window into another
  // workspace is the only choice the desktop offers as a list (DASH-C-74).
  function handleChange(event) {
    const control = closest(event.target, "data-window-workspace");
    if (!control) return undefined;
    const workspace = event.target && typeof event.target.value === "string" ? event.target.value : "";
    return moveWindow(control.getAttribute("data-window-workspace"), workspace);
  }

  // handleContextMenu opens the dock menu for one entry or for the dock itself.
  function handleContextMenu(event) {
    const item = closest(event.target, "data-dock-app");
    if (item) {
      if (typeof event.preventDefault === "function") event.preventDefault();
      state.dockMenu = { kind: "app", appId: item.getAttribute("data-dock-app") };
      render();
      return true;
    }
    const dock = closest(event.target, "data-dock");
    if (dock) {
      if (typeof event.preventDefault === "function") event.preventDefault();
      state.dockMenu = { kind: "dock" };
      render();
      return true;
    }
    return false;
  }

  // openStream follows the event stream, so the notification centre shows what
  // the object route answers and says so when the read stops (KOSD-C-13). Read
  // state is a control-plane projection (DASH-C-27), so a delivered read
  // refreshes the page rather than being merged with a guessed flag.
  function openStream() {
    if (closeStream) return;
    closeStream = subscribeStream("/api/v1/stream", {
      EventSource,
      onEvents: () => {
        void refreshSurfaces();
      },
      onStale: (notice) => markNotificationsStale(notice),
    });
  }

  // refresh re-reads a page the shell already shows, without clearing it; a
  // page whose read is refused reports the refusal and drops the projection it
  // can no longer show.
  async function refresh(pageId) {
    if (!state.session || state.pageId !== pageId) return;
    try {
      const page = await readPage(pageId);
      if (state.pageId !== pageId) return;
      state.page = page;
      state.error = null;
    } catch (failure) {
      if (failure && failure.status === 401) {
        forgetSession();
      } else {
        state.error = failure;
        if (failure && failure.code === "page_not_found") state.page = null;
      }
    }
    render();
  }

  // markNotificationsStale keeps the last observation and says why it may be
  // old, instead of showing the history as empty (KOSD-C-64).
  function markNotificationsStale(notice) {
    const page = state.page;
    if (!page || page.id !== DESKTOP_PAGE_ID) return;
    const reason = notice && notice.reason ? notice.reason : "事件流已断开";
    state.page = {
      ...page,
      items: (page.items || []).map((item) =>
        item.id === NOTIFICATIONS_ITEM ? { ...item, stale: true, reason } : item,
      ),
    };
    render();
  }

  return {
    start,
    navigate,
    login,
    logout,
    submitAction,
    retry,
    handleClick,
    handleChange,
    handleContextMenu,
    launch,
    closeWindow,
    addressChanged,
    searchLauncher,
    closeOverlays,
    state,
  };
}

// originOf reads the origin of one document address; an address that does not
// parse names no console origin.
function originOf(location) {
  if (!location || typeof location.origin !== "string") return "";
  return location.origin;
}

// deepLinkPathOf names the application path the address carries. The shell's
// own routes and static files are not deep links; every other document path is
// offered to the installed applications' declarations (DASH-C-30).
export function deepLinkPathOf(location) {
  const pathname = location && typeof location.pathname === "string" ? location.pathname : "";
  if (pathname === "" || pathname === "/" || pathname === "/index.html") return "";
  if (/\.[a-z0-9]+$/i.test(pathname)) return "";
  return pathname;
}

// fillPath fills the object reference an action declares. A path that still
// names a placeholder is refused rather than sent half-built.
export function fillPath(path, { name = "", namespace = "" } = {}) {
  const template = String(path);
  const empty = (template.includes("{name}") && name === "") || (template.includes("{namespace}") && namespace === "");
  const filled = template.replaceAll("{name}", name).replaceAll("{namespace}", namespace);
  if (empty || filled.includes("{")) {
    throw new Failure(400, {
      error: { code: "action_unresolved", message: "动作缺少对象引用，无法提交", stage: "validate" },
    });
  }
  return filled;
}

// closest resolves the nearest ancestor carrying one attribute.
function closest(node, attribute) {
  if (!node || typeof node.closest !== "function") return null;
  return node.closest(`[${attribute}]`);
}
