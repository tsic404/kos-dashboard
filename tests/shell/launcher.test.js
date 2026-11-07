// The launcher: the installed application list, the name search, the account's
// category order, the browser-local recent list and the launch entry
// (DASH-C-30, DASH-C-52, DASH-C-27).

import assert from "node:assert/strict";
import test from "node:test";

import { filterApps, groupApps, orderApps, recentOf, renderLauncher } from "../../pkg/bff/assets/shell/launcher.js";
import { filesApp, launcherView, notesApp } from "./fixtures.js";

const apps = [filesApp, notesApp];

test("launcher 只列出清单里的应用，名称与图标取清单", () => {
  const html = renderLauncher({
    launcher: { ...launcherView, applications: ["files"] },
    apps,
    windows: [],
    recent: [],
    query: "",
    open: true,
    refusals: [],
  });
  assert.match(html, /data-launcher-app="files"/);
  assert.ok(!html.includes('data-launcher-app="notes"'), "清单外的应用出现在 launcher 上");
  assert.match(html, />文件</);
  assert.match(html, /src="https:\/\/files\.apps\.example\.test\/assets\/icon\.svg"/);
});

test("launcher 关闭时不渲染", () => {
  assert.equal(
    renderLauncher({ launcher: launcherView, apps, windows: [], recent: [], query: "", open: false, refusals: [] }),
    "",
  );
});

test("搜索按名称与标识过滤，无结果给出空状态", () => {
  assert.deepEqual(filterApps(apps, "笔").map((app) => app.id), ["notes"]);
  assert.deepEqual(filterApps(apps, "FILE").map((app) => app.id), ["files"]);
  const html = renderLauncher({
    launcher: launcherView,
    apps,
    windows: [],
    recent: [],
    query: "没有这个应用",
    open: true,
    refusals: [],
  });
  assert.match(html, /未找到匹配结果/);
  assert.ok(!html.includes("data-launcher-app="), "搜索无结果时仍渲染了应用");
});

test("最近使用来自浏览器本地源，按最近顺序排列", () => {
  const recent = [
    { id: "notes", name: "笔记", openedAt: "2026-03-01T12:00:00Z" },
    { name: "不存在的应用", openedAt: "2026-03-01T11:00:00Z" },
  ];
  assert.deepEqual(recentOf(apps, recent).map((app) => app.id), ["notes"]);
  const ordered = orderApps(launcherView.applications, apps, "recent", recent);
  assert.deepEqual(ordered.map((app) => app.id), ["notes", "files"]);
  const html = renderLauncher({
    launcher: { ...launcherView, sort: "recent" },
    apps,
    windows: [],
    recent,
    query: "",
    open: true,
    refusals: [],
  });
  assert.match(html, /data-launcher-recent/);
  assert.match(html, /data-launcher-app="notes"/);
});

test("分类按账号布局的分组顺序排列", () => {
  const groups = groupApps(apps, ["notes-ext", "files-ext"]);
  assert.deepEqual(groups.map((group) => group.category), ["notes-ext", "files-ext"]);
  const html = renderLauncher({
    launcher: { ...launcherView, categories: ["notes-ext", "files-ext"] },
    apps,
    windows: [],
    recent: [],
    query: "",
    open: true,
    refusals: [],
  });
  assert.ok(html.indexOf('data-category="notes-ext"') < html.indexOf('data-category="files-ext"'), "分类没有按布局排序");
});

test("未就绪的贡献只报告原因，不产生应用入口", () => {
  const html = renderLauncher({
    launcher: launcherView,
    apps: [filesApp],
    windows: [],
    recent: [],
    query: "",
    open: true,
    refusals: [{ extension: "broken-ext", component: "broken", reason: "清单缺少 entry" }],
  });
  assert.match(html, /data-launcher-not-ready/);
  assert.match(html, /data-not-ready="broken"/);
  assert.match(html, /清单缺少 entry/);
  assert.ok(!html.includes('data-launcher-app="broken"'), "声明无效的应用出现了入口");
});
