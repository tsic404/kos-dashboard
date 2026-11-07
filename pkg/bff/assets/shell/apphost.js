// The console side of an application window (KOSD-C-75, KOSD-C-73): it opens an
// instance, mounts the signed frame address with the sandbox the front declared,
// and answers the frame's `ready` with the `init` payload. A `ready` the console
// cannot attribute to this frame — another origin, another window or another
// message shape — has no effect and delivers no token.

import { Failure } from "./api.js";
import { escapeHtml } from "./html.js";
import { appOrigin } from "./apps.js";

// The message names the handshake uses (KOSD-C-75).
export const READY_MESSAGE = "ready";
export const INIT_MESSAGE = "init";

// MAX_TIMEOUT_MS is the largest delay setTimeout honours; a longer wait is
// re-armed.
const MAX_TIMEOUT_MS = 2147483647;

// createAppHost binds the window layer to one document and one clock. Every
// dependency is injected so the handshake can be exercised without a browser.
export function createAppHost({ api, document: doc, container, window: hostWindow, consoleOrigin = "" }) {
  const records = new Map();
  let zOrder = 0;

  if (hostWindow && typeof hostWindow.addEventListener === "function") {
    hostWindow.addEventListener("message", handleMessage);
  }

  // open registers one window instance through the front. The token is not
  // part of this answer; the handshake delivers it once (KOSD-C-75).
  async function open(app) {
    const answer = await api.write("POST", `/api/v1/apps/${encodeURIComponent(app.id)}/windows`);
    if (!answer || !answer.window || typeof answer.window.id !== "string") {
      throw new Failure(502, {
        error: { code: "invalid_response", message: "窗口打开应答缺少实例", stage: "read" },
      });
    }
    return answer;
  }

  // mount attaches one window to the layer. A window with a frame gets an
  // iframe carrying exactly the sandbox the front declared; anything less fails
  // closed and shows the error state (KOSD-C-75).
  function mount(answer, app) {
    const instance = answer.window;
    const existing = records.get(instance.id);
    if (existing) {
      focus(instance.id);
      return existing;
    }
    const record = {
      id: instance.id,
      appId: app.id,
      name: app.name,
      origin: instance.origin || appOrigin(app),
      nonce: instance.handshakeNonce || "",
      state: instance.state || "opening",
      node: null,
      body: null,
      frame: null,
      delivered: false,
      timer: null,
    };
    record.node = doc.createElement("section");
    record.node.className = "window";
    record.node.setAttribute("data-window", record.id);
    record.node.setAttribute("data-app", record.appId);
    record.node.setAttribute("data-state", record.state);
    record.node.style.setProperty("--window-width", `${Number(instance.defaultWidth) || 640}px`);
    record.node.style.setProperty("--window-height", `${Number(instance.defaultHeight) || 480}px`);
    record.node.style.setProperty("--window-offset", `${records.size * 24}px`);
    record.node.innerHTML = `<header class="window-bar"><span class="window-title">${escapeHtml(app.name)}</span><button type="button" class="window-close" data-command="window-close" data-window="${escapeHtml(record.id)}">关闭</button></header>`;
    record.body = doc.createElement("div");
    record.body.className = "window-body";
    record.node.appendChild(record.body);
    records.set(record.id, record);
    container.appendChild(record.node);
    focus(record.id);

    if (!record.origin) {
      enterError(record.id, "应用入口不是绝对地址，无法建立握手");
      return record;
    }
    if (!answer.frame) {
      enterError(record.id, answer.frameError || "应用没有可加载的资产地址");
      return record;
    }
    if (typeof answer.sandbox !== "string" || answer.sandbox.trim() === "") {
      enterError(record.id, "缺少 iframe 沙箱约束");
      return record;
    }
    if (originOf(answer.frame) !== record.origin) {
      enterError(record.id, "资产地址不在应用 origin 上，拒绝加载");
      return record;
    }
    const frame = doc.createElement("iframe");
    frame.className = "window-frame";
    frame.setAttribute("sandbox", answer.sandbox);
    frame.setAttribute("src", frameSource(answer.frame, record, consoleOrigin));
    frame.setAttribute("title", app.name);
    frame.setAttribute("referrerpolicy", "no-referrer");
    record.frame = frame;
    record.body.appendChild(frame);
    return record;
  }

  // handleMessage judges one message against the frames this host mounted. A
  // message from another origin, another source or of another shape is ignored
  // (KOSD-C-75).
  function handleMessage(event) {
    const record = sourceRecord(event && event.source);
    if (!record || record.state === "error") return;
    if (!event.origin || event.origin !== record.origin) return;
    const ready = readReady(event.data);
    if (!ready) return;
    if (ready.window && ready.window !== record.id) return;
    if (record.delivered) return;
    record.delivered = true;
    api
      .write("POST", `/api/v1/windows/${encodeURIComponent(record.id)}/handshake`, {
        origin: event.origin,
        nonce: record.nonce,
        message: encodeMessage(ready),
      })
      .then((init) => {
        if (!isInit(init)) {
          enterError(record.id, "握手应答不是合法的 init 消息");
          return;
        }
        if (record.state === "error" || !record.frame) return;
        // The target origin is the application origin, never a wildcard
        // (KOSD-C-75); the frame validates the answer the same way.
        record.frame.contentWindow.postMessage(init, record.origin);
        record.body.setAttribute("data-handshaked", "true");
        scheduleExpiry(record, init.expiresAt);
      })
      .catch((failure) => enterError(record.id, failure && failure.message ? failure.message : "握手被拒绝"));
  }

  // scheduleExpiry ends the window when its token expires; the application must
  // be reopened, so the frame is destroyed rather than left running
  // (KOSD-C-75). A wait beyond the setTimeout range re-arms instead of firing
  // at once.
  function scheduleExpiry(record, expiresAt) {
    const deadline = Date.parse(expiresAt);
    if (Number.isNaN(deadline)) return;
    const remaining = deadline - Date.now();
    if (remaining <= 0) {
      enterError(record.id, "应用令牌已过期，请重新打开窗口");
      return;
    }
    record.timer = setTimeout(() => {
      if (Date.now() < deadline) scheduleExpiry(record, expiresAt);
      else enterError(record.id, "应用令牌已过期，请重新打开窗口");
    }, Math.min(remaining, MAX_TIMEOUT_MS));
    if (record.timer && typeof record.timer.unref === "function") record.timer.unref();
  }

  // enterError moves one window into its error state and destroys the frame:
  // the token is revoked, the window cannot be reopened, and no other window is
  // touched (DASH-C-30, KOSD-C-75).
  function enterError(windowId, reason) {
    const record = records.get(windowId);
    if (!record) return;
    record.state = "error";
    record.delivered = false;
    if (record.timer) {
      clearTimeout(record.timer);
      record.timer = null;
    }
    if (record.frame && typeof record.body.removeChild === "function") {
      record.body.removeChild(record.frame);
    }
    record.frame = null;
    record.node.setAttribute("data-state", "error");
    record.node.setAttribute("data-error", reason);
    record.body.innerHTML = `<p class="window-error" data-window-error="true">${escapeHtml(reason)}</p>`;
  }

  // reconcile settles this browser's windows against the server's list and the
  // installed applications: a window whose application is gone enters the error
  // state, while every other application's window stays untouched
  // (DASH-C-30).
  function reconcile({ apps, windows }) {
    const installed = new Set((Array.isArray(apps) ? apps : []).map((app) => app.id));
    const server = new Map((Array.isArray(windows) ? windows : []).map((entry) => [entry.id, entry]));
    for (const record of [...records.values()]) {
      const instance = server.get(record.id);
      if (!instance) {
        destroy(record.id);
        continue;
      }
      // An error state is terminal for this window: only closing it and
      // opening a new instance replaces it (DASH-C-30).
      if (record.state === "error") continue;
      if (instance.state === "error" || !installed.has(record.appId)) {
        enterError(record.id, instance.state === "error" ? "应用已卸载，窗口已进入错误态" : "应用已卸载");
        continue;
      }
      if (record.state !== instance.state) {
        record.state = instance.state;
        record.node.setAttribute("data-state", instance.state);
      }
    }
  }

  // focus brings one window to the front. A single-mode reopen focuses the
  // instance it already has instead of creating a second one (DASH-C-30).
  function focus(windowId) {
    const record = records.get(windowId);
    if (!record) return false;
    for (const other of records.values()) {
      other.node.setAttribute("data-focused", String(other.id === windowId));
    }
    zOrder += 1;
    record.node.style.setProperty("--window-z", String(zOrder));
    return true;
  }

  // close ends one window locally and on the server, which revokes its token
  // (KOSD-C-75).
  async function close(windowId) {
    const record = records.get(windowId);
    if (!record) return;
    try {
      await api.write("DELETE", `/api/v1/windows/${encodeURIComponent(windowId)}`);
    } catch (failure) {
      // A window the front no longer knows is already gone; any other refusal
      // keeps the window on screen.
      if (!failure || failure.status !== 404) throw failure;
    }
    destroy(windowId);
  }

  // closeForApp closes every window one application owns.
  async function closeForApp(appId) {
    const targets = [...records.values()].filter((record) => record.appId === appId);
    await Promise.all(targets.map((record) => close(record.id)));
  }

  // destroy drops one window's frame and node without a server call.
  function destroy(windowId) {
    const record = records.get(windowId);
    if (!record) return;
    clearTimeout(record.timer);
    record.frame = null;
    if (record.node && typeof container.removeChild === "function") {
      container.removeChild(record.node);
    }
    records.delete(windowId);
  }

  // closeAll drops every window this browser mounted, which is what a sign-out
  // does; the server revokes the tokens on the same session end.
  function closeAll() {
    for (const id of [...records.keys()]) destroy(id);
  }

  function sourceRecord(source) {
    if (!source) return null;
    for (const record of records.values()) {
      if (record.frame && record.frame.contentWindow === source) return record;
    }
    return null;
  }

  function recordsOf() {
    return [...records.values()].map((record) => ({
      id: record.id,
      appId: record.appId,
      state: record.state,
      hasFrame: Boolean(record.frame),
      origin: record.origin,
    }));
  }

  return { open, mount, reconcile, focus, close, closeForApp, closeAll, handleMessage, records: recordsOf };
}

