import assert from "node:assert/strict";
import test from "node:test";

import {
  renderItem,
  renderPage,
  renderValue,
  roleAllows,
  sourceLabel,
} from "../../pkg/bff/assets/shell/render.js";
import { desktopPage } from "./fixtures.js";

// itemOf returns one item of the desktop page by identity.
function itemOf(id) {
  return desktopPage.items.find((item) => item.id === id);
}

// accessOf is what one render knows about the caller: the declared actions and
// the session role.
function accessOf(role) {
  return { actions: desktopPage.actions, role };
}

test("桌面页渲染 DASH-C-52 的四类展示项，逐项带来源", () => {
  const html = renderPage(desktopPage, { role: "Owner" });
  for (const [id, source, label] of [
    ["entries", "object", "对象源"],
    ["applications", "object", "对象源"],
    ["recent", "local", "本地源"],
    ["notifications", "object", "对象源"],
  ]) {
    assert.match(html, new RegExp(`data-item="${id}" data-source="${source}"`), `条目 ${id} 未带来源 ${source}`);
    assert.ok(html.includes(label), `条目 ${id} 未显示来源名称 ${label}`);
  }
  assert.equal(html.match(/data-item="/g).length, 4, "桌面页应只有四类展示项");
  assert.match(html, /data-page="desktop"/);
});

test("条目在应用不可用时保留并给出原因", () => {
  const html = renderItem(itemOf("entries"), accessOf("Owner"));
  assert.match(html, /data-entry="media-desktop"/, "可用应用的条目不见了");
  assert.match(html, /data-entry="player-desktop"/, "不可用应用的条目被丢弃");
  assert.match(html, /data-available="true"/);
  assert.match(html, /不可用：应用不可用：阶段 Failed/);
  assert.match(html, /data-action-id="remove-entry" data-name="media-desktop" data-namespace="default"/);
});

test("只有动作声明的角色能看到条目移除入口", () => {
  assert.deepEqual(
    ["Owner", "Member", "Guest"].map((role) => ({
      role,
      hasRemove: renderPage(desktopPage, { role }).includes('data-action-id="remove-entry"'),
    })),
    [
      { role: "Owner", hasRemove: true },
      { role: "Member", hasRemove: false },
      { role: "Guest", hasRemove: false },
    ],
    "移除入口只在 Owner 下出现",
  );
  assert.equal(roleAllows("Owner", "Owner"), true);
  assert.equal(roleAllows("Member", "Owner"), false);
  assert.equal(roleAllows("Guest", "Member"), false);
  assert.equal(roleAllows("Owner", "Member"), true);
  assert.equal(roleAllows("Member", ""), true, "未声明角色的动作对所有角色可用");
  assert.equal(roleAllows("Member", "Unknown"), false, "未知角色要求失败关闭");
});

test("通知中心渲染事件、对象与计数", () => {
  const html = renderItem(itemOf("notifications"), accessOf("Owner"));
  assert.match(html, /data-event="media\.1" data-type="Normal" data-read="true"/);
  assert.match(html, /data-event="media\.2" data-type="Warning" data-read="false"/);
  assert.match(html, /Created/);
  assert.match(html, /应用已创建/);
  assert.match(html, /Application\/default\/media/);
  assert.match(html, /×2/);
  assert.match(html, /窗口 1h/);
});

test("通知中心渲染未读计数与已读水位", () => {
  const html = renderItem(itemOf("notifications"), accessOf("Member"));
  assert.match(html, /data-unread="1"/);
  assert.match(html, /未读 1/);
  assert.match(html, /已读至 2026-03-01 11:55/);
  assert.match(html, /data-read="true">已读/);
  assert.match(html, /data-read="false">未读/);
  assert.match(html, /data-action-id="mark-notifications-read"/);

  const noUnread = renderItem(
    {
      ...itemOf("notifications"),
      value: { ...itemOf("notifications").value, unread: 0 },
    },
    accessOf("Member"),
  );
  assert.match(noUnread, /全部已读/);
  assert.ok(!noUnread.includes('data-action-id="mark-notifications-read"'), "没有未读事件时仍给出标记入口");
});

test("标记已读入口只对动作声明的角色出现", () => {
  assert.match(renderPage(desktopPage, { role: "Member" }), /data-action-id="mark-notifications-read"/);
  assert.match(renderPage(desktopPage, { role: "Owner" }), /data-action-id="mark-notifications-read"/);
  assert.ok(
    !renderPage(desktopPage, { role: "Guest" }).includes('data-action-id="mark-notifications-read"'),
    "Guest 看到了必然被拒的标记入口",
  );
});

test("陈旧读取带陈旧标记，新鲜读取没有", () => {
  const fresh = renderPage(desktopPage, { role: "Owner" });
  assert.ok(!fresh.includes("数据已陈旧"), "新鲜页面被标成陈旧");

  const stale = {
    ...desktopPage,
    stale: true,
    items: [{ ...itemOf("applications"), stale: true }],
  };
  const html = renderPage(stale, { role: "Owner" });
  assert.match(html, /data-stale="true"/);
  assert.ok(html.includes("数据已陈旧"));
  assert.match(html, /观测于 \d{4}-\d{2}-\d{2} \d{2}:\d{2}/);
});

test("只读页面明示原因且不提供修改入口", () => {
  const html = renderPage(
    {
      id: "console",
      title: "只读视图",
      location: "panel",
      readOnly: true,
      readOnlyReason: "原始字段只读",
      stale: false,
      items: [{ id: "raw", label: "原始字段", source: "object", stale: false, value: { phase: "Ready" } }],
    },
    { role: "Owner" },
  );
  assert.match(html, /data-readonly="true"/);
  assert.ok(html.includes("原始字段只读"));
  assert.ok(!html.includes("data-action-id="), "只读页面提供了修改入口");
  assert.match(html, /<dt>phase<\/dt><dd>Ready<\/dd>/);
});

test("没有数据的来源显示空状态", () => {
  const html = renderPage(
    {
      ...desktopPage,
      items: [
        { ...itemOf("recent"), value: [] },
        { ...itemOf("notifications"), value: { observedAt: "2026-03-01T12:00:00Z", window: "1h", unread: 0, events: [] } },
        { ...itemOf("entries"), value: [] },
      ],
    },
    { role: "Owner" },
  );
  assert.match(html, /data-empty="recent"/);
  assert.match(html, /data-empty="notifications"/);
  assert.ok(!html.includes("data-entry="), "空条目列表仍渲染了行");
});

test("值里的标记被转义", () => {
  const html = renderItem(
    {
      ...itemOf("entries"),
      value: [
        {
          name: "media-desktop",
          namespace: "default",
          application: "media",
          displayName: '<img src=x onerror="alert(1)">',
          visibility: "everyone",
          order: 1,
          isAvailable: true,
        },
      ],
    },
    accessOf("Owner"),
  );
  assert.ok(!html.includes("<img"), "未转义的值进入了文档");
  assert.ok(html.includes("&lt;img src=x onerror=&quot;alert(1)&quot;&gt;"));
});

test("对象存在但没有名称时说明无法定位", () => {
  const entry = { method: "PUT", path: "/api/v1/resources/Report/{name}?namespace=default" };
  const html = renderItem(
    {
      id: "records",
      label: "报表",
      source: "object",
      action: "mark-viewed",
      entry,
      value: [{ title: "无名字" }],
    },
    { actions: [{ id: "mark-viewed", entry }], role: "Owner" },
  );
  assert.match(html, /data-action-reason="records"/);
  assert.ok(html.includes("对象缺少名称，无法定位"), "记录没有名称时仍报没有对象");
  assert.ok(!html.includes('data-action-id="mark-viewed"'), "缺名称的记录仍给出了修改入口");
});

test("没有记录时说明没有可修改的对象", () => {
  const entry = { method: "PUT", path: "/api/v1/resources/Report/{name}?namespace=default" };
  const html = renderItem(
    { id: "records", label: "报表", source: "object", action: "mark-viewed", entry, value: [] },
    { actions: [{ id: "mark-viewed", entry }], role: "Owner" },
  );
  assert.ok(html.includes("当前没有可修改的对象"), "空列表没有说明没有对象");
  assert.ok(!html.includes('data-action-id="mark-viewed"'), "空列表给出了修改入口");
});

test("七类来源各有名称", () => {
  for (const [source, label] of [
    ["object", "对象源"],
    ["computed", "计算源"],
    ["metric", "指标源"],
    ["probe", "探测源"],
    ["read", "读取源"],
    ["local", "本地源"],
    ["database", "数据库源"],
  ]) {
    assert.equal(sourceLabel(source), label);
    assert.match(renderItem({ id: "x", label: "项", source, value: "值" }), new RegExp(label));
  }
});

test("通用值按形状渲染", () => {
  assert.match(renderValue([{ name: "media", phase: "Ready" }]), /<th>name<\/th><th>phase<\/th>/);
  assert.match(renderValue(["a", "b"]), /<li>a<\/li><li>b<\/li>/);
  assert.match(renderValue(null), /暂无数据/);
  assert.match(renderValue("就绪"), /<p class="scalar">就绪<\/p>/);
});

test("页面按声明渲染标题、位置、所需权限与来源类型", () => {
  const html = renderPage({ ...desktopPage, requiredRole: "Member" }, { role: "Owner" });
  assert.match(html, /data-required-role="Member"/);
  assert.ok(html.includes("所需权限：成员"));
  assert.match(html, /data-sources="object,local"/);
  assert.ok(html.includes("来源：对象源、本地源"));
  assert.match(html, /<span class="location">桌面<\/span>/);
});
