// DASH-TC-161 from the browser side: after windows are opened, closed,
// minimized, focused and reordered, and workspaces are switched, the preference
// value the shell writes carries layout records only — no open set, z-order,
// focus or minimization (DASH-AC-70, DASH-C-27, DASH-C-74, KOSD-F-19).

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

// SESSION_NAMES are the session-only names no stored layout value may carry.
const SESSION_NAMES = [
  "windows",
  "openSet",
  "openWindows",
  "zOrder",
  "z",
  "focus",
  "focused",
  "isFocused",
  "minimized",
  "isMinimized",
  "isUnsaved",
  "address",
];

// ALLOWED_RECORD_FIELDS are the only fields one stored layout record carries.
const ALLOWED_RECORD_FIELDS = new Set([
  "application",
  "instance",
  "x",
  "y",
  "width",
  "height",
  "state",
  "workspace",
  "display",
  "pinned",
]);

// recordOf renders one layout record of the session projection.
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

// windowOf renders one desktop window; z-order, focus and the address live only
// on this value (DASH-C-74).
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
function sessionOf(windows, layout, overrides = {}) {
  return {
    windows,
    workspaces: ["main", "work"],
    currentWorkspace: "main",
    taskbar: windows,
    address: "",
    appInstances: Object.fromEntries(windows.map((window) => [window.id, `instance-${window.instance}`])),
    layout,
    ...overrides,
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
  return { app, calls };
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

// assertLayoutOnly fails on anything but the layout collection and its records;
// the walk rejects the session-only names at any depth (DASH-C-27, KOSD-F-19).
function assertLayoutOnly(value) {
  assert.deepEqual(Object.keys(value).sort(), ["records", "schemaVersion"], "布局值带了布局集合以外的字段");
  for (const record of value.records) {
    for (const key of Object.keys(record)) {
      assert.ok(ALLOWED_RECORD_FIELDS.has(key), `布局记录带了字段 ${key}`);
    }
    assert.notEqual(record.state, "minimized", "最小化状态进入了布局记录");
  }
  const walk = (node, path) => {
    if (Array.isArray(node)) {
      node.forEach((item, index) => walk(item, `${path}[${index}]`));
      return;
    }
    if (!node || typeof node !== "object") return;
    for (const [key, nested] of Object.entries(node)) {
      assert.ok(!SESSION_NAMES.includes(key), `布局值在 ${path}.${key} 带了会话字段`);
      walk(nested, `${path}.${key}`);
    }
  };
  walk(value, "");
}

test("窗口交互后偏好值只有布局记录，没有打开集合、z-order、焦点或最小化", async () => {
  let state = "normal";
  // The front normalizes a minimized window's layout record to normal
  // (DASH-C-27, DASH-C-74).
  const projection = () => ({
    schemaVersion: 2,
    records: [recordOf({ state: state === "maximized" ? "maximized" : "normal" })],
  });
  const answer = () => sessionOf([windowOf({ state })], projection());
  const routes = surfaces({
    "POST /api/v1/desktop/windows": () => reply(202, openAnswer(windowOf(), projection())),
    "PUT /api/v1/desktop/windows/window-1/state": (call) => {
      state = JSON.parse(call.body).state;
      return reply(200, { ...answer(), window: windowOf({ state }) });
    },
    "POST /api/v1/desktop/windows/window-1/focus": () => reply(200, answer()),
    "POST /api/v1/desktop/workspaces": (call) =>
      reply(200, { ...answer(), currentWorkspace: JSON.parse(call.body).workspace }),
    "DELETE /api/v1/desktop/windows/window-1": () =>
      reply(200, sessionOf([], { schemaVersion: 2, records: [] })),
    [LAYOUT_WRITE_ROUTE]: () => reply(204),
  });
  const { app, calls } = startShell(routes);

  await app.start();
  await app.handleClick(click({ "data-open-app": "notes" }));
  assert.equal(layoutWrites(calls).length, 1, "打开窗口没有写入布局");

  // Focus reorders the session's z-order; the layout value does not change.
  await app.handleClick(click({ "data-command": "window-focus", "data-window": "window-1" }));
  assert.equal(layoutWrites(calls).length, 1, "聚焦改变了偏好值");

  await app.handleClick(click({ "data-command": "window-minimize", "data-window": "window-1" }));
  assert.equal(layoutWrites(calls).length, 1, "最小化改变了偏好值");

  await app.handleClick(click({ "data-command": "window-restore", "data-window": "window-1" }));
  assert.equal(layoutWrites(calls).length, 1, "从最小化恢复改变了偏好值");

  await app.handleClick(click({ "data-command": "window-maximize", "data-window": "window-1" }));
  assert.equal(layoutWrites(calls).length, 2, "最大化没有写入布局");
  assert.deepEqual(layoutWrites(calls).at(-1).records, [recordOf({ state: "maximized" })]);

  await app.handleClick(click({ "data-command": "window-restore", "data-window": "window-1" }));
  assert.equal(layoutWrites(calls).length, 3, "恢复没有写入布局");

  await app.handleClick(click({ "data-command": "workspace", "data-workspace": "work" }));
  assert.equal(layoutWrites(calls).length, 3, "切换工作区改变了偏好值");

  await app.handleClick(click({ "data-command": "window-close", "data-window": "window-1" }));
  assert.equal(layoutWrites(calls).length, 4, "关闭窗口没有更新布局");
  assert.deepEqual(layoutWrites(calls).at(-1).records, [], "关闭后偏好里还留着窗口记录");

  for (const value of layoutWrites(calls)) assertLayoutOnly(value);
});

test("读取的布局值同样只有布局记录", async () => {
  const stored = { schemaVersion: 2, records: [recordOf({ display: "left" })] };
  const routes = surfaces({ [LAYOUT_READ_ROUTE]: () => reply(200, { ...stored, isStored: true, isSupported: true }) });
  const { app, calls } = startShell(routes);

  await app.start();

  assert.deepEqual(app.state.windowLayout.records, stored.records, "读取到的布局不可用");
  assert.deepEqual(layoutWrites(calls), [], "读取布局就产生了写入");
});
