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

// NOTIFICATIONS_ITEM is the desktop item the event stream refreshes.
const NOTIFICATIONS_ITEM = "notifications";
const REGISTRY_PATH = "/api/v1/pages";
const APPS_PATH = "/api/v1/apps";
const WINDOWS_PATH = "/api/v1/windows";
const DEEP_LINKS_PATH = "/api/v1/deep-links";

// createApp wires the shell to one root element. Every dependency is injected
// so the behaviour can be exercised without a browser; the app host is absent
// when there is no window layer to mount into.
export function createApp({ api, root, storage, EventSource, host = null, location = null }) {
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
    recent: [],
    launcherOpen: false,
    launcherQuery: "",
    dockMenu: null,
    surfaceError: null,
  };
  let closeStream = null;
  let isReadingSurfaces = false;

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

  // openDeepLink opens the application that declared the address the browser
  // loaded. A path no installed application declares resolves to nothing, so no
  // window appears (DASH-C-30).
  async function openDeepLink() {
    const path = deepLinkPathOf(location);
    if (path === "") return;
    let answer = null;
    try {
      answer = await api.get(`${DEEP_LINKS_PATH}?path=${encodeURIComponent(path)}`);
    } catch (failure) {
      if (failure.status === 401) forgetSession();
      return;
    }
    if (!answer || typeof answer.app !== "string" || answer.app === "") return;
    await launch(answer.app);
  }

  // navigate renders one listed page.
  async function navigate(pageId) {
    if (!state.session) return;
    state.pageId = pageId;
    state.error = null;
    state.page = null;
    if (!findPage(state.registry, pageId)) {
      state.error = new Failure(404, {
        error: { code: "page_not_found", message: "页面不存在或所属组件已停用", stage: "read" },
      });
      render();
      return;
    }
    render();
    try {
      const page = await api.get(sourceOf(pageId));
      state.page = withLocalSources(page);
    } catch (failure) {
      state.page = null;
      state.error = failure;
      if (failure.status === 401) forgetSession();
    }
    render();
  }

  // forgetSession drops everything the ended session owned — the stream, the
  // windows the browser mounted and the surfaces read for it — so the shell
  // returns to the sign-in form clean.
  function forgetSession() {
    if (closeStream) {
      closeStream();
      closeStream = null;
    }
    if (host) host.closeAll();
    state.session = null;
    state.registry = { items: [] };
    state.pageId = "";
    state.page = null;
    state.apps = [];
    state.refusals = [];
    state.dock = null;
    state.launcher = null;
    state.windows = [];
    state.launcherOpen = false;
    state.launcherQuery = "";
    state.dockMenu = null;
    state.surfaceError = null;
  }

  // loadSurfaces reads the installed applications, both layouts and the
  // account's windows. A surface that fails keeps what the shell already shows
  // and reports the refusal without replacing the current page.
  async function loadSurfaces() {
    if (isReadingSurfaces) return;
    isReadingSurfaces = true;
    try {
      const [appsRead, dockRead, launcherRead, windowsRead] = await Promise.allSettled([
        api.get(APPS_PATH),
        readDock(api),
        readLauncher(api),
        api.get(WINDOWS_PATH),
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
        const serverWindows = windowsOf(windowsRead.value);
        if (host) {
          // Windows are this browser session's state (DASH-C-74): the server
          // list settles the frames already mounted, and is not used as an
          // open set of its own.
          host.reconcile({ apps: state.apps, windows: serverWindows });
          state.windows = host.records();
        } else {
          state.windows = serverWindows;
        }
      } else {
        absorbSurfaceFailure(windowsRead.reason);
      }
    } finally {
      isReadingSurfaces = false;
    }
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

  // refreshSurfaces re-reads the surfaces after the event stream reports a
  // change, so an uninstalled application leaves the dock and the launcher and
  // its open window enters the error state (DASH-C-30).
  async function refreshSurfaces() {
    if (!state.session) return;
    await loadSurfaces();
    if (!state.session) {
      render();
      return;
    }
    if (state.pageId === DESKTOP_PAGE_ID) await refresh(DESKTOP_PAGE_ID);
    else render();
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

  // launch opens one application from the launcher or the dock. A single-mode
  // application answers its open instance, which is focused instead of mounted
  // again (DASH-C-30); the recent list records the open locally (KOSD-C-53).
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
    if (!host) {
      render();
      return;
    }
    try {
      const answer = await host.open(app);
      host.mount(answer, app);
      state.windows = host.records();
      recordRecent(storage, { id: app.id, name: app.name, openedAt: new Date().toISOString() });
      state.surfaceError = null;
    } catch (failure) {
      if (failure && failure.status === 401) forgetSession();
      else state.surfaceError = failure;
    }
    render();
  }

  // closeWindow ends one application window; its token is revoked with it
  // (KOSD-C-75).
  async function closeWindow(windowId) {
    if (!host || !windowId) return;
    try {
      await host.close(windowId);
      state.windows = host.records();
      state.surfaceError = null;
    } catch (failure) {
      if (failure && failure.status === 401) forgetSession();
      else state.surfaceError = failure;
    }
    render();
  }

  // closeAppWindows closes every window one application owns.
  async function closeAppWindows(appId) {
    if (!host || !appId) return;
    state.dockMenu = null;
    try {
      await host.closeForApp(appId);
      state.windows = host.records();
      state.surfaceError = null;
    } catch (failure) {
      if (failure && failure.status === 401) forgetSession();
      else state.surfaceError = failure;
    }
    render();
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
  // shell, decides the write (DASH-C-22).
  async function submitAction(actionId, target = {}) {
    const page = state.page;
    if (!page) return;
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
    try {
      // The entry's body travels verbatim: the page declares the write,
      // including the watermark a mark-read carries (DASH-C-22, DASH-C-27).
      await api.write(action.entry.method, path, action.entry.body);
    } catch (failure) {
      state.error = failure;
      render();
      return;
    }
    await navigate(page.id);
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
    if (windowNode && host) {
      host.focus(windowNode.getAttribute("data-window"));
      return undefined;
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
      default:
        return undefined;
    }
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

  // refresh re-reads a page the shell already shows, without clearing it.
  async function refresh(pageId) {
    if (!state.session || state.pageId !== pageId) return;
    try {
      const page = await api.get(sourceOf(pageId));
      if (state.pageId === pageId) {
        state.page = withLocalSources(page);
        state.error = null;
      }
    } catch (failure) {
      state.error = failure;
      if (failure.status === 401) forgetSession();
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
    handleContextMenu,
    launch,
    closeWindow,
    searchLauncher,
    closeOverlays,
    state,
  };
}

// windowsOf returns the window instances an answer carries.
function windowsOf(answer) {
  const items = answer && Array.isArray(answer.items) ? answer.items : [];
  return items.filter((entry) => entry && typeof entry.id === "string");
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
  const filled = String(path).replaceAll("{name}", name).replaceAll("{namespace}", namespace);
  if (filled.includes("{")) {
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
