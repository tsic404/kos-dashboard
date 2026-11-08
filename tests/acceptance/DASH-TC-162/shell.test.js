// DASH-TC-162 from the browser side: the shell renders a page an extension
// declared — title, location, source type and current value of every item, the
// read-only notice of a read-only page — submits the page's action as the
// control-plane write it names and re-reads the page, keeps a withdrawn page
// out of the navigation, and never creates a frame for a page (DASH-AC-71,
// DASH-C-22, KOSD-C-53).

import assert from "node:assert/strict";
import test from "node:test";

import { createApi } from "../../../pkg/bff/assets/shell/api.js";
import { createApp } from "../../../pkg/bff/assets/shell/app.js";
import { createAppHost } from "../../../pkg/bff/assets/shell/apphost.js";
import { dockView, launcherView } from "../../shell/fixtures.js";
import {
  elementWith,
  fakeDocument,
  fakeElement,
  fakeEventSource,
  fakeFetch,
  fakeWindow,
  memoryStorage,
  reply,
  waitForHtml,
} from "../../shell/harness.js";

// reportsPage is the writable page the extension declares: one object-source
// item with its modification entry and one browser-local item (KOSD-C-53).
const reportsPage = {
  id: "reports",
  title: "报表",
  location: "panel",
  requiredRole: "Member",
  readOnly: false,
  sources: ["object", "local"],
  items: [
    {
      id: "reports",
      label: "报表",
      source: "object",
      action: "mark-viewed",
      entry: { method: "PUT", path: "/api/v1/preferences/reports-ext/view" },
      observedAt: "2026-11-08T04:00:00Z",
      stale: false,
      value: [{ name: "ink", title: "月度报表" }],
    },
    {
      id: "viewed",
      label: "最近查看",
      source: "local",
      store: "reports.view",
      reason: "最近查看只存在于浏览器",
      value: null,
    },
  ],
  actions: [
    {
      id: "mark-viewed",
      requiredRole: "Owner",
      entry: { method: "PUT", path: "/api/v1/preferences/reports-ext/view" },
    },
  ],
};

// readOnlyPage states why it is read-only and offers no write (KOSD-C-53).
const readOnlyPage = {
  id: "reports-readonly",
  title: "只读报表",
  location: "panel",
  readOnly: true,
  readOnlyReason: "报表只读展示",
  sources: ["object"],
  items: [
    {
      id: "fields",
      label: "字段",
      source: "object",
      observedAt: "2026-11-08T04:00:00Z",
      stale: false,
      value: { phase: "Reconciled" },
    },
  ],
  actions: [],
};

// registry is the page navigation the control plane answered for the session:
// the two declared pages, already trimmed to the caller's role.
const registry = {
  items: [
    { id: reportsPage.id, title: reportsPage.title, location: reportsPage.location, sources: ["object", "local"], items: [], actions: [] },
    { id: readOnlyPage.id, title: readOnlyPage.title, location: readOnlyPage.location, sources: ["object"], items: [], actions: [] },
  ],
};

// routesFor answers every read the shell makes for one page registry.
function routesFor(overrides = {}) {
  return {
    "GET /api/v1/auth/session": () => reply(200, { username: "owner", role: "Owner" }),
    "GET /api/v1/pages": () => reply(200, registry),
    "GET /api/v1/pages/reports": () => reply(200, reportsPage),
    "GET /api/v1/pages/reports-readonly": () => reply(200, readOnlyPage),
    "GET /api/v1/apps": () => reply(200, { items: [], notReady: [] }),
    "GET /api/v1/dock": () => reply(200, dockView),
    "GET /api/v1/launcher": () => reply(200, launcherView),
    "GET /api/v1/windows": () => reply(200, { items: [] }),
    "PUT /api/v1/preferences/reports-ext/view": () => reply(202, { accepted: true, ref: { kind: "Preference" } }),
    ...overrides,
  };
}

