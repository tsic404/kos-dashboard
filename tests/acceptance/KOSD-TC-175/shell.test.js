// KOSD-TC-175 from the browser side: the shell mounts two window instances of
// one application in one session boundary, and judges every frame message
// against the instance the frame belongs to. A message that names the other
// instance is ignored — it obtains no token, renews no liveness and vetoes no
// close — while the frame's own handshake and reports still work; an
// application that stops reporting enters the error state inside the
// judgement window (KOSD-AC-71, KOSD-C-81).
//
// The transport is the real front the Go half starts: this file loads the shell
// modules and uses fetch, so every assertion reads the front's window session
// and application instances rather than a stub's answer. The DOM is the shared
// fake browser, since the case runs without a browser.

import assert from "node:assert/strict";
import test from "node:test";

import { createApi } from "../../../pkg/bff/assets/shell/api.js";
import { createAppHost } from "../../../pkg/bff/assets/shell/apphost.js";
import { createWindowLayer } from "../../../pkg/bff/assets/shell/windowlayer.js";
import { createWindowSession } from "../../../pkg/bff/assets/shell/windows.js";
import { fakeDocument, fakeElement, fakeWindow } from "../../shell/harness.js";

const front = process.env.KOSD_TC175_FRONT;
const clock = process.env.KOSD_TC175_CLOCK;
const cookie = process.env.KOSD_TC175_COOKIE;
const csrf = process.env.KOSD_TC175_CSRF;
const application = process.env.KOSD_TC175_APP;
const now = process.env.KOSD_TC175_NOW;

const configured = Boolean(front && clock && cookie && csrf && application && now);

// windowOf finds one desktop window in the front's session answer.
function windowOf(session, id) {
  return (session.windows || []).find((window) => window.id === id) || null;
}

// stateOf reads one window's state; an absent window is not a live state.
function stateOf(session, id) {
  const window = windowOf(session, id);
  return window ? window.state : "";
}

