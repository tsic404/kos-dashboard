// The window layer: one node per desktop window, the node carrying the state,
// the geometry, the z-order and the focus the session answered, and the frame
// living in the body so a state change never reloads the application
// (DASH-C-74, KOSD-C-75).

import assert from "node:assert/strict";
import test from "node:test";

import { createAppHost } from "../../pkg/bff/assets/shell/apphost.js";
import { createWindowLayer } from "../../pkg/bff/assets/shell/windowlayer.js";
import { renderTaskbar } from "../../pkg/bff/assets/shell/windowbar.js";
import { filesApp, notesApp, openSessionFor, sessionOf, windowOf } from "./fixtures.js";
import { fakeDocument, fakeElement, fakeWindow } from "./harness.js";

// layerFor builds one window layer over the real frame host and fakes.
function layerFor() {
  const container = fakeElement("div");
  const failures = [];
  const host = createAppHost({
    api: { write: async () => null },
    document: fakeDocument(),
    window: fakeWindow(),
    onFailure: (windowId, reason) => failures.push({ windowId, reason }),
  });
  const layer = createWindowLayer({ document: fakeDocument(), container, host });
  return { layer, host, container, failures };
}

test("打开窗口挂载一个节点：状态、几何、z-order、焦点与 frame 都取自会话", () => {
  const { layer, container } = layerFor();
  const bounds = { x: 40, y: 24, width: 900, height: 640 };
  const answer = openSessionFor(filesApp, { window: windowOf(filesApp, { bounds }) });
  layer.mount(answer, filesApp);

  assert.equal(container.children.length, 1, "打开后没有挂载一个窗口节点");
  const node = container.children[0];
  assert.equal(node.getAttribute("data-window"), "window-1");
  assert.equal(node.getAttribute("data-app"), "files");
  assert.equal(node.getAttribute("data-state"), "normal");
  assert.equal(node.getAttribute("data-focused"), "true");
  assert.equal(node.getAttribute("data-workspace"), "main");
  assert.equal(node.getAttribute("data-instance"), "1");
  assert.equal(node.style.values.get("--window-x"), "40px");
  assert.equal(node.style.values.get("--window-y"), "24px");
  assert.equal(node.style.values.get("--window-width"), "900px");
  assert.equal(node.style.values.get("--window-height"), "640px");
  assert.equal(node.style.values.get("--window-z"), "1");
  // The bar is the window's header and its title is the manifest's name.
  assert.equal(node.children[0].tagName, "HEADER");
  assert.match(node.children[0].innerHTML, /文件/);
  assert.equal(node.children[1].children.length, 1, "窗口没有挂载 frame");
});

test("状态变化只改节点属性与按钮，不重建 frame", () => {
  const { layer, container } = layerFor();
  const window = windowOf(filesApp);
  layer.mount(openSessionFor(filesApp, { window }), filesApp);
  const node = container.children[0];
  const frame = node.children[1].children[0];

  const records = layer.sync(sessionOf([{ ...window, state: "maximized", z: 4 }]));
  assert.equal(records.length, 1);
  assert.equal(node.getAttribute("data-state"), "maximized");
  assert.equal(node.style.values.get("--window-z"), "4");
  assert.equal(node.children[1].children[0], frame, "状态变化重建了 frame");
  // A maximized window offers the restore, and no maximize it already is.
  assert.match(node.children[0].innerHTML, /data-command="window-restore"/);
  assert.doesNotMatch(node.children[0].innerHTML, /data-command="window-maximize"/);
});

test("错误态不保留 frame 且给出重启；会话不再持有的窗口连同 frame 一起移除", () => {
  const { layer, container } = layerFor();
  const window = windowOf(filesApp);
  layer.mount(openSessionFor(filesApp, { window }), filesApp);
  const node = container.children[0];
  assert.equal(node.children[1].children.length, 1);

  layer.sync(sessionOf([{ ...window, state: "error", errorReason: "renderer crashed" }]));
  assert.equal(node.getAttribute("data-state"), "error");
  assert.equal(node.children[1].children.length, 0, "错误态仍保留 frame");
  assert.match(node.children[1].innerHTML, /renderer crashed/);
  assert.match(node.children[1].innerHTML, /data-command="window-restart"/);

  layer.sync(sessionOf([]));
  assert.equal(container.children.length, 0, "关闭后仍保留窗口节点");
  assert.deepEqual(layer.ids(), []);
});

test("重启换新实例并保留窗口节点；同一实例再次挂载不重建 frame；登出移除全部", () => {
  const { layer, container } = layerFor();
  const window = windowOf(filesApp);
  layer.mount(openSessionFor(filesApp, { window }), filesApp);
  const node = container.children[0];
  layer.sync(sessionOf([{ ...window, state: "error", errorReason: "应用崩溃" }]));
  assert.match(node.children[1].innerHTML, /应用崩溃/);

  const recovered = openSessionFor(filesApp, { window, instanceId: "instance-2" });
  layer.mount(recovered, filesApp);
  assert.equal(node.getAttribute("data-state"), "normal");
  assert.equal(node.children[1].children.length, 1, "重启后没有挂载新 frame");
  assert.equal(
    new URL(node.children[1].children[0].getAttribute("src")).searchParams.get("kos-window"),
    "instance-2",
  );

  const frame = node.children[1].children[0];
  layer.mount(recovered, filesApp);
  assert.equal(node.children[1].children[0], frame, "同一实例再次挂载重建了 frame");

  layer.mount(openSessionFor(notesApp, { window: windowOf(notesApp, { id: "window-2", instance: 2 }) }), notesApp);
  assert.equal(container.children.length, 2);
  layer.closeAll();
  assert.equal(container.children.length, 0);
  assert.deepEqual(layer.ids(), []);
});

test("任务栏只列当前工作区的运行窗口，工作区切换器标记当前工作区", () => {
  const main = windowOf(filesApp, { id: "window-main", instance: 1, workspace: "main" });
  const work = windowOf(filesApp, { id: "window-work", instance: 2, workspace: "work", focused: false });
  const failed = windowOf(filesApp, { id: "window-error", instance: 3, workspace: "work", focused: false, state: "error" });
  // The front answers the taskbar; a stale answer that still lists the error
  // window must not make it a running window (DASH-C-74).
  const session = sessionOf([main, work, failed], {
    workspaces: ["main", "work"],
    currentWorkspace: "work",
    taskbar: [main, work, failed],
  });

  const html = renderTaskbar(session, { mounted: ["window-main", "window-work", "window-error"], apps: [filesApp] });
  assert.match(html, /data-taskbar-window="window-work"/);
  assert.match(html, /data-command="window-focus" data-window="window-work"/);
  assert.doesNotMatch(html, /data-taskbar-window="window-main"/);
  assert.doesNotMatch(html, /data-taskbar-window="window-error"/, "错误窗口出现在任务栏上");
  assert.match(html, /data-command="workspace" data-workspace="main"/);
  assert.match(html, /data-command="workspace" data-workspace="work" aria-current="true"/);
});
