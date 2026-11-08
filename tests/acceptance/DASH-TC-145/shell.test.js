// DASH-TC-145 from the browser side: the window client sends every state the
// contract fixes, snaps to an edge with the viewport, restores the original
// rectangle, and reports the one close veto instead of dropping the window
// (DASH-AC-69, DASH-C-74).

import assert from "node:assert/strict";
import test from "node:test";

import { createApi } from "../../../pkg/bff/assets/shell/api.js";
import { createApp } from "../../../pkg/bff/assets/shell/app.js";
import { createWindowSession, windowById } from "../../../pkg/bff/assets/shell/windows.js";
import { desktopPage, dockView, filesApp, launcherView, openSessionFor, registry, sessionOf, windowOf } from "../../shell/fixtures.js";
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

// bounds is the original rectangle the case restores to.
const bounds = { x: 40, y: 24, width: 800, height: 600 };

// windowAt renders one session window in a state.
function windowAt(state, nextBounds = bounds) {
  return {
    id: "window-1",
    application: "files",
    instance: 1,
    state,
    bounds: nextBounds,
    restore: bounds,
    workspace: "main",
    z: 1,
    isFocused: true,
  };
}

// sessionAnswer renders one window-session answer.
function sessionAnswer(window, address = "") {
  return {
    windows: [window],
    workspaces: ["main"],
    currentWorkspace: "main",
    taskbar: window.state === "error" ? [] : [window],
    address,
    appInstances: { "window-1": "instance-1" },
  };
}

// startClient starts the window client over the routes of a case.
function startClient(routes) {
  const { fetch, calls } = fakeFetch(routes);
  const api = createApi({ fetch, document: { cookie: "kos_bff_csrf=csrf-token" } });
  return { session: createWindowSession({ api }), calls };
}

test("状态请求与吸附视口正确，恢复回原边界", async () => {
  const routes = {
    "POST /api/v1/desktop/windows": () => reply(202, sessionAnswer(windowAt("normal"))),
    "PUT /api/v1/desktop/windows/window-1/state": (call) => {
      const state = JSON.parse(call.body).state;
      if (state === "snapped") return reply(200, sessionAnswer(windowAt("snapped", { x: 0, y: 0, width: 720, height: 900 })));
      return reply(200, sessionAnswer(windowAt(state === "normal" ? "normal" : state)));
    },
  };
  const { session, calls } = startClient(routes);
  await session.open("files");
  assert.equal(windowById(session.current(), "window-1").state, "normal");

  await session.minimize("window-1");
  assert.equal(windowById(session.current(), "window-1").state, "minimized");
  await session.restore("window-1");
  await session.maximize("window-1");
  await session.fullscreen("window-1");
  await session.snap("window-1", "left", { x: 0, y: 0, width: 1440, height: 900 });
  assert.equal(windowById(session.current(), "window-1").state, "snapped");
  assert.deepEqual(windowById(session.current(), "window-1").bounds, { x: 0, y: 0, width: 720, height: 900 });
  await session.restore("window-1");
  assert.deepEqual(windowById(session.current(), "window-1").bounds, bounds);

  const states = calls
    .filter((call) => call.path === "/api/v1/desktop/windows/window-1/state")
    .map((call) => JSON.parse(call.body));
  assert.deepEqual(
    states.map((body) => body.state),
    ["minimized", "normal", "maximized", "fullscreen", "snapped", "normal"],
    "状态请求序列与契约不一致",
  );
  const snapped = states.find((body) => body.state === "snapped");
  assert.equal(snapped.edge, "left");
  assert.deepEqual(snapped.viewport, { x: 0, y: 0, width: 1440, height: 900 });
});

test("未保存时第一次关闭被否决，窗口仍在；第二次关闭移除窗口", async () => {
  let vetoed = false;
  const routes = {
    "POST /api/v1/desktop/windows": () => reply(202, sessionAnswer(windowAt("normal"))),
    "PUT /api/v1/desktop/windows/window-1/unsaved": (call) => {
      assert.deepEqual(JSON.parse(call.body), { unsaved: true });
      return reply(200, sessionAnswer(windowAt("normal")));
    },
    "DELETE /api/v1/desktop/windows/window-1": () => {
      if (!vetoed) {
        vetoed = true;
        return reply(409, { error: { code: "close_vetoed", message: "应用有未保存的更改", stage: "authorize" } });
      }
      return reply(200, { windows: [], workspaces: ["main"], currentWorkspace: "main", taskbar: [], address: "", appInstances: {} });
    },
  };
  const { session } = startClient(routes);
  await session.open("files");
  await session.markUnsaved("window-1", true);

  const first = await session.close("window-1");
  assert.equal(first.vetoed, true, "第一次关闭没有被否决");
  assert.equal(session.current().windows.length, 1, "被否决的关闭移除了窗口");

  const second = await session.close("window-1");
  assert.equal(second.vetoed, false);
  assert.equal(session.current().windows.length, 0);
});

