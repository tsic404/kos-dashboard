// DASH-TC-163 from the browser side: the page navigation lists pages alone
// while an `App` contribution appears on the launcher and the dock; rendering a
// page creates no frame, and the one frame the shell mounts is the `App`'s
// window; an account without the page's permission gets no entry at all
// (DASH-AC-71, DASH-C-22, KOSD-C-18, KOSD-C-59).

import assert from "node:assert/strict";
import test from "node:test";

import { createApi } from "../../../pkg/bff/assets/shell/api.js";
import { createApp } from "../../../pkg/bff/assets/shell/app.js";
import { dockView, launcherView, notesApp, openSessionFor } from "../../shell/fixtures.js";
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

// reportsPage is the page-only extension's page.
const reportsPage = {
  id: "reports",
  title: "报表",
  location: "panel",
  requiredRole: "Member",
  readOnly: false,
  sources: ["object"],
  items: [
    {
      id: "reports",
      label: "报表",
      source: "object",
      observedAt: "2026-11-08T04:00:00Z",
      stale: false,
      value: [{ name: "ink" }],
    },
  ],
  actions: [],
};

// registry holds the pages the session may see; the application is never one.
const registry = {
  items: [{ id: reportsPage.id, title: reportsPage.title, location: reportsPage.location, sources: ["object"], items: [], actions: [] }],
};

const emptyRegistry = { items: [] };

function routesFor(overrides = {}) {
  return {
    "GET /api/v1/auth/session": () => reply(200, { username: "owner", role: "Owner" }),
    "GET /api/v1/pages": () => reply(200, registry),
    "GET /api/v1/pages/reports": () => reply(200, reportsPage),
    "GET /api/v1/apps": () => reply(200, { items: [notesApp], notReady: [] }),
    "GET /api/v1/dock": () => reply(200, { ...dockView, pinned: [], applications: [notesApp.id] }),
    "GET /api/v1/launcher": () => reply(200, { ...launcherView, applications: [notesApp.id] }),
    "GET /api/v1/windows": () => reply(200, { items: [] }),
    "POST /api/v1/desktop/windows": () => reply(202, openSessionFor(notesApp)),
    ...overrides,
  };
}

function startShell(routes) {
  const { fetch, calls } = fakeFetch(routes);
  const api = createApi({ fetch, document: { cookie: "kos_bff_csrf=csrf-token" } });
  const root = { innerHTML: "" };
  const container = fakeElement("div");
  const { EventSource } = fakeEventSource();
  const app = createApp({
    api,
    root,
    storage: memoryStorage(),
    EventSource,
    container,
    document: fakeDocument(),
    window: fakeWindow(),
    location: { pathname: "/", origin: "https://console.example.test" },
  });
  return { app, root, calls, container };
}

test("页面导航只列页面，App 只出现在 launcher 与 dock", async () => {
  const { app, root } = startShell(routesFor());
  await app.start();

  assert.match(root.innerHTML, /data-nav="reports"/);
  assert.ok(!root.innerHTML.includes(`data-nav="${notesApp.id}"`), "应用出现在页面导航里");

  // The `App` keeps its launcher entry: the two declarations are independent
  // (KOSD-C-18).
  await app.handleClick({ target: elementWith({ "data-action": "launcher" }) });
  assert.match(root.innerHTML, new RegExp(`data-launcher-app="${notesApp.id}"`));
});

test("页面渲染不创建 iframe，唯一的 iframe 来自 App 窗口", async () => {
  const { app, root, container } = startShell(routesFor());
  await app.start();

  assert.ok(!root.innerHTML.includes("<iframe"), "页面渲染出了 iframe");
  assert.equal(container.children.length, 0, "页面渲染创建了窗口");

  await app.launch(notesApp.id);
  assert.equal(container.children.length, 1, "声明了 App 的应用没有挂载窗口");
  const windowNode = container.children[0];
  assert.equal(windowNode.tagName, "SECTION");
  assert.equal(windowNode.getAttribute("data-app"), notesApp.id);
  const frame = windowNode.children[1].children[0];
  assert.equal(frame.tagName, "IFRAME", "App 窗口没有挂载 iframe");
});

test("无页面权限的账号看不到入口，直接导航被拒绝", async () => {
  const { app, root } = startShell(routesFor({ "GET /api/v1/pages": () => reply(200, emptyRegistry) }));
  await app.start();
  assert.ok(!root.innerHTML.includes("data-nav="), "无权限账号看到了页面入口");
  await app.navigate("reports");
  assert.match(root.innerHTML, /data-code="page_not_found"/);
  assert.ok(!root.innerHTML.includes('data-page="reports"'), "无权限账号仍渲染了页面");
});
