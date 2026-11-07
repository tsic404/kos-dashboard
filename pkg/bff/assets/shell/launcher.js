// The launcher (DASH-C-30, DASH-C-52): every installed application, a search
// over the names it declares, the account's grouping and order, and the recent
// list the browser holds. The recent list is the only local source (KOSD-C-53);
// the listing itself is the account's preference (DASH-C-27).

import { escapeHtml } from "./html.js";
import { iconURL, windowCounts } from "./apps.js";

const EMPTY = "暂无数据";
const NO_MATCH = "未找到匹配结果";
const MAX_RECENT_SHOWN = 6;

// recentOf resolves the browser-local recent entries against the installed
// applications. An entry naming an application that is gone is skipped, and a
// repeated application appears once (KOSD-C-53).
export function recentOf(apps, recent) {
  const list = Array.isArray(apps) ? apps : [];
  const byID = new Map(list.map((app) => [app.id, app]));
  const byName = new Map(list.map((app) => [app.name, app]));
  const seen = new Set();
  const resolved = [];
  for (const entry of Array.isArray(recent) ? recent : []) {
    if (!entry) continue;
    const app = (entry.id && byID.get(entry.id)) || (entry.name && byName.get(entry.name)) || null;
    if (!app || seen.has(app.id)) continue;
    seen.add(app.id);
    resolved.push(app);
  }
  return resolved.slice(0, MAX_RECENT_SHOWN);
}

// orderApps puts the launcher list in the order the account chose. The front's
// answer already carries the name order; the recent order is the browser's own
// source, so it is applied here (KOSD-C-53).
export function orderApps(applications, apps, sort, recent) {
  const byID = new Map((apps || []).map((app) => [app.id, app]));
  const listed = (Array.isArray(applications) ? applications : []).map((id) => byID.get(id)).filter(Boolean);
  if (sort !== "recent") return listed;
  const rank = new Map();
  for (const entry of Array.isArray(recent) ? recent : []) {
    if (!entry) continue;
    const id = entry.id || "";
    if (id && !rank.has(id)) rank.set(id, rank.size);
  }
  return [...listed].sort((left, right) => {
    const leftRank = rank.has(left.id) ? rank.get(left.id) : Infinity;
    const rightRank = rank.has(right.id) ? rank.get(right.id) : Infinity;
    if (leftRank !== rightRank) return leftRank - rightRank;
    return String(left.name).localeCompare(String(right.name), "zh-Hans-CN");
  });
}

// filterApps keeps the applications whose name or identity matches the search
// term. An empty term matches every application.
export function filterApps(apps, query) {
  const term = String(query ?? "").trim().toLowerCase();
  if (term === "") return Array.isArray(apps) ? apps : [];
  return (Array.isArray(apps) ? apps : []).filter(
    (app) =>
      String(app.name ?? "").toLowerCase().includes(term) || String(app.id ?? "").toLowerCase().includes(term),
  );
}

// groupApps groups the applications by the extension that declared them, in the
// order the launcher layout stores. An empty category list keeps one flat list.
export function groupApps(apps, categories) {
  const list = Array.isArray(apps) ? apps : [];
  const declared = Array.isArray(categories) ? categories : [];
  if (declared.length === 0) return [{ category: "", apps: list }];
  const groups = new Map();
  for (const app of list) {
    const key = typeof app.extension === "string" ? app.extension : "";
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(app);
  }
  const rank = new Map(declared.map((category, index) => [category, index]));
  return [...groups.entries()]
    .map(([category, entries]) => ({ category, apps: entries }))
    .sort((left, right) => {
      const leftRank = rank.has(left.category) ? rank.get(left.category) : Infinity;
      const rightRank = rank.has(right.category) ? rank.get(right.category) : Infinity;
      if (leftRank !== rightRank) return leftRank - rightRank;
      return left.category.localeCompare(right.category);
    });
}

