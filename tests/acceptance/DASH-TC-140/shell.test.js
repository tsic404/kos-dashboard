// DASH-TC-140 from the browser side: after an extension declaring one window
// application is installed, the launcher and the dock both list it with the
// manifest's name and icon; opening it mounts one frame at the manifest's
// default size and a single-mode reopen focuses that instance; uninstalling it
// removes both entries, leaves its open window in the error state where it
// cannot be reopened, and leaves another application untouched
// (DASH-AC-67, DASH-C-30, KOSD-C-73, KOSD-C-75). The preference assertions at
// the end cover this issue's DASH-C-27 delivery point.

import assert from "node:assert/strict";
import test from "node:test";

import { createApi } from "../../../pkg/bff/assets/shell/api.js";
import { createApp } from "../../../pkg/bff/assets/shell/app.js";
import { createAppHost } from "../../../pkg/bff/assets/shell/apphost.js";
import {
  appsAnswer,
  desktopPage,
  dockView,
  filesApp,
  launcherView,
  notesApp,
  openAnswerFor,
  registry,
} from "../../shell/fixtures.js";
import {
  elementWith,
  fakeDocument,
  fakeElement,
  fakeEventSource,
  fakeFetch,
  fakeWindow,
  memoryStorage,
  reply,
  waitForHtml,
} from "../../shell/harness.js";

const filesOrigin = "https://files.apps.example.test";

// routesFor answers the shell's reads with the fixtures of one installed set.
function routesFor(overrides = {}) {
  return {
    "GET /api/v1/auth/session": () => reply(200, { username: "owner", role: "Owner" }),
    "GET /api/v1/pages": () => reply(200, registry),
    "GET /api/v1/desktop": () => reply(200, desktopPage),
    "GET /api/v1/apps": () => reply(200, appsAnswer),
    "GET /api/v1/dock": () => reply(200, dockView),
    "GET /api/v1/launcher": () => reply(200, launcherView),
    "GET /api/v1/windows": () => reply(200, { items: [] }),
    ...overrides,
  };
}

// startShell starts the real shell with the real application host over fakes.
function startShell(routes) {
  const { fetch, calls } = fakeFetch(routes);
  const api = createApi({ fetch, document: { cookie: "kos_bff_csrf=csrf-token" } });
  const root = { innerHTML: "" };
  const container = fakeElement("div");
  const window = fakeWindow();
  const host = createAppHost({
    api,
    document: fakeDocument(),
    container,
    window,
    consoleOrigin: "https://console.example.test",
  });
  const { EventSource, sources } = fakeEventSource();
  const storage = memoryStorage();
  const app = createApp({ api, root, storage, EventSource, host });
  return { app, root, calls, container, window, sources, storage };
}

function click(attributes) {
  return { target: elementWith(attributes) };
}

// waitFor waits for one settled piece of the shell's work.
async function waitFor(predicate, label) {
  const deadline = Date.now() + 2000;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  assert.fail(`timed out waiting for ${label}`);
}

test("安装声明后 launcher 与 dock 都出现入口，名称与图标等于清单", async () => {
  const { app, root } = startShell(routesFor());
  await app.start();

  // The dock lists both installed applications; the pinned one carries its pin
  // and the manifest's name and icon (DASH-C-30, KOSD-C-73).
  assert.match(root.innerHTML, /data-dock-app="files" data-pinned="true"/);
  assert.match(root.innerHTML, /data-dock-app="notes" data-pinned="false"/);
  assert.match(root.innerHTML, /title="文件"/);
  assert.match(root.innerHTML, /src="https:\/\/files\.apps\.example\.test\/assets\/icon\.svg"/);
  assert.match(root.innerHTML, /src="https:\/\/notes\.apps\.example\.test\/assets\/icon\.svg"/);

  // The launcher lists the same applications with the same names and icons.
  await app.handleClick(click({ "data-action": "launcher" }));
  assert.match(root.innerHTML, /data-launcher-app="files"/);
  assert.match(root.innerHTML, /data-launcher-app="notes"/);
  assert.match(root.innerHTML, />文件</);
  assert.match(root.innerHTML, />笔记</);
});