function startShell(routes) {
  const { fetch, calls } = fakeFetch(routes);
  const api = createApi({ fetch, document: { cookie: "kos_bff_csrf=csrf-token" } });
  const root = { innerHTML: "" };
  const container = fakeElement("div");
  const host = createAppHost({ api, document: fakeDocument(), container, window: fakeWindow() });
  const { EventSource, sources } = fakeEventSource();
  const storage = memoryStorage({ "reports.view": JSON.stringify([{ name: "本月" }]) });
  const app = createApp({ api, root, storage, EventSource, host });
  return { app, root, calls, container, sources };
}

test("扩展声明的页面按投影渲染：标题、位置、来源与当前值", async () => {
  const { app, root, calls, container } = startShell(routesFor());
  await app.start();

  assert.match(root.innerHTML, /data-nav="reports"/);
  assert.match(root.innerHTML, /data-nav="reports-readonly"/);
  assert.match(root.innerHTML, /data-page="reports"/);
  assert.match(root.innerHTML, /报表/);
  assert.match(root.innerHTML, /data-item="reports" data-source="object"/);
  assert.match(root.innerHTML, /data-item="viewed" data-source="local"/);
  assert.match(root.innerHTML, /对象源/);
  assert.match(root.innerHTML, /本地源/);
  assert.match(root.innerHTML, /月度报表/);
  assert.match(root.innerHTML, /最近查看只存在于浏览器/);
  assert.ok(
    calls.some((call) => call.path === "/api/v1/pages/reports"),
    "页面没有读控制面的页面投影",
  );

  // A page is no iframe application: rendering it creates no frame and no
  // window instance (DASH-C-22, KOSD-C-18).
  assert.ok(!root.innerHTML.includes("<iframe"), "页面渲染出了 iframe");
  assert.equal(container.children.length, 0, "页面渲染创建了窗口");
});

test("只读页明示只读与原因且不给写入口", async () => {
  const { app, root } = startShell(routesFor());
  await app.start();
  await app.navigate("reports-readonly");
  assert.match(root.innerHTML, /data-readonly="true"/);
  assert.match(root.innerHTML, /报表只读展示/);
  assert.ok(!root.innerHTML.includes("data-action-id="), "只读页给出了修改入口");
});

test("页面动作提交为声明的控制面写请求，随后重读页面", async () => {
  const { app, calls, root } = startShell(routesFor());
  await app.start();
  const before = calls.filter((call) => call.path === "/api/v1/pages/reports").length;

  await app.submitAction("mark-viewed");

  const write = calls.find((call) => call.method === "PUT" && call.path === "/api/v1/preferences/reports-ext/view");
  assert.ok(write, "动作没有提交为页面声明的写请求");
  assert.equal(write.headers["X-CSRF-Token"], "csrf-token", "写请求没有带上 CSRF 令牌");
  const after = calls.filter((call) => call.path === "/api/v1/pages/reports").length;
  assert.equal(after, before + 1, "动作提交后没有重读页面");
  assert.match(root.innerHTML, /data-page="reports"/);
});

test("组件停用后页面入口消失，直接导航被拒绝", async () => {
  const { app, root } = startShell(routesFor({ "GET /api/v1/pages": () => reply(200, { items: [] }) }));
  await app.start();
  assert.ok(!root.innerHTML.includes("data-nav="), "停用组件后仍有页面入口");
  await app.navigate("reports");
  assert.match(root.innerHTML, /data-code="page_not_found"/);
  assert.ok(!root.innerHTML.includes('data-page="reports"'), "未列出的页面仍被渲染");
});

test("dock 与 launcher 只列应用，页面不出现在应用表面", async () => {
  const { app, root } = startShell(routesFor());
  await app.start();
  assert.ok(!root.innerHTML.includes("data-dock-app="), "没有应用时 dock 出现了条目");
  await app.handleClick({ target: elementWith({ "data-action": "launcher" }) });
  assert.ok(!root.innerHTML.includes("data-launcher-app="), "页面出现在 launcher 里");
  assert.match(root.innerHTML, /data-launcher-not-ready|data-launcher-empty|暂无/);
});

