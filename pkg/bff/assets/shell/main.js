// The browser entry: binds the shell to the document it is served in, and gives
// the application host the one layer the shell's re-renders never replace, so a
// mounted frame keeps its document (KOSD-C-75).
import { createApi } from "./api.js";
import { createApp } from "./app.js";
import { createAppHost } from "./apphost.js";

const root = document.querySelector("#app");
const layer = document.querySelector("#windows");
const api = createApi({ fetch: (path, init) => window.fetch(path, init), document });
const host = createAppHost({
  api,
  document,
  container: layer,
  window: window,
  consoleOrigin: window.location.origin,
});
const app = createApp({
  api,
  root,
  storage: window.localStorage,
  EventSource: window.EventSource,
  host,
  location: window.location,
});

root.addEventListener("click", (event) => app.handleClick(event));
layer.addEventListener("click", (event) => app.handleClick(event));
root.addEventListener("contextmenu", (event) => {
  if (app.handleContextMenu(event)) event.stopPropagation();
});
root.addEventListener("input", (event) => {
  const search = event.target && event.target.closest ? event.target.closest("[data-launcher-search]") : null;
  if (search) app.searchLauncher(search.value);
});
root.addEventListener("submit", (event) => {
  const form = event.target.closest('[data-form="login"]');
  if (!form) return;
  event.preventDefault();
  app.login(form.elements.username.value, form.elements.password.value);
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape") app.closeOverlays();
});
window.addEventListener("hashchange", () => {
  const pageId = window.location.hash.replace(/^#\//, "");
  if (pageId && pageId !== app.state.pageId) app.navigate(pageId);
});
app.start(window.location.hash.replace(/^#\//, ""));
