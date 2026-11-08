// The console side of an application frame (KOSD-C-75, KOSD-C-73): it mounts the
// signed address of one application instance with the sandbox the front
// declared, answers the frame's `ready` with the `init` payload, and carries the
// two reports the window contract needs from the application itself — its
// unsaved changes and its liveness (DASH-C-74).
//
// A frame hangs inside the window the layer rendered, so this module owns the
// frame alone: a failure is reported through `onFailure` rather than turned into
// a second window-state source.

import { Failure } from "./api.js";

// The message names the frame uses on the console channel. A message of another
// shape, from another origin or from another frame is ignored.
export const READY_MESSAGE = "ready";
export const ALIVE_MESSAGE = "alive";
export const UNSAVED_MESSAGE = "unsaved";
export const INIT_MESSAGE = "init";

// LOAD_TIMEOUT_MS is how long a frame may take to complete its handshake. A
// frame that never reports is reported as a failed window, so a broken load does
// not leave a blank surface that never resolves (DASH-C-74).
const LOAD_TIMEOUT_MS = 20000;

// ALIVE_TIMEOUT_MS is how long a handshaked frame may stay silent. The
// application proves liveness with `alive`; a frame that stops reporting is a
// window that stopped running (DASH-C-74).
const ALIVE_TIMEOUT_MS = 35000;

// MAX_TIMEOUT_MS is the largest delay setTimeout honours; a longer wait is
// re-armed.
const MAX_TIMEOUT_MS = 2147483647;

// createAppHost binds the frame layer to one document and one clock. Every
// dependency is injected so the handshake and the liveness watch can be
// exercised without a browser.
export function createAppHost({
  api,
  document: doc,
  window: hostWindow,
  consoleOrigin = "",
  onFailure = null,
  onAlive = null,
  onUnsaved = null,
  now = () => Date.now(),
  loadTimeout = LOAD_TIMEOUT_MS,
  aliveTimeout = ALIVE_TIMEOUT_MS,
}) {
  if (!(loadTimeout > 0)) loadTimeout = LOAD_TIMEOUT_MS;
  if (!(aliveTimeout > 0)) aliveTimeout = ALIVE_TIMEOUT_MS;
  const records = new Map();

  if (hostWindow && typeof hostWindow.addEventListener === "function") {
    hostWindow.addEventListener("message", handleMessage);
  }

  // mount attaches one window's frame to the body the window layer rendered.
  // The frame carries exactly the sandbox the front declared; anything less
  // fails closed and is reported as a window failure (KOSD-C-75, DASH-C-74).
  function mount({ windowId, instance, frame, frameError = "", sandbox, origin = "", name = "" }, body) {
    if (!windowId || !instance || typeof instance.id !== "string") {
      throw new Failure(502, {
        error: { code: "invalid_response", message: "窗口打开应答缺少实例", stage: "read" },
      });
    }
    const existing = records.get(windowId);
    if (existing && existing.instanceID === instance.id) return existing;
    if (existing) unmount(windowId);
    const record = {
      windowId,
      instanceID: instance.id,
      origin: instance.origin || origin,
      nonce: instance.handshakeNonce || "",
      body: body || null,
      frame: null,
      delivered: false,
      alive: false,
      expiry: null,
      loadTimer: null,
      aliveTimer: null,
    };
    records.set(windowId, record);
    if (!record.body) return fail(windowId, "窗口没有可挂载 frame 的位置");
    if (!record.origin) return fail(windowId, "应用入口不是绝对地址，无法建立握手");
    if (!frame) return fail(windowId, frameError || "应用没有可加载的资产地址");
    if (typeof sandbox !== "string" || sandbox.trim() === "") return fail(windowId, "缺少 iframe 沙箱约束");
    if (originOf(frame) !== record.origin) return fail(windowId, "资产地址不在应用 origin 上，拒绝加载");
    const node = doc.createElement("iframe");
    node.className = "window-frame";
    node.setAttribute("sandbox", sandbox);
    node.setAttribute("src", frameSource(frame, record, consoleOrigin));
    node.setAttribute("title", name);
    node.setAttribute("referrerpolicy", "no-referrer");
    record.frame = node;
    record.body.appendChild(node);
    // The frame must complete its handshake, or the window is reported as one
    // that never loaded: a blank surface that resolves to nothing is exactly
    // the failure the contract asks the desktop to surface (DASH-C-74).
    record.loadTimer = setTimeout(() => {
      fail(record.windowId, "应用未能在限定时间内完成加载");
    }, loadTimeout);
    unref(record.loadTimer);
    return record;
  }

  // unmount drops one window's frame; the window itself stays whatever the
  // session says it is.
  function unmount(windowId) {
    const record = records.get(windowId);
    if (!record) return;
    clearTimers(record);
    dropFrame(record);
    records.delete(windowId);
  }

  // closeAll drops every frame this page mounted, which is what a sign-out does;
  // the front revokes the tokens on the same session end.
  function closeAll() {
    for (const windowId of [...records.keys()]) unmount(windowId);
  }

  // handleMessage judges one message against the frames this host mounted. A
  // message from another origin, another source or of another shape is ignored
  // (KOSD-C-75).
  function handleMessage(event) {
    const record = sourceRecord(event && event.source);
    if (!record) return;
    if (!event.origin || event.origin !== record.origin) return;
    const message = readMessage(event.data);
    if (!message) return;
    if (message.window && message.window !== record.instanceID) return;
    if (message.type === UNSAVED_MESSAGE) {
      if (!record.delivered) return;
      if (typeof onUnsaved === "function") onUnsaved(record.windowId, message.unsaved);
      return;
    }
    if (message.type === ALIVE_MESSAGE) {
      // Liveness only counts after the handshake: an unhandshaked frame cannot
      // prove it is the instance the token belongs to.
      if (!record.delivered) return;
      record.alive = true;
      armLiveness(record);
      if (typeof onAlive === "function") onAlive(record.windowId);
      return;
    }
    if (record.delivered) return;
    record.delivered = true;
    if (record.loadTimer) {
      clearTimeout(record.loadTimer);
      record.loadTimer = null;
    }
    armLiveness(record);
    api
      .write("POST", `/api/v1/windows/${encodeURIComponent(record.instanceID)}/handshake`, {
        origin: event.origin,
        nonce: record.nonce,
        message: encodeMessage(message),
      })
      .then((init) => {
        if (!isInit(init)) {
          fail(record.windowId, "握手应答不是合法的 init 消息");
          return;
        }
        if (!record.frame) return;
        // The target origin is the application origin, never a wildcard
        // (KOSD-C-75); the frame validates the answer the same way.
        record.frame.contentWindow.postMessage(init, record.origin);
        if (record.body && typeof record.body.setAttribute === "function") {
          record.body.setAttribute("data-handshaked", "true");
        }
        scheduleExpiry(record, init.expiresAt);
      })
      .catch((failure) => fail(record.windowId, failure && failure.message ? failure.message : "握手被拒绝"));
  }

  // armLiveness restarts the silence watch. The frame proves it is still running
  // with `alive`; a frame that stops is a window that stopped (DASH-C-74).
  function armLiveness(record) {
    if (record.aliveTimer) clearTimeout(record.aliveTimer);
    record.aliveTimer = setTimeout(() => fail(record.windowId, "窗口心跳超时"), aliveTimeout);
    unref(record.aliveTimer);
  }

  // scheduleExpiry ends the frame when its token expires; the application must
  // be restarted, so the frame is destroyed rather than left running
  // (KOSD-C-75). A wait beyond the setTimeout range re-arms instead of firing
  // at once.
  function scheduleExpiry(record, expiresAt) {
    const deadline = Date.parse(expiresAt);
    if (Number.isNaN(deadline)) return;
    const remaining = deadline - now();
    if (remaining <= 0) {
      fail(record.windowId, "应用令牌已过期，请重启窗口");
      return;
    }
    record.expiry = setTimeout(() => {
      if (now() < deadline) scheduleExpiry(record, expiresAt);
      else fail(record.windowId, "应用令牌已过期，请重启窗口");
    }, Math.min(remaining, MAX_TIMEOUT_MS));
    unref(record.expiry);
  }

  // fail removes one frame and reports why it stopped. The window's own state is
  // the front's answer to that report (DASH-C-74).
  function fail(windowId, reason) {
    const record = records.get(windowId);
    if (!record) return null;
    clearTimers(record);
    dropFrame(record);
    records.delete(windowId);
    if (typeof onFailure === "function") onFailure(windowId, reason);
    return null;
  }

  function clearTimers(record) {
    for (const name of ["expiry", "loadTimer", "aliveTimer"]) {
      if (record[name]) {
        clearTimeout(record[name]);
        record[name] = null;
      }
    }
  }

  function dropFrame(record) {
    if (record.frame && record.body && typeof record.body.removeChild === "function") {
      record.body.removeChild(record.frame);
    }
    record.frame = null;
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
      windowId: record.windowId,
      instanceID: record.instanceID,
      hasFrame: Boolean(record.frame),
      hasHandshaked: record.delivered,
      isAlive: record.alive,
    }));
  }

  return { mount, unmount, closeAll, records: recordsOf, handleMessage };
}

