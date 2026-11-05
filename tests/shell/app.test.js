import assert from "node:assert/strict";
import test from "node:test";

import { createApi } from "../../pkg/bff/assets/shell/api.js";
import { createApp } from "../../pkg/bff/assets/shell/app.js";
import { desktopPage, registry } from "./fixtures.js";

const ownerSession = { username: "owner", role: "Owner", mustChangePassword: false };

const appstorePage = {
  id: "appstore",
  title: "应用中心",
  location: "panel",
  readOnly: true,
  readOnlyReason: "目录来源只读",
  stale: false,
  items: [{ id: "catalogue", label: "可安装", source: "object", stale: false, value: [{ name: "media", version: "1.0.0" }] }],
};

// reply builds the one response shape the client reads.
function reply(status, body) {
  return {
    ok: status >= 200 && status < 300,
    status,
    text: async () => (body === undefined ? "" : JSON.stringify(body)),
  };
}

// fakeFetch answers by "METHOD PATH" and records what the shell sent.
function fakeFetch(routes) {
  const calls = [];
  const fetch = async (path, init = {}) => {
    const method = init.method || "GET";
    calls.push({ method, path, headers: init.headers || {}, body: init.body });
    const handler = routes[`${method} ${path}`];
    if (!handler) return reply(404, { error: { code: "not_found", message: "missing", stage: "read" } });
    return handler();
  };
  return { fetch, calls };
}

function memoryStorage(initial = {}) {
  const values = new Map(Object.entries(initial));
  return {
    getItem: (key) => (values.has(key) ? values.get(key) : null),
    setItem: (key, value) => values.set(key, String(value)),
  };
}

// fakeEventSource records the subscription so a case can push one frame.
function fakeEventSource() {
  const sources = [];
  class FakeEventSource {
    constructor(url) {
      this.url = url;
      this.handlers = new Map();
      this.isClosed = false;
      sources.push(this);
    }

    addEventListener(name, handler) {
      this.handlers.set(name, handler);
    }

    close() {
      this.isClosed = true;
    }

    emit(name, payload) {
      const handler = this.handlers.get(name);
      if (handler) handler({ data: JSON.stringify(payload) });
    }
  }
  return { EventSource: FakeEventSource, sources };
}

// elementWith answers closest() for the attributes one click target carries.
function elementWith(attributes) {
  return {
    closest(selector) {
      const attribute = selector.slice(1, -1);
      if (!(attribute in attributes)) return null;
      return { getAttribute: (name) => (name in attributes ? attributes[name] : null) };
    },
  };
}

