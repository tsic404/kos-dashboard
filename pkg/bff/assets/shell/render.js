// The document the shell writes. Every function here is pure: it takes the
// data the control-plane API answered and returns the HTML the browser shows,
// so the rendered page can be judged without a DOM.

import { pagesOf } from "./registry.js";

const SOURCE_LABELS = {
  object: "对象源",
  computed: "计算源",
  metric: "指标源",
  probe: "探测源",
  read: "读取源",
  local: "本地源",
  database: "数据库源",
};

const LOCATION_LABELS = { desktop: "桌面", panel: "面板", window: "窗口" };
const ROLE_LABELS = { Owner: "所有者", Member: "成员", Guest: "访客" };
// ROLE_RANK orders the roles the control plane serves (KOSD-C-59): a role
// performs every action the roles below it may perform.
const ROLE_RANK = { Guest: 1, Member: 2, Owner: 3 };
const VISIBILITY_LABELS = { everyone: "所有人", owner: "仅所有者" };
const EMPTY = "暂无数据";
const MAX_VALUE_COLUMNS = 6;

// escapeHtml keeps every answered value out of the document's markup.
function escapeHtml(value) {
  return String(value ?? "").replace(/[&<>"']/g, (character) => {
    switch (character) {
      case "&":
        return "&amp;";
      case "<":
        return "&lt;";
      case ">":
        return "&gt;";
      case '"':
        return "&quot;";
      default:
        return "&#39;";
    }
  });
}

// sourceLabel names one of the seven source types (KOSD-C-53).
export function sourceLabel(source) {
  return SOURCE_LABELS[source] || source || "未知来源";
}

// roleAllows reports whether one session role may submit an action that names
// requiredRole. An unknown role or an unknown requirement fails closed, so the
// shell never offers a write the control plane would refuse (KOSD-C-59).
export function roleAllows(sessionRole, requiredRole) {
  if (!requiredRole) return true;
  if (!Object.prototype.hasOwnProperty.call(ROLE_RANK, requiredRole)) return false;
  return (ROLE_RANK[sessionRole] || 0) >= ROLE_RANK[requiredRole];
}

// access is what one page render knows about the caller: the actions the page
// declared and the session's role.
function accessOf(page, session) {
  return {
    actions: Array.isArray(page.actions) ? page.actions : [],
    role: session && session.role ? session.role : "",
  };
}

// permits reports whether the page declares the action and the role may submit
// it. An action the payload does not declare is never offered.
function permits(access, actionId) {
  if (!actionId) return false;
  const actions = access && Array.isArray(access.actions) ? access.actions : [];
  const action = actions.find((candidate) => candidate.id === actionId);
  if (!action) return false;
  return roleAllows(access.role, action.requiredRole);
}

function locationLabel(location) {
  return LOCATION_LABELS[location] || location || "";
}

function roleLabel(role) {
  return ROLE_LABELS[role] || role || "";
}

function visibilityLabel(visibility) {
  return VISIBILITY_LABELS[visibility] || visibility || "";
}

function pad(value) {
  return String(value).padStart(2, "0");
}

// formatTime renders one observation moment; an unreadable value renders
// nothing rather than a wrong time.
function formatTime(value) {
  if (!value) return "";
  const moment = new Date(value);
  if (Number.isNaN(moment.getTime())) return "";
  return `${moment.getFullYear()}-${pad(moment.getMonth() + 1)}-${pad(moment.getDate())} ${pad(moment.getHours())}:${pad(moment.getMinutes())}`;
}

// renderBoot is the state before the session is known.
export function renderBoot() {
  return `<p class="booting" data-state="booting">正在载入桌面…</p>`;
}

// renderLogin is the sign-in form the shell shows when the session is refused
// (KOSD-C-63). The BFF keeps the control-plane token out of this exchange.
export function renderLogin({ error } = {}) {
  return `
<form class="login" data-form="login">
  <h1>KOS 桌面</h1>
  <p class="hint">使用控制面账号登录。</p>
  ${error ? renderFailure(error) : ""}
  <label>账号<input name="username" autocomplete="username" required></label>
  <label>口令<input name="password" type="password" autocomplete="current-password" required></label>
  <button type="submit">登录</button>
</form>`;
}

