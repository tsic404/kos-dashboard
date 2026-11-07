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

// recordRecent puts one opened application in front of the recent list. The
// list is browser storage and never enters a preference or an object
// (KOSD-C-53); the same application appears once.
export function recordRecent(storage, entry, store = RECENT_STORE) {
  if (!storage || typeof storage.setItem !== "function") return [];
  const name = entry && typeof entry.name === "string" ? entry.name : "";
  if (name === "") return readRecent(storage, store);
  const id = entry && typeof entry.id === "string" ? entry.id : "";
  const openedAt = entry && entry.openedAt ? entry.openedAt : new Date().toISOString();
  const kept = readRecent(storage, store).filter((item) => {
    if (id !== "" && item.id === id) return false;
    return !(id === "" && item.name === name);
  });
  const next = [{ ...(id === "" ? {} : { id }), name, openedAt }, ...kept].slice(0, MAX_RECENT);
  storage.setItem(store, JSON.stringify(next));
  return next;
}
