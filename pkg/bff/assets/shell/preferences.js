// The dock and launcher layouts are account preferences, not browser state
// (DASH-C-27): the shell reads them from the front and writes them back through
// the same proxy. It keeps no copy, so a layout set in another browser is the
// one this browser shows.

// The front reads both surfaces from its own routes.
export const DOCK_PATH = "/api/v1/dock";
export const LAUNCHER_PATH = "/api/v1/launcher";

// Writes go through the preference proxy; the front judges a value it could not
// read back before the control plane stores it.
const PREFERENCE_PREFIX = "/api/v1/preferences/kos-dashboard/";
export const DOCK_LAYOUT_PATH = PREFERENCE_PREFIX + "layout";
export const LAUNCHER_LAYOUT_PATH = PREFERENCE_PREFIX + "listing";

// dockLayoutOf keeps only the stored layout fields, so a write never carries
// the answer's derived application list or its isStored marker.
export function dockLayoutOf(view) {
  return {
    schemaVersion: view && view.schemaVersion ? view.schemaVersion : 1,
    position: view && view.position ? view.position : "bottom",
    autoHide: Boolean(view && view.autoHide),
    pinned: Array.isArray(view && view.pinned) ? [...view.pinned] : [],
  };
}

// launcherLayoutOf keeps only the stored launcher fields.
export function launcherLayoutOf(view) {
  return {
    schemaVersion: view && view.schemaVersion ? view.schemaVersion : 1,
    sort: view && view.sort ? view.sort : "name",
    categories: Array.isArray(view && view.categories) ? [...view.categories] : [],
  };
}

// withPinned adds one application at the end of the pins or removes it.
export function withPinned(layout, appId, isPinned) {
  const pinned = (layout.pinned || []).filter((id) => id !== appId);
  if (isPinned) pinned.push(appId);
  return { ...layout, pinned };
}

// withDockPosition moves the dock to one edge.
export function withDockPosition(layout, position) {
  return { ...layout, position };
}

// withAutoHide toggles whether the dock hides until its edge is reached.
export function withAutoHide(layout, autoHide) {
  return { ...layout, autoHide: Boolean(autoHide) };
}

// withSort changes the launcher order.
export function withSort(layout, sort) {
  return { ...layout, sort };
}

// The window layout is the account's cross-browser placement (DASH-C-27): the
// front answers it adapted to the asking browser, and the shell writes back the
// layout projection a window change produced.
export const DESKTOP_LAYOUT_PATH = "/api/v1/desktop/layout";
export const WINDOW_LAYOUT_PATH = PREFERENCE_PREFIX + "windowLayout";

// windowLayoutQuery renders the browser's report: the viewport it renders in
// and the display set it has. The shell places windows on the primary display
// only, so it reports that complete set; a record on a display this browser
// does not have falls back to the primary display (DASH-C-27).
export function windowLayoutQuery(viewport = null) {
  const query = new URLSearchParams();
  const width = Number(viewport && viewport.width) || 0;
  const height = Number(viewport && viewport.height) || 0;
  if (width > 0 && height > 0) query.set("viewport", `${width}x${height}`);
  query.append("display", "");
  return `?${query}`;
}

// encodeWindowLayout renders one layout projection as the stored value: the
// version and its records, and nothing else (DASH-C-27).
export function encodeWindowLayout(layout) {
  return JSON.stringify({
    schemaVersion: layout && layout.schemaVersion,
    records: Array.isArray(layout && layout.records) ? layout.records : [],
  });
}

// readWindowLayout reads the account's layout as this browser sees it: the
// front clamps the bounds and falls back a removed display, and that adaptation
// is the answer's alone (DASH-C-27).
export function readWindowLayout(api, { viewport = null } = {}) {
  return api.get(DESKTOP_LAYOUT_PATH + windowLayoutQuery(viewport));
}

// saveWindowLayout stores one layout projection; the front judges the value
// against the account's stored one before the control plane keeps it.
export async function saveWindowLayout(api, layout) {
  await api.write("PUT", WINDOW_LAYOUT_PATH, { value: encodeWindowLayout(layout) });
}

// readDock reads the account's dock layout and its application order.
export function readDock(api) {
  return api.get(DOCK_PATH);
}

// readLauncher reads the account's launcher layout and its application order.
export function readLauncher(api) {
  return api.get(LAUNCHER_PATH);
}

// saveDock stores one layout and answers the front's read of it, so the surface
// always shows what the account's store now holds.
export async function saveDock(api, layout) {
  await api.write("PUT", DOCK_LAYOUT_PATH, { value: JSON.stringify(layout) });
  return readDock(api);
}

// saveLauncher stores one launcher layout and answers the front's read of it.
export async function saveLauncher(api, layout) {
  await api.write("PUT", LAUNCHER_LAYOUT_PATH, { value: JSON.stringify(layout) });
  return readLauncher(api);
}
