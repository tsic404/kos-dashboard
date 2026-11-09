// DASH-TC-61 from the browser side: the window client opens two windows of one
// application and closes one and then all. Every request it sends is a
// window-session route; no preference and no cluster object is written, because
// the open set is browser session state (DASH-AC-30, DASH-C-74, KOSD-F-19).

import assert from "node:assert/strict";
import test from "node:test";

import { createApi } from "../../../pkg/bff/assets/shell/api.js";
import { createWindowSession } from "../../../pkg/bff/assets/shell/windows.js";
import { fakeFetch, reply } from "../../shell/harness.js";

// sessionAnswer renders one window-session answer.
function sessionAnswer(windows, { address = "", taskbar = windows } = {}) {
  return {
    windows,
    workspaces: ["main"],
    currentWorkspace: "main",
    taskbar,
    address,
    appInstances: Object.fromEntries(windows.map((window) => [window.id, "instance-" + window.id])),
  };
}

// windowOf renders one session window.
function windowOf(id, instance, state = "normal") {
  return {
    id,
    application: "media",
    instance,
    state,
    bounds: { x: 0, y: 0, width: 1024, height: 720 },
    restore: { x: 0, y: 0, width: 1024, height: 720 },
    workspace: "main",
    z: instance,
    isFocused: true,
  };
}

test("打开两个窗口再关闭一个与全部，只发窗口会话请求", async () => {
  let windows = [];
  const routes = {
    "POST /api/v1/desktop/windows": () => {
      const next = windowOf("window-" + (windows.length + 1), windows.length + 1);
      windows = [...windows, next];
      return reply(202, { ...sessionAnswer(windows), window: next, isNew: true });
    },
    "DELETE /api/v1/desktop/windows/window-1": () => {
      windows = windows.filter((window) => window.id !== "window-1");
      return reply(200, sessionAnswer(windows));
    },
    "DELETE /api/v1/desktop/windows/window-2": () => {
      windows = [];
      return reply(200, sessionAnswer(windows));
    },
  };
  const { fetch, calls } = fakeFetch(routes);
  const api = createApi({ fetch, document: { cookie: "kos_bff_csrf=csrf-token" } });
  const session = createWindowSession({ api });

  await session.open("media");
  await session.open("media");
  assert.equal(session.current().windows.length, 2);

  const firstClose = await session.close("window-1");
  assert.equal(firstClose.vetoed, false);
  assert.equal(session.current().windows.length, 1);
  const lastClose = await session.close("window-2");
  assert.equal(lastClose.vetoed, false);
  assert.equal(session.current().windows.length, 0);

  // The negative criterion: the window layer wrote no preference and no object.
  for (const call of calls) {
    assert.ok(
      call.path.startsWith("/api/v1/desktop/windows"),
      `窗口交互写了窗口会话之外的路径：${call.method} ${call.path}`,
    );
    assert.ok(!call.path.startsWith("/api/v1/preferences"), "窗口交互写了偏好");
    assert.ok(!call.path.startsWith("/api/v1/resources"), "窗口交互写了集群对象");
  }
});