test("从 dock 与 launcher 各打开一次：默认尺寸、同一实例只聚焦", async () => {
  const opened = { answer: openAnswerFor(filesApp) };
  const { app, root, calls, container } = startShell(
    routesFor({
      "POST /api/v1/apps/files/windows": () => reply(202, opened.answer),
      "DELETE /api/v1/windows/win-1": () => reply(204),
    }),
  );
  await app.start();

  await app.handleClick(click({ "data-open-app": "files" }));
  assert.equal(container.children.length, 1, "打开后没有挂载一个窗口");
  const node = container.children[0];
  assert.equal(node.getAttribute("data-window"), "win-1");
  assert.equal(node.style.values.get("--window-width"), "900px");
  assert.equal(node.style.values.get("--window-height"), "640px");
  const body = node.children[0];
  const frame = body.children[0];
  assert.equal(frame.getAttribute("sandbox"), "allow-scripts allow-same-origin");
  const source = frame.getAttribute("src");
  assert.equal(new URL(source).origin, filesOrigin);
  assert.equal(new URL(source).searchParams.get("kos-console"), "https://console.example.test");
  // The console mounts the signed address as the front issued it: extension,
  // application, version, digest and signature are untouched (KOSD-C-73).
  assert.equal(new URL(source).pathname, new URL(opened.answer.frame).pathname, "外壳改写了签名资产地址");
  assert.match(
    new URL(source).pathname,
    /^\/app-assets\/files-ext\/files\/1\.0\.0\/sha256:[0-9a-f]{64}\/index\.html$/,
    "签名资产地址不是按扩展、应用、版本与摘要寻址",
  );
  assert.equal(new URL(source).searchParams.get("kos-sig"), "signature");
  assert.ok(!source.includes("kubeconfig"), "frame 地址携带了集群凭据");

  // The second open names the same instance, so the shell focuses it instead of
  // mounting a second frame (DASH-TC-140).
  opened.answer = openAnswerFor(filesApp, { isNew: false });
  await app.handleClick(click({ "data-action": "launcher" }));
  await app.handleClick(click({ "data-open-app": "files" }));
  assert.equal(container.children.length, 1, "单例应用打开了第二个窗口");
  assert.equal(container.children[0].getAttribute("data-focused"), "true");
  const opens = calls.filter((call) => call.method === "POST" && call.path === "/api/v1/apps/files/windows");
  assert.equal(opens.length, 2, "两次打开没有各发一次注册请求");

  // Closing removes the frame and revokes the token through the front
  // (KOSD-C-75).
  await app.handleClick(click({ "data-command": "window-close", "data-window": "win-1" }));
  assert.ok(
    calls.some((call) => call.method === "DELETE" && call.path === "/api/v1/windows/win-1"),
    "关窗没有请求撤销令牌",
  );
  assert.equal(container.children.length, 0, "关窗后仍保留窗口");
});

test("来源不符的 ready 被忽略，正确来源完成握手且 init 目标 origin 明确", async () => {
  const init = {
    type: "init",
    token: "token-1",
    theme: "system",
    capabilities: ["notifications"],
    expiresAt: "2099-01-01T00:00:00Z",
  };
  const { app, window, container, calls } = startShell(
    routesFor({
      "POST /api/v1/apps/files/windows": () => reply(202, openAnswerFor(filesApp)),
      "POST /api/v1/windows/win-1/handshake": () => reply(200, init),
    }),
  );
  await app.start();
  await app.handleClick(click({ "data-open-app": "files" }));
  const frame = container.children[0].children[0].children[0];

  window.message({ origin: "https://evil.example.test", source: frame.contentWindow, data: { type: "ready" } });
  window.message({ origin: filesOrigin, source: {}, data: { type: "ready" } });
  await new Promise((resolve) => setTimeout(resolve, 5));
  assert.ok(
    !calls.some((call) => call.path.endsWith("/handshake")),
    "来源不符的消息产生了握手",
  );

  window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "ready", window: "win-1" } });
  const deadline = Date.now() + 2000;
  while (frame.messages.length === 0 && Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  assert.equal(frame.messages.length, 1, "init 没有送达 frame");
  assert.equal(frame.messages[0].targetOrigin, filesOrigin);
  assert.equal(frame.messages[0].payload.token, "token-1");
});

