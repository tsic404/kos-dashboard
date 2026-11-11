// DASH-TC-148 from the browser side: a layout written in one browser is read
// back in another browser of the same account. The read reports the viewport
// and the display set the browser has, adopts the front's adapted answer (a
// clamped rectangle, a record fallen back to the primary display) and never
// writes that answer back; a user change persists the session's layout
// projection and nothing of the session state around it (DASH-AC-70,
// DASH-C-27, DASH-C-74).

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
const LAYOUT_READ = "/api/v1/desktop/layout?viewport=800x600&display=";
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

// openAnswer renders one open answer: the session answer, the application
// instance that mounts the frame and the signed frame address (KOSD-C-73).
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

// startShell starts the shell over fakes at one browser viewport.
function startShell(routes, { width = 1440, height = 900 } = {}) {
  const { fetch, calls } = fakeFetch(routes);
  const api = createApi({ fetch, document: { cookie: "kos_bff_csrf=csrf-token" } });
  const root = { innerHTML: "" };
  const container = fakeElement("div");
  const window = fakeWindow({ innerWidth: width, innerHeight: height });
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
  return { app, root, container, calls };
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

// SESSION_NAMES are the session-only names no layout value may carry.
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

// assertLayoutOnly fails on anything but the layout collection and its records
// (DASH-C-27, DASH-C-74).
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

test("浏览器 B 按自己的视口与显示器读取适配后的布局，且不回写", async () => {
  const adapted = layoutView([recordOf({ x: 0, y: 0, width: 800, height: 600, display: "" })]);
  const routes = surfaces({ [LAYOUT_READ_ROUTE]: () => reply(200, adapted) });
  const { app, calls } = startShell(routes, { width: 800, height: 600 });

  await app.start();

  assert.ok(
    calls.some((call) => call.method === "GET" && call.path === LAYOUT_READ),
    "读取没有携带视口与浏览器报告的显示器集合",
  );
  assert.deepEqual(app.state.windowLayout, adapted, "读取到的适配布局没有被采用");
  assert.deepEqual(layoutWrites(calls), [], "适配后的读取被写回了偏好");
});

test("浏览器 A 的布局变化把会话投影写进账号偏好，不带会话状态", async () => {
  const chosen = recordOf();
  const routes = surfaces({
    "POST /api/v1/desktop/windows": () => reply(202, openAnswer(windowOf(), { schemaVersion: 2, records: [chosen] })),
    [LAYOUT_WRITE_ROUTE]: () => reply(204),
  });
  const { app, calls } = startShell(routes);

  await app.start();
  await app.handleClick(click({ "data-open-app": "notes" }));

  const writes = layoutWrites(calls);
  assert.equal(writes.length, 1, "打开窗口没有把布局写进账号偏好");
  assert.deepEqual(writes[0], { schemaVersion: 2, records: [chosen] }, "写入的不是会话的布局投影");
  assertLayoutOnly(writes[0]);
  // The session window carries z-order, focus and the address; the layout
  // record does not (DASH-C-74).
  assert.ok(!JSON.stringify(writes[0]).includes("/notes/one"), "布局写入带了窗口地址");
});

test("固定项经窗口会话写入并按布局记录归档", async () => {
  const opened = windowOf();
  const pinned = windowOf({ isPinned: true });
  const routes = surfaces({
    "POST /api/v1/desktop/windows": () => reply(202, openAnswer(opened, { schemaVersion: 2, records: [recordOf()] })),
    "PUT /api/v1/desktop/windows/window-1/pinned": (call) => {
      assert.deepEqual(JSON.parse(call.body), { pinned: true }, "固定请求体与契约不一致");
      return reply(200, sessionOf([pinned], { schemaVersion: 2, records: [recordOf({ pinned: true })] }));
    },
    [LAYOUT_WRITE_ROUTE]: () => reply(204),
  });
  const { app, container, calls } = startShell(routes);

  await app.start();
  await app.handleClick(click({ "data-open-app": "notes" }));
  assert.match(
    container.children[0].children[0].innerHTML,
    /data-command="window-pin"/,
    "窗口条没有固定入口",
  );

  await app.handleClick(click({ "data-command": "window-pin", "data-window": "window-1" }));

  const writes = layoutWrites(calls);
  assert.deepEqual(writes.at(-1), { schemaVersion: 2, records: [recordOf({ pinned: true })] });
  assertLayoutOnly(writes.at(-1));
});

test("用户再次改变布局时写的是会话投影，而不是读取到的 clamp", async () => {
  const clamped = recordOf({ x: 0, y: 0, width: 800, height: 600, display: "" });
  const chosen = recordOf({ x: 40, y: 24, width: 900, height: 640 });
  const routes = surfaces({
    [LAYOUT_READ_ROUTE]: () => reply(200, layoutView([clamped])),
    "POST /api/v1/desktop/windows": () => reply(202, openAnswer(windowOf(), { schemaVersion: 2, records: [chosen] })),
    [LAYOUT_WRITE_ROUTE]: () => reply(204),
  });
  const { app, calls } = startShell(routes, { width: 800, height: 600 });

  await app.start();
  assert.deepEqual(app.state.windowLayout.records, [clamped]);
  assert.deepEqual(layoutWrites(calls), [], "读取布局就产生了写入");

  await app.handleClick(click({ "data-open-app": "notes" }));

  const writes = layoutWrites(calls);
  assert.equal(writes.length, 1, "用户改变布局没有写入");
  assert.deepEqual(writes[0].records, [chosen], "clamp 后的读取被写回，覆盖了用户选择的布局");
});

// deferred holds one answered write open until the case releases it, so two
// session changes overlap exactly as two clicks can.
function deferred() {
  let resolve;
  const promise = new Promise((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

// settle lets the answer of one request run through to the next await.
function settle() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

// waitForCondition waits for one state the shell reaches after an answered
// request; assertions then run on a settled state, not on scheduling.
async function waitForCondition(predicate, label) {
  const deadline = Date.now() + 2000;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await settle();
  }
  assert.fail(`timed out waiting for ${label}`);
}

test("重叠的打开与关闭串行写入布局，落盘的是关窗后的最后一次会话状态", async () => {
  const chosen = recordOf();
  const firstStarted = deferred();
  const firstSettled = deferred();
  let stored = null;
  let writeCount = 0;
  const routes = surfaces({
    "POST /api/v1/desktop/windows": () => reply(202, openAnswer(windowOf(), { schemaVersion: 2, records: [chosen] })),
    "DELETE /api/v1/desktop/windows/window-1": () => reply(200, sessionOf([], { schemaVersion: 2, records: [] })),
    [LAYOUT_WRITE_ROUTE]: async (call) => {
      writeCount += 1;
      if (writeCount === 1) {
        firstStarted.resolve();
        await firstSettled.promise;
      }
      // The stored value is whatever the account's last answered write carried.
      stored = JSON.parse(JSON.parse(call.body).value);
      return reply(204);
    },
  });
  const { app } = startShell(routes);

  await app.start();
  const opening = app.handleClick(click({ "data-open-app": "notes" }));
  await firstStarted.promise;

  const closing = app.handleClick(click({ "data-command": "window-close", "data-window": "window-1" }));
  await waitForCondition(() => app.state.windows.length === 0, "关窗后的会话答案");
  await settle();
  assert.equal(writeCount, 1, "第一次布局写入未完成就发出了第二次写入");

  firstSettled.resolve();
  await opening;
  await closing;

  assert.equal(writeCount, 2, "关窗后的布局没有写入");
  assert.deepEqual(stored, { schemaVersion: 2, records: [] }, "先发的旧快照盖掉了关窗后的最后一次会话状态");
});

test("写入期间的新快照不被丢弃，最终落盘最后一次会话状态", async () => {
  const chosen = recordOf();
  const pinned = recordOf({ pinned: true });
  const firstStarted = deferred();
  const firstSettled = deferred();
  let stored = null;
  let writeCount = 0;
  const routes = surfaces({
    "POST /api/v1/desktop/windows": () => reply(202, openAnswer(windowOf(), { schemaVersion: 2, records: [chosen] })),
    "PUT /api/v1/desktop/windows/window-1/pinned": (call) => {
      assert.deepEqual(JSON.parse(call.body), { pinned: true }, "固定请求体与契约不一致");
      return reply(200, sessionOf([windowOf({ isPinned: true })], { schemaVersion: 2, records: [pinned] }));
    },
    [LAYOUT_WRITE_ROUTE]: async (call) => {
      writeCount += 1;
      if (writeCount === 1) {
        firstStarted.resolve();
        await firstSettled.promise;
      }
      stored = JSON.parse(JSON.parse(call.body).value);
      return reply(204);
    },
  });
  const { app } = startShell(routes);

  await app.start();
  const opening = app.handleClick(click({ "data-open-app": "notes" }));
  await firstStarted.promise;

  const pinning = app.handleClick(click({ "data-command": "window-pin", "data-window": "window-1" }));
  await waitForCondition(() => app.state.windows.some((window) => window.isPinned), "固定后的会话答案");
  await settle();
  assert.equal(writeCount, 1, "写入未完成就发出了新快照的写入");

  firstSettled.resolve();
  await opening;
  await pinning;

  assert.deepEqual(stored, { schemaVersion: 2, records: [pinned] }, "落盘的不是最后一次会话状态");
  assert.equal(writeCount, 2, "写入期间的新快照没有被写出");
});