test("可写展示项渲染页面声明的修改入口，角色不符时不出现", async () => {
  const { app, root } = startShell(routesFor());
  await app.start();
  assert.match(root.innerHTML, /data-item-actions="reports">.*data-action-id="mark-viewed"/s);

  const member = startShell(
    routesFor({ "GET /api/v1/auth/session": () => reply(200, { username: "member", role: "Member" }) }),
  );
  await member.app.start();
  assert.ok(!member.root.innerHTML.includes('data-action-id="mark-viewed"'), "角色不符仍给出修改入口");
});

test("只读页即使声明了动作也不提供写入口", async () => {
  const entry = { method: "PUT", path: "/api/v1/preferences/reports-ext/view" };
  const declared = {
    ...readOnlyPage,
    actions: [{ id: "edit", entry }],
    items: [{ ...readOnlyPage.items[0], action: "edit", entry }],
  };
  const { app, root, calls } = startShell(
    routesFor({ "GET /api/v1/pages/reports-readonly": () => reply(200, declared) }),
  );
  await app.start();
  await app.navigate("reports-readonly");
  assert.match(root.innerHTML, /data-readonly="true"/);
  assert.match(root.innerHTML, /报表只读展示/);
  assert.ok(!root.innerHTML.includes("data-action-id="), "只读页给出了修改入口");

  await app.submitAction("edit");
  assert.ok(!calls.some((call) => call.method === "PUT"), "只读页仍发出了写请求");
});

test("动作提交后显示已接受与对象引用，终态从对象状态与条件读取", async () => {
  const holder = { page: reportsPage };
  const routes = routesFor({
    "GET /api/v1/pages/reports": () => reply(200, holder.page),
    "PUT /api/v1/preferences/reports-ext/view": () => {
      holder.page = {
        ...reportsPage,
        items: [
          {
            ...reportsPage.items[0],
            observedAt: "2026-11-08T05:00:00Z",
            value: [{ name: "ink", status: { phase: "Reconciled", conditions: [{ type: "Ready", status: "True" }] } }],
          },
          reportsPage.items[1],
        ],
      };
      return reply(202, {
        accepted: true,
        ref: { group: "preferences.kos.tsic.top", kind: "Preference", namespace: "default", name: "view" },
      });
    },
  });
  const { app, root, calls } = startShell(routes);
  await app.start();
  await app.submitAction("mark-viewed");

  assert.match(root.innerHTML, /data-accepted="true"/);
  assert.ok(root.innerHTML.includes("已接受"), "动作被接受后没有给出已接受");
  assert.ok(
    root.innerHTML.includes("preferences.kos.tsic.top/Preference/default/view"),
    "已接受没有带上对象引用",
  );
  assert.ok(root.innerHTML.includes("Reconciled"), "终态没有从对象状态读出");
  assert.ok(root.innerHTML.includes("Ready"), "终态条件没有从对象条件读出");
  assert.ok(
    !calls.some((call) => call.path.startsWith("/api/v1/tasks") || call.path.startsWith("/api/v1/jobs")),
    "外壳自设了任务存储",
  );
});

test("动作提交期间给出提交中状态且不重复提交", async () => {
  let release;
  const write = new Promise((resolve) => {
    release = resolve;
  });
  const { app, root, calls } = startShell(
    routesFor({ "PUT /api/v1/preferences/reports-ext/view": () => write }),
  );
  await app.start();
  const writes = () => calls.filter((call) => call.method === "PUT").length;

  const first = app.submitAction("mark-viewed");
  assert.match(root.innerHTML, /data-pending="true"/);
  assert.ok(root.innerHTML.includes("正在提交…"), "提交中缺少提示");
  assert.equal(writes(), 1, "第一次提交没有发出写请求");

  await app.submitAction("mark-viewed");
  assert.equal(writes(), 1, "提交中重复发出了写请求");

  release(reply(202, { accepted: true, ref: { kind: "Preference", name: "view" } }));
  await first;
  assert.ok(!root.innerHTML.includes('data-pending="true"'), "提交结束后仍显示提交中");
});