// renderShell is the whole document: the top bar, the page registry as
// navigation, and the current page.
export function renderShell({ session, registry, pageId, page, error }) {
  const pages = pagesOf(registry);
  const isListed = pages.some((entry) => entry.id === pageId);
  let body = "";
  if (page) body = renderPage(page, session);
  else if (!error) body = isListed ? `<p class="loading" data-state="loading">正在读取页面…</p>` : renderNoPages();
  // A refusal keeps the page it refused to update, so the shell never drops
  // what the browser already shows because one write failed.
  const content = (error ? renderFailure(error) : "") + body;
  return `
<div class="shell" data-route="${escapeHtml(pageId || "")}">
  <header class="topbar">
    <span class="brand">KOS 桌面</span>
    <span class="account" data-username="${escapeHtml(session.username)}" data-role="${escapeHtml(session.role)}">${escapeHtml(session.username)} · ${escapeHtml(roleLabel(session.role))}</span>
    <button type="button" data-action="logout">退出</button>
  </header>
  <div class="layout">
    ${renderNavigation(pages, pageId)}
    <main class="page-area">${content}</main>
  </div>
</div>`;
}

// renderNavigation renders one entry per page the registry lists, grouped by
// the location the page declares.
function renderNavigation(pages, pageId) {
  const groups = new Map();
  for (const page of pages) {
    const key = page.location || "panel";
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(page);
  }
  const sections = [...groups.entries()]
    .map(
      ([location, entries]) => `
    <section class="nav-group" data-location="${escapeHtml(location)}">
      <p class="nav-title">${escapeHtml(locationLabel(location))}</p>
      ${entries
        .map(
          (page) =>
            `<a class="nav-entry" href="#/${escapeHtml(page.id)}" data-nav="${escapeHtml(page.id)}"${page.id === pageId ? ' aria-current="page"' : ""}>${escapeHtml(page.title)}</a>`,
        )
        .join("")}
    </section>`,
    )
    .join("");
  return `<nav class="nav" aria-label="页面">${sections}</nav>`;
}

function renderNoPages() {
  return `<section class="empty-pages"><h1>没有可打开的页面</h1><p class="empty">${EMPTY}</p></section>`;
}

// renderPage renders one page: its declared items with their sources, its
// read-only notice and the banner the shell shows above it. Only actions the
// session role may submit get an entry (KOSD-C-59).
export function renderPage(page, session) {
  const access = accessOf(page, session);
  const items = (page.items || []).map((item) => renderItem(item, access)).join("");
  return `
<article class="page" data-page="${escapeHtml(page.id)}" data-stale="${Boolean(page.stale)}">
  <header class="page-header">
    <h1>${escapeHtml(page.title)}</h1>
    <span class="location">${escapeHtml(locationLabel(page.location))}</span>
    ${page.stale ? renderStaleMark() : ""}
  </header>
  ${renderBanner(page.banner)}
  ${page.readOnly ? `<p class="readonly" data-readonly="true">只读：${escapeHtml(page.readOnlyReason || "该页面不提供修改入口")}</p>` : ""}
  <div class="items">${items || `<p class="empty">${EMPTY}</p>`}</div>
</article>`;
}

function renderBanner(banner) {
  if (!banner) return "";
  return `<div class="banner" data-banner="${escapeHtml(banner.kind)}">${escapeHtml(banner.message)}${banner.reason ? `：${escapeHtml(banner.reason)}` : ""}</div>`;
}

function renderStaleMark() {
  return `<span class="stale" data-stale="true">数据已陈旧</span>`;
}

// renderItem renders one display item: its label, its source type and the
// value its source answered (KOSD-C-53).
export function renderItem(item, access = { actions: [], role: "" }) {
  const observed = item.observedAt
    ? `<span class="observed">观测于 ${escapeHtml(formatTime(item.observedAt))}</span>`
    : "";
  return `
<section class="item" data-item="${escapeHtml(item.id)}" data-source="${escapeHtml(item.source)}">
  <header class="item-header">
    <h2>${escapeHtml(item.label)}</h2>
    <span class="source">${escapeHtml(sourceLabel(item.source))}</span>
    ${observed}
    ${item.stale ? renderStaleMark() : ""}
  </header>
  <div class="value">${renderItemValue(item, access)}</div>
  ${item.reason ? `<p class="reason">${escapeHtml(item.reason)}</p>` : ""}
</section>`;
}