test("令牌过期后窗口进入错误态、frame 销毁且不能继续使用", async () => {
  const { app, container, window } = startShell(
    routesFor({
      "POST /api/v1/apps/files/windows": () => reply(202, openAnswerFor(filesApp)),
      "POST /api/v1/windows/win-1/handshake": () =>
        reply(200, { type: "init", token: "token-1", theme: "system", capabilities: [], expiresAt: "2000-01-01T00:00:00Z" }),
    }),
  );
  await app.start();
  await app.handleClick(click({ "data-open-app": "files" }));
  const frame = container.children[0].children[0].children[0];
  window.message({
    origin: filesOrigin,
    source: frame.contentWindow,
    data: { type: "ready" },
  });
  const node = container.children[0];
  const deadline = Date.now() + 2000;
  while (node.getAttribute("data-state") !== "error" && Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  assert.equal(node.getAttribute("data-state"), "error");
  assert.ok(node.getAttribute("data-error").includes("过期"));
  assert.equal(node.children[0].children.length, 0, "过期后仍保留 frame");
});

test("卸载后两处入口消失，已打开窗口进入错误态且不能重开，其他应用不受影响", async () => {
  const opened = {
    "POST /api/v1/apps/files/windows": () => reply(202, openAnswerFor(filesApp, { id: "win-files" })),
    "POST /api/v1/apps/notes/windows": () => reply(202, openAnswerFor(notesApp, { id: "win-notes" })),
  };
  const routes = routesFor(opened);
  const { app, root, sources, container, calls } = startShell(routes);
  await app.start();
  await app.handleClick(click({ "data-open-app": "files" }));
  await app.handleClick(click({ "data-open-app": "notes" }));
  assert.equal(container.children.length, 2, "两个应用没有各自挂载窗口");

  // The extension is uninstalled: the application list no longer names it and
  // the front reports its window in the error state (DASH-C-30).
  routes["GET /api/v1/apps"] = () => reply(200, { items: [notesApp], notReady: [] });
  routes["GET /api/v1/dock"] = () => reply(200, { ...dockView, pinned: [], applications: ["notes"] });
  routes["GET /api/v1/launcher"] = () => reply(200, { ...launcherView, applications: ["notes"] });
  routes["GET /api/v1/windows"] = () =>
    reply(200, {
      items: [
        { id: "win-files", appId: "files", state: "error" },
        { id: "win-notes", appId: "notes", state: "normal" },
      ],
    });
  sources[0].emit("events", { observedAt: "2026-03-01T12:00:30Z", window: "1h", events: [] });
  await waitForHtml(root, (html) => !html.includes('data-dock-app="files"'), "the uninstalled entry to leave the dock");

  assert.ok(!root.innerHTML.includes('data-dock-app="files"'), "卸载后 dock 仍有入口");
  await app.handleClick(click({ "data-action": "launcher" }));
  assert.ok(!root.innerHTML.includes('data-launcher-app="files"'), "卸载后 launcher 仍有入口");
  assert.match(root.innerHTML, /data-launcher-app="notes"/);

  const filesNode = container.children[0];
  const notesNode = container.children[1];
  assert.equal(filesNode.getAttribute("data-state"), "error");
  assert.equal(filesNode.children[0].children.length, 0, "错误态仍保留 frame");
  assert.match(filesNode.children[0].innerHTML, /应用已卸载/);
  assert.equal(notesNode.getAttribute("data-state"), "normal");
  assert.equal(notesNode.children[0].children.length, 1, "其他应用的窗口被卸载影响");

  // Reopening the uninstalled application is refused before any registration
  // request is sent.
  const opensBefore = calls.filter((call) => call.path === "/api/v1/apps/files/windows").length;
  await app.launch("files");
  const opensAfter = calls.filter((call) => call.path === "/api/v1/apps/files/windows").length;
  assert.equal(opensAfter, opensBefore, "卸载后的应用仍被重新打开");
  assert.match(root.innerHTML, /data-surface-error="app_unavailable"/);
});

test("dock 与 launcher 布局读写走账号偏好域，不落浏览器副本", async () => {
  const views = {
    dock: { ...dockView },
    launcher: { ...launcherView },
  };
  const routes = routesFor({
    "GET /api/v1/dock": () => reply(200, views.dock),
    "GET /api/v1/launcher": () => reply(200, views.launcher),
    "PUT /api/v1/preferences/kos-dashboard/layout": () => {
      views.dock = { ...views.dock, pinned: [], position: "right", autoHide: true };
      return reply(204);
    },
    "PUT /api/v1/preferences/kos-dashboard/listing": () => {
      views.launcher = { ...views.launcher, sort: "recent" };
      return reply(204);
    },
  });
  const { app, root, calls, storage } = startShell(routes);
  await app.start();

  // The dock menu reads the stored layout: the pinned application offers
  // "unpin", the background menu offers the three positions.
  app.handleContextMenu({ target: elementWith({ "data-dock-app": "files" }), preventDefault() {} });
  assert.match(root.innerHTML, /data-command="unpin" data-app="files"/);
  await app.handleClick(click({ "data-command": "unpin", "data-app": "files" }));
  const layoutWrite = calls.find(
    (call) => call.method === "PUT" && call.path === "/api/v1/preferences/kos-dashboard/layout",
  );
  assert.ok(layoutWrite, "取消固定没有写入偏好域");
  assert.equal(layoutWrite.headers["X-CSRF-Token"], "csrf-token");
  assert.deepEqual(JSON.parse(JSON.parse(layoutWrite.body).value), {
    schemaVersion: 1,
    position: "bottom",
    autoHide: false,
    pinned: [],
  });
  assert.ok(
    calls.filter((call) => call.method === "GET" && call.path === "/api/v1/dock").length >= 2,
    "写后没有从偏好域重读 dock 布局",
  );

  app.handleContextMenu({ target: elementWith({ "data-dock": "true" }), preventDefault() {} });
  await app.handleClick(click({ "data-command": "position", "data-position": "right" }));
  await app.handleClick(click({ "data-command": "autohide" }));
  assert.match(root.innerHTML, /data-position="right"/);
  assert.match(root.innerHTML, /data-autohide="true"/);

  await app.handleClick(click({ "data-action": "launcher" }));
  await app.handleClick(click({ "data-command": "launcher-sort", "data-sort": "recent" }));
  const listingWrite = calls.find(
    (call) => call.method === "PUT" && call.path === "/api/v1/preferences/kos-dashboard/listing",
  );
  assert.ok(listingWrite, "launcher 排序没有写入偏好域");
  assert.deepEqual(JSON.parse(JSON.parse(listingWrite.body).value), {
    schemaVersion: 1,
    sort: "recent",
    categories: [],
  });

  // The layout is only ever read from the front; browser storage carries the
  // recent list alone (KOSD-C-53).
  assert.equal(storage.getItem("kos-dashboard"), null);
});

test("握手成功后刷新页面：关闭已交付令牌的旧实例，挂载并再次握手新实例", async () => {
  const init = { type: "init", token: "token-1", theme: "system", capabilities: [], expiresAt: "2099-01-01T00:00:00Z" };
  const answers = [
    openAnswerFor(filesApp),
    openAnswerFor(filesApp, { isNew: false, hasToken: true }),
    openAnswerFor(filesApp, { id: "win-2" }),
  ];
  const routes = routesFor({
    "POST /api/v1/apps/files/windows": () => reply(202, answers.shift()),
    "DELETE /api/v1/windows/win-1": () => reply(204),
    "POST /api/v1/windows/win-1/handshake": () => reply(200, init),
    "POST /api/v1/windows/win-2/handshake": () => reply(200, init),
  });

  // The first page opens the only instance and handshakes, so the instance has
  // already delivered its token.
  const first = startShell(routes);
  await first.app.start();
  await first.app.handleClick(click({ "data-open-app": "files" }));
  const firstFrame = first.container.children[0].children[0].children[0];
  first.window.message({ origin: filesOrigin, source: firstFrame.contentWindow, data: { type: "ready" } });
  await waitFor(() => firstFrame.messages.length === 1, "the first handshake");

  // The page reloads: the shell keeps no record of the instance, while the
  // front still answers that same instance for this browser session and marks
  // its token as delivered (DASH-C-74, KOSD-C-75).
  const second = startShell(routes);
  await second.app.start();
  await second.app.handleClick(click({ "data-open-app": "files" }));

  const sequence = second.calls
    .filter((call) => call.path === "/api/v1/apps/files/windows" || call.method === "DELETE")
    .map((call) => `${call.method} ${call.path}`);
  assert.deepEqual(
    sequence,
    [
      "POST /api/v1/apps/files/windows",
      "DELETE /api/v1/windows/win-1",
      "POST /api/v1/apps/files/windows",
    ],
    "刷新后没有先关闭已交付令牌的实例再注册新实例",
  );

  assert.equal(second.container.children.length, 1, "刷新后的页面挂载了多个窗口");
  const node = second.container.children[0];
  assert.equal(node.getAttribute("data-window"), "win-2");
  assert.equal(node.getAttribute("data-state"), "opening");
  const frame = node.children[0].children[0];
  second.window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "ready", window: "win-2" } });
  await waitFor(() => frame.messages.length === 1, "the recovered handshake");
  assert.equal(frame.messages[0].targetOrigin, filesOrigin);
  assert.equal(frame.messages[0].payload.token, "token-1");
});

