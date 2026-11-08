// The frame host: a window's frame mounts only the signed address the front
// returned with the declared sandbox, the `ready` handshake is judged against the
// instance's origin and frame, the token is delivered once, and a frame that
// stops running is reported instead of inventing a window state
// (KOSD-C-73, KOSD-C-75, DASH-C-74).

import assert from "node:assert/strict";
import test from "node:test";

import { createAppHost, encodeMessage, isInit, readReady } from "../../pkg/bff/assets/shell/apphost.js";
import { filesApp, notesApp, openAnswerFor } from "./fixtures.js";
import { fakeDocument, fakeElement, fakeWindow } from "./harness.js";

const filesOrigin = "https://files.apps.example.test";
const init = {
  type: "init",
  token: "token-1",
  theme: "system",
  capabilities: ["notifications"],
  expiresAt: "2099-01-01T00:00:00Z",
};

// hostFor builds one frame host with a recorded transport. A failure the host
// reports is what the front would turn into a window state.
function hostFor({ answer = init, loadTimeout = 20000, aliveTimeout = 35000 } = {}) {
  const calls = [];
  const api = {
    write: async (method, path, body) => {
      calls.push({ method, path, body });
      if (path.endsWith("/handshake")) return answer;
      return null;
    },
  };
  const document = fakeDocument();
  const window = fakeWindow();
  const failures = [];
  const alives = [];
  const unsaved = [];
  const host = createAppHost({
    api,
    document,
    window,
    consoleOrigin: "https://console.example.test",
    loadTimeout,
    aliveTimeout,
    onFailure: (windowId, reason) => failures.push({ windowId, reason }),
    onAlive: (windowId) => alives.push(windowId),
    onUnsaved: (windowId, value) => unsaved.push({ windowId, unsaved: value }),
  });
  return { host, calls, window, failures, alives, unsaved };
}

// mountWindow mounts one answer's frame into a fresh body the way the window
// layer does.
function mountWindow(harness, answer = openAnswerFor(filesApp), windowId = "window-1") {
  const body = fakeElement("div");
  const record = harness.host.mount(
    {
      windowId,
      instance: answer.window,
      frame: answer.frame,
      sandbox: answer.sandbox,
      name: filesApp.name,
    },
    body,
  );
  return { body, record, frame: body.children[0] };
}

function tick() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

async function waitFor(predicate, label) {
  const deadline = Date.now() + 2000;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await tick();
  }
  assert.fail(`timed out waiting for ${label}`);
}

test("frame 挂载签名地址，沙箱与 console origin 取后端的声明", () => {
  const harness = hostFor();
  const answer = openAnswerFor(filesApp);
  const { frame } = mountWindow(harness, answer);
  assert.equal(frame.getAttribute("sandbox"), "allow-scripts allow-same-origin");
  const source = frame.getAttribute("src");
  assert.equal(new URL(source).origin, filesOrigin);
  assert.equal(new URL(source).searchParams.get("kos-console"), "https://console.example.test");
  // The frame names the application instance the handshake authenticates.
  assert.equal(new URL(source).searchParams.get("kos-window"), answer.window.id);
  // The signed address is mounted as the front issued it (KOSD-C-73).
  assert.equal(new URL(source).pathname, new URL(answer.frame).pathname, "外壳改写了签名资产地址");
  assert.equal(new URL(source).searchParams.get("kos-sig"), "signature");
  assert.ok(!source.includes("kubeconfig"), "frame 地址携带了集群凭据");
  assert.ok(!frame.getAttribute("sandbox").includes("allow-top-navigation"), "沙箱允许了顶层导航");
});

test("来源、发送者或消息形状不符的 ready 被忽略，不产生握手", async () => {
  const harness = hostFor();
  const { frame } = mountWindow(harness);
  harness.window.message({ origin: "https://evil.example.test", source: frame.contentWindow, data: { type: "ready" } });
  harness.window.message({ origin: filesOrigin, source: {}, data: { type: "ready" } });
  harness.window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "something-else" } });
  harness.window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "ready", window: "another" } });
  await tick();
  assert.equal(harness.calls.length, 0, "来源或结构不符的消息产生了握手");
});

test("正确来源的 ready 完成握手，init 只发一次且目标 origin 明确", async () => {
  const harness = hostFor();
  const answer = openAnswerFor(filesApp);
  const { body, frame } = mountWindow(harness, answer);
  harness.window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "ready", window: answer.window.id } });
  await waitFor(() => frame.messages.length === 1, "the init message");
  const call = harness.calls[0];
  assert.equal(call.method, "POST");
  assert.equal(call.path, `/api/v1/windows/${answer.window.id}/handshake`);
  assert.equal(call.body.origin, filesOrigin);
  assert.equal(call.body.nonce, answer.window.handshakeNonce);
  assert.deepEqual(JSON.parse(Buffer.from(call.body.message, "base64").toString()), {
    type: "ready",
    window: answer.window.id,
  });
  assert.equal(frame.messages[0].targetOrigin, filesOrigin);
  assert.equal(frame.messages[0].payload.token, "token-1");
  assert.equal(body.getAttribute("data-handshaked"), "true");

  harness.window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "ready", window: answer.window.id } });
  await tick();
  assert.equal(harness.calls.length, 1, "第二次 ready 又换了一个令牌");
});

