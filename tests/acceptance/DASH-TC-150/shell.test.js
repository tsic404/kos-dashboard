// DASH-TC-150 from the browser side: window interaction stays in the browser
// session. The shell writes only the front's layout projection — never the open
// set, z-order, focus or minimization — and once the control-plane database is
// unavailable it presents the refusal for the layout read and write while the
// rest of the desktop keeps rendering (DASH-AC-70, DASH-C-27, DASH-C-74,
// KOSD-C-61, KOSD-F-19).

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
// on this value and must not reach the layout preference (DASH-C-74).
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

// assertLayoutOnly fails on anything but the layout collection and its records.
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

test("打开、最小化、聚焦与最大化只把布局记录写进偏好", async () => {
  let state = "normal";
  // The front normalizes a minimized window's layout record to normal: the
  // minimization itself is session state (DASH-C-74).
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
    [LAYOUT_WRITE_ROUTE]: () => reply(204),
  });
  const { app, calls } = startShell(routes);

  await app.start();
  await app.handleClick(click({ "data-open-app": "notes" }));
  const opened = layoutWrites(calls).length;
  assert.equal(opened, 1, "打开窗口没有写入布局");

  await app.handleClick(click({ "data-command": "window-minimize", "data-window": "window-1" }));
  assert.equal(layoutWrites(calls).length, opened, "最小化写进了偏好");

  await app.handleClick(click({ "data-command": "window-restore", "data-window": "window-1" }));
  assert.equal(layoutWrites(calls).length, opened, "从最小化恢复写进了偏好");

  await app.handleClick(click({ "data-command": "window-focus", "data-window": "window-1" }));
  assert.equal(layoutWrites(calls).length, opened, "聚焦写进了偏好");

  await app.handleClick(click({ "data-command": "window-maximize", "data-window": "window-1" }));
  assert.equal(layoutWrites(calls).length, opened + 1, "最大化没有写入布局");
  assert.deepEqual(layoutWrites(calls).at(-1).records, [recordOf({ state: "maximized" })]);

  await app.handleClick(click({ "data-command": "workspace", "data-workspace": "work" }));
  assert.equal(layoutWrites(calls).length, opened + 1, "切换工作区写进了偏好");

  for (const value of layoutWrites(calls)) assertLayoutOnly(value);
});

test("布局不可读时呈现数据库拒绝，桌面其余表面照常渲染", async () => {
  const routes = surfaces({
    [LAYOUT_READ_ROUTE]: () =>
      reply(503, { error: { code: "database_unavailable", message: "控制面数据库不可用", stage: "read" } }),
  });
  const { app, root, calls } = startShell(routes);

  await app.start();

  assert.match(root.innerHTML, /data-surface-error="database_unavailable"/, "布局读取失败没有呈现");
  assert.ok(root.innerHTML.includes("控制面数据库不可用"), "布局读取失败没有带上原因");
  assert.match(root.innerHTML, /data-page="desktop"/, "布局读取失败清空了页面");
  assert.match(root.innerHTML, /data-dock/, "布局读取失败清空了 dock");
  assert.deepEqual(layoutWrites(calls), [], "读取失败后仍写了偏好");
});

test("数据库停止后布局写入被拒绝并呈现原因", async () => {
  const routes = surfaces({
    "POST /api/v1/desktop/windows": () =>
      reply(202, openAnswer(windowOf(), { schemaVersion: 2, records: [recordOf()] })),
    [LAYOUT_WRITE_ROUTE]: () =>
      reply(503, { error: { code: "database_unavailable", message: "控制面数据库不可用", stage: "write" } }),
  });
  const { root, app } = startShell(routes);

  await app.start();
  await app.handleClick(click({ "data-open-app": "notes" }));

  assert.match(root.innerHTML, /data-surface-error="database_unavailable"/, "布局写入被拒没有呈现");
  assert.ok(root.innerHTML.includes("控制面数据库不可用"), "布局写入被拒没有带上原因");
});

