// DASH-TC-147 from the browser side: a crashed window enters its error state
// and renders restart and close; a restart keeps its instance key; the taskbar
// lists only the current workspace's windows (DASH-AC-69, DASH-C-74).

import assert from "node:assert/strict";
import test from "node:test";

import { createApi } from "../../../pkg/bff/assets/shell/api.js";
import { createApp } from "../../../pkg/bff/assets/shell/app.js";
import { createWindowSession, taskbarOf, windowsOf } from "../../../pkg/bff/assets/shell/windows.js";
import { renderTaskbar, renderWindowErrorBody } from "../../../pkg/bff/assets/shell/windowbar.js";
import {
  desktopPage,
  dockView,
  filesApp,
  launcherView,
  notesApp,
  openSessionFor,
  registry,
  sessionOf as fixtureSession,
  windowOf,
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
} from "../../shell/harness.js";

// windowAt renders one session window in a workspace and state.
function windowAt(id, instance, workspace, state = "normal") {
  return {
    id,
    application: "media",
    instance,
    state,
    bounds: { x: 0, y: 0, width: 1024, height: 720 },
    restore: { x: 0, y: 0, width: 1024, height: 720 },
    workspace,
    z: instance,
    isFocused: true,
  };
}

// workspaceAnswer renders one session answer with the given windows and current
// workspace. The taskbar and address are the front's, not the shell's.
function workspaceAnswer(windows, current) {
  return {
    windows,
    workspaces: ["main", "work"],
    currentWorkspace: current,
    taskbar: windows.filter((window) => window.workspace === current && window.state !== "error"),
    address: "",
    appInstances: Object.fromEntries(windows.map((window) => [window.id, "instance-" + window.id])),
  };
}

// startClient starts the window client over the routes of a case.
function startClient(routes) {
  const { fetch, calls } = fakeFetch(routes);
  const api = createApi({ fetch, document: { cookie: "kos_bff_csrf=csrf-token" } });
  return { session: createWindowSession({ api }), calls };
}

test("崩溃进入错误态，界面给出重启与关闭；重启保留实例键", async () => {
  const live = windowAt("window-1", 1, "main");
  const failed = { ...live, state: "error", errorReason: "renderer crashed" };
  const routes = {
    "POST /api/v1/desktop/windows": () => reply(202, { ...workspaceAnswer([live], "main"), isNew: true }),
    "POST /api/v1/desktop/windows/window-1/crash": () => reply(200, workspaceAnswer([failed], "main")),
    "POST /api/v1/desktop/windows/window-1/restart": () =>
      reply(202, { ...workspaceAnswer([{ ...live, z: 2 }], "main"), isNew: true, instance: { id: "instance-2" } }),
  };
  const { session } = startClient(routes);
  await session.open("media");

  await session.crash("window-1", "renderer crashed");
  const crashed = windowsOf(session.current())[0];
  assert.equal(crashed.state, "error");
  assert.equal(crashed.errorReason, "renderer crashed");
  const html = renderWindowErrorBody(crashed);
  assert.match(html, /data-command="window-restart"/, "错误窗口没有重启入口");
  assert.match(html, /data-command="window-close"/, "错误窗口没有关闭入口");
  assert.equal(taskbarOf(session.current()).length, 0, "错误窗口仍被当作运行窗口");

  await session.restart("window-1");
  const recovered = windowsOf(session.current())[0];
  assert.equal(recovered.state, "normal");
  assert.equal(recovered.instance, 1, "重启改变了实例键");
});

test("任务栏只列当前工作区的窗口；切换工作区改变任务栏", async () => {
  const main = windowAt("window-main", 1, "main");
  const work = windowAt("window-work", 2, "work");
  const routes = {
    "POST /api/v1/desktop/windows": () => reply(202, { ...workspaceAnswer([main, work], "work"), isNew: true }),
    "POST /api/v1/desktop/workspaces": (call) => {
      const workspace = JSON.parse(call.body).workspace;
      return reply(200, workspaceAnswer([main, work], workspace));
    },
  };
  const { session } = startClient(routes);
  await session.open("media");
  assert.deepEqual(taskbarOf(session.current()).map((window) => window.id), ["window-work"]);
  assert.match(renderTaskbar(session.current()), /data-taskbar-window="window-work"/);
  assert.doesNotMatch(renderTaskbar(session.current()), /data-taskbar-window="window-main"/);

  await session.switchWorkspace("main");
  assert.equal(session.current().currentWorkspace, "main");
  assert.deepEqual(taskbarOf(session.current()).map((window) => window.id), ["window-main"]);
  assert.match(renderTaskbar(session.current()), /data-taskbar-window="window-main"/);
  assert.doesNotMatch(renderTaskbar(session.current()), /data-taskbar-window="window-work"/);
});