test("同一账号的第二浏览器各自挂载自己的实例，不关闭另一浏览器的窗口", async () => {
  const init = { type: "init", token: "token-1", theme: "system", capabilities: [], expiresAt: "2099-01-01T00:00:00Z" };
  const first = startShell(
    routesFor({
      "POST /api/v1/apps/files/windows": () => reply(202, openAnswerFor(filesApp, { id: "win-a" })),
      "POST /api/v1/windows/win-a/handshake": () => reply(200, init),
    }),
  );
  const second = startShell(
    routesFor({
      "POST /api/v1/apps/files/windows": () => reply(202, openAnswerFor(filesApp, { id: "win-b" })),
      "POST /api/v1/windows/win-b/handshake": () => reply(200, init),
    }),
  );

  await first.app.start();
  await first.app.handleClick(click({ "data-open-app": "files" }));
  const firstFrame = first.container.children[0].children[0].children[0];
  first.window.message({ origin: filesOrigin, source: firstFrame.contentWindow, data: { type: "ready", window: "win-a" } });
  await waitFor(() => firstFrame.messages.length === 1, "the first browser handshake");

  await second.app.start();
  await second.app.handleClick(click({ "data-open-app": "files" }));
  assert.equal(second.container.children.length, 1);
  assert.equal(second.container.children[0].getAttribute("data-window"), "win-b", "第二浏览器没有挂载自己的实例");
  assert.ok(
    !second.calls.some((call) => call.method === "DELETE"),
    "第二浏览器关闭了另一浏览器的窗口",
  );
  const secondFrame = second.container.children[0].children[0].children[0];
  second.window.message({ origin: filesOrigin, source: secondFrame.contentWindow, data: { type: "ready", window: "win-b" } });
  await waitFor(() => secondFrame.messages.length === 1, "the second browser handshake");
  assert.equal(secondFrame.messages[0].payload.token, "token-1");

  // The first browser's own window keeps its frame and state.
  assert.equal(first.container.children.length, 1);
  assert.equal(first.container.children[0].getAttribute("data-window"), "win-a");
  assert.equal(first.container.children[0].children[0].children.length, 1, "第一浏览器的窗口被第二浏览器关掉");
});

