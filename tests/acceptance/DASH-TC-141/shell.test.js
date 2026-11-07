// DASH-TC-141 from the browser side: an extension whose `App` manifest lacks a
// required field makes its component not ready and surfaces no entry; an
// extension that declares only a page adds a page entry and no window
// application; a path or launch that names no installed application creates no
// window (DASH-AC-67, DASH-C-22, KOSD-C-73).

import assert from "node:assert/strict";
import test from "node:test";

import { createApi } from "../../../pkg/bff/assets/shell/api.js";
import { createApp } from "../../../pkg/bff/assets/shell/app.js";
import { createAppHost } from "../../../pkg/bff/assets/shell/apphost.js";
import { desktopPage, launcherView, notesApp, openAnswerFor, registry } from "../../shell/fixtures.js";
import {
  elementWith,
  fakeDocument,
  fakeElement,
  fakeEventSource,
  fakeFetch,
  fakeWindow,
  memoryStorage,
  reply,
} from "../../shell/harness.js";

// brokenRefusal is one contribution whose manifest lacks the required entry.
const brokenRefusal = {
  extension: "broken-ext",
  component: "broken",
  app: "broken",
  reason: "清单缺少必填项 entry",
};

// pageOnlyRegistry is the page registry after a page-only extension installed:
// its page is listed and no window application exists (DASH-C-22).
const pageOnlyRegistry = {
  items: [
    ...registry.items,
    { id: "reports", title: "报表", location: "panel", sources: ["object"], items: [], actions: [] },
  ],
};

function routesFor(overrides = {}) {
  return {
    "GET /api/v1/auth/session": () => reply(200, { username: "owner", role: "Owner" }),
    "GET /api/v1/pages": () => reply(200, pageOnlyRegistry),
    "GET /api/v1/desktop": () => reply(200, desktopPage),
    "GET /api/v1/apps": () => reply(200, { items: [], notReady: [brokenRefusal] }),
    "GET /api/v1/dock": () => reply(200, { schemaVersion: 1, position: "bottom", autoHide: false, pinned: [], isStored: false, applications: [] }),
    "GET /api/v1/launcher": () => reply(200, { ...launcherView, applications: [], isStored: false }),
    "GET /api/v1/windows": () => reply(200, { items: [] }),
    ...overrides,
  };
}

function startShell(routes, location = null) {
  const { fetch, calls } = fakeFetch(routes);
  const api = createApi({ fetch, document: { cookie: "kos_bff_csrf=csrf-token" } });
  const root = { innerHTML: "" };
  const container = fakeElement("div");
  const host = createAppHost({ api, document: fakeDocument(), container, window: fakeWindow() });
  const { EventSource } = fakeEventSource();
  const app = createApp({ api, root, storage: memoryStorage(), EventSource, host, location });
  return { app, root, calls, container };
}

test("缺必填项与只声明页面的扩展都不出现 launcher 与 dock 入口，页面入口仍在", async () => {
  const { app, root, container } = startShell(routesFor());
  await app.start();

  assert.ok(!root.innerHTML.includes("data-dock-app="), "声明无效或未声明 App 的扩展出现在 dock 上");
  await app.handleClick({ target: elementWith({ "data-action": "launcher" }) });
  assert.ok(!root.innerHTML.includes("data-launcher-app="), "声明无效或未声明 App 的扩展出现在 launcher 上");
  assert.match(root.innerHTML, /data-launcher-not-ready/, "未就绪的组件没有给出原因");
  assert.match(root.innerHTML, /data-not-ready="broken"/);
  assert.match(root.innerHTML, /清单缺少必填项 entry/);

  // The page-only extension's page keeps its entry (DASH-C-22).
  assert.match(root.innerHTML, /data-nav="reports"/);
  assert.equal(container.children.length, 0, "没有 App 的扩展创建了窗口");
});

test("深链与启动都不为未安装的应用创建窗口", async () => {
  const { app, root, calls, container } = startShell(routesFor());
  await app.start();

  // A deep link the shell cannot resolve to an installed application leaves no
  // window and no registration request (DASH-C-30).
  const opened = calls.filter((call) => call.method === "POST" && call.path.endsWith("/windows"));
  assert.deepEqual(opened, []);
  assert.equal(container.children.length, 0);

  await app.launch("notes");
  assert.equal(container.children.length, 0, "未安装的应用创建了窗口");
  assert.match(root.innerHTML, /data-surface-error="app_unavailable"/);
  assert.ok(
    !calls.some((call) => call.method === "POST" && call.path === "/api/v1/apps/notes/windows"),
    "未安装的应用仍发出了窗口注册请求",
  );
});

test("深链只在已安装应用声明该路径时打开窗口", async () => {
  const routes = routesFor({
    "GET /api/v1/apps": () => reply(200, { items: [notesApp], notReady: [] }),
    "GET /api/v1/deep-links?path=%2Fnotes": () => reply(200, { app: "notes" }),
    "POST /api/v1/apps/notes/windows": () => reply(202, openAnswerFor(notesApp, { id: "win-notes" })),
  });
  const { app, container } = startShell(routes, { pathname: "/notes" });
  await app.start();
  assert.equal(container.children.length, 1, "声明了该深链的应用没有打开窗口");
  assert.equal(container.children[0].getAttribute("data-app"), "notes");
});

test("没有应用声明该路径时深链不解析、不创建窗口", async () => {
  const routes = routesFor({
    "GET /api/v1/apps": () => reply(200, { items: [notesApp], notReady: [] }),
    "GET /api/v1/deep-links?path=%2Funknown": () =>
      reply(404, { error: { code: "not_found", message: "没有已安装应用声明这个深链", stage: "authorize" } }),
  });
  const { app, root, container } = startShell(routes, { pathname: "/unknown" });
  await app.start();
  assert.equal(container.children.length, 0, "未声明的深链创建了窗口");
  assert.ok(!root.innerHTML.includes("data-window-error"), "深链拒绝被当成了窗口错误");
});
