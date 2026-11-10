// DASH-TC-149 from the browser side: a layout write this product cannot keep
// is refused with the reason the control plane gave and the previously read
// layout stays the one the shell presents; a migratable legacy value is read as
// the current version without being re-encoded; an unsupported higher version
// answers the default layout with its reason and is never overwritten
// (DASH-AC-70, DASH-C-27).

import assert from "node:assert/strict";
import test from "node:test";

import { createApi } from "../../../pkg/bff/assets/shell/api.js";
import { createApp } from "../../../pkg/bff/assets/shell/app.js";
import { appsAnswer, desktopPage, dockView, launcherView, notesApp, openSessionFor, registry } from "../../shell/fixtures.js";
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

const LAYOUT_WRITE = "/api/v1/preferences/kos-dashboard/windowLayout";
const LAYOUT_WRITE_ROUTE = "PUT " + LAYOUT_WRITE;
const LAYOUT_READ = "/api/v1/desktop/layout?viewport=1440x900&display=";
const LAYOUT_READ_ROUTE = "GET " + LAYOUT_READ;

// recordOf renders one layout record the front projects for a session window.
function recordOf(overrides = {}) {
  return {
    application: "notes",
    instance: 1,
    x: 40,
    y: 24,
    width: 900,
    height: 640,
    state: "normal",
    workspace: "main",
    ...overrides,
  };
}

// layoutView renders the front's adapted read answer (DASH-C-27).
function layoutView(records, overrides = {}) {
  return { schemaVersion: 2, records, isStored: true, isSupported: true, ...overrides };
}

// windowOf renders one desktop window of the browser session.
function windowOf(overrides = {}) {
  return {
    id: "window-1",
    application: "notes",
    instance: 1,
    state: "normal",
    bounds: { x: 40, y: 24, width: 900, height: 640 },
    restore: { x: 40, y: 24, width: 900, height: 640 },
    workspace: "main",
    z: 1,
    isFocused: true,
    address: "/notes/one",
    isUnsaved: false,
    ...overrides,
  };
}

// sessionOf renders one window-session answer carrying the layout projection.
function sessionOf(windows, layout) {
  return {
    windows,
    workspaces: ["main"],
    currentWorkspace: "main",
    taskbar: windows,
    address: "",
    appInstances: Object.fromEntries(windows.map((window) => [window.id, `instance-${window.instance}`])),
    layout,
  };
}

// openAnswer renders one open answer with its application instance and frame.
function openAnswer(window, layout) {
  const base = openSessionFor(notesApp, { window });
  return { ...base, ...sessionOf([window], layout) };
}

// surfaces answers the reads the shell makes before it can open a window.
function surfaces(overrides = {}) {
  return {
    "GET /api/v1/auth/session": () => reply(200, { username: "owner", role: "Owner" }),
    "GET /api/v1/pages": () => reply(200, registry),
    "GET /api/v1/desktop": () => reply(200, desktopPage),
    "GET /api/v1/apps": () => reply(200, appsAnswer),
    "GET /api/v1/dock": () => reply(200, dockView),
    "GET /api/v1/launcher": () => reply(200, launcherView),
    "GET /api/v1/desktop/windows": () => reply(200, sessionOf([], { schemaVersion: 2, records: [] })),
    ...overrides,
  };
}

// startShell starts the shell over fakes.
function startShell(routes) {
  const { fetch, calls } = fakeFetch(routes);
  const api = createApi({ fetch, document: { cookie: "kos_bff_csrf=csrf-token" } });
  const root = { innerHTML: "" };
  const container = fakeElement("div");
  const window = fakeWindow();
  const { EventSource } = fakeEventSource();
  const app = createApp({
    api,
    root,
    storage: memoryStorage(),
    EventSource,
    document: fakeDocument(),
    container,
    window,
    location: { pathname: "/", origin: "https://console.example.test" },
  });
  return { app, root, calls };
}

// click sends one click at the shell.
function click(attributes) {
  return { target: elementWith(attributes) };
}

// layoutWrites lists the layout values the shell wrote, decoded.
function layoutWrites(calls) {
  return calls
    .filter((call) => call.method === "PUT" && call.path === LAYOUT_WRITE)
    .map((call) => JSON.parse(JSON.parse(call.body).value));
}

