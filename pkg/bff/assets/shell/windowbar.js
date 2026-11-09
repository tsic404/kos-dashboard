// The taskbar and window surfaces (DASH-C-74). The taskbar lists the current
// workspace's running windows and nothing else; a window in its error state
// offers restart and close instead of a frame. Every value here comes from the
// front's window-session answer, so the browser keeps no second copy of the
// open set, the z-order or the focus.

import { escapeHtml } from "./html.js";
import { taskbarOf } from "./windows.js";

const STATE_LABELS = {
  opening: "正在打开",
  normal: "",
  minimized: "已最小化",
  maximized: "已最大化",
  fullscreen: "全屏",
  snapped: "已吸附",
  closing: "正在关闭",
  error: "错误",
};

// renderTaskbar renders the current workspace's running windows and the
// workspace switcher. A window of another workspace is absent, so switching
// workspaces changes the bar without touching the windows themselves
// (DASH-C-74). mounted, when given, limits the bar to the windows this page
// mounted: a page load restores no open set, so a window a previous page left
// in the session is not presented as running. The label is the application's
// declared name when the shell has it.
export function renderTaskbar(session, { mounted = null, apps = [] } = {}) {
  const shown = mounted === null ? null : new Set(mounted);
  const names = new Map((Array.isArray(apps) ? apps : []).map((app) => [app.id, app.name]));
  const windows = taskbarOf(session).filter((window) => shown === null || shown.has(window.id));
  const workspaces = renderWorkspaces(session);
  if (windows.length === 0) {
    return `<aside class="taskbar" data-taskbar data-workspace="${escapeHtml(workspaceOf(session))}" data-count="0">${workspaces}<p class="empty">暂无运行窗口</p></aside>`;
  }
  const items = windows
    .map(
      (window) => `
    <button type="button" class="taskbar-item" data-command="window-focus" data-window="${escapeHtml(window.id)}" data-taskbar-window="${escapeHtml(window.id)}" data-app="${escapeHtml(window.application)}"
      data-state="${escapeHtml(window.state)}" data-focused="${Boolean(window.isFocused)}" title="${escapeHtml(names.get(window.application) || window.application)}">
      ${escapeHtml(names.get(window.application) || window.application)}${STATE_LABELS[window.state] ? ` · ${escapeHtml(STATE_LABELS[window.state])}` : ""}
    </button>`,
    )
    .join("");
  return `
<aside class="taskbar" data-taskbar data-workspace="${escapeHtml(workspaceOf(session))}" data-count="${windows.length}">
  ${workspaces}
  <ul class="taskbar-windows">${items}</ul>
</aside>`;
}

// renderWorkspaces renders the session's workspaces as the taskbar's switcher;
// the current one is marked, and switching only changes which windows the bar
// lists (DASH-C-74). The trailing button creates a workspace, so the workspace
// grouping is reachable from the shell and not only from the API.
function renderWorkspaces(session) {
  const workspaces = Array.isArray(session && session.workspaces) ? session.workspaces : [];
  const current = workspaceOf(session);
  if (workspaces.length === 0) return "";
  const entries = workspaces
    .map(
      (workspace) => `<button type="button" class="taskbar-workspace" data-command="workspace" data-workspace="${escapeHtml(workspace)}"${workspace === current ? ' aria-current="true"' : ""}>${escapeHtml(workspace)}</button>`,
    )
    .join("");
  const create = `<button type="button" class="taskbar-workspace-new" data-command="workspace-create" title="新建工作区" aria-label="新建工作区">＋</button>`;
  return `<nav class="taskbar-workspaces" aria-label="工作区">${entries}${create}</nav>`;
}

// renderWindowChrome is the content of one window's bar: its title, the
// transitions DASH-C-74 allows from its state, and the workspace the window is
// grouped into. A state offers no transition it cannot take.
export function renderWindowChrome(window, app = null, workspaces = []) {
  if (!window || window.state === "error") return "";
  const title = (app && app.name) || window.application;
  const commands = windowCommands(window).join("");
  const close = `<button type="button" class="window-close" data-command="window-close" data-window="${escapeHtml(window.id)}">关闭${window.isUnsaved ? "（未保存）" : ""}</button>`;
  return `<span class="window-title">${escapeHtml(title)}</span>${renderWindowWorkspace(window, workspaces)}${commands}${close}`;
}

// renderWindowWorkspace is the control that moves one window into a workspace.
// The list is the session's; a window is grouped by one of its values only
// (DASH-C-74).
function renderWindowWorkspace(window, workspaces) {
  const names = Array.isArray(workspaces) ? workspaces : [];
  if (names.length === 0) return "";
  const options = names
    .map(
      (workspace) => `<option value="${escapeHtml(workspace)}"${workspace === window.workspace ? " selected" : ""}>${escapeHtml(workspace)}</option>`,
    )
    .join("");
  return `<select class="window-workspace" data-window-workspace="${escapeHtml(window.id)}" aria-label="工作区">${options}</select>`;
}

// windowCommands lists the transitions one state allows (DASH-C-74): normal and
// minimized and maximized convert into each other through normal, and a normal,
// maximized or fullscreen window may fill the display (DASH-C-74). Offering a
// transition the front refuses would leave the window unchanged.
function windowCommands(window) {
  const windowId = escapeHtml(window.id);
  const restore = `<button type="button" class="window-restore" data-command="window-restore" data-window="${windowId}">恢复</button>`;
  const minimize = `<button type="button" class="window-minimize" data-command="window-minimize" data-window="${windowId}">最小化</button>`;
  const maximize = `<button type="button" class="window-maximize" data-command="window-maximize" data-window="${windowId}">最大化</button>`;
  const fullscreen = `<button type="button" class="window-fullscreen" data-command="window-fullscreen" data-window="${windowId}">全屏</button>`;
  const snapLeft = `<button type="button" class="window-snap" data-command="window-snap" data-edge="left" data-window="${windowId}">吸附左半</button>`;
  const snapRight = `<button type="button" class="window-snap" data-command="window-snap" data-edge="right" data-window="${windowId}">吸附右半</button>`;
  switch (window.state) {
    case "normal":
      return [minimize, maximize, fullscreen, snapLeft, snapRight];
    case "minimized":
      return [restore];
    case "maximized":
      return [restore, fullscreen];
    case "fullscreen":
      return [restore, snapLeft, snapRight];
    case "snapped":
      return [restore];
    default:
      return [];
  }
}

// renderWindowErrorBody is the body of an error window: why it stopped, and the
// restart and close commands (DASH-C-74). A window that crashed or missed its
// heartbeat never disappears silently.
export function renderWindowErrorBody(window) {
  const reason = (window && window.errorReason) || "窗口已停止运行";
  const windowId = escapeHtml(window && window.id);
  return `<p class="window-error" data-window-error="true">${escapeHtml(reason)}</p>
  <button type="button" class="window-restart" data-command="window-restart" data-window="${windowId}">重启</button>
  <button type="button" class="window-close" data-command="window-close" data-window="${windowId}">关闭</button>`;
}

// workspaceOf names the workspace the session shows.
function workspaceOf(session) {
  return session && typeof session.currentWorkspace === "string" ? session.currentWorkspace : "";
}