// deferred holds one answered write open until the case releases it, so the
// write outlives the session that started it.
function deferred() {
  let resolve;
  const promise = new Promise((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

// accountStore stands in for the control plane's preference store: the layout
// is kept per account, and a write belongs to the account whose session sent
// it, so a write that settles after a later login still lands on its sender
// (DASH-C-27). hold(index) holds the indexed write open before it answers.
function accountStore({ status = 204, envelope, hold = null } = {}) {
  const values = new Map();
  const writes = [];
  let account = "";
  const routes = {
    "GET /api/v1/auth/session": () => {
      account = "owner";
      return reply(200, { username: account, role: "Owner" });
    },
    "POST /api/v1/auth/login": (call) => {
      account = JSON.parse(call.body).username;
      return reply(200, { username: account, role: "Owner" });
    },
    [LAYOUT_WRITE_ROUTE]: async (call) => {
      // The account is the one whose session sent the request: a write that
      // settles after a later login is still the ended session's.
      const owner = account;
      const value = JSON.parse(JSON.parse(call.body).value);
      writes.push({ account: owner, value });
      if (hold) await hold(writes.length);
      if (status >= 200 && status < 300) values.set(owner, value);
      return reply(status, envelope);
    },
  };
  return { routes, values, writes };
}

// nextTick lets the answers of one request run through to the next await.
function nextTick() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

// waitForCondition waits for one state the shell reaches after an answered
// request, so assertions run on a settled state, not on scheduling.
async function waitForCondition(predicate, label) {
  const deadline = Date.now() + 2000;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await nextTick();
  }
  assert.fail(`timed out waiting for ${label}`);
}

// staleWriteCase signs in, starts one layout write, signs out and back in as
// nextAccount, then answers the ended session's write: its outcome must not
// speak for the new session. isHeld hands that answer back, so a case can act
// after it settles.
async function staleWriteCase(status, envelope, { isHeld = false, nextAccount = "owner" } = {}) {
  const writeStarted = deferred();
  const writeSettled = deferred();
  const store = accountStore({
    status,
    envelope,
    hold: async (index) => {
      if (index !== 1) return;
      writeStarted.resolve();
      await writeSettled.promise;
    },
  });
  const routes = surfaces({
    ...store.routes,
    "POST /api/v1/auth/logout": () => reply(204),
    "POST /api/v1/desktop/windows": () =>
      reply(202, openAnswer(windowOf(), { schemaVersion: 2, records: [recordOf()] })),
    [LAYOUT_READ_ROUTE]: () => reply(200, { schemaVersion: 2, records: [], isStored: false, isSupported: true }),
  });
  const { app, calls } = startShell(routes);

  await app.start();
  const opening = app.handleClick(click({ "data-open-app": "notes" }));
  await writeStarted.promise;

  await app.logout();
  assert.equal(app.state.session, null, "登出后会话仍在");
  await app.login(nextAccount, "secret");
  assert.ok(app.state.session, "重新登录没有建立会话");

  // settle answers the ended session's write and waits for the queue to drain.
  const settle = async () => {
    writeSettled.resolve();
    await opening;
  };
  if (!isHeld) await settle();

  return { app, calls, store, settle };
}

test("会话结束前发出的布局写入以 401 返回时不登出新会话", async () => {
  const { app } = await staleWriteCase(401, {
    error: { code: "unauthenticated", message: "会话已结束", stage: "authorize" },
  });

  assert.ok(app.state.session, "旧会话的 401 把新会话登出了");
  assert.equal(app.state.surfaceError, null, "旧会话的拒绝成了新会话的错误");
});

test("会话结束前发出的布局写入以 500 返回时不覆盖新会话的错误呈现", async () => {
  const { app } = await staleWriteCase(500, {
    error: { code: "internal", message: "控制面内部错误", stage: "write" },
  });

  assert.ok(app.state.session, "旧会话的 500 把新会话登出了");
  assert.equal(app.state.surfaceError, null, "旧会话的拒绝覆盖了新会话的错误呈现");
});

test("会话结束前发出的布局写入以 204 成功后换账号登录的同编码布局仍然写入", async () => {
  const { app, store, settle } = await staleWriteCase(204, undefined, { isHeld: true, nextAccount: "guest" });
  assert.equal(store.writes.length, 1, "旧会话的布局写入没有发出");

  await settle();
  assert.equal(store.values.has("guest"), false, "旧会话的写入落进了新账号的偏好");

  await app.handleClick(click({ "data-open-app": "notes" }));

  assert.equal(store.writes.length, 2, "旧会话 204 落定的布局被记成新账号已存的值，同编码布局没有写入");
  assert.equal(store.writes[1].account, "guest", "第二次写入没有落给新账号");
  assert.deepEqual(store.writes[1].value, store.writes[0].value, "两次写入的布局值不同，用例没有覆盖同编码跳过");
  assert.deepEqual(store.values.get("guest"), store.writes[0].value, "新账号的偏好里没有这次布局");
});

test("账号 A 的布局写入落定后换账号登录，同编码布局仍写进账号 B", async () => {
  const store = accountStore();
  const routes = surfaces({
    ...store.routes,
    "POST /api/v1/auth/logout": () => reply(204),
    "POST /api/v1/desktop/windows": () =>
      reply(202, openAnswer(windowOf(), { schemaVersion: 2, records: [recordOf()] })),
    [LAYOUT_READ_ROUTE]: () => reply(200, { schemaVersion: 2, records: [], isStored: false, isSupported: true }),
  });
  const { app } = startShell(routes);

  await app.start();
  await app.handleClick(click({ "data-open-app": "notes" }));
  assert.equal(store.writes.length, 1, "账号 A 的布局写入没有发出");
  assert.deepEqual(store.values.get("owner"), store.writes[0].value, "账号 A 的布局没有落定");

  await app.logout();
  await app.login("guest", "secret");
  await app.handleClick(click({ "data-open-app": "notes" }));

  assert.equal(store.writes.length, 2, "账号 A 落定的布局被当成账号 B 已存的值，同编码布局没有写进账号 B");
  assert.equal(store.writes[1].account, "guest", "第二次写入没有落给账号 B");
  assert.deepEqual(store.values.get("guest"), store.values.get("owner"), "账号 B 没有拿到同编码布局的落盘");
});

test("登出时排队中的旧会话快照不写给下一个账号", async () => {
  const writeStarted = deferred();
  const writeSettled = deferred();
  const store = accountStore({
    hold: async (index) => {
      if (index !== 1) return;
      writeStarted.resolve();
      await writeSettled.promise;
    },
  });
  const routes = surfaces({
    ...store.routes,
    "POST /api/v1/auth/logout": () => reply(204),
    "POST /api/v1/desktop/windows": () =>
      reply(202, openAnswer(windowOf(), { schemaVersion: 2, records: [recordOf()] })),
    "DELETE /api/v1/desktop/windows/window-1": () => reply(200, sessionOf([], { schemaVersion: 2, records: [] })),
    [LAYOUT_READ_ROUTE]: () => reply(200, { schemaVersion: 2, records: [], isStored: false, isSupported: true }),
  });
  const { app } = startShell(routes);

  await app.start();
  const opening = app.handleClick(click({ "data-open-app": "notes" }));
  await writeStarted.promise;

  const closing = app.handleClick(click({ "data-command": "window-close", "data-window": "window-1" }));
  await waitForCondition(() => app.state.windows.length === 0, "关窗后的会话答案");
  await nextTick();
  assert.equal(store.writes.length, 1, "首次布局写入未完成就发出了第二次写入");

  await app.logout();
  await app.login("guest", "secret");

  writeSettled.resolve();
  await opening;
  await closing;

  assert.equal(store.writes.length, 1, "登出时排队中的旧会话快照被写给了新账号");
  assert.equal(store.values.has("guest"), false, "新账号的偏好被写入了上一个账号的布局");
});
