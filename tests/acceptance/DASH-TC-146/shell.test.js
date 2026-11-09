// DASH-TC-146 from the browser side: a deep link resolves to its installed
// application, opens or focuses the one window that shows it, and the address
// the browser writes follows the focused window. A single-mode application keeps
// one window; a multi-mode application has one window per deep link with its own
// instance key (DASH-AC-69, DASH-C-74).

import assert from "node:assert/strict";
import test from "node:test";

import { createApi } from "../../../pkg/bff/assets/shell/api.js";
import { createApp } from "../../../pkg/bff/assets/shell/app.js";
import { createWindowSession, windowsOf } from "../../../pkg/bff/assets/shell/windows.js";
import { desktopPage, dockView, filesApp, launcherView, openSessionFor, registry, sessionOf as fixtureSession } from "../../shell/fixtures.js";
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

// windowOf renders one session window.
function windowOf(id, application, instance, address) {
  return {
    id,
    application,
    instance,
    state: "normal",
    bounds: { x: 0, y: 0, width: 800, height: 600 },
    restore: { x: 0, y: 0, width: 800, height: 600 },
    workspace: "main",
    z: instance,
    isFocused: true,
    address,
  };
}

// sessionOf renders one window-session answer.
function sessionOf(windows, address) {
  return {
    windows,
    workspaces: ["main"],
    currentWorkspace: "main",
    taskbar: windows,
    address,
    appInstances: Object.fromEntries(windows.map((window) => [window.id, "instance-" + window.id])),
  };
}

// startClient starts the window client over the routes of a case.
function startClient(routes) {
  const { fetch, calls } = fakeFetch(routes);
  const api = createApi({ fetch, document: { cookie: "kos_bff_csrf=csrf-token" } });
  return { session: createWindowSession({ api }), calls };
}

test("单例深链只聚焦；地址跟随焦点与关闭", async () => {
  const one = windowOf("window-1", "files", 1, "/files/one");
  const two = { ...one, address: "/files/two" };
  const routes = {
    "GET /api/v1/deep-links?path=%2Ffiles%2Fone": () => reply(200, { app: "files" }),
    "GET /api/v1/deep-links?path=%2Ffiles%2Ftwo": () => reply(200, { app: "files" }),
    "POST /api/v1/desktop/windows": (call) => {
      const body = JSON.parse(call.body);
      if (body.path === "/files/one") return reply(202, { ...sessionOf([one], "/files/one"), isNew: true });
      return reply(202, { ...sessionOf([two], "/files/two"), isNew: false });
    },
    "POST /api/v1/desktop/windows/window-1/focus": () => reply(200, sessionOf([two], "/files/two")),
  };
  const { session, calls } = startClient(routes);

  await session.openDeepLink("/files/one");
  assert.equal(windowsOf(session.current()).length, 1);
  assert.equal(session.current().address, "/files/one");

  const secondOpen = await session.openDeepLink("/files/two");
  assert.equal(windowsOf(session.current()).length, 1, "单例深链打开了第二个窗口");
  assert.equal(secondOpen.isNew, false, "单例深链没有只聚焦");
  assert.equal(session.current().address, "/files/two");

  // The deep link travels in the open request, so the window layer never builds
  // a second address source.
  const opens = calls.filter((call) => call.method === "POST" && call.path === "/api/v1/desktop/windows");
  assert.deepEqual(opens.map((call) => JSON.parse(call.body).path), ["/files/one", "/files/two"]);
});

