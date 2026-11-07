// The application host: the frame mounts only the signed address the front
// returned with the declared sandbox, the `ready` handshake is judged against
// the window's origin and frame, the token is delivered once, and an uninstalled
// application leaves its open window in the error state (KOSD-C-73, KOSD-C-75,
// DASH-C-30).

import assert from "node:assert/strict";
import test from "node:test";

import { createAppHost, encodeMessage, isInit, readReady } from "../../pkg/bff/assets/shell/apphost.js";
import { filesApp, notesApp, openAnswerFor } from "./fixtures.js";
import { fakeDocument, fakeElement, fakeWindow } from "./harness.js";

const filesOrigin = "https://files.apps.example.test";

// hostFor builds one host with a recorded transport. The handshake answer is
// the init payload the front sends once.
function hostFor({ init = { type: "init", token: "token-1", theme: "system", capabilities: ["notifications"], expiresAt: "2099-01-01T00:00:00Z" } } = {}) {
  const calls = [];
  const api = {
    write: async (method, path, body) => {
      calls.push({ method, path, body });
      if (path.endsWith("/handshake")) return init;
      return null;
    },
  };
  const document = fakeDocument();
  const container = fakeElement("div");
  const window = fakeWindow();
  const host = createAppHost({ api, document, container, window, consoleOrigin: "https://console.example.test" });
  return { host, calls, container, window };
}

// mountFiles registers one files window and returns its parts.
function mountFiles(harness, answer = openAnswerFor(filesApp)) {
  harness.host.mount(answer, filesApp);
  const node = harness.container.children[0];
  const body = node.children[0];
  return { node, body, frame: body.children[0], answer };
}

// tick waits for one settled promise chain.
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

test("窗口按清单默认尺寸挂载签名地址上的 iframe，沙箱取后端的声明", () => {
  const harness = hostFor();
  const answer = openAnswerFor(filesApp);
  const { node, frame } = mountFiles(harness, answer);
  assert.equal(node.getAttribute("data-window"), "win-1");
  assert.equal(node.getAttribute("data-app"), "files");
  assert.equal(node.getAttribute("data-state"), "opening");
  assert.equal(node.style.values.get("--window-width"), "900px");
  assert.equal(node.style.values.get("--window-height"), "640px");
  assert.equal(frame.getAttribute("sandbox"), "allow-scripts allow-same-origin");
  const source = frame.getAttribute("src");
  assert.equal(new URL(source).origin, filesOrigin);
  assert.equal(new URL(source).searchParams.get("kos-console"), "https://console.example.test");
  assert.equal(new URL(source).searchParams.get("kos-window"), "win-1");
  assert.equal(new URL(source).searchParams.get("kos-sig"), "signature");
  assert.ok(!frame.getAttribute("sandbox").includes("allow-top-navigation"), "沙箱允许了顶层导航");
});

test("来源、发送者或消息形状不符的 ready 被忽略，不产生握手", async () => {
  const harness = hostFor();
  const { frame } = mountFiles(harness);
  harness.window.message({ origin: "https://evil.example.test", source: frame.contentWindow, data: { type: "ready" } });
  harness.window.message({ origin: filesOrigin, source: {}, data: { type: "ready" } });
  harness.window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "something-else" } });
  harness.window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "ready", window: "another" } });
  await tick();
  assert.equal(harness.calls.length, 0, "来源或结构不符的消息产生了握手");
});

test("正确来源的 ready 完成握手，init 只发一次且目标 origin 明确", async () => {
  const harness = hostFor();
  const { frame, answer } = mountFiles(harness);
  harness.window.message({
    origin: filesOrigin,
    source: frame.contentWindow,
    data: { type: "ready", window: "win-1" },
  });
  await waitFor(() => harness.calls.length === 1, "the handshake call");
  await waitFor(() => frame.messages.length === 1, "the init message");
  const call = harness.calls[0];
  assert.equal(call.method, "POST");
  assert.equal(call.path, "/api/v1/windows/win-1/handshake");
  assert.equal(call.body.origin, filesOrigin);
  assert.equal(call.body.nonce, answer.window.handshakeNonce);
  assert.deepEqual(JSON.parse(Buffer.from(call.body.message, "base64").toString()), {
    type: "ready",
    window: "win-1",
  });
  assert.equal(frame.messages.length, 1, "init 没有送达 frame");
  assert.equal(frame.messages[0].targetOrigin, filesOrigin);
  assert.equal(frame.messages[0].payload.token, "token-1");

  harness.window.message({
    origin: filesOrigin,
    source: frame.contentWindow,
    data: { type: "ready", window: "win-1" },
  });
  await tick();
  assert.equal(harness.calls.length, 1, "第二次 ready 又换了一个令牌");
});

test("资产地址不在应用 origin 上时窗口进入错误态且不加载 frame", () => {
  const harness = hostFor();
  const answer = openAnswerFor(filesApp);
  answer.frame = "https://evil.example.test/app.js";
  harness.host.mount(answer, filesApp);
  const node = harness.container.children[0];
  assert.equal(node.getAttribute("data-state"), "error");
  assert.equal(node.children[0].children.length, 0, "错误态仍挂载了 frame");
});

