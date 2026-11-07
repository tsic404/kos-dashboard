// The dock at the bottom, left or right edge (DASH-C-30): the applications the
// account's dock layout orders, the running indicator and count of each one, the
// launcher entry, and the context menu that changes the layout. Every value the
// dock stores is a preference (DASH-C-27), so the menu writes through the front
// instead of keeping a browser copy.

import { escapeHtml } from "./html.js";
import { iconURL, windowCounts } from "./apps.js";

const POSITION_LABELS = { bottom: "底部", left: "左侧", right: "右侧" };
const POSITIONS = ["bottom", "left", "right"];

// renderDock renders the dock surface. A layout the front could not read keeps
// the launcher reachable but shows no application order the browser invented.
export function renderDock({ dock, apps, windows, menu }) {
  if (!dock) {
    return `
<aside class="dock" data-dock data-state="unavailable">
  <button type="button" class="launcher-toggle" data-action="launcher" aria-label="应用列表" title="应用列表">应用</button>
  <p class="empty dock-empty">dock 布局不可用</p>
</aside>`;
  }
  const byID = new Map((apps || []).map((app) => [app.id, app]));
  const counts = windowCounts(windows);
  const pinned = new Set(Array.isArray(dock.pinned) ? dock.pinned : []);
  const items = (Array.isArray(dock.applications) ? dock.applications : [])
    .map((id) => byID.get(id))
    .filter(Boolean)
    .map((app) => renderDockApp(app, { pinned: pinned.has(app.id), count: counts.get(app.id) || 0 }))
    .join("");
  return `
<aside class="dock" data-dock data-position="${escapeHtml(dock.position)}" data-autohide="${Boolean(dock.autoHide)}" aria-label="dock">
  <button type="button" class="launcher-toggle" data-action="launcher" aria-label="应用列表" title="应用列表">应用</button>
  ${items ? `<ul class="dock-apps">${items}</ul>` : `<p class="empty dock-empty">暂无应用</p>`}
  ${menu ? renderDockMenu(menu, { dock, apps, counts }) : ""}
</aside>`;
}

// renderDockApp renders one dock entry with its running dot and window count.
function renderDockApp(app, { pinned, count }) {
  const icon = iconURL(app);
  const running = count > 0;
  const badge = count > 1 ? `<span class="dock-badge" data-badge="${count}">${count}</span>` : "";
  const picture = icon
    ? `<img class="app-icon" src="${escapeHtml(icon)}" data-icon="${escapeHtml(app.icon)}" alt="" loading="lazy">`
    : `<span class="app-icon app-icon-text" data-icon="${escapeHtml(app.icon)}">${escapeHtml(firstCharacter(app.name))}</span>`;
  return `<li class="dock-app" data-dock-app="${escapeHtml(app.id)}" data-pinned="${pinned}" data-running="${running}">
    <button type="button" class="dock-button" data-open-app="${escapeHtml(app.id)}" title="${escapeHtml(app.name)}" aria-label="${escapeHtml(app.name)}">
      ${picture}
      <span class="dock-running" data-running="${running}" aria-hidden="true"></span>
      ${badge}
    </button>
  </li>`;
}

// renderDockMenu renders the context menu of one dock entry or the dock itself.
function renderDockMenu(menu, { dock, apps, counts }) {
  if (menu.kind === "app") {
    const app = (apps || []).find((entry) => entry.id === menu.appId);
    if (!app) return "";
    const isPinned = Array.isArray(dock.pinned) && dock.pinned.includes(app.id);
    const running = (counts.get(app.id) || 0) > 0;
    return `
  <div class="context-menu" data-menu="app" data-app="${escapeHtml(app.id)}" role="menu">
    <button type="button" role="menuitem" data-command="open" data-app="${escapeHtml(app.id)}">打开</button>
    <button type="button" role="menuitem" data-command="${isPinned ? "unpin" : "pin"}" data-app="${escapeHtml(app.id)}">${isPinned ? "取消固定" : "固定到 dock"}</button>
    ${running ? `<button type="button" role="menuitem" data-command="close-windows" data-app="${escapeHtml(app.id)}">关闭窗口</button>` : ""}
    <button type="button" role="menuitem" data-command="menu-close">关闭菜单</button>
  </div>`;
  }
  const position = POSITIONS.map(
    (candidate) => `<button type="button" role="menuitem" data-command="position" data-position="${candidate}" aria-pressed="${dock.position === candidate}">${POSITION_LABELS[candidate]}</button>`,
  ).join("");
  return `
  <div class="context-menu" data-menu="dock" role="menu">
    <p class="menu-title">位置</p>
    ${position}
    <button type="button" role="menuitem" data-command="autohide">自动隐藏：${dock.autoHide ? "开" : "关"}</button>
    <button type="button" role="menuitem" data-command="menu-close">关闭菜单</button>
  </div>`;
}

function firstCharacter(value) {
  const text = String(value ?? "").trim();
  return text ? text.slice(0, 1) : "?";
}