// renderLauncher renders the launcher overlay. An unreadable layout still lists
// the applications, but the ordering controls say the layout is unavailable.
export function renderLauncher({ launcher, apps, windows, recent, query, open, refusals }) {
  if (!open) return "";
  const counts = windowCounts(windows);
  const ordered = launcher ? orderApps(launcher.applications, apps, launcher.sort, recent) : apps || [];
  const matched = filterApps(ordered, query);
  const groups = groupApps(matched, launcher && launcher.categories);
  const recentApps = recentOf(apps, recent);
  const searching = String(query ?? "").trim() !== "";
  const body = matched.length === 0
    ? `<p class="empty" data-empty="launcher">${searching ? NO_MATCH : EMPTY}</p>`
    : groups.map((group) => renderGroup(group, launcher, counts)).join("");
  return `
<section class="launcher" data-launcher aria-label="应用列表">
  <header class="launcher-header">
    <input type="search" class="launcher-search" data-launcher-search value="${escapeHtml(query)}" placeholder="搜索应用" aria-label="搜索应用">
    <span class="launcher-sort">
      <button type="button" data-command="launcher-sort" data-sort="name" aria-pressed="${Boolean(launcher && launcher.sort === "name")}">名称</button>
      <button type="button" data-command="launcher-sort" data-sort="recent" aria-pressed="${Boolean(launcher && launcher.sort === "recent")}">最近使用</button>
    </span>
    <button type="button" data-action="launcher-close">关闭</button>
  </header>
  ${launcher ? "" : `<p class="launcher-notice" data-launcher-unavailable="true">launcher 布局不可用</p>`}
  ${!searching && recentApps.length > 0 ? renderRecent(recentApps, counts) : ""}
  <div class="launcher-groups">${body}</div>
  ${renderRefusals(refusals)}
</section>`;
}

// renderGroup renders one category section; the flat list carries no heading.
function renderGroup(group, launcher, counts) {
  const heading = group.category && launcher && (launcher.categories || []).includes(group.category)
    ? `<h2 class="launcher-category" data-category="${escapeHtml(group.category)}">${escapeHtml(group.category)}</h2>`
    : "";
  return `${heading}<ul class="launcher-apps">${group.apps.map((app) => renderLauncherApp(app, counts)).join("")}</ul>`;
}

// renderLauncherApp renders one launchable application.
function renderLauncherApp(app, counts) {
  const icon = iconURL(app);
  const count = counts.get(app.id) || 0;
  const picture = icon
    ? `<img class="app-icon" src="${escapeHtml(icon)}" data-icon="${escapeHtml(app.icon)}" alt="" loading="lazy">`
    : `<span class="app-icon app-icon-text" data-icon="${escapeHtml(app.icon)}">${escapeHtml(firstCharacter(app.name))}</span>`;
  return `<li class="launcher-app" data-launcher-app="${escapeHtml(app.id)}" data-running="${count > 0}">
    <button type="button" data-open-app="${escapeHtml(app.id)}">
      ${picture}
      <span class="app-name">${escapeHtml(app.name)}</span>
    </button>
  </li>`;
}

function renderRecent(apps, counts) {
  return `
  <section class="launcher-recent" data-launcher-recent>
    <h2>最近使用</h2>
    <ul class="launcher-apps">${apps.map((app) => renderLauncherApp(app, counts)).join("")}</ul>
  </section>`;
}

// renderRefusals keeps an incomplete contribution observable without listing it
// as an application (KOSD-C-73, DASH-TC-141).
function renderRefusals(refusals) {
  const list = Array.isArray(refusals) ? refusals : [];
  if (list.length === 0) return "";
  const rows = list
    .map(
      (refusal) =>
        `<li data-not-ready="${escapeHtml(refusal.component || refusal.extension || "")}">${escapeHtml(refusal.component || refusal.extension || "")}：${escapeHtml(refusal.reason || "声明无效")}</li>`,
    )
    .join("");
  return `<section class="launcher-not-ready" data-launcher-not-ready><h2>未就绪</h2><ul>${rows}</ul></section>`;
}

function firstCharacter(value) {
  const text = String(value ?? "").trim();
  return text ? text.slice(0, 1) : "?";
}
