// The page registry the control plane answers (DASH-C-22). The list is already
// trimmed to the session role and omits the pages of a disabled component, so
// the shell renders exactly what it receives instead of deciding visibility
// itself.

// DESKTOP_PAGE_ID is the page this product declares (DASH-C-52).
export const DESKTOP_PAGE_ID = "desktop";

// sourceOf names the read that renders one page. The desktop is this product's
// own page and comes from its BFF projection; every other page is read from
// the control-plane page projection.
export function sourceOf(pageId) {
  if (pageId === DESKTOP_PAGE_ID) return "/api/v1/desktop";
  return `/api/v1/pages/${encodeURIComponent(pageId)}`;
}

// pagesOf returns the page summaries a registry answer carries.
export function pagesOf(registry) {
  const items = registry && Array.isArray(registry.items) ? registry.items : [];
  return items.filter((page) => page && typeof page.id === "string" && page.id !== "");
}

// findPage resolves one listed page; null covers a page the session may not
// see and a page whose component is disabled.
export function findPage(registry, pageId) {
  return pagesOf(registry).find((page) => page.id === pageId) || null;
}

// firstPage picks the route the shell opens: the desktop when it is declared,
// otherwise the first page the registry lists.
export function firstPage(registry) {
  const pages = pagesOf(registry);
  return pages.find((page) => page.id === DESKTOP_PAGE_ID) || pages[0] || null;
}
