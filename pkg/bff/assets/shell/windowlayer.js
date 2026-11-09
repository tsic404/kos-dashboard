// The window layer: the document surface the shell's re-renders never replace,
// so a mounted frame keeps its document (KOSD-C-75). One node per desktop window
// (DASH-C-74) carrying the state, geometry, z-order and focus the session
// answered; the frame lives in the node's body so a state change never reloads
// it.
//
// A page load restores no open set: this layer mounts only the windows this page
// opened and holds a frame for.

import { escapeHtml } from "./html.js";
import { renderWindowChrome, renderWindowErrorBody } from "./windowbar.js";
import { windowsOf } from "./windows.js";

// createWindowLayer binds the window nodes to one container and one frame host.
export function createWindowLayer({ document: doc, container, host }) {
  const records = new Map();
  let workspaces = [];

  // mount renders one window the front opened and attaches its frame. An answer
  // that carries no frame (a focus on an existing window) keeps the node it
  // already shows.
  function mount(answer, app = null) {
    const window = answer && answer.window;
    if (!window || typeof window.id !== "string") return null;
    let record = records.get(window.id);
    if (!record) {
      record = create(window, app);
      records.set(window.id, record);
    }
    if (app) record.app = app;
    if (Array.isArray(answer.workspaces)) workspaces = answer.workspaces;
    if (answer.instance && window.state !== "error") {
      // A window that recovered from its error state has the error text in its
      // body; the fresh frame replaces it (DASH-C-74).
      if (record.state === "error") record.body.innerHTML = "";
      host.mount(
        {
          windowId: window.id,
          instance: answer.instance,
          frame: answer.frame,
          frameError: answer.frameError,
          sandbox: answer.sandbox,
          name: titleOf(record.app, window),
        },
        record.body,
      );
    }
    apply(record, window);
    return record;
  }

  // sync settles every mounted node against the session's answer: a window the
  // session no longer holds is gone, and every other window takes the state,
  // the geometry and the focus the front answered. It answers the windows this
  // page mounted, in the session's order.
  function sync(session) {
    const windows = windowsOf(session);
    if (Array.isArray(session && session.workspaces)) workspaces = session.workspaces;
    const byID = new Map(windows.map((window) => [window.id, window]));
    for (const windowId of [...records.keys()]) {
      const window = byID.get(windowId);
      if (!window) {
        destroy(windowId);
        continue;
      }
      apply(records.get(windowId), window);
    }
    return windows.filter((window) => records.has(window.id));
  }

  // destroy drops one window and its frame.
  function destroy(windowId) {
    const record = records.get(windowId);
    if (!record) return;
    host.unmount(windowId);
    if (typeof container.removeChild === "function") container.removeChild(record.node);
    records.delete(windowId);
  }

  // closeAll drops every window and frame, which is what a sign-out does.
  function closeAll() {
    for (const windowId of [...records.keys()]) destroy(windowId);
  }

  // holds reports whether this page mounted one window.
  function holds(windowId) {
    return records.has(windowId);
  }

  // ids lists the windows this page mounted.
  function ids() {
    return [...records.keys()];
  }

  return { mount, sync, destroy, closeAll, holds, ids };

  // create builds one window node: its chrome, and the body the frame mounts in.
  function create(window, app) {
    const node = doc.createElement("section");
    node.className = "window";
    node.setAttribute("data-window", window.id);
    const bar = doc.createElement("header");
    bar.className = "window-bar";
    const body = doc.createElement("div");
    body.className = "window-body";
    node.appendChild(bar);
    node.appendChild(body);
    container.appendChild(node);
    return { node, bar, body, app };
  }

  // apply writes one session window onto its node. A frame state never renders
  // the frame: an error window offers restart and close instead of a frame
  // (DASH-C-74), and a window that recovered from one mounts a fresh frame
  // before this runs.
  function apply(record, window) {
    const { node, bar, body } = record;
    const title = titleOf(record.app, window);
    record.state = window.state;
    node.setAttribute("data-app", window.application);
    node.setAttribute("data-state", window.state);
    node.setAttribute("data-focused", String(Boolean(window.isFocused)));
    node.setAttribute("data-workspace", window.workspace || "");
    node.setAttribute("data-instance", String(window.instance ?? ""));
    const bounds = window.bounds || {};
    node.style.setProperty("--window-x", `${Number(bounds.x) || 0}px`);
    node.style.setProperty("--window-y", `${Number(bounds.y) || 0}px`);
    node.style.setProperty("--window-width", `${Number(bounds.width) || 0}px`);
    node.style.setProperty("--window-height", `${Number(bounds.height) || 0}px`);
    node.style.setProperty("--window-z", String(Number(window.z) || 1));
    if (window.state === "error") {
      host.unmount(window.id);
      bar.innerHTML = `<span class="window-title">${escapeHtml(title)}</span>`;
      body.innerHTML = renderWindowErrorBody(window);
      return;
    }
    bar.innerHTML = renderWindowChrome(window, record.app, workspaces);
  }
}

// titleOf names one window: the application's declared name, or its identity
// when the manifest is gone.
function titleOf(app, window) {
  if (app && typeof app.name === "string" && app.name !== "") return app.name;
  return window.application;
}