test("页面随事件流重读，进度从对象状态与条件更新", async () => {
  const holder = { page: reportsPage };
  const { app, root, sources, calls } = startShell(
    routesFor({ "GET /api/v1/pages/reports": () => reply(200, holder.page) }),
  );
  await app.start();
  const reads = () => calls.filter((call) => call.path === "/api/v1/pages/reports").length;
  const before = reads();

  holder.page = {
    ...reportsPage,
    items: [
      { ...reportsPage.items[0], value: [{ name: "ink", status: { phase: "Reconciled" } }] },
      reportsPage.items[1],
    ],
  };
  sources[0].emit("events", { observedAt: "2026-11-08T05:01:00Z", window: "1h", events: [] });
  await waitForHtml(root, (html) => html.includes("Reconciled"), "页面按事件重读");
  assert.equal(reads(), before + 1, "事件流到达后没有重读当前页");
});

test("create 类动作在空列表上给出可提交的修改入口", async () => {
  // The declaration names its namespace literally: the projection carries no
  // page- or item-level namespace, so a create entry has nothing to fill.
  const collection = "/api/v1/resources/Report?apiVersion=reports.kos.tsic.top/v1&namespace=default";
  const entry = { method: "POST", path: collection };
  const page = {
    ...reportsPage,
    items: [{ ...reportsPage.items[0], action: "mark-viewed", entry, value: [] }, reportsPage.items[1]],
    actions: [{ id: "mark-viewed", requiredRole: "Owner", entry }],
  };
  const { app, root, calls } = startShell(
    routesFor({
      "GET /api/v1/pages/reports": () => reply(200, page),
      "POST /api/v1/resources/Report?apiVersion=reports.kos.tsic.top/v1&namespace=default": () =>
        reply(202, { accepted: true, ref: { group: "reports.kos.tsic.top", kind: "Report", namespace: "default", name: "ink" } }),
    }),
  );
  await app.start();

  assert.match(root.innerHTML, /data-item-actions="reports">.*data-action-id="mark-viewed"/s);
  assert.ok(!root.innerHTML.includes('data-blocked="true"'), "空列表上的 create 入口被禁用");
  assert.ok(!root.innerHTML.includes("data-action-reason="), "可提交的入口附带了原因");

  await app.handleClick({ target: elementWith({ "data-action-id": "mark-viewed" }) });
  assert.ok(
    calls.some((call) => call.method === "POST" && call.path === collection),
    "入口没有按声明的写请求提交",
  );
});

test("拿不到完整引用时明示原因而不是让修改入口消失", async () => {
  const collection = "/api/v1/resources/Report?apiVersion=reports.kos.tsic.top/v1&namespace={namespace}";
  const entry = { method: "POST", path: collection };
  const page = {
    ...reportsPage,
    items: [{ ...reportsPage.items[0], action: "mark-viewed", entry, value: [] }],
    actions: [{ id: "mark-viewed", requiredRole: "Owner", entry }],
  };
  const { app, root, calls } = startShell(
    routesFor({ "GET /api/v1/pages/reports": () => reply(200, page) }),
  );
  await app.start();

  assert.match(root.innerHTML, /data-action-reason="reports"/);
  assert.match(root.innerHTML, /data-blocked="true"/);
  assert.ok(root.innerHTML.includes('data-action-id="mark-viewed"'), "修改入口消失了");

  await app.submitAction("mark-viewed");
  assert.ok(!calls.some((call) => call.method === "POST"), "缺目标命名空间仍发出了写请求");
});