test("超过 50 条的布局被拒绝：原因呈现，已读布局不被替换", async () => {
  const stored = layoutView([recordOf(), recordOf({ instance: 2 })]);
  const oversized = {
    schemaVersion: 2,
    records: Array.from({ length: 51 }, (_, index) => recordOf({ instance: index + 1 })),
  };
  const routes = surfaces({
    [LAYOUT_READ_ROUTE]: () => reply(200, stored),
    "POST /api/v1/desktop/windows": () => reply(202, openAnswer(windowOf(), oversized)),
    [LAYOUT_WRITE_ROUTE]: () =>
      reply(400, {
        error: { code: "invalid_request", message: "布局记录 51 条超过上限 50 条", stage: "validate" },
      }),
  });
  const { app, root, calls } = startShell(routes);

  await app.start();
  await app.handleClick(click({ "data-open-app": "notes" }));

  assert.equal(layoutWrites(calls)[0].records.length, 51, "写入没有带上会话的 51 条记录");
  assert.match(root.innerHTML, /data-surface-error="invalid_request"/, "超限拒绝没有呈现");
  assert.ok(root.innerHTML.includes("超过上限 50 条"), "超限拒绝没有带上原因");
  assert.deepEqual(app.state.windowLayout.records, stored.records, "被拒绝的写入替换了已读布局");
});

test("超过序列化上限的布局被拒绝并呈现字节上限", async () => {
  const stored = layoutView([recordOf()]);
  const oversized = {
    schemaVersion: 2,
    records: [recordOf({ display: "d".repeat(70 * 1024) })],
  };
  const routes = surfaces({
    [LAYOUT_READ_ROUTE]: () => reply(200, stored),
    "POST /api/v1/desktop/windows": () => reply(202, openAnswer(windowOf(), oversized)),
    [LAYOUT_WRITE_ROUTE]: () =>
      reply(400, {
        error: {
          code: "invalid_request",
          message: "布局值 71791 字节超过序列化上限 65536 字节",
          stage: "validate",
        },
      }),
  });
  const { root, app } = startShell(routes);

  await app.start();
  await app.handleClick(click({ "data-open-app": "notes" }));

  assert.match(root.innerHTML, /data-surface-error="invalid_request"/, "序列化上限拒绝没有呈现");
  assert.ok(root.innerHTML.includes("65536"), "序列化上限拒绝没有带上字节上限");
});

test("迁移后的当前版本布局可读，且读取不改写存储", async () => {
  const migrated = layoutView([recordOf({ state: "normal" })]);
  const routes = surfaces({ [LAYOUT_READ_ROUTE]: () => reply(200, migrated) });
  const { app, calls } = startShell(routes);

  await app.start();

  assert.equal(app.state.windowLayout.schemaVersion, 2, "迁移后的布局不是当前版本");
  assert.deepEqual(app.state.windowLayout.records, migrated.records, "迁移后的布局不可读");
  assert.deepEqual(layoutWrites(calls), [], "迁移后的读取写回了存储");
});

test("读到更高版本时使用默认布局、呈现原因且不覆盖存储", async () => {
  const unsupported = {
    schemaVersion: 2,
    records: [],
    isStored: true,
    isSupported: false,
    reason: "布局版本 3 高于当前支持的版本 2",
  };
  const routes = surfaces({ [LAYOUT_READ_ROUTE]: () => reply(200, unsupported) });
  const { app, root, calls } = startShell(routes);

  await app.start();

  assert.deepEqual(app.state.windowLayout.records, [], "不支持的高版本没有回落到默认布局");
  assert.match(root.innerHTML, /data-surface-error="layout_unsupported"/, "不支持的高版本没有呈现原因");
  assert.ok(root.innerHTML.includes("布局版本 3"), "原因没有带上存储的版本");
  assert.deepEqual(layoutWrites(calls), [], "不支持的高版本被覆盖了");
});

test("旧版本对更高版本的写入被拒绝并提示升级", async () => {
  const routes = surfaces({
    "POST /api/v1/desktop/windows": () =>
      reply(202, openAnswer(windowOf(), { schemaVersion: 2, records: [recordOf()] })),
    [LAYOUT_WRITE_ROUTE]: () =>
      reply(409, {
        error: {
          code: "failed_precondition",
          message: "存储的布局版本 3 高于当前支持的版本 2，请升级桌面后再修改",
          stage: "validate",
        },
      }),
  });
  const { root, app } = startShell(routes);

  await app.start();
  await app.handleClick(click({ "data-open-app": "notes" }));

  assert.match(root.innerHTML, /data-surface-error="failed_precondition"/, "版本冲突没有呈现");
  assert.ok(root.innerHTML.includes("升级"), "版本冲突没有提示升级");
});
