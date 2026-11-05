// The recent list is a browser-local source (KOSD-C-53): it stays in the
// browser and never enters a cluster object or the control-plane projection.

// RECENT_STORE is the key the desktop page declares for its local item.
export const RECENT_STORE = "desktop.recent";
const MAX_RECENT = 20;

// readRecent returns the stored recent list, newest first. A damaged value
// reads as an empty list: the local source is not worth failing the page for,
// and the empty state says so.
export function readRecent(storage, store = RECENT_STORE) {
  if (!storage || typeof storage.getItem !== "function") return [];
  let parsed = null;
  try {
    parsed = JSON.parse(storage.getItem(store) || "[]");
  } catch {
    return [];
  }
  if (!Array.isArray(parsed)) return [];
  return parsed
    .filter((entry) => entry && typeof entry.name === "string" && entry.name !== "")
    .slice(0, MAX_RECENT);
}
