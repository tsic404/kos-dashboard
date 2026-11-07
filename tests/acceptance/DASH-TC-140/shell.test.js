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
