// The shell: it signs in, renders the pages the registry lists, and keeps the
// desktop page current from the event stream. Visibility is never decided
// here — the registry answer already carries only the pages this session may
// see (DASH-C-22, KOSD-C-59).

import { Failure } from "./api.js";
import { DESKTOP_PAGE_ID, findPage, firstPage, sourceOf } from "./registry.js";
import { readRecent } from "./recent.js";
import { renderBoot, renderLogin, renderShell } from "./render.js";
import { subscribeStream } from "./stream.js";

// NOTIFICATIONS_ITEM is the desktop item the event stream refreshes.
const NOTIFICATIONS_ITEM = "notifications";
const REGISTRY_PATH = "/api/v1/pages";

// createApp wires the shell to one root element. Every dependency is injected
// so the behaviour can be exercised without a browser.
export function createApp({ api, root, storage, EventSource }) {
  const state = {
    session: null,
    registry: { items: [] },
    requested: "",
    pageId: "",
    page: null,
    error: null,
  };
  let closeStream = null;

  function render() {
    if (!state.session) {
      root.innerHTML = renderLogin({ error: state.error });
      return;
    }
    root.innerHTML = renderShell(state);
  }

  // start resolves the session, then the page registry. requested names the
  // page the address asked for; a page the registry does not list falls back to
  // the default route.
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
    await openRegistry(state.requested);
  }

  // openRegistry reads the pages this session may see and opens one of them.
  // A page the registry omits has no entry and no route: that is how a
  // disabled component disappears (DASH-C-22).
  async function openRegistry(requested = "") {
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

  // forgetSession drops everything the ended session owned, including the
  // event stream, so the shell returns to the sign-in form clean.
  function forgetSession() {
    if (closeStream) {
      closeStream();
      closeStream = null;
    }
    state.session = null;
    state.registry = { items: [] };
    state.pageId = "";
    state.page = null;
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

  // retry repeats the step that failed: the session, the registry or the page.
  function retry() {
    if (!state.session) return start();
    if (!state.pageId) return openRegistry();
    return navigate(state.pageId);
  }

  // handleClick maps one click to an action: a navigation entry, a page action
  // or a shell command. It returns the promise the action settles with.
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
    const command = closest(event.target, "data-action");
    if (!command) return undefined;
    const name = command.getAttribute("data-action");
    if (name === "logout") return logout();
    if (name === "retry") return retry();
    return undefined;
  }

  // openStream follows the event stream, so the notification centre shows what
  // the object route answers and says so when the read stops (KOSD-C-13). Read
  // state is a control-plane projection (DASH-C-27), so a delivered read
  // refreshes the page rather than being merged with a guessed flag.
  function openStream() {
    if (closeStream) return;
    closeStream = subscribeStream("/api/v1/stream", {
      EventSource,
      onEvents: () => refresh(DESKTOP_PAGE_ID),
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

  return { start, navigate, login, logout, submitAction, retry, handleClick, state };
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