test("页面记录没有命名空间时不自造目标", async () => {
  const collection = "/api/v1/resources/Report?apiVersion=reports.kos.tsic.top/v1&namespace={namespace}";
  const entry = { method: "POST", path: collection };
  // The projection carries no page- or item-level namespace: a payload that
  // happens to spell one must not become the write's target.
  const page = {
    ...reportsPage,
    namespace: "default",
    items: [{ ...reportsPage.items[0], action: "mark-viewed", entry, namespace: "default", value: [] }],
    actions: [{ id: "mark-viewed", requiredRole: "Owner", entry }],
  };
  const { app, root, calls } = startShell(
    routesFor({ "GET /api/v1/pages/reports": () => reply(200, page) }),
  );
  await app.start();

  assert.match(root.innerHTML, /data-blocked="true"/);
  assert.match(root.innerHTML, /data-action-reason="reports"/);
  assert.ok(root.innerHTML.includes("缺少完整的对象引用，无法提交"));

  await app.submitAction("mark-viewed");
  assert.ok(!calls.some((call) => call.method === "POST"), "缺目标命名空间仍发出了写请求");
});

test("写入在途时切换页面，新页的入口不被提交中状态占用", async () => {
  let release;
  const write = new Promise((resolve) => {
    release = resolve;
  });
  const archive = {
    ...reportsPage,
    id: "archive",
    title: "归档",
    items: [{ ...reportsPage.items[0], id: "archived", value: [{ name: "old" }] }],
    actions: [{ id: "mark-viewed", requiredRole: "Owner", entry: { method: "PUT", path: "/api/v1/preferences/archive/view" } }],
  };
  const listed = {
    items: [
      ...registry.items,
      { id: "archive", title: "归档", location: "panel", sources: ["object"], items: [], actions: [] },
    ],
  };
  const { app, root } = startShell(
    routesFor({
      "GET /api/v1/pages": () => reply(200, listed),
      "GET /api/v1/pages/archive": () => reply(200, archive),
      "PUT /api/v1/preferences/reports-ext/view": () => write,
    }),
  );
  await app.start();
  const first = app.submitAction("mark-viewed");
  assert.match(root.innerHTML, /data-pending="true"/);

  await app.navigate("archive");
  assert.ok(!root.innerHTML.includes("data-pending"), "新页继承了上一页的提交中状态");
  assert.ok(!root.innerHTML.includes(" disabled"), "新页的入口被上一页的在途写入禁用");

  release(reply(202, { accepted: true, ref: { kind: "Preference", name: "view" } }));
  await first;
});

test("写入在途时切换页面，旧页的失败不显示在新页", async () => {
  let release;
  const write = new Promise((resolve) => {
    release = resolve;
  });
  const archive = {
    ...reportsPage,
    id: "archive",
    title: "归档",
    items: [{ ...reportsPage.items[0], id: "archived", value: [{ name: "old" }] }],
    actions: [{ id: "mark-viewed", requiredRole: "Owner", entry: { method: "PUT", path: "/api/v1/preferences/archive/view" } }],
  };
  const listed = {
    items: [
      ...registry.items,
      { id: "archive", title: "归档", location: "panel", sources: ["object"], items: [], actions: [] },
    ],
  };
  const { app, root } = startShell(
    routesFor({
      "GET /api/v1/pages": () => reply(200, listed),
      "GET /api/v1/pages/archive": () => reply(200, archive),
      "PUT /api/v1/preferences/reports-ext/view": () => write,
    }),
  );
  await app.start();
  const first = app.submitAction("mark-viewed");
  await app.navigate("archive");

  release(reply(500, { error: { code: "write_failed", message: "写入失败", stage: "write" } }));
  await first;

  assert.ok(!root.innerHTML.includes('data-code="write_failed"'), "旧页的失败显示在新页上");
  assert.match(root.innerHTML, /data-page="archive"/);
});