test("指向另一实例的 ready 与会话上报被忽略，自己的上报才续存", { skip: !configured }, async () => {
  let nowMs = Date.parse(now);
  const document = fakeDocument();
  document.cookie = `kos_bff_csrf=${csrf}`;
  const fetchFront = (path, init = {}) =>
    fetch(new URL(path, front), {
      ...init,
      headers: { ...(init.headers || {}), Cookie: cookie },
    });
  const api = createApi({ fetch: fetchFront, document });

  const sessions = createWindowSession({ api });
  // reports are the shell's own writes to the window session; the case waits
  // for one before it moves the front's clock, so a slow write never turns into
  // a false pass.
  const reports = [];
  const page = fakeWindow();
  const container = fakeElement("div");
  const host = createAppHost({
    api,
    document,
    window: page,
    consoleOrigin: front,
    now: () => nowMs,
    loadTimeout: 600000,
    aliveTimeout: 600000,
    onAlive: (windowId) => {
      const report = sessions.heartbeat(windowId);
      reports.push(report);
      void report.catch(() => {});
    },
    onUnsaved: (windowId, unsaved) => {
      const report = sessions.markUnsaved(windowId, unsaved);
      reports.push(report);
      void report.catch(() => {});
    },
    onFailure: (windowId, reason) => {
      const report = sessions.crash(windowId, reason);
      reports.push(report);
      void report.catch(() => {});
    },
  });
  const layer = createWindowLayer({ document, container, host });

  // One browser session, two window instances of one multi-mode application.
  const first = await sessions.open(application);
  const second = await sessions.open(application);
  const a = { window: first.window, instance: first.instance };
  const b = { window: second.window, instance: second.instance };
  assert.ok(a.window && b.window, "打开两个窗口没有拿到窗口");
  assert.ok(a.instance && b.instance, "打开两个窗口没有拿到应用实例");
  assert.notEqual(a.window.id, b.window.id, "两个窗口共享了身份");
  assert.notEqual(a.instance.id, b.instance.id, "两个实例共享了实例键");
  assert.equal(a.instance.origin, b.instance.origin, "两个实例的 origin 不同，来源校验会混进这条判据");

  layer.mount(first);
  layer.mount(second);
  const frames = container.children.map((node) => node.children[1].children[0]);
  assert.equal(frames.length, 2, "两个窗口没有各自挂载 frame");
  assert.ok(frames[0].contentWindow && frames[1].contentWindow, "挂载的 frame 没有可用的发送者");

  // instanceOf reads one application instance's state from the front.
  async function instanceOf(id) {
    const answer = await api.get("/api/v1/windows");
    return (answer.items || []).find((window) => window.id === id) || null;
  }

  // session reads the front's window session.
  function session() {
    return api.get("/api/v1/desktop/windows");
  }

  // advance moves the front's clock; the shell's own clock follows, so a
  // delivered token never looks expired against a frozen front.
  async function advance(seconds) {
    const answer = await fetch(new URL(`/advance?seconds=${seconds}`, clock), { method: "POST" });
    assert.equal(answer.status, 204, "推进前端时钟失败");
    nowMs += seconds * 1000;
  }

  async function settle() {
    await new Promise((resolve) => setTimeout(resolve, 100));
  }

  async function waitFor(predicate, label) {
    const deadline = Date.now() + 5000;
    while (Date.now() < deadline) {
      if (await predicate()) return;
      await new Promise((resolve) => setTimeout(resolve, 5));
    }
    assert.fail(`等待${label}超时`);
  }

  // send posts one message from a mounted frame of the application's origin.
  function send(frame, data) {
    page.message({ origin: a.instance.origin, source: frame.contentWindow, data });
  }

  // A frame that names the other instance obtains no token for it, and neither
  // window changes state.
  send(frames[0], { type: "ready", window: b.instance.id });
  await settle();
  assert.equal((await instanceOf(a.instance.id)).hasToken, false, "指向 B 的 ready 给 A 发了令牌");
  assert.equal((await instanceOf(b.instance.id)).hasToken, false, "指向 B 的 ready 给 B 发了令牌");
  let current = await session();
  assert.equal(stateOf(current, a.window.id), "normal", "指向 B 的 ready 改变了 A 的窗口状态");
  assert.equal(stateOf(current, b.window.id), "normal", "指向 B 的 ready 改变了 B 的窗口状态");

  // The front itself refuses the same message posted directly, so the check is
  // the console's and not only the shell's early return (KOSD-C-81).
  const named = Buffer.from(JSON.stringify({ type: "ready", window: b.instance.id })).toString("base64");
  const refused = await fetchFront(`/api/v1/windows/${encodeURIComponent(a.instance.id)}/handshake`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
    body: JSON.stringify({ origin: a.instance.origin, nonce: a.instance.handshakeNonce, message: named }),
  });
  assert.equal(refused.status, 403, "指向另一实例的 ready 在前端被受理了");
  assert.equal((await refused.json()).error.code, "permission_denied", "拒绝原因不是实例校验");
  assert.equal((await instanceOf(a.instance.id)).hasToken, false, "被拒绝的握手发放了令牌");

  // The frame's own ready handshakes its instance, and only that one.
  send(frames[0], { type: "ready" });
  await waitFor(async () => (await instanceOf(a.instance.id)).hasToken, "A 自己的握手");
  assert.equal((await instanceOf(b.instance.id)).hasToken, false, "A 的握手给 B 发了令牌");
  assert.equal(frames[0].messages.length, 1, "init 没有投递到握手用的 frame");
  assert.equal(frames[0].messages[0].payload.type, "init", "frame 收到的不是 init");
  assert.equal(frames[0].messages[0].targetOrigin, a.instance.origin, "init 投递到了通配目标");
  assert.equal((await instanceOf(a.instance.id)).state, "normal", "握手没有把 A 带入运行态");

  // After the handshake the frame reports liveness and unsaved changes for the
  // other instance: the front takes neither, and B's close is not vetoed.
  send(frames[0], { type: "alive", window: b.instance.id });
  send(frames[0], { type: "unsaved", unsaved: true, window: b.instance.id });
  await settle();
  current = await session();
  assert.equal(stateOf(current, a.window.id), "normal", "指向 B 的上报改变了 A 的窗口状态");
  assert.equal(stateOf(current, b.window.id), "normal", "指向 B 的上报改变了 B 的窗口状态");
  assert.equal(windowOf(current, a.window.id).isUnsaved, undefined, "指向 B 的 unsaved 落在了 A 上");
  assert.equal(windowOf(current, b.window.id).isUnsaved, undefined, "指向 B 的 unsaved 落在了 B 上");

  // Neither window received a heartbeat, so neither expires: the front's
  // heartbeat timeout only watches a window that reported at least once.
  await advance(31);
  current = await session();
  assert.equal(stateOf(current, a.window.id), "normal", "指向 B 的 alive 续了 A 的存活");
  assert.equal(stateOf(current, b.window.id), "normal", "指向 B 的 alive 续了 B 的存活");

  const closed = await sessions.close(b.window.id);
  assert.equal(closed.vetoed, false, "B 的关闭被另一实例的未保存上报否决了");
  assert.equal(windowOf(closed.session, b.window.id), null, "B 的关闭没有移除窗口");

  // The application's own liveness renews its window; after it stops reporting,
  // the window enters the error state inside the judgement window.
  for (const round of [0, 1]) {
    const reportsBefore = reports.length;
    send(frames[0], { type: "alive" });
    await waitFor(() => reports.length > reportsBefore, `A 的第 ${round + 1} 次存活上报`);
    await reports[reportsBefore];
    await advance(20);
    current = await session();
    assert.equal(stateOf(current, a.window.id), "normal", `A 自己的第 ${round + 1} 次存活没有续存`);
  }
  await advance(31);
  current = await session();
  const stopped = windowOf(current, a.window.id);
  assert.equal(stopped && stopped.state, "error", "停止上报后窗口没有在判定窗口内进入错误态");
  assert.ok(stopped.errorReason, "错误窗口没有给出原因");
});