test("应用卸载后已打开窗口进入错误态且 frame 被销毁，其他应用不受影响", () => {
  const harness = hostFor();
  mountFiles(harness, openAnswerFor(filesApp, { id: "win-files" }));
  harness.host.mount(openAnswerFor(notesApp, { id: "win-notes" }), notesApp);
  harness.host.reconcile({
    apps: [notesApp],
    windows: [
      { id: "win-files", appId: "files", state: "error" },
      { id: "win-notes", appId: "notes", state: "normal" },
    ],
  });
  const filesNode = harness.container.children[0];
  const notesNode = harness.container.children[1];
  assert.equal(filesNode.getAttribute("data-state"), "error");
  assert.equal(filesNode.getAttribute("data-error"), "应用已卸载，窗口已进入错误态");
  assert.ok(filesNode.children[0].innerHTML.includes("窗口已进入错误态"), "错误态没有给出原因");
  assert.equal(filesNode.children[0].children.length, 0, "错误态仍保留 frame");
  assert.equal(notesNode.getAttribute("data-state"), "normal");
  assert.equal(notesNode.children[0].children.length, 1, "其他应用的窗口被销毁了");
});

test("关窗移除本地窗口并请求后端撤销令牌", async () => {
  const harness = hostFor();
  mountFiles(harness);
  await harness.host.close("win-1");
  assert.deepEqual(harness.calls.map((call) => `${call.method} ${call.path}`), ["DELETE /api/v1/windows/win-1"]);
  assert.equal(harness.container.children.length, 0);
  assert.deepEqual(harness.host.records(), []);
});

// hostWith builds one host over a scripted transport, so a case can answer the
// registration with the instance a reload or another window would see.
function hostWith(write) {
  const document = fakeDocument();
  const container = fakeElement("div");
  const window = fakeWindow();
  const host = createAppHost({ api: { write }, document, container, window, consoleOrigin: "https://console.example.test" });
  return { host, container, window };
}

test("本地没有记录且实例已交付令牌时：先关闭旧实例，再注册可重新握手的实例", async () => {
  const init = { type: "init", token: "token-1", theme: "system", capabilities: [], expiresAt: "2099-01-01T00:00:00Z" };
  const answers = [
    openAnswerFor(filesApp, { isNew: false, hasToken: true }),
    openAnswerFor(filesApp, { id: "win-2" }),
  ];
  const calls = [];
  const { host, container, window } = hostWith(async (method, path) => {
    calls.push(`${method} ${path}`);
    if (method === "POST" && path === "/api/v1/apps/files/windows") return answers.shift();
    if (method === "DELETE") return null;
    if (path.endsWith("/handshake")) return init;
    return null;
  });

  const answer = await host.open(filesApp);
  assert.equal(answer.window.id, "win-2");
  assert.deepEqual(
    calls,
    [
      "POST /api/v1/apps/files/windows",
      "DELETE /api/v1/windows/win-1",
      "POST /api/v1/apps/files/windows",
    ],
    "刷新后的恢复没有先关闭已交付令牌的实例",
  );

  host.mount(answer, filesApp);
  const frame = container.children[0].children[0].children[0];
  window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "ready", window: "win-2" } });
  await waitFor(() => frame.messages.length === 1, "the recovered handshake");
  assert.equal(frame.messages[0].payload.token, "token-1");
  assert.ok(calls.includes("POST /api/v1/windows/win-2/handshake"), "新实例没有用新 nonce 握手");
});

test("已挂载的实例再次打开只聚焦，不关闭也不重新注册", async () => {
  const calls = [];
  const { host } = hostWith(async (method, path) => {
    calls.push(`${method} ${path}`);
    if (path === "/api/v1/apps/files/windows") return openAnswerFor(filesApp, { isNew: false, hasToken: true });
    return null;
  });
  host.mount(openAnswerFor(filesApp), filesApp);

  const answer = await host.open(filesApp);
  assert.equal(answer.window.id, "win-1");
  assert.deepEqual(calls, ["POST /api/v1/apps/files/windows"], "已挂载实例的再次打开被关闭或重新注册");
});

test("本地没有记录但实例未交付令牌时直接复用，不关闭也不再注册", async () => {
  const init = { type: "init", token: "token-1", theme: "system", capabilities: [], expiresAt: "2099-01-01T00:00:00Z" };
  const calls = [];
  const { host, container, window } = hostWith(async (method, path) => {
    calls.push(`${method} ${path}`);
    if (path === "/api/v1/apps/files/windows") return openAnswerFor(filesApp, { isNew: false });
    if (path.endsWith("/handshake")) return init;
    return null;
  });

  const answer = await host.open(filesApp);
  assert.equal(answer.window.id, "win-1");
  host.mount(answer, filesApp);
  const frame = container.children[0].children[0].children[0];
  window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "ready", window: "win-1" } });
  await waitFor(() => frame.messages.length === 1, "the handshake of the reused instance");
  assert.deepEqual(calls.filter((call) => call.startsWith("DELETE")), [], "未交付令牌的实例被关闭");
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
