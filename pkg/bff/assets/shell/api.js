// The control-plane API the BFF proxies (KOSD-C-12). Every refusal keeps the
// envelope the control plane wrote, so the shell shows the same code, message
// and stage a direct call would answer.

const CSRF_COOKIE = "kos_bff_csrf";
const CSRF_HEADER = "X-CSRF-Token";
const SAFE_METHODS = ["GET", "HEAD"];

// Failure is one refused call, carrying the control-plane envelope.
export class Failure extends Error {
  constructor(status, envelope) {
    const error = envelope && envelope.error ? envelope.error : {};
    super(error.message || `请求失败（${status}）`);
    this.name = "Failure";
    this.status = status;
    this.code = error.code || "internal";
    this.stage = error.stage || "";
    this.retryable = Boolean(error.retryable);
    this.requiresUserAction = Boolean(error.requiresUserAction);
  }
}

// cookieValue reads one cookie; the CSRF token is the only cookie a page
// script may see, the session itself stays HttpOnly (KOSD-F-9).
export function cookieValue(cookie, name) {
  for (const part of String(cookie || "").split(";")) {
    const separator = part.indexOf("=");
    if (separator < 0) continue;
    if (part.slice(0, separator).trim() === name) {
      return decodeURIComponent(part.slice(separator + 1).trim());
    }
  }
  return "";
}

// createApi binds the client to one fetch and one document. Both are injected
// so the shell can be exercised without a browser.
export function createApi({ fetch: doFetch, document }) {
  async function request(method, path, body) {
    const headers = { Accept: "application/json" };
    const init = { method, headers, credentials: "same-origin" };
    if (body !== undefined) {
      headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body);
    }
    if (!SAFE_METHODS.includes(method)) {
      // A state-changing request repeats the double-submit token; the BFF
      // refuses the write without it.
      const token = cookieValue(document.cookie, CSRF_COOKIE);
      if (token) headers[CSRF_HEADER] = token;
    }
    const response = await doFetch(path, init);
    const text = await response.text();
    let payload = null;
    if (text) {
      try {
        payload = JSON.parse(text);
      } catch {
        payload = null;
      }
    }
    if (!response.ok) throw new Failure(response.status, payload);
    return payload;
  }

  return {
    get: (path) => request("GET", path),
    write: (method, path, body) => request(method, path, body),
    session: () => request("GET", "/api/v1/auth/session"),
    login: (username, password) => request("POST", "/api/v1/auth/login", { username, password }),
    logout: () => request("POST", "/api/v1/auth/logout"),
  };
}
