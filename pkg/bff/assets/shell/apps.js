// The installed applications the control plane answers (DASH-C-30, KOSD-C-73).
// The dock and the launcher render this one list: an application whose manifest
// satisfies the required fields appears on both surfaces, and a contribution
// that produced no entry appears on neither.

// appsOf returns the application entries an answer carries.
export function appsOf(answer) {
  const items = answer && Array.isArray(answer.items) ? answer.items : [];
  return items.filter((entry) => entry && typeof entry.id === "string" && entry.id !== "");
}

// refusalsOf returns the contributions the answer refused, with their reasons.
// A refusal is observable without surfacing an entry.
export function refusalsOf(answer) {
  return answer && Array.isArray(answer.notReady) ? answer.notReady : [];
}

// findApp resolves one installed application.
export function findApp(apps, appId) {
  const list = Array.isArray(apps) ? apps : [];
  return list.find((entry) => entry.id === appId) || null;
}

// appOrigin reads the origin the application entry lives on. A manifest whose
// entry is not an absolute address names no origin.
export function appOrigin(app) {
  if (!app || typeof app.entry !== "string" || app.entry === "") return "";
  try {
    return new URL(app.entry).origin;
  } catch {
    return "";
  }
}

// iconURL resolves the manifest icon against the application origin. An icon
// from another origin is not loaded (KOSD-C-73).
export function iconURL(app) {
  if (!app || typeof app.icon !== "string" || app.icon === "") return "";
  const origin = appOrigin(app);
  if (!origin) return "";
  let resolved = null;
  try {
    resolved = new URL(app.icon, app.entry);
  } catch {
    return "";
  }
  if (resolved.origin !== origin) return "";
  return resolved.toString();
}

// windowCounts counts the live windows of every application. A window in its
// error state has no frame left, so it never marks an application as running
// (DASH-C-30, DASH-C-74).
const LIVE_STATES = new Set(["opening", "normal", "minimized", "maximized", "fullscreen", "snapped"]);

export function windowCounts(windows) {
  const counts = new Map();
  for (const window of Array.isArray(windows) ? windows : []) {
    if (!window || typeof window.application !== "string") continue;
    if (!LIVE_STATES.has(window.state)) continue;
    counts.set(window.application, (counts.get(window.application) || 0) + 1);
  }
  return counts;
}
