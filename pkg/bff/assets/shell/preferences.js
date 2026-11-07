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
