// The browser entry: binds the shell to the document it is served in.
import { createApi } from "./api.js";
import { createApp } from "./app.js";

const root = document.querySelector("#app");
const api = createApi({ fetch: (path, init) => window.fetch(path, init), document });
const app = createApp({
  api,
  root,
  storage: window.localStorage,
  EventSource: window.EventSource,
});

root.addEventListener("click", (event) => app.handleClick(event));
root.addEventListener("submit", (event) => {
  const form = event.target.closest('[data-form="login"]');
  if (!form) return;
  event.preventDefault();
  app.login(form.elements.username.value, form.elements.password.value);
});
window.addEventListener("hashchange", () => {
  const pageId = window.location.hash.replace(/^#\//, "");
  if (pageId && pageId !== app.state.pageId) app.navigate(pageId);
});
app.start(window.location.hash.replace(/^#\//, ""));