test("关窗后重开得到新实例，旧实例不阻挡重新握手", async () => {
  const init = { type: "init", token: "token-1", theme: "system", capabilities: [], expiresAt: "2099-01-01T00:00:00Z" };
  const answers = [openAnswerFor(filesApp), openAnswerFor(filesApp, { id: "win-2" })];
  const { app, container, window, calls } = startShell(
    routesFor({
      "POST /api/v1/apps/files/windows": () => reply(202, answers.shift()),
      "DELETE /api/v1/windows/win-1": () => reply(204),
      "POST /api/v1/windows/win-2/handshake": () => reply(200, init),
    }),
  );
  await app.start();
  await app.handleClick(click({ "data-open-app": "files" }));
  await app.handleClick(click({ "data-command": "window-close", "data-window": "win-1" }));
  assert.equal(container.children.length, 0, "关窗后仍保留窗口");

  await app.handleClick(click({ "data-open-app": "files" }));
  assert.equal(container.children.length, 1, "重开没有挂载一个窗口");
  assert.equal(container.children[0].getAttribute("data-window"), "win-2", "重开没有拿到新实例");
  const frame = container.children[0].children[0].children[0];
  window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "ready", window: "win-2" } });
  await waitFor(() => frame.messages.length === 1, "the reopened handshake");
  assert.equal(frame.messages[0].payload.token, "token-1");
  const handshakes = calls
    .filter((call) => call.path.endsWith("/handshake"))
    .map((call) => `${call.method} ${call.path}`);
  assert.deepEqual(handshakes, ["POST /api/v1/windows/win-2/handshake"], "重开后的握手没有走新实例");
});