test("默认不恢复上次会话：新会话从空窗口集合开始", async () => {
  const routes = {
    "GET /api/v1/desktop/windows": () =>
      reply(200, { windows: [], workspaces: ["main"], currentWorkspace: "main", taskbar: [], address: "", appInstances: {} }),
  };
  const { session } = startClient(routes);
  await session.load();
  assert.equal(windowsOf(session.current()).length, 0);
  assert.equal(session.current().address, "");
});

// --- The desktop surface -------------------------------------------------
//
// The client tests above pin the requests; these drive the shell the browser
// runs, where the taskbar, the error window and the restart are what a user
// observes (DASH-AC-69, DASH-C-74, DASH-C-30).

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

// startDesktop starts the shell with its window layer over fakes. liveness
// shortens the frame host's waits so a timeout is exercised in real time.
function startDesktop(routes, liveness = {}) {
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
    liveness,
  });
  return { app, root, container, window, calls };
}

function click(attributes) {
  return { target: elementWith(attributes) };
}

function bodyOf(node) {
  return node.children[1];
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

test("任务栏只列当前工作区的窗口，切换工作区改变任务栏而不动窗口", async () => {
  const mainWindow = windowOf(filesApp, { id: "window-main", instance: 1, workspace: "main" });
  const workWindow = windowOf(filesApp, { id: "window-work", instance: 2, workspace: "work", focused: false });
  const answers = [
    openSessionFor(filesApp, { window: mainWindow }),
    {
      ...openSessionFor(filesApp, { window: workWindow, windows: [mainWindow, workWindow] }),
      currentWorkspace: "work",
      taskbar: [workWindow],
    },
  ];
  const { app, root, container, calls } = startDesktop(
    surfaces({
      "POST /api/v1/desktop/windows": () => reply(202, answers.shift()),
      "POST /api/v1/desktop/workspaces": (call) => {
        const workspace = JSON.parse(call.body).workspace;
        return reply(200, {
          ...fixtureSession([mainWindow, workWindow], { currentWorkspace: workspace }),
        });
      },
    }),
  );
  await app.start();
  await app.handleClick(click({ "data-open-app": "files" }));
  await app.handleClick(click({ "data-open-app": "files" }));

  // The work workspace is current, so its window is the one the bar lists.
  assert.equal(container.children.length, 2, "两个窗口没有各自挂载");
  assert.match(root.innerHTML, /data-taskbar-window="window-work"/);
  assert.doesNotMatch(root.innerHTML, /data-taskbar-window="window-main"/);

  await app.handleClick(click({ "data-command": "workspace", "data-workspace": "main" }));
  assert.deepEqual(JSON.parse(calls.at(-1).body), { workspace: "main" });
  assert.match(root.innerHTML, /data-taskbar-window="window-main"/);
  assert.doesNotMatch(root.innerHTML, /data-taskbar-window="window-work"/);
  // Switching workspaces moves nothing: both windows are still mounted.
  assert.equal(container.children.length, 2, "切换工作区关闭了窗口");
});

test("任务栏选中最小化窗口时先恢复再聚焦，地址不指向隐藏窗口", async () => {
  const live = windowOf(filesApp);
  const minimized = { ...live, state: "minimized", isFocused: false };
  const restored = { ...live, isFocused: true, z: 2 };
  const { app, root, calls, container } = startDesktop(
    surfaces({
      "POST /api/v1/desktop/windows": () => reply(202, openSessionFor(filesApp)),
      "PUT /api/v1/desktop/windows/window-1/state": (call) => {
        const state = JSON.parse(call.body).state;
        const window = state === "minimized" ? minimized : restored;
        return reply(200, fixtureSession([window]));
      },
    }),
  );
  await app.start();
  await app.handleClick(click({ "data-open-app": "files" }));
  await app.handleClick(click({ "data-command": "window-minimize", "data-window": "window-1" }));
  assert.equal(container.children[0].getAttribute("data-state"), "minimized");

  await app.handleClick(click({ "data-command": "window-focus", "data-window": "window-1" }));
  assert.equal(container.children[0].getAttribute("data-state"), "normal", "最小化窗口没有先恢复");
  const writes = calls.filter((call) => call.method !== "GET").slice(-1);
  assert.equal(writes[0].path, "/api/v1/desktop/windows/window-1/state", "选中最小化窗口没有走恢复");
  assert.deepEqual(JSON.parse(writes[0].body), { state: "normal" }, "选中最小化窗口没有恢复它");
  assert.ok(
    !calls.some((call) => call.path === "/api/v1/desktop/windows/window-1/focus"),
    "聚焦请求把隐藏窗口设成了焦点",
  );
  assert.match(root.innerHTML, /data-taskbar-window="window-1"/);
});

test("frame 无法运行进入错误态，错误窗口提供重启与关闭；重启保留窗口并换新实例", async () => {
  const broken = openSessionFor(filesApp);
  broken.frame = "https://evil.example.test/app.js";
  const failed = { ...windowOf(filesApp), state: "error", errorReason: "资产地址不在应用 origin 上，拒绝加载" };
  const recovered = { ...openSessionFor(filesApp, { instanceId: "instance-2" }) };
  const { app, root, container, calls } = startDesktop(
    surfaces({
      "POST /api/v1/desktop/windows": () => reply(202, broken),
      "POST /api/v1/desktop/windows/window-1/crash": () => reply(200, fixtureSession([failed])),
      "POST /api/v1/desktop/windows/window-1/restart": () => reply(202, recovered),
    }),
  );
  await app.start();
  await app.handleClick(click({ "data-open-app": "files" }));

  const node = container.children[0];
  await waitFor(() => node.getAttribute("data-state") === "error", "the window to enter its error state");
  assert.equal(node.getAttribute("data-window"), "window-1", "错误窗口换了身份");
  assert.match(bodyOf(node).innerHTML, /拒绝加载/);
  assert.match(bodyOf(node).innerHTML, /data-command="window-restart"/);
  assert.match(bodyOf(node).innerHTML, /data-command="window-close"/);
  assert.equal(bodyOf(node).children.length, 0, "错误窗口仍保留 frame");
  // A window in its error state is not a running window (DASH-C-74).
  assert.doesNotMatch(root.innerHTML, /data-taskbar-window="window-1"/);

  await app.handleClick(click({ "data-command": "window-restart", "data-window": "window-1" }));
  assert.equal(node.getAttribute("data-state"), "normal");
  const frame = bodyOf(node).children[0];
  assert.ok(frame, "重启后没有挂载 frame");
  assert.equal(new URL(frame.getAttribute("src")).searchParams.get("kos-window"), "instance-2");
  assert.match(root.innerHTML, /data-taskbar-window="window-1"/);

  // No window interaction touches a lifecycle route: the only writes are the
  // window session's own (DASH-C-30).
  const writes = calls.filter((call) => call.method !== "GET");
  assert.ok(
    writes.every((call) => call.path.startsWith("/api/v1/desktop/") || call.path.startsWith("/api/v1/windows/")),
    "窗口交互触发了窗口会话之外的写入",
  );
});
test("应用停止上报存活后 frame 宿主报告崩溃，前端把窗口带入错误态并给出重启", async () => {
  const init = { type: "init", token: "token-1", theme: "system", capabilities: [], expiresAt: "2099-01-01T00:00:00Z" };
  const live = openSessionFor(filesApp);
  const failed = { ...windowOf(filesApp), state: "error" };
  const { app, root, container, window, calls } = startDesktop(
    surfaces({
      "POST /api/v1/desktop/windows": () => reply(202, live),
      "POST /api/v1/windows/instance-1/handshake": () => reply(200, init),
      "POST /api/v1/desktop/windows/window-1/heartbeat": () => reply(200, fixtureSession([windowOf(filesApp)])),
      // The front answers the crash the shell reports; that answer is the state
      // the desktop shows (DASH-C-74).
      "POST /api/v1/desktop/windows/window-1/crash": (call) =>
        reply(200, fixtureSession([{ ...failed, errorReason: JSON.parse(call.body).reason }])),
    }),
    { loadTimeout: 5000, aliveTimeout: 40 },
  );
  await app.start();
  await app.handleClick(click({ "data-open-app": "files" }));
  const node = container.children[0];
  const frame = bodyOf(node).children[0];
  assert.equal(node.getAttribute("data-state"), "normal");

  // Liveness before the handshake cannot prove the instance.
  window.message({
    origin: "https://files.apps.example.test",
    source: frame.contentWindow,
    data: { type: "alive", window: "instance-1" },
  });
  await new Promise((resolve) => setTimeout(resolve, 10));
  assert.ok(
    !calls.some((call) => call.path === "/api/v1/desktop/windows/window-1/heartbeat"),
    "握手前的存活消息被当成了心跳",
  );

  window.message({ origin: "https://files.apps.example.test", source: frame.contentWindow, data: { type: "ready" } });
  await waitFor(() => frame.messages.length === 1, "the handshake");

  // The application proves it is alive; the shell forwards the proof, so the
  // heartbeat describes the frame rather than the page.
  window.message({
    origin: "https://files.apps.example.test",
    source: frame.contentWindow,
    data: { type: "alive", window: "instance-1" },
  });
  await waitFor(
    () => calls.some((call) => call.path === "/api/v1/desktop/windows/window-1/heartbeat"),
    "the application's liveness to reach the window session",
  );

  // The application goes silent: the frame host reports the missed liveness and
  // the front's crash answer is what the desktop renders.
  await waitFor(() => node.getAttribute("data-state") === "error", "the liveness timeout to reach the desktop");
  const crash = calls.find((call) => call.path === "/api/v1/desktop/windows/window-1/crash");
  assert.ok(crash, "存活超时没有报告给窗口会话");
  assert.match(JSON.parse(crash.body).reason, /心跳超时/);
  assert.match(bodyOf(node).innerHTML, /心跳超时/);
  assert.match(bodyOf(node).innerHTML, /data-command="window-restart"/);
  assert.equal(bodyOf(node).children.length, 0, "错误窗口仍保留 frame");
  assert.doesNotMatch(root.innerHTML, /data-taskbar-window="window-1"/);
  // The shell never heartbeats on its own clock: only the application's report
  // produced a heartbeat.
  assert.equal(
    calls.filter((call) => call.path === "/api/v1/desktop/windows/window-1/heartbeat").length,
    1,
    "外壳按自己的时钟代发了心跳",
  );
});

test("崩溃上报失败时窗口仍进入错误态并给出原因与重启", async () => {
  // The front refuses the crash report. The frame is gone either way, so the
  // window cannot stay a running window with an empty body (DASH-C-74).
  const broken = openSessionFor(filesApp);
  delete broken.frame;
  broken.frameError = "应用未声明包摘要，无法签发可校验的资产地址";
  const live = fixtureSession([windowOf(filesApp)]);
  const { app, root, container, calls } = startDesktop(
    surfaces({
      "POST /api/v1/desktop/windows": () => reply(202, broken),
      "GET /api/v1/desktop/windows": () => reply(200, live),
      "POST /api/v1/desktop/windows/window-1/crash": () =>
        reply(500, { error: { code: "internal", message: "窗口会话写入失败", stage: "write" } }),
    }),
  );
  await app.start();
  await app.handleClick(click({ "data-open-app": "files" }));
  const node = container.children[0];
  await waitFor(() => node.getAttribute("data-state") === "error", "the stopped window to enter its error state");

  assert.equal(node.getAttribute("data-window"), "window-1", "错误窗口换了身份");
  assert.match(bodyOf(node).innerHTML, /包摘要/, "错误窗口没有给出原因");
  assert.match(bodyOf(node).innerHTML, /data-command="window-restart"/, "错误窗口没有重启入口");
  assert.match(bodyOf(node).innerHTML, /data-command="window-close"/, "错误窗口没有关闭入口");
  assert.equal(bodyOf(node).children.length, 0, "错误窗口仍保留 frame");
  // A window whose frame stopped is not a running window (DASH-C-74).
  assert.doesNotMatch(root.innerHTML, /data-taskbar-window="window-1"/, "任务栏仍把停止的窗口列为运行窗口");
  assert.match(root.innerHTML, /data-surface-error=/, "崩溃上报失败没有呈现");
  assert.ok(
    calls.some((call) => call.path === "/api/v1/desktop/windows/window-1/crash"),
    "崩溃没有报告给窗口会话",
  );
});

test("本地错误窗口在 dock 重开时保持错误，不为同一实例再挂 frame", async () => {
  // The front answers a reopen of the single-mode window with the instance that
  // already delivered its token (KOSD-C-75), so the page cannot mount a frame for
  // it. The error and its reason stay until the user restarts the window
  // (DASH-C-74).
  const broken = openSessionFor(filesApp);
  delete broken.frame;
  broken.frameError = "应用未声明包摘要，无法签发可校验的资产地址";
  const focus = openSessionFor(filesApp, { isNew: false, hasToken: true });
  const live = fixtureSession([windowOf(filesApp)]);
  let opens = 0;
  const { app, root, container, calls } = startDesktop(
    surfaces({
      "POST /api/v1/desktop/windows": () => reply(202, opens++ === 0 ? broken : focus),
      "GET /api/v1/desktop/windows": () => reply(200, live),
      "POST /api/v1/desktop/windows/window-1/crash": () =>
        reply(500, { error: { code: "internal", message: "窗口会话写入失败", stage: "write" } }),
    }),
  );
  await app.start();
  await app.handleClick(click({ "data-open-app": "files" }));
  const node = container.children[0];
  await waitFor(() => node.getAttribute("data-state") === "error", "the stopped window to enter its error state");
  const reason = bodyOf(node).innerHTML;

  // The dock reopen only focuses the window; the error, its reason and the restart
  // entry stay, and no frame is mounted for the spent instance.
  await app.handleClick(click({ "data-open-app": "files" }));
  assert.equal(node.getAttribute("data-state"), "error", "重开把本地错误态翻成了运行");
  assert.equal(bodyOf(node).innerHTML, reason, "重开换掉了原来的原因与入口");
  assert.equal(bodyOf(node).children.length, 0, "重开为已交付实例又挂了一个 frame");
  assert.equal(node.getAttribute("data-window"), "window-1", "错误窗口换了身份");
  assert.doesNotMatch(root.innerHTML, /data-taskbar-window="window-1"/, "重开后任务栏把停止的窗口列为运行窗口");
  assert.equal(
    calls.filter((call) => call.path === "/api/v1/windows/instance-1/handshake").length,
    0,
    "为已交付实例发起了握手",
  );
});

test("本地错误窗口在 dock 重开拿到未交付实例时恢复运行", async () => {
  // The same window comes back with an instance whose token was never delivered,
  // so a frame can handshake again: the error this page kept is over and the
  // window runs (KOSD-C-75, DASH-C-74).
  const broken = openSessionFor(filesApp);
  delete broken.frame;
  broken.frameError = "应用未声明包摘要，无法签发可校验的资产地址";
  const resumed = openSessionFor(filesApp, { isNew: false, hasToken: false });
  const live = fixtureSession([windowOf(filesApp)]);
  let opens = 0;
  const { app, root, container, calls } = startDesktop(
    surfaces({
      "POST /api/v1/desktop/windows": () => reply(202, opens++ === 0 ? broken : resumed),
      "GET /api/v1/desktop/windows": () => reply(200, live),
      "POST /api/v1/desktop/windows/window-1/crash": () =>
        reply(500, { error: { code: "internal", message: "窗口会话写入失败", stage: "write" } }),
    }),
  );
  await app.start();
  await app.handleClick(click({ "data-open-app": "files" }));
  const node = container.children[0];
  await waitFor(() => node.getAttribute("data-state") === "error", "the stopped window to enter its error state");
  assert.equal(bodyOf(node).children.length, 0, "错误窗口已经挂了一个 frame");

  // The dock reopen answers the instance that never delivered its token; the
  // window runs again, its frame is the address this answer signed, and the
  // taskbar lists it as a running window.
  await app.handleClick(click({ "data-open-app": "files" }));
  assert.equal(node.getAttribute("data-state"), "normal", "未交付实例的重开没有恢复窗口");
  assert.equal(bodyOf(node).innerHTML, "", "恢复后仍留着错误文案");
  assert.equal(bodyOf(node).children.length, 1, "恢复后没有挂上 frame");
  const frame = bodyOf(node).children[0];
  const source = new URL(frame.getAttribute("src"));
  assert.equal(source.origin, "https://files.apps.example.test", "frame 不是应用 origin");
  assert.equal(source.searchParams.get("kos-window"), "instance-1", "frame 地址没有指向这次作答的实例");
  assert.equal(source.searchParams.get("kos-sig"), "signature", "frame 地址不是这次作答签发的地址");
  assert.equal(node.getAttribute("data-window"), "window-1", "恢复换了窗口身份");
  assert.match(root.innerHTML, /data-taskbar-window="window-1"/, "恢复后任务栏没有重新列出该窗口");
  assert.equal(frame.messages.length, 0, "没有 ready 就向 frame 投递了 init");
  assert.equal(
    calls.filter((call) => call.path === "/api/v1/windows/instance-1/handshake").length,
    0,
    "为旧实例发起了握手",
  );
});

test("本地错误窗口在 dock 重开拿到新实例键时恢复并保留窗口身份", async () => {
  // A reopen that answers a different application instance key mounts that
  // instance's frame while the desktop window keeps its identity (DASH-C-74).
  const broken = openSessionFor(filesApp);
  delete broken.frame;
  broken.frameError = "应用未声明包摘要，无法签发可校验的资产地址";
  const resumed = openSessionFor(filesApp, { isNew: true, instanceId: "instance-2" });
  const live = fixtureSession([windowOf(filesApp)]);
  let opens = 0;
  const { app, root, container, calls } = startDesktop(
    surfaces({
      "POST /api/v1/desktop/windows": () => reply(202, opens++ === 0 ? broken : resumed),
      "GET /api/v1/desktop/windows": () => reply(200, live),
      "POST /api/v1/desktop/windows/window-1/crash": () =>
        reply(500, { error: { code: "internal", message: "窗口会话写入失败", stage: "write" } }),
    }),
  );
  await app.start();
  await app.handleClick(click({ "data-open-app": "files" }));
  const node = container.children[0];
  await waitFor(() => node.getAttribute("data-state") === "error", "the stopped window to enter its error state");

  await app.handleClick(click({ "data-open-app": "files" }));
  assert.equal(node.getAttribute("data-state"), "normal", "新实例的重开没有恢复窗口");
  assert.equal(node.getAttribute("data-window"), "window-1", "恢复换了窗口身份");
  assert.equal(bodyOf(node).children.length, 1, "恢复后没有挂上 frame");
  const frame = bodyOf(node).children[0];
  assert.equal(new URL(frame.getAttribute("src")).searchParams.get("kos-window"), "instance-2", "frame 没有指向新实例");
  assert.match(root.innerHTML, /data-taskbar-window="window-1"/, "恢复后任务栏没有重新列出该窗口");
  assert.equal(
    calls.filter((call) => call.path === "/api/v1/windows/instance-1/handshake").length,
    0,
    "为旧实例发起了握手",
  );
});

test("点 dock 关闭全部窗口时被否决的错误窗口保持错误态与原因", async () => {
  // One multi-mode application, two windows: the first stopped locally, the
  // second running. The front vetoes the close of the stopped window and accepts
  // the other one (DASH-C-74).
  const stopped = windowOf(notesApp, { id: "window-1", instance: 1 });
  const live = windowOf(notesApp, { id: "window-2", instance: 2 });
  const broken = openSessionFor(notesApp, { window: stopped });
  delete broken.frame;
  broken.frameError = "应用未声明包摘要，无法签发可校验的资产地址";
  const second = openSessionFor(notesApp, { window: live, windows: [stopped, live], instanceId: "instance-2" });
  const answers = [broken, second];
  const { app, root, container, calls } = startDesktop(
    surfaces({
      "GET /api/v1/apps": () => reply(200, { items: [filesApp, notesApp], notReady: [] }),
      "POST /api/v1/desktop/windows": () => reply(202, answers.shift()),
      "GET /api/v1/desktop/windows": () => reply(200, fixtureSession([stopped, live])),
      "POST /api/v1/desktop/windows/window-1/crash": () =>
        reply(500, { error: { code: "internal", message: "窗口会话写入失败", stage: "write" } }),
      "DELETE /api/v1/desktop/windows/window-1": () =>
        reply(409, { error: { code: "close_vetoed", message: "应用有未保存的更改，关闭已被否决", stage: "authorize" } }),
      "DELETE /api/v1/desktop/windows/window-2": () => reply(200, fixtureSession([stopped])),
    }),
  );
  await app.start();
  await app.handleClick(click({ "data-open-app": "notes" }));
  await app.handleClick(click({ "data-open-app": "notes" }));
  const node = container.children[0];
  await waitFor(() => node.getAttribute("data-state") === "error", "the stopped window to enter its error state");
  const reason = bodyOf(node).innerHTML;
  assert.equal(container.children.length, 2, "两个窗口没有各自挂载");

  // The dock's close-windows command closes every window of the application. The
  // vetoed one keeps its error, its reason and its restart entry; the other one
  // is destroyed.
  await app.handleClick(click({ "data-command": "close-windows", "data-app": "notes" }));
  assert.equal(container.children.length, 1, "被否决的窗口被销毁或未被否决的窗口没有关闭");
  assert.equal(node.getAttribute("data-state"), "error", "被否决的窗口回到了运行态");
  assert.equal(bodyOf(node).innerHTML, reason, "被否决的窗口换了原因或丢了入口");
  assert.match(bodyOf(node).innerHTML, /data-command="window-restart"/, "错误窗口没有重启入口");
  assert.doesNotMatch(root.innerHTML, /data-taskbar-window="window-1"/, "任务栏把停止的窗口列为运行窗口");
  assert.match(root.innerHTML, /data-surface-error="close_vetoed"/, "关闭被否决没有呈现");
  assert.ok(
    calls.some((call) => call.method === "DELETE" && call.path === "/api/v1/desktop/windows/window-1"),
    "没有请求关闭被否决的窗口",
  );
});

test("壳内可新建工作区并把窗口移入；新窗口按当前工作区打开", async () => {
  const opened = [];
  const { app, root, container, calls } = startDesktop(
    surfaces({
      "POST /api/v1/desktop/windows": (call) => {
        const body = JSON.parse(call.body);
        opened.push(body.workspace || "");
        const window = windowOf(filesApp, { workspace: body.workspace || "main" });
        return reply(202, openSessionFor(filesApp, { window, windows: [window] }));
      },
      "POST /api/v1/desktop/workspaces": (call) => {
        const workspace = JSON.parse(call.body).workspace;
        return reply(200, {
          ...fixtureSession([windowOf(filesApp)], { workspaces: ["main", workspace], currentWorkspace: workspace }),
          taskbar: [],
        });
      },
      "PUT /api/v1/desktop/windows/window-1/workspace": (call) => {
        const workspace = JSON.parse(call.body).workspace;
        return reply(200, fixtureSession([windowOf(filesApp, { workspace })], {
          workspaces: ["main", workspace],
          currentWorkspace: workspace,
        }));
      },
    }),
  );
  await app.start();
  await app.handleClick(click({ "data-open-app": "files" }));
  assert.equal(container.children.length, 1);

  // The window opened in the current workspace, named by the request.
  assert.deepEqual(opened, ["main"]);
  assert.match(root.innerHTML, /data-taskbar-window="window-1"/);
  assert.match(root.innerHTML, /data-command="workspace-create"/, "任务栏没有新建工作区的入口");

  // Creating a workspace makes it current and leaves the existing window in its
  // own workspace, so the taskbar of the new one is empty (DASH-C-74).
  await app.handleClick(click({ "data-command": "workspace-create" }));
  const created = JSON.parse(calls.find((call) => call.path === "/api/v1/desktop/workspaces").body);
  assert.deepEqual(created, { workspace: "工作区 2" });
  assert.match(root.innerHTML, /data-command="workspace" data-workspace="工作区 2" aria-current="true"/);
  assert.doesNotMatch(root.innerHTML, /data-taskbar-window="window-1"/);

  // A new window opens in the workspace that is current now.
  await app.handleClick(click({ "data-open-app": "files" }));
  assert.equal(opened.at(-1), "工作区 2", "新窗口没有开在当前工作区");

  // Moving the window groups it into the workspace the user chose; the taskbar
  // then lists it (DASH-C-74).
  const move = {
    target: {
      closest: (selector) =>
        selector === "[data-window-workspace]" ? { getAttribute: () => "window-1" } : null,
      value: "工作区 2",
    },
  };
  await app.handleChange(move);
  const moveCall = calls.find((call) => call.path === "/api/v1/desktop/windows/window-1/workspace");
  assert.deepEqual(JSON.parse(moveCall.body), { workspace: "工作区 2" });
  assert.match(root.innerHTML, /data-taskbar-window="window-1"/);
  assert.equal(container.children.length, 1, "移动工作区关闭了窗口");
});