// waitForHtml waits for one rendered document to satisfy a predicate; the
// shell renders after its reads settle, so the wait is what makes the
// assertion independent of scheduling.
async function waitForHtml(root, predicate, label) {
  const deadline = Date.now() + 2000;
  while (Date.now() < deadline) {
    if (predicate(root.innerHTML)) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  assert.fail(`timed out waiting for ${label}`);
}

function startShell({ routes, storage = memoryStorage() }) {
  const { fetch, calls } = fakeFetch(routes);
  const api = createApi({ fetch, document: { cookie: "kos_bff_csrf=csrf-token" } });
  const root = { innerHTML: "" };
  const { EventSource, sources } = fakeEventSource();
  const app = createApp({ api, root, storage, EventSource });
  return { app, root, calls, sources };
}

function routesFor({ pages = registry, session = () => reply(200, ownerSession), page = () => desktopPage } = {}) {
  return {
    "GET /api/v1/auth/session": session,
    "GET /api/v1/pages": () => reply(200, pages),
    "GET /api/v1/desktop": () => reply(200, page()),
    "GET /api/v1/pages/appstore": () => reply(200, appstorePage),
    "DELETE /api/v1/resources/DesktopEntry/media-desktop?apiVersion=desktop.kos.tsic.top/v1&namespace=default": () =>
      reply(202, { accepted: true }),
    "PUT /api/v1/preferences/kos-dashboard/read": () => reply(200, { accepted: true }),
  };
}

test("会话被拒绝时显示登录表单", async () => {
  const { app, root } = startShell({
    routes: {
      "GET /api/v1/auth/session": () =>
        reply(401, { error: { code: "unauthenticated", message: "会话无效或已过期，请重新登录", stage: "authorize" } }),
    },
  });
  await app.start();
  assert.match(root.innerHTML, /data-form="login"/);
  assert.match(root.innerHTML, /name="password"/);
  assert.ok(!root.innerHTML.includes("data-nav="), "未登录就渲染了页面入口");
});

test("登录后按页面注册表渲染入口并打开桌面页", async () => {
  const { app, root, calls } = startShell({ routes: routesFor() });
  await app.start();
  assert.match(root.innerHTML, /data-nav="desktop"/);
  assert.match(root.innerHTML, /data-nav="appstore"/);
  assert.match(root.innerHTML, /data-nav="desktop" aria-current="page"/);
  assert.match(root.innerHTML, /data-page="desktop"/);
  assert.match(root.innerHTML, /data-item="entries"/);
  assert.match(root.innerHTML, /owner · 所有者/);
  assert.ok(
    calls.some((call) => call.path === "/api/v1/desktop"),
    "桌面页没有读本产品的投影",
  );
  assert.ok(
    !calls.some((call) => call.path === "/api/v1/pages/desktop"),
    "桌面页读了控制面页面投影",
  );
});

test("注册表没列出的页面没有入口，直接导航被拒绝", async () => {
  const { app, root } = startShell({
    routes: routesFor({ pages: { items: [registry.items[0]] } }),
  });
  await app.start();
  assert.ok(!root.innerHTML.includes('data-nav="appstore"'), "注册表之外的页面出现在入口里");
  await app.navigate("appstore");
  assert.match(root.innerHTML, /data-code="page_not_found"/);
  assert.ok(!root.innerHTML.includes('data-page="appstore"'), "未列出的页面仍被渲染");
});

test("组件停用后页面入口消失", async () => {
  const { app, root } = startShell({ routes: routesFor({ pages: { items: [] } }) });
  await app.start();
  assert.ok(!root.innerHTML.includes("data-nav="), "停用组件后仍有页面入口");
  await app.navigate("desktop");
  assert.match(root.innerHTML, /data-code="page_not_found"/);
});

test("导航到另一个页面时读取该页面的投影", async () => {
  const { app, root, calls } = startShell({ routes: routesFor() });
  await app.start();
  await app.handleClick({ target: elementWith({ "data-nav": "appstore" }) });
  assert.match(root.innerHTML, /data-page="appstore"/);
  assert.match(root.innerHTML, /data-readonly="true"/);
  assert.ok(calls.some((call) => call.path === "/api/v1/pages/appstore"));
});

test("最近使用读浏览器存储，键取页面声明的 store", async () => {
  const declared = {
    ...desktopPage,
    items: desktopPage.items.map((item) => (item.id === "recent" ? { ...item, store: "desk.recent.v2" } : item)),
  };
  const storage = memoryStorage({
    "desktop.recent": JSON.stringify([{ name: "不该读到" }]),
    "desk.recent.v2": JSON.stringify([{ name: "播放器", openedAt: "2026-03-01T10:00:00Z" }]),
  });
  const { app, root } = startShell({ routes: routesFor({ page: () => declared }), storage });
  await app.start();
  assert.match(root.innerHTML, /data-recent="播放器"/);
  assert.ok(!root.innerHTML.includes("不该读到"), "最近使用读了别的键");
});

test("事件流带来新事件后重读桌面页，读取状态来自投影", async () => {
  const holder = { page: desktopPage };
  const { app, root, sources, calls } = startShell({ routes: routesFor({ page: () => holder.page }) });
  await app.start();
  assert.equal(sources.length, 1, "登录后没有订阅事件流");
  assert.equal(sources[0].url, "/api/v1/stream");
  assert.match(root.innerHTML, /data-event="media\.1"/);
  assert.match(root.innerHTML, /data-unread="1"/);

  const refreshed = {
    ...desktopPage,
    items: desktopPage.items.map((item) =>
      item.id === "notifications"
        ? {
            ...item,
            value: {
              ...item.value,
              unread: 2,
              events: [
                {
                  name: "media.3",
                  namespace: "default",
                  type: "Warning",
                  reason: "Failed",
                  message: "应用重启",
                  count: 1,
                  lastSeen: "2026-03-01T11:59:30Z",
                  read: false,
                  involvedObject: { kind: "Application", namespace: "default", name: "media" },
                },
                ...item.value.events,
              ],
            },
          }
        : item,
    ),
  };
  holder.page = refreshed;
  const before = calls.length;
  sources[0].emit("events", { observedAt: "2026-03-01T12:00:30Z", window: "1h", events: [] });
  await waitForHtml(root, (html) => html.includes('data-event="media.3"'), "the streamed event");
  assert.match(root.innerHTML, /data-unread="2"/);
  assert.ok(
    calls.slice(before).some((call) => call.method === "GET" && call.path === "/api/v1/desktop"),
    "事件流到达后没有重读桌面页",
  );

  sources[0].emit("stale", { observedAt: "2026-03-01T12:01:00Z", stale: true, reason: "控制面事件读取失败" });
  assert.match(root.innerHTML, /数据已陈旧/);
  assert.ok(root.innerHTML.includes("控制面事件读取失败"));
  assert.match(root.innerHTML, /data-event="media\.3"/, "断流后丢掉了上次观测");
});

test("页面动作提交声明的控制面写请求并重新读取", async () => {
  const { app, calls } = startShell({ routes: routesFor() });
  await app.start();
  const before = calls.length;
  await app.handleClick({
    target: elementWith({ "data-action-id": "remove-entry", "data-name": "media-desktop", "data-namespace": "default" }),
  });
  const issued = calls.slice(before);
  const write = issued.find((call) => call.method === "DELETE");
  assert.ok(write, "移除动作没有发出写请求");
  assert.equal(
    write.path,
    "/api/v1/resources/DesktopEntry/media-desktop?apiVersion=desktop.kos.tsic.top/v1&namespace=default",
  );
  assert.equal(write.headers["X-CSRF-Token"], "csrf-token");
  assert.ok(
    issued.some((call) => call.method === "GET" && call.path === "/api/v1/desktop"),
    "写请求后没有重新读取桌面页",
  );
});

test("标记已读提交页面声明的写入体并重读", async () => {
  const { app, root, calls } = startShell({ routes: routesFor() });
  await app.start();
  assert.match(root.innerHTML, /data-action-id="mark-notifications-read"/);
  const before = calls.length;
  await app.handleClick({ target: elementWith({ "data-action-id": "mark-notifications-read" }) });
  const issued = calls.slice(before);
  const write = issued.find((call) => call.method === "PUT");
  assert.ok(write, "标记已读没有发出写请求");
  assert.equal(write.path, "/api/v1/preferences/kos-dashboard/read");
  assert.deepEqual(JSON.parse(write.body), {
    value: '{"schemaVersion":1,"readThrough":"2026-03-01T12:00:00Z"}',
  });
  assert.equal(write.headers["X-CSRF-Token"], "csrf-token");
  assert.ok(
    issued.some((call) => call.method === "GET" && call.path === "/api/v1/desktop"),
    "标记已读后没有重读桌面页",
  );
});

test("Member 与 Guest 看不到必然被拒的入口", async () => {
  for (const [role, hasMarkRead] of [
    ["Member", true],
    ["Guest", false],
  ]) {
    const { app, root } = startShell({
      routes: routesFor({ session: () => reply(200, { username: "member", role }) }),
    });
    await app.start();
    assert.ok(!root.innerHTML.includes('data-action-id="remove-entry"'), `${role} 看到了移除入口`);
    assert.equal(
      root.innerHTML.includes('data-action-id="mark-notifications-read"'),
      hasMarkRead,
      `${role} 的标记已读入口与动作要求的角色不符`,
    );
  }
});

test("地址里的页面在启动时打开，未知页面回到默认路由", async () => {
  const { app, root } = startShell({ routes: routesFor() });
  await app.start("appstore");
  assert.match(root.innerHTML, /data-page="appstore"/);

  const fallback = startShell({ routes: routesFor() });
  await fallback.app.start("missing-page");
  assert.match(fallback.root.innerHTML, /data-page="desktop"/);
});

test("动作被拒绝时保留当前页面并显示拒绝原因", async () => {
  const routes = routesFor();
  routes["DELETE /api/v1/resources/DesktopEntry/media-desktop?apiVersion=desktop.kos.tsic.top/v1&namespace=default"] = () =>
    reply(403, { error: { code: "permission_denied", message: "只有所有者可以删除条目", stage: "authorize" } });
  const { app, root } = startShell({ routes });
  await app.start();
  await app.handleClick({
    target: elementWith({ "data-action-id": "remove-entry", "data-name": "media-desktop", "data-namespace": "default" }),
  });
  assert.match(root.innerHTML, /data-code="permission_denied"/);
  assert.ok(root.innerHTML.includes("只有所有者可以删除条目"));
  assert.match(root.innerHTML, /data-page="desktop"/, "动作失败后页面被清空了");
});

test("读取失败显示控制面的错误包络", async () => {
  const routes = routesFor();
  routes["GET /api/v1/desktop"] = () =>
    reply(403, { error: { code: "permission_denied", message: "没有权限读取该页面", stage: "authorize" } });
  const { app, root } = startShell({ routes });
  await app.start();
  assert.match(root.innerHTML, /data-code="permission_denied"/);
  assert.ok(root.innerHTML.includes("没有权限读取该页面"));
});

test("登录失败显示控制面的拒绝", async () => {
  const routes = routesFor({
    session: () => reply(401, { error: { code: "unauthenticated", message: "会话无效或已过期，请重新登录", stage: "authorize" } }),
  });
  routes["POST /api/v1/auth/login"] = () =>
    reply(401, { error: { code: "unauthenticated", message: "账号或口令不正确", stage: "authorize" } });
  const { app, root } = startShell({ routes });
  await app.start();
  await app.login("owner", "wrong");
  assert.ok(root.innerHTML.includes("账号或口令不正确"));
  assert.match(root.innerHTML, /data-form="login"/);

  routes["POST /api/v1/auth/login"] = () => reply(200, { ...ownerSession, csrfToken: "token" });
  await app.login("owner", "right");
  assert.match(root.innerHTML, /data-nav="desktop"/);
});

test("深链在登录后打开请求的页面", async () => {
  const routes = routesFor({
    session: () => reply(401, { error: { code: "unauthenticated", message: "会话无效或已过期，请重新登录", stage: "authorize" } }),
  });
  routes["POST /api/v1/auth/login"] = () => reply(200, { ...ownerSession, csrfToken: "token" });
  const { app, root } = startShell({ routes });
  await app.start("appstore");
  assert.match(root.innerHTML, /data-form="login"/);
  await app.login("owner", "right");
  assert.match(root.innerHTML, /data-page="appstore"/);
});