// readReady accepts exactly the ready message shape the front validates: an
// object naming the message and, optionally, its window (KOSD-C-75).
export function readReady(data) {
  if (!data || typeof data !== "object" || Array.isArray(data)) return null;
  if (data.type !== READY_MESSAGE) return null;
  if (data.window !== undefined && typeof data.window !== "string") return null;
  return data.window === undefined ? { type: READY_MESSAGE } : { type: READY_MESSAGE, window: data.window };
}

// isInit accepts the init payload the front answers with, so the console never
// forwards a body the application could not read.
export function isInit(data) {
  return Boolean(data) && data.type === INIT_MESSAGE && typeof data.token === "string" && data.token !== "";
}

// encodeMessage renders one message as the byte string the handshake accepts:
// the front carries the message as a JSON []byte, which is base64 on the wire.
export function encodeMessage(message) {
  const bytes = new TextEncoder().encode(JSON.stringify(message));
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

// originOf reads the origin of one absolute address; an address that does not
// parse names no origin.
function originOf(address) {
  try {
    return new URL(address).origin;
  } catch {
    return "";
  }
}

// frameSource names this window and the console origin in the signed address,
// so the application can post its `ready` to a concrete origin instead of a
// wildcard (KOSD-C-75). The signature covers the asset path, not these query
// values, and the front ignores them.
function frameSource(address, record, consoleOrigin) {
  const target = new URL(address);
  if (consoleOrigin) target.searchParams.set("kos-console", consoleOrigin);
  target.searchParams.set("kos-window", record.id);
  return target.toString();
}