function renderItemValue(item, access) {
  switch (item.id) {
    case "entries":
      return renderEntries(item, access);
    case "applications":
      return renderApplications(item);
    case "recent":
      return renderRecent(item);
    case "notifications":
      return renderNotifications(item, access);
    default:
      return renderValue(item.value);
  }
}

// renderEntries renders the desktop entries with the application they point
// at, whether it is openable, and the modification entry the page declares.
// The removal button appears only for a role the action admits (KOSD-C-59).
function renderEntries(item, access) {
  const entries = Array.isArray(item.value) ? item.value : [];
  if (entries.length === 0) return `<p class="empty">${EMPTY}</p>`;
  const canRemove = permits(access, item.action);
  const rows = entries
    .map((entry) => {
      const state = entry.isAvailable
        ? `<span class="available" data-available="true">可用</span>`
        : `<span class="unavailable" data-available="false">不可用：${escapeHtml(entry.reason || "原因未知")}</span>`;
      const action = canRemove
        ? `<button type="button" data-action-id="${escapeHtml(item.action)}" data-name="${escapeHtml(entry.name)}" data-namespace="${escapeHtml(entry.namespace || "")}">移除</button>`
        : "";
      return `<tr data-entry="${escapeHtml(entry.name)}">
        <td class="display-name">${escapeHtml(entry.displayName || entry.name)}</td>
        <td class="application">${escapeHtml(entry.application)}</td>
        <td class="visibility">${escapeHtml(visibilityLabel(entry.visibility))}</td>
        <td class="order">${escapeHtml(entry.order)}</td>
        <td class="state">${state}</td>
        <td class="address">${escapeHtml(entry.address || entry.endpoint || "")}</td>
        <td class="row-actions">${action}</td>
      </tr>`;
    })
    .join("");
  return `<table class="entries"><thead><tr><th>名称</th><th>应用</th><th>可见性</th><th>排序</th><th>状态</th><th>地址</th><th></th></tr></thead><tbody>${rows}</tbody></table>`;
}

// renderApplications renders the installed applications the session may see.
function renderApplications(item) {
  const applications = Array.isArray(item.value) ? item.value : [];
  if (applications.length === 0) return `<p class="empty">${EMPTY}</p>`;
  const rows = applications
    .map(
      (application) => `<tr data-application="${escapeHtml(application.name)}">
        <td class="name">${escapeHtml(application.name)}</td>
        <td class="namespace">${escapeHtml(application.namespace || "")}</td>
        <td class="phase">${escapeHtml(application.phase || "")}</td>
        <td class="health">${escapeHtml(application.health || "")}</td>
      </tr>`,
    )
    .join("");
  return `<table class="applications"><thead><tr><th>应用</th><th>命名空间</th><th>阶段</th><th>健康</th></tr></thead><tbody>${rows}</tbody></table>`;
}

// renderRecent renders the browser-local recent list; the empty state says the
// browser holds nothing yet.
function renderRecent(item) {
  const recent = Array.isArray(item.value) ? item.value : [];
  if (recent.length === 0) return `<p class="empty" data-empty="recent">${EMPTY}</p>`;
  const rows = recent
    .map(
      (entry) => `<li data-recent="${escapeHtml(entry.name)}">
        <span class="recent-name">${escapeHtml(entry.name)}</span>
        <time>${escapeHtml(formatTime(entry.openedAt))}</time>
      </li>`,
    )
    .join("");
  return `<ol class="recent">${rows}</ol>`;
}