// readMessage accepts exactly the message shapes the console validates: an
// object naming the message and, optionally, its window (KOSD-C-75).
export function readMessage(data) {
  if (!data || typeof data !== "object" || Array.isArray(data)) return null;
  if (data.window !== undefined && typeof data.window !== "string") return null;
  const named = data.window === undefined ? {} : { window: data.window };
  switch (data.type) {
    case READY_MESSAGE:
      return { type: READY_MESSAGE, ...named };
    case ALIVE_MESSAGE:
      return { type: ALIVE_MESSAGE, ...named };
    case UNSAVED_MESSAGE:
      if (typeof data.unsaved !== "boolean") return null;
      return { type: UNSAVED_MESSAGE, unsaved: data.unsaved, ...named };
    default:
      return null;
  }
}

// readReady keeps the handshake shape judge available on its own.
export function readReady(data) {
  const message = readMessage(data);
  return message && message.type === READY_MESSAGE ? message : null;
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

// frameSource names this frame and the console origin in the signed address, so
// the application can post its `ready` to a concrete origin instead of a
// wildcard (KOSD-C-75). The signature covers the extension, application, version,
// digest and asset path; these two query values stay outside it, so adding them
// never widens what a signed address authorises.
function frameSource(address, record, consoleOrigin) {
  const target = new URL(address);
  if (consoleOrigin) target.searchParams.set("kos-console", consoleOrigin);
  target.searchParams.set("kos-window", record.instanceID);
  return target.toString();
}

// unref keeps a timer from holding a Node process open in the test harness.
function unref(timer) {
  if (timer && typeof timer.unref === "function") timer.unref();
}