test("令牌过期时 frame 被移除并把原因报告给窗口层", async () => {
  const harness = hostFor({
    answer: { type: "init", token: "token-1", theme: "system", capabilities: [], expiresAt: "2000-01-01T00:00:00Z" },
  });
  const answer = openAnswerFor(filesApp);
  const { body, frame } = mountWindow(harness, answer);
  harness.window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "ready" } });
  await waitFor(() => harness.failures.length === 1, "the expiry to be reported");
  assert.equal(harness.failures[0].windowId, "window-1");
  assert.match(harness.failures[0].reason, /过期/);
  assert.equal(body.children.length, 0, "过期后仍保留 frame");
  assert.deepEqual(harness.host.records(), []);
});

test("沙箱、origin 与签名地址不合规时拒绝挂载并报告失败", () => {
  const cases = [
    { apply: (answer) => ({ ...answer, sandbox: "" }), reason: /沙箱/ },
    { apply: (answer) => ({ ...answer, frame: "https://evil.example.test/app.js" }), reason: /origin/ },
    { apply: (answer) => ({ ...answer, frame: "" }), reason: /资产地址/ },
    { apply: (answer) => ({ ...answer, window: { ...answer.window, origin: "" } }), reason: /绝对地址/ },
  ];
  for (const one of cases) {
    const harness = hostFor();
    const answer = one.apply(openAnswerFor(filesApp));
    const { body } = mountWindow(harness, answer);
    assert.equal(body.children.length, 0, "不合规的 frame 被挂载了");
    assert.equal(harness.failures.length, 1, "不合规的 frame 没有报告失败");
    assert.match(harness.failures[0].reason, one.reason);
  }
});

test("卸载与登出移除 frame，records 只列仍挂载的窗口", () => {
  const harness = hostFor();
  mountWindow(harness, openAnswerFor(filesApp), "window-1");
  mountWindow(harness, openAnswerFor(notesApp, { id: "instance-notes" }), "window-2");
  assert.deepEqual(
    harness.host.records().map((record) => record.windowId),
    ["window-1", "window-2"],
  );
  harness.host.unmount("window-1");
  assert.deepEqual(harness.host.records().map((record) => record.windowId), ["window-2"]);
  harness.host.closeAll();
  assert.deepEqual(harness.host.records(), []);
});

test("消息形状的判定与编码如实反映握手约定", () => {
  assert.deepEqual(readReady({ type: "ready" }), { type: "ready" });
  assert.deepEqual(readReady({ type: "ready", window: "w" }), { type: "ready", window: "w" });
  assert.equal(readReady({ type: "ready", window: 1 }), null);
  assert.equal(readReady("ready"), null);
  assert.ok(isInit({ type: "init", token: "t" }));
  assert.ok(!isInit({ type: "init", token: "" }));
  assert.ok(!isInit({ type: "ready", token: "t" }));
  assert.equal(
    Buffer.from(encodeMessage({ type: "ready" }), "base64").toString(),
    JSON.stringify({ type: "ready" }),
  );
});

test("应用上报未保存与存活：来源与结构不符的消息被忽略，正确的消息交给窗口层", async () => {
  const harness = hostFor();
  const answer = openAnswerFor(filesApp);
  const { frame } = mountWindow(harness, answer);

  // Before the handshake the frame has not proven it is the instance.
  harness.window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "alive" } });
  harness.window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "unsaved", unsaved: true } });
  assert.deepEqual(harness.alives, []);
  assert.deepEqual(harness.unsaved, []);

  harness.window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "ready" } });
  await waitFor(() => frame.messages.length === 1, "the handshake");

  harness.window.message({ origin: "https://evil.example.test", source: frame.contentWindow, data: { type: "alive" } });
  harness.window.message({ origin: filesOrigin, source: {}, data: { type: "alive" } });
  harness.window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "alive", window: "another" } });
  harness.window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "unsaved", unsaved: "yes" } });
  assert.deepEqual(harness.alives, []);
  assert.deepEqual(harness.unsaved, []);

  harness.window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "alive", window: answer.window.id } });
  harness.window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "unsaved", unsaved: true } });
  assert.deepEqual(harness.alives, ["window-1"]);
  assert.deepEqual(harness.unsaved, [{ windowId: "window-1", unsaved: true }]);
});

test("应用未在限定时间内完成握手时报告失败", async () => {
  const harness = hostFor({ loadTimeout: 5 });
  mountWindow(harness);
  await waitFor(() => harness.failures.length === 1, "the load timeout");
  assert.equal(harness.failures[0].windowId, "window-1");
  assert.match(harness.failures[0].reason, /加载/);
  assert.deepEqual(harness.host.records(), []);
});

test("握手后应用停止上报存活时报告心跳超时", async () => {
  const harness = hostFor({ aliveTimeout: 5 });
  const { frame } = mountWindow(harness);
  harness.window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "ready" } });
  await waitFor(() => frame.messages.length === 1, "the handshake");
  await waitFor(() => harness.failures.length === 1, "the liveness timeout");
  assert.match(harness.failures[0].reason, /心跳超时/);
});

test("应用持续上报存活时不会超时", async () => {
  const harness = hostFor({ aliveTimeout: 20 });
  const { frame } = mountWindow(harness);
  harness.window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "ready" } });
  await waitFor(() => frame.messages.length === 1, "the handshake");
  for (let round = 0; round < 3; round += 1) {
    harness.window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "alive" } });
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  assert.deepEqual(harness.failures, [], "上报存活的应用被判成了超时");
  assert.equal(harness.alives.length, 3);
});