test("多例两个深链各窗口各有实例键；地址随焦点更新", async () => {
  const a = windowOf("window-a", "notes", 1, "/notes/a");
  const b = windowOf("window-b", "notes", 2, "/notes/b");
  const routes = {
    "GET /api/v1/deep-links?path=%2Fnotes%2Fa": () => reply(200, { app: "notes" }),
    "GET /api/v1/deep-links?path=%2Fnotes%2Fb": () => reply(200, { app: "notes" }),
    "POST /api/v1/desktop/windows": (call) => {
      const body = JSON.parse(call.body);
      if (body.path === "/notes/a") return reply(202, { ...sessionOf([a], "/notes/a"), isNew: true });
      return reply(202, { ...sessionOf([a, b], "/notes/b"), isNew: true });
    },
    "POST /api/v1/desktop/windows/window-a/focus": () => reply(200, sessionOf([a, b], "/notes/a")),
  };
  const { session } = startClient(routes);

  await session.openDeepLink("/notes/a");
  await session.openDeepLink("/notes/b");
  const windows = windowsOf(session.current());
  assert.equal(windows.length, 2);
  assert.notEqual(windows[0].instance, windows[1].instance);
  assert.equal(session.current().address, "/notes/b");

  // A back step focuses the earlier window; the address follows it and the open
  // set is unchanged.
  await session.focus("window-a");
  assert.equal(session.current().address, "/notes/a");
  assert.equal(windowsOf(session.current()).length, 2);
});

test("地址写入浏览器历史，关闭后跟随新的焦点", async () => {
  const a = windowOf("window-a", "notes", 1, "/notes/a");
  const b = windowOf("window-b", "notes", 2, "/notes/b");
  const routes = {
    "GET /api/v1/deep-links?path=%2Fnotes%2Fa": () => reply(200, { app: "notes" }),
    "GET /api/v1/deep-links?path=%2Fnotes%2Fb": () => reply(200, { app: "notes" }),
    "POST /api/v1/desktop/windows": (call) =>
      JSON.parse(call.body).path === "/notes/a"
        ? reply(202, { ...sessionOf([a], "/notes/a"), isNew: true })
        : reply(202, { ...sessionOf([a, b], "/notes/b"), isNew: true }),
    "DELETE /api/v1/desktop/windows/window-b": () => reply(200, sessionOf([a], "/notes/a")),
  };
  const { session } = startClient(routes);
  await session.openDeepLink("/notes/a");
  await session.openDeepLink("/notes/b");

  const pushed = [];
  const location = { pathname: "/", search: "" };
  session.syncAddress(location, { pushState: (state, _title, path) => pushed.push(path) });
  assert.deepEqual(pushed, ["/notes/b"]);

  await session.close("window-b");
  session.syncAddress(location, { pushState: (state, _title, path) => pushed.push(path) });
  assert.deepEqual(pushed, ["/notes/b", "/notes/a"]);
});

// --- The desktop surface -------------------------------------------------
//
// The client tests above pin the requests; these drive the shell the browser
// runs, where the address and the open set have to agree after a deep link and
// after a back step (DASH-AC-69, DASH-C-74).

// surfaces answers the reads the shell makes before it can open a window.
function surfaces(overrides = {}) {
  return {
    "GET /api/v1/auth/session": () => reply(200, { username: "owner", role: "Owner" }),
    "GET /api/v1/pages": () => reply(200, registry),
    "GET /api/v1/desktop": () => reply(200, desktopPage),
    "GET /api/v1/apps": () => reply(200, { items: [filesApp], notReady: [] }),
    "GET /api/v1/dock": () => reply(200, dockView),
    "GET /api/v1/launcher": () => reply(200, launcherView),
    "GET /api/v1/desktop/windows": () => reply(200, fixtureSession([])),
    ...overrides,
  };
}

// startDesktop starts the shell at one address.
function startDesktop(routes, location = { pathname: "/", origin: "https://console.example.test" }) {
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
    location,
  });
  return { app, root, container, window, calls };
}

function click(attributes) {
  return { target: elementWith(attributes) };
}

