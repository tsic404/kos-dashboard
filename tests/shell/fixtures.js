// The desktop page as the BFF projects it: the four display items DASH-C-52
// declares, each with the source type it is read from (KOSD-C-53), the actions
// the page declares with the role each one requires (DASH-C-22, KOSD-C-59) and
// the notification centre's read state (DASH-C-27).
const markReadEntry = {
  method: "PUT",
  path: "/api/v1/preferences/kos-dashboard/read",
  body: { value: '{"schemaVersion":1,"readThrough":"2026-03-01T12:00:00Z"}' },
};

export const desktopPage = {
  id: "desktop",
  title: "桌面",
  location: "desktop",
  readOnly: false,
  sources: ["object", "local"],
  items: [
    {
      id: "entries",
      label: "桌面条目",
      source: "object",
      action: "remove-entry",
      entry: {
        method: "DELETE",
        path: "/api/v1/resources/DesktopEntry/{name}?apiVersion=desktop.kos.tsic.top/v1&namespace={namespace}",
      },
      observedAt: "2026-03-01T11:59:00Z",
      stale: false,
      value: [
        {
          name: "media-desktop",
          namespace: "default",
          application: "media",
          displayName: "媒体",
          visibility: "everyone",
          order: 1,
          isAvailable: true,
          endpoint: "media.test",
          address: "https://media.test",
        },
        {
          name: "player-desktop",
          namespace: "default",
          application: "player",
          displayName: "播放器",
          visibility: "owner",
          order: 2,
          isAvailable: false,
          reason: "应用不可用：阶段 Failed",
        },
      ],
    },
    {
      id: "applications",
      label: "已安装应用",
      source: "object",
      observedAt: "2026-03-01T11:59:00Z",
      stale: false,
      reason: "桌面只打开应用，不修改应用",
      value: [
        { name: "media", namespace: "default", phase: "Ready", health: "Healthy" },
        { name: "player", namespace: "default", phase: "Failed", health: "Degraded" },
      ],
    },
    {
      id: "recent",
      label: "最近使用",
      source: "local",
      store: "desktop.recent",
      reason: "最近使用只存在于浏览器",
      value: null,
    },
    {
      id: "notifications",
      label: "通知中心",
      source: "object",
      action: "mark-notifications-read",
      entry: markReadEntry,
      observedAt: "2026-03-01T12:00:00Z",
      stale: false,
      reason: "只读展示最近事件",
      value: {
        observedAt: "2026-03-01T12:00:00Z",
        window: "1h",
        readThrough: "2026-03-01T11:55:00Z",
        unread: 1,
        events: [
          {
            name: "media.1",
            namespace: "default",
            type: "Normal",
            reason: "Created",
            message: "应用已创建",
            count: 2,
            lastSeen: "2026-03-01T11:50:00Z",
            read: true,
            involvedObject: { kind: "Application", namespace: "default", name: "media" },
          },
          {
            name: "media.2",
            namespace: "default",
            type: "Warning",
            reason: "Failed",
            message: "应用重试中",
            count: 1,
            lastSeen: "2026-03-01T11:58:00Z",
            read: false,
            involvedObject: { kind: "Application", namespace: "default", name: "media" },
          },
        ],
      },
    },
  ],
  actions: [
    {
      id: "add-entry",
      requiredRole: "Owner",
      entry: {
        method: "POST",
        path: "/api/v1/resources/DesktopEntry?apiVersion=desktop.kos.tsic.top/v1&namespace={namespace}",
      },
    },
    {
      id: "move-entry",
      requiredRole: "Owner",
      entry: {
        method: "PUT",
        path: "/api/v1/resources/DesktopEntry/{name}?apiVersion=desktop.kos.tsic.top/v1&namespace={namespace}",
      },
    },
    {
      id: "remove-entry",
      requiredRole: "Owner",
      entry: {
        method: "DELETE",
        path: "/api/v1/resources/DesktopEntry/{name}?apiVersion=desktop.kos.tsic.top/v1&namespace={namespace}",
      },
    },
    {
      id: "mark-notifications-read",
      requiredRole: "Member",
      entry: markReadEntry,
    },
  ],
  observedAt: "2026-03-01T11:59:00Z",
  stale: false,
};

// registry lists the pages the control plane answered for one session. The
// answer is already trimmed to the role, so the shell never filters it.
export const registry = {
  items: [
    {
      id: "desktop",
      title: "桌面",
      location: "desktop",
      sources: ["object", "local"],
      items: [],
      actions: desktopPage.actions,
    },
    {
      id: "appstore",
      title: "应用中心",
      location: "panel",
      sources: ["object"],
      items: [],
      actions: [],
    },
  ],
};

// A window application as the control plane reports one loaded `App`
// contribution: the required manifest fields plus the declaring extension
// (KOSD-C-73) and the two surfaces it appears on (DASH-C-30).
export const filesApp = {
  id: "files",
  name: "文件",
  version: "1.0.0",
  icon: "https://files.apps.example.test/assets/icon.svg",
  entry: "https://files.apps.example.test/index.html",
  window: {
    width: 900,
    height: 640,
    minWidth: 320,
    minHeight: 240,
    resizable: true,
    mode: "single",
    defaultState: "normal",
  },
  permissions: ["notifications"],
  extension: "files-ext",
  component: "files",
  digest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
  surfaces: ["launcher", "dock"],
};

export const notesApp = {
  id: "notes",
  name: "笔记",
  version: "1.0.0",
  icon: "assets/icon.svg",
  entry: "https://notes.apps.example.test/index.html",
  window: {
    width: 800,
    height: 600,
    minWidth: 320,
    minHeight: 240,
    resizable: true,
    mode: "multi",
    defaultState: "normal",
  },
  permissions: [],
  extension: "notes-ext",
  component: "notes",
  digest: "sha256:2222222222222222222222222222222222222222222222222222222222222222",
  surfaces: ["launcher", "dock"],
};

export const appsAnswer = { items: [filesApp, notesApp], notReady: [] };

export const dockView = {
  schemaVersion: 1,
  position: "bottom",
  autoHide: false,
  pinned: ["files"],
  isStored: true,
  applications: ["files", "notes"],
};

export const launcherView = {
  schemaVersion: 1,
  sort: "name",
  categories: [],
  isStored: true,
  applications: ["files", "notes"],
};

// openAnswerFor renders one window registration answer for an application.
export function openAnswerFor(app, { id = "win-1", isNew = true } = {}) {
  const origin = new URL(app.entry).origin;
  return {
    window: {
      id,
      appId: app.id,
      account: "owner",
      origin,
      state: "opening",
      isFocused: true,
      defaultWidth: app.window.width,
      defaultHeight: app.window.height,
      defaultState: app.window.defaultState,
      mode: app.window.mode,
      openedAt: "2026-03-01T12:00:00Z",
      expiresAt: "2026-03-01T12:10:00Z",
      handshakeNonce: `nonce-${id}`,
    },
    isNew,
    sandbox: "allow-scripts allow-same-origin",
    frame: `${origin}/app-assets/${app.extension}/${app.version}/${app.digest}/index.html?kos-exp=1&kos-sig=signature`,
  };
}