// --- The desktop surface -------------------------------------------------
//
// The window client above is one half of the case: these tests drive the shell
// the browser runs, so a state change, the restore rectangle and the close veto
// are observable on the rendered desktop (DASH-AC-69, DASH-C-74).

// SURFACES answers the reads the shell makes before it can open a window.
function surfaces(overrides = {}) {
  return {
    "GET /api/v1/auth/session": () => reply(200, { username: "owner", role: "Owner" }),
    "GET /api/v1/pages": () => reply(200, registry),
    "GET /api/v1/desktop": () => reply(200, desktopPage),
    "GET /api/v1/apps": () => reply(200, { items: [filesApp], notReady: [] }),
    "GET /api/v1/dock": () => reply(200, dockView),
    "GET /api/v1/launcher": () => reply(200, launcherView),
    "GET /api/v1/desktop/windows": () => reply(200, sessionOf([])),
    ...overrides,
  };
}

// startDesktop starts the shell with its window layer over fakes.
function startDesktop(routes) {
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
  return { app, api, root, container, window, calls };
}

// click sends one click at the shell.
function click(attributes) {
  return { target: elementWith(attributes) };
}

// nodeOf reads the one mounted window.
function nodeOf(container) {
  return container.children[0];
}

// bodyOf is the element a window's frame mounts in.
// waitFor waits for one settled piece of the shell's work.
async function waitFor(predicate, label) {
  const deadline = Date.now() + 2000;
  while (Date.now() < deadline) {
    if (predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  assert.fail(`timed out waiting for ${label}`);
}

function bodyOf(node) {
  return node.children[1];
}

test("窗口按钮只提供该状态允许的转移：最小化保留 frame，吸附带视口，恢复回原边界", async () => {
  const snapped = { x: 0, y: 0, width: 720, height: 900 };
  const states = {
    minimized: (window) => ({ ...window, state: "minimized" }),
    normal: (window) => ({ ...window, state: "normal" }),
    maximized: (window) => ({ ...window, state: "maximized" }),
    fullscreen: (window) => ({ ...window, state: "fullscreen" }),
    snapped: (window) => ({ ...window, state: "snapped", bounds: snapped }),
  };
  // The stub enforces DASH-C-74's transitions, so a button the shell should not
  // offer would be refused here instead of silently succeeding.
  const allowed = {
    normal: ["minimized", "maximized", "fullscreen", "snapped"],
    minimized: ["normal"],
    maximized: ["normal", "fullscreen"],
    fullscreen: ["normal", "snapped"],
    snapped: ["normal"],
  };
  let current = "normal";
  const refused = [];
  const { app, container, calls } = startDesktop(
    surfaces({
      "POST /api/v1/desktop/windows": () => reply(202, openSessionFor(filesApp)),
      "PUT /api/v1/desktop/windows/window-1/state": (call) => {
        const body = JSON.parse(call.body);
        if (!(allowed[current] || []).includes(body.state)) {
          refused.push(`${current}->${body.state}`);
          return reply(409, { error: { code: "failed_precondition", message: "窗口状态转移不合法", stage: "authorize" } });
        }
        current = body.state;
        return reply(200, sessionOf([states[body.state](windowOf(filesApp))]));
      },
    }),
  );
  await app.start();
  await app.handleClick(click({ "data-open-app": "files" }));

  const node = nodeOf(container);
  const frame = bodyOf(node).children[0];
  const bar = () => node.children[0].innerHTML;
  assert.match(bar(), /data-command="window-minimize"/);
  assert.match(bar(), /data-command="window-maximize"/);
  assert.match(bar(), /data-command="window-fullscreen"/);
  assert.match(bar(), /data-command="window-snap" data-edge="left"/);

  await app.handleClick(click({ "data-command": "window-minimize", "data-window": "window-1" }));
  assert.equal(node.getAttribute("data-state"), "minimized");
  assert.equal(bodyOf(node).children[0], frame, "最小化销毁了 frame");
  // A minimized window offers the restore it needs to come back.
  assert.match(bar(), /data-command="window-restore"/);
  assert.doesNotMatch(bar(), /data-command="window-maximize"/);

  // DASH-C-74 only lets a minimized window return through normal: the case
  // follows the transitions the state machine allows, so a regression that
  // offers an illegal one fails here.
  await app.handleClick(click({ "data-command": "window-restore", "data-window": "window-1" }));
  assert.equal(node.getAttribute("data-state"), "normal");
  assert.match(bar(), /data-command="window-maximize"/, "恢复后没有重新提供最大化");
  await app.handleClick(click({ "data-command": "window-maximize", "data-window": "window-1" }));
  assert.equal(node.getAttribute("data-state"), "maximized");
  await app.handleClick(click({ "data-command": "window-fullscreen", "data-window": "window-1" }));
  assert.equal(node.getAttribute("data-state"), "fullscreen");
  await app.handleClick(click({ "data-command": "window-snap", "data-edge": "left", "data-window": "window-1" }));
  assert.equal(node.getAttribute("data-state"), "snapped");
  assert.equal(node.style.values.get("--window-width"), "720px");
  await app.handleClick(click({ "data-command": "window-restore", "data-window": "window-1" }));
  assert.equal(node.getAttribute("data-state"), "normal");
  assert.equal(node.style.values.get("--window-width"), "900px", "恢复没有回到原边界");

  const requests = calls
    .filter((call) => call.path === "/api/v1/desktop/windows/window-1/state")
    .map((call) => JSON.parse(call.body));
  assert.deepEqual(
    requests.map((body) => body.state),
    ["minimized", "normal", "maximized", "fullscreen", "snapped", "normal"],
    "状态请求序列与契约不一致",
  );
  const snap = requests.find((body) => body.state === "snapped");
  assert.equal(snap.edge, "left");
  assert.deepEqual(snap.viewport, { x: 0, y: 0, width: 1440, height: 900 });
  assert.deepEqual(refused, [], "窗口条提供了契约不允许的转移");
});

test("应用上报未保存后第一次关闭被否决且窗口仍在，第二次关闭销毁窗口与 frame", async () => {
  const init = { type: "init", token: "token-1", theme: "system", capabilities: [], expiresAt: "2099-01-01T00:00:00Z" };
  // The stub answers the close veto only when the application's unsaved report
  // reached it, so this case fails if the postMessage chain is missing
  // (DASH-C-74, KOSD-C-75).
  let unsaved = false;
  let vetoed = false;
  const { app, root, container, window, calls } = startDesktop(
    surfaces({
      "POST /api/v1/desktop/windows": () => reply(202, openSessionFor(filesApp)),
      "POST /api/v1/windows/instance-1/handshake": () => reply(200, init),
      "PUT /api/v1/desktop/windows/window-1/unsaved": (call) => {
        unsaved = JSON.parse(call.body).unsaved === true;
        return reply(200, sessionOf([{ ...windowOf(filesApp), isUnsaved: unsaved }]));
      },
      "DELETE /api/v1/desktop/windows/window-1": () => {
        if (unsaved && !vetoed) {
          vetoed = true;
          return reply(409, { error: { code: "close_vetoed", message: "应用有未保存的更改，关闭已被否决", stage: "authorize" } });
        }
        return reply(200, sessionOf([]));
      },
    }),
  );
  await app.start();
  await app.handleClick(click({ "data-open-app": "files" }));
  const node = nodeOf(container);
  const frame = bodyOf(node).children[0];
  window.message({ origin: "https://files.apps.example.test", source: frame.contentWindow, data: { type: "ready" } });
  await waitFor(() => frame.messages.length === 1, "the handshake");

  // An origin that is not the application's never reaches the window session.
  window.message({
    origin: "https://evil.example.test",
    source: frame.contentWindow,
    data: { type: "unsaved", unsaved: true },
  });
  await new Promise((resolve) => setTimeout(resolve, 10));
  assert.ok(
    !calls.some((call) => call.path === "/api/v1/desktop/windows/window-1/unsaved"),
    "异源的未保存消息被当成了窗口状态",
  );

  // The application reports unsaved changes on its channel; the shell forwards
  // the report and the window bar marks it.
  window.message({
    origin: "https://files.apps.example.test",
    source: frame.contentWindow,
    data: { type: "unsaved", unsaved: true, window: "instance-1" },
  });
  await waitFor(
    () => calls.some((call) => call.path === "/api/v1/desktop/windows/window-1/unsaved"),
    "the unsaved report to reach the window session",
  );
  const report = calls.find((call) => call.path === "/api/v1/desktop/windows/window-1/unsaved");
  assert.deepEqual(JSON.parse(report.body), { unsaved: true });
  await waitFor(() => nodeOf(container).children[0].innerHTML.includes("未保存"), "the unsaved marker");

  await app.handleClick(click({ "data-command": "window-close", "data-window": "window-1" }));
  assert.equal(container.children.length, 1, "被否决的关闭移除了窗口");
  assert.equal(nodeOf(container).getAttribute("data-state"), "normal");
  assert.match(root.innerHTML, /close_vetoed/, "被否决的关闭没有给出原因");

  await app.handleClick(click({ "data-command": "window-close", "data-window": "window-1" }));
  assert.equal(container.children.length, 0, "第二次关闭没有销毁窗口");
  assert.equal(
    calls.filter((call) => call.method === "DELETE" && call.path === "/api/v1/desktop/windows/window-1").length,
    2,
    "两次关闭没有各发一次关闭请求",
  );
});