test("深链给窗口地址，后退到前一地址时聚焦该窗口且地址跟随", async () => {
  const one = { ...windowOf("window-a", "files", 1, "/files/one") };
  const two = { ...windowOf("window-b", "files", 2, "/files/two"), isFocused: false, z: 2 };
  const opened = {
    ...fixtureSession([one, two], { address: "/files/one" }),
    window: one,
    instance: { id: "instance-1", origin: "https://files.apps.example.test", handshakeNonce: "nonce-1" },
    sandbox: "allow-scripts allow-same-origin",
    frame: "https://files.apps.example.test/app-assets/files-ext/files/1.0.0/sha256:1111111111111111111111111111111111111111111111111111111111111111/index.html?kos-exp=1&kos-sig=signature",
    isNew: true,
  };
  const { app, window, calls } = startDesktop(
    surfaces({
      "GET /api/v1/deep-links?path=%2Ffiles%2Fone": () => reply(200, { app: "files" }),
      "POST /api/v1/desktop/windows": () => reply(202, opened),
      "POST /api/v1/desktop/windows/window-b/focus": () =>
        reply(200, { ...fixtureSession([one, two], { address: "/files/two" }), window: { ...two, isFocused: true } }),
    }),
    { pathname: "/files/one", origin: "https://console.example.test" },
  );
  await app.start();

  // The page already sits at the deep link's address, so nothing is pushed; the
  // window that shows it is the one the front focused.
  assert.deepEqual(window.history.entries, []);
  assert.equal(app.state.windowSession.address, "/files/one");

  // A back step returns to the other window's address: the shell focuses the
  // window that shows it, and the address follows (DASH-C-74).
  await app.addressChanged("/files/two");
  assert.ok(
    calls.some((call) => call.method === "POST" && call.path === "/api/v1/desktop/windows/window-b/focus"),
    "后退没有聚焦显示该地址的窗口",
  );
  assert.deepEqual(window.history.entries, ["/files/two"]);
  assert.equal(app.state.windowSession.address, "/files/two");
});

test("单例应用两次深链只有一个窗口，第二次只聚焦并更新地址", async () => {
  const first = openSessionFor(filesApp, { address: "/files/one" });
  const one = first.window;
  const second = {
    ...openSessionFor(filesApp, { address: "/files/two", isNew: false, window: { ...one, address: "/files/two" } }),
  };
  const answers = [first, second];
  const routes = surfaces({
    "GET /api/v1/deep-links?path=%2Ffiles%2Fone": () => reply(200, { app: "files" }),
    "GET /api/v1/deep-links?path=%2Ffiles%2Ftwo": () => reply(200, { app: "files" }),
    "POST /api/v1/desktop/windows": () => reply(202, answers.shift()),
  });

  const page = startDesktop(routes, { pathname: "/files/one", origin: "https://console.example.test" });
  await page.app.start();
  assert.equal(page.container.children.length, 1);
  const opened = page.container.children[0];

  // The second deep link is a second page load: the front answers the one live
  // window and focuses it, so the page mounts that window and nothing more.
  const again = startDesktop(routes, { pathname: "/files/two", origin: "https://console.example.test" });
  await again.app.start();
  assert.equal(again.container.children.length, 1, "单例深链打开了第二个窗口");
  assert.equal(again.container.children[0].getAttribute("data-window"), "window-1");
  // The page already sits at that address, so nothing is pushed; the address the
  // front answered is the one the browser shows.
  assert.deepEqual(again.window.history.entries, []);
  assert.equal(again.app.state.windowSession.address, "/files/two");
  assert.ok(opened);
});

test("状态转移把焦点移到该窗口时地址跟着更新", async () => {
  const { app, window, calls } = startDesktop(
    surfaces({
      "POST /api/v1/desktop/windows": () => reply(202, openSessionFor(filesApp)),
      "PUT /api/v1/desktop/windows/window-1/state": () =>
        reply(200, {
          ...fixtureSession([{ ...windowOf(filesApp), address: "/files/a", isFocused: true }], { address: "/files/a" }),
          window: { ...windowOf(filesApp), address: "/files/a" },
        }),
    }),
    { pathname: "/", origin: "https://console.example.test" },
  );
  await app.start();
  await app.handleClick(click({ "data-open-app": "files" }));

  await app.handleClick(click({ "data-command": "window-maximize", "data-window": "window-1" }));
  assert.ok(
    calls.some((call) => call.method === "PUT" && call.path === "/api/v1/desktop/windows/window-1/state"),
    "没有发出状态请求",
  );
  assert.deepEqual(window.history.entries, ["/files/a"], "状态转移后地址没有跟随焦点");
});
