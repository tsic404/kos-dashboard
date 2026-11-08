// The dock surface: the account's application order, the running indicator and
// its window count, the context menu, and the layout commands the menu runs
// (DASH-C-30, DASH-C-27).

import assert from "node:assert/strict";
import test from "node:test";

import { renderDock } from "../../pkg/bff/assets/shell/dock.js";
import { dockView, filesApp, notesApp } from "./fixtures.js";

test("dock 按账号布局的顺序渲染应用、运行指示与窗口计数", () => {
  const windows = [
    { id: "win-1", application: "files", state: "normal" },
    { id: "win-2", application: "files", state: "normal" },
    { id: "win-3", application: "notes", state: "error" },
  ];
  const html = renderDock({
    dock: { ...dockView, applications: ["notes", "files"] },
    apps: [filesApp, notesApp],
    windows,
    menu: null,
  });
  assert.match(html, /data-position="bottom"/);
  assert.match(html, /data-autohide="false"/);
  assert.ok(html.indexOf('data-dock-app="notes"') < html.indexOf('data-dock-app="files"'), "dock 没有按布局排序");
  assert.match(html, /data-dock-app="files" data-pinned="true" data-running="true"/);
  assert.match(html, /data-dock-app="notes" data-pinned="false" data-running="false"/);
  assert.match(html, /data-badge="2">2</, "多个窗口没有渲染计数徽标");
  assert.match(html, /data-running="true" aria-hidden="true"/, "运行中的应用没有运行指示");
});

test("dock 只渲染已安装应用，清单外的固定项不出现", () => {
  const html = renderDock({
    dock: { ...dockView, pinned: ["absent", "files"], applications: ["files"] },
    apps: [filesApp],
    windows: [],
    menu: null,
  });
  assert.match(html, /data-dock-app="files"/);
  assert.ok(!html.includes("absent"), "未安装的固定项出现在 dock 上");
});

test("dock 应用图标取清单声明，跨 origin 的图标不加载", () => {
  const foreign = { ...filesApp, id: "foreign", name: "外来", icon: "https://evil.example.test/icon.svg" };
  const html = renderDock({
    dock: { ...dockView, applications: ["files", "notes", "foreign"] },
    apps: [filesApp, notesApp, foreign],
    windows: [],
    menu: null,
  });
  assert.match(html, /src="https:\/\/files\.apps\.example\.test\/assets\/icon\.svg" data-icon="https:\/\/files\.apps\.example\.test\/assets\/icon\.svg"/);
  assert.match(html, /src="https:\/\/notes\.apps\.example\.test\/assets\/icon\.svg" data-icon="assets\/icon\.svg"/);
  assert.ok(!html.includes('src="https://evil.example.test'), "外来 origin 的图标被加载了");
});

test("dock 右键菜单提供打开、固定与关闭窗口", () => {
  const html = renderDock({
    dock: { ...dockView, pinned: ["files"] },
    apps: [filesApp],
    windows: [{ id: "win-1", application: "files", state: "normal" }],
    menu: { kind: "app", appId: "files" },
  });
  assert.match(html, /data-menu="app" data-app="files"/);
  assert.match(html, /data-command="open" data-app="files"/);
  assert.match(html, /data-command="unpin" data-app="files"/);
  assert.match(html, /data-command="close-windows" data-app="files"/);
});

test("dock 背景菜单提供位置与自动隐藏", () => {
  const html = renderDock({
    dock: { ...dockView, position: "left", autoHide: true },
    apps: [filesApp],
    windows: [],
    menu: { kind: "dock" },
  });
  assert.match(html, /data-position="left"/);
  assert.match(html, /data-autohide="true"/);
  for (const position of ["bottom", "left", "right"]) {
    assert.match(html, new RegExp(`data-command="position" data-position="${position}"`));
  }
  assert.match(html, /自动隐藏：开/);
});

test("布局不可读时 dock 只保留 launcher 入口", () => {
  const html = renderDock({ dock: null, apps: [filesApp], windows: [], menu: null });
  assert.match(html, /data-state="unavailable"/);
  assert.match(html, /data-action="launcher"/);
  assert.ok(!html.includes("data-dock-app="), "布局不可读时仍渲染了应用顺序");
});