test("卸载后重装，重开不受错误态旧实例阻挡", async () => {
  const init = { type: "init", token: "token-1", theme: "system", capabilities: [], expiresAt: "2099-01-01T00:00:00Z" };
  const routes = routesFor({
    "POST /api/v1/apps/files/windows": () => reply(202, openAnswerFor(filesApp, { id: "win-1" })),
  });
  const { app, root, container, window, sources } = startShell(routes);
  await app.start();
  await app.handleClick(click({ "data-open-app": "files" }));
  const oldNode = container.children[0];
  assert.equal(oldNode.getAttribute("data-window"), "win-1");

  // The extension is uninstalled: the entries leave both surfaces and the open
  // window enters its error state (DASH-C-30).
  routes["GET /api/v1/apps"] = () => reply(200, { items: [notesApp], notReady: [] });
  routes["GET /api/v1/dock"] = () => reply(200, { ...dockView, pinned: [], applications: ["notes"] });
  routes["GET /api/v1/launcher"] = () => reply(200, { ...launcherView, applications: ["notes"] });
  routes["GET /api/v1/windows"] = () => reply(200, { items: [{ id: "win-1", appId: "files", state: "error" }] });
  sources[0].emit("events", { observedAt: "2026-03-01T12:00:30Z", window: "1h", events: [] });
  await waitForHtml(root, (html) => !html.includes('data-dock-app="files"'), "the uninstalled entry to leave the dock");
  assert.equal(oldNode.getAttribute("data-state"), "error");

  // It is installed again; the error-state instance stays as the record of the
  // uninstalled window, and opening the application registers a new one
  // (DASH-C-30).
  routes["GET /api/v1/apps"] = () => reply(200, appsAnswer);
  routes["GET /api/v1/dock"] = () => reply(200, dockView);
  routes["GET /api/v1/launcher"] = () => reply(200, launcherView);
  routes["POST /api/v1/apps/files/windows"] = () => reply(202, openAnswerFor(filesApp, { id: "win-2" }));
  routes["POST /api/v1/windows/win-2/handshake"] = () => reply(200, init);
  sources[0].emit("events", { observedAt: "2026-03-01T12:01:00Z", window: "1h", events: [] });
  await waitForHtml(root, (html) => html.includes('data-dock-app="files"'), "the reinstalled entry to return to the dock");

  await app.launch("files");
  assert.equal(container.children.length, 2, "重装后重开没有挂载新窗口");
  const fresh = container.children[1];
  assert.equal(fresh.getAttribute("data-window"), "win-2");
  assert.equal(fresh.getAttribute("data-state"), "opening");
  const frame = fresh.children[0].children[0];
  window.message({ origin: filesOrigin, source: frame.contentWindow, data: { type: "ready", window: "win-2" } });
  await waitFor(() => frame.messages.length === 1, "the reinstalled handshake");
  assert.equal(frame.messages[0].payload.token, "token-1");
  assert.equal(oldNode.getAttribute("data-state"), "error", "重开改变了旧实例的错误态");
});