// renderNotifications renders the notification centre from the event read
// (KOSD-C-64): the same events the object route answers, each with the
// account's read state and the watermark it last marked (DASH-C-27). The
// mark-read entry appears only for a role the action admits.
function renderNotifications(item, access) {
  const centre = item.value && typeof item.value === "object" ? item.value : null;
  const events = centre && Array.isArray(centre.events) ? centre.events : [];
  const hasReadState = Boolean(centre) && typeof centre.unread === "number";
  const unread = hasReadState ? centre.unread : 0;
  const summary = hasReadState ? renderNotificationSummary(centre) : "";
  const markRead =
    hasReadState && unread > 0 && permits(access, item.action)
      ? `<p class="item-actions"><button type="button" data-action-id="${escapeHtml(item.action)}">标为已读</button></p>`
      : "";
  if (events.length === 0) return `${summary}${markRead}<p class="empty" data-empty="notifications">${EMPTY}</p>`;
  const rows = events
    .map(
      (event) => `<li data-event="${escapeHtml(event.name)}" data-type="${escapeHtml(event.type)}"${typeof event.read === "boolean" ? ` data-read="${event.read}"` : ""}>
        <span class="read-state" data-read="${event.read === true}">${typeof event.read === "boolean" ? (event.read ? "已读" : "未读") : ""}</span>
        <span class="event-type">${escapeHtml(event.type)}</span>
        <span class="event-reason">${escapeHtml(event.reason)}</span>
        <span class="event-message">${escapeHtml(event.message)}</span>
        <span class="event-object">${escapeHtml(objectLabel(event.involvedObject))}</span>
        <time>${escapeHtml(formatTime(event.lastSeen))}</time>
        <span class="event-count">×${escapeHtml(event.count)}</span>
      </li>`,
    )
    .join("");
  return `${summary}${markRead}<ul class="events">${rows}</ul>`;
}

// renderNotificationSummary states the unread count, the watermark the account
// last marked and the read window.
function renderNotificationSummary(centre) {
  const unread = centre.unread > 0 ? `未读 ${centre.unread}` : "全部已读";
  const readThrough = centre.readThrough ? ` · 已读至 ${escapeHtml(formatTime(centre.readThrough))}` : "";
  const window = centre.window ? ` · 窗口 ${escapeHtml(centre.window)}` : "";
  return `<p class="notifications-summary" data-unread="${escapeHtml(centre.unread)}">${escapeHtml(unread)}${readThrough}${window}</p>`;
}

function objectLabel(reference) {
  if (!reference || typeof reference !== "object") return "";
  return [reference.kind, reference.namespace, reference.name].filter(Boolean).join("/");
}

// renderValue renders a value no dedicated renderer covers: a table for a list
// of records, a definition list for one record, and text otherwise.
export function renderValue(value) {
  if (value === null || value === undefined) return `<p class="empty">${EMPTY}</p>`;
  if (Array.isArray(value)) {
    if (value.length === 0) return `<p class="empty">${EMPTY}</p>`;
    if (value.every((row) => row && typeof row === "object" && !Array.isArray(row))) {
      return renderTable(value);
    }
    return `<ul class="values">${value.map((entry) => `<li>${escapeHtml(formatValue(entry))}</li>`).join("")}</ul>`;
  }
  if (typeof value === "object") {
    const rows = Object.entries(value)
      .map(([key, entry]) => `<dt>${escapeHtml(key)}</dt><dd>${escapeHtml(formatValue(entry))}</dd>`)
      .join("");
    return `<dl class="record">${rows}</dl>`;
  }
  return `<p class="scalar">${escapeHtml(formatValue(value))}</p>`;
}

function renderTable(rows) {
  const columns = Object.keys(rows[0]).slice(0, MAX_VALUE_COLUMNS);
  const head = columns.map((column) => `<th>${escapeHtml(column)}</th>`).join("");
  const body = rows
    .map(
      (row) => `<tr>${columns.map((column) => `<td>${escapeHtml(formatValue(row[column]))}</td>`).join("")}</tr>`,
    )
    .join("");
  return `<table class="values"><thead><tr>${head}</tr></thead><tbody>${body}</tbody></table>`;
}

function formatValue(value) {
  if (value === null || value === undefined) return "";
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}

// renderFailure renders one refusal in the control-plane envelope, so a caller
// reads the same code and stage from the page as from the API (KOSD-C-12).
function renderFailure(failure) {
  const code = failure && failure.code ? failure.code : "internal";
  const message = failure && failure.message ? failure.message : "请求失败";
  const stage = failure && failure.stage ? failure.stage : "";
  const status = failure && failure.status ? failure.status : "";
  return `<section class="failure" data-code="${escapeHtml(code)}" data-status="${escapeHtml(status)}">
    <h2>无法完成</h2>
    <p class="message">${escapeHtml(message)}</p>
    <p class="detail">${escapeHtml(code)}${stage ? ` · ${escapeHtml(stage)}` : ""}</p>
    ${failure && failure.retryable ? `<p class="retryable">可以重试</p>` : ""}
    <button type="button" data-action="retry">重试</button>
  </section>`;
}
