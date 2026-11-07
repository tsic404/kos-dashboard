// Shared test harness for the browser shell: a fetch that answers by method and
// path, browser-storage stand-ins, and the minimal DOM the application host
// mounts into. The shell injects every one of them, so these fakes exercise the
// same code the browser runs.

import assert from "node:assert/strict";

// reply builds the one response shape the client reads.
export function reply(status, body) {
  return {
    ok: status >= 200 && status < 300,
    status,
    text: async () => (body === undefined ? "" : JSON.stringify(body)),
  };
}

// fakeFetch answers by "METHOD PATH" and records what the shell sent.
export function fakeFetch(routes) {
  const calls = [];
  const fetch = async (path, init = {}) => {
    const method = init.method || "GET";
    calls.push({ method, path, headers: init.headers || {}, body: init.body });
    const handler = routes[`${method} ${path}`];
    if (!handler) return reply(404, { error: { code: "not_found", message: "missing", stage: "read" } });
    return handler();
  };
  return { fetch, calls };
}

export function memoryStorage(initial = {}) {
  const values = new Map(Object.entries(initial));
  return {
    getItem: (key) => (values.has(key) ? values.get(key) : null),
    setItem: (key, value) => values.set(key, String(value)),
  };
}

// fakeEventSource records the subscription so a case can push one frame.
export function fakeEventSource() {
  const sources = [];
  class FakeEventSource {
    constructor(url) {
      this.url = url;
      this.handlers = new Map();
      this.isClosed = false;
      sources.push(this);
    }

    addEventListener(name, handler) {
      this.handlers.set(name, handler);
    }

    close() {
      this.isClosed = true;
    }

    emit(name, payload) {
      const handler = this.handlers.get(name);
      if (handler) handler({ data: JSON.stringify(payload) });
    }
  }
  return { EventSource: FakeEventSource, sources };
}

// elementWith answers closest() for the attributes one click target carries.
export function elementWith(attributes) {
  return {
    closest(selector) {
      const attribute = selector.slice(1, -1);
      if (!(attribute in attributes)) return null;
      return { getAttribute: (name) => (name in attributes ? attributes[name] : null) };
    },
  };
}

// waitForHtml waits for one rendered document to satisfy a predicate.
export async function waitForHtml(root, predicate, label) {
  const deadline = Date.now() + 2000;
  while (Date.now() < deadline) {
    if (predicate(root.innerHTML)) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  assert.fail(`timed out waiting for ${label}`);
}

// fakeElement is the element API the window layer uses: attributes, a style
// object, children and an optional contentWindow.
export function fakeElement(tag) {
  const element = {
    tagName: String(tag).toUpperCase(),
    className: "",
    attributes: new Map(),
    children: [],
    messages: [],
    style: {
      values: new Map(),
      setProperty(name, value) {
        this.values.set(name, String(value));
      },
    },
    innerHTML: "",
    contentWindow: null,
    setAttribute(name, value) {
      this.attributes.set(name, String(value));
    },
    getAttribute(name) {
      return this.attributes.has(name) ? this.attributes.get(name) : null;
    },
    appendChild(child) {
      this.children.push(child);
      child.parentNode = this;
      return child;
    },
    removeChild(child) {
      const index = this.children.indexOf(child);
      if (index >= 0) this.children.splice(index, 1);
      child.parentNode = null;
      return child;
    },
    querySelector(selector) {
      if (selector === "iframe") return this.children.find((child) => child.tagName === "IFRAME") || null;
      return null;
    },
  };
  return element;
}

// fakeDocument creates the elements the host asks for. An iframe carries a
// contentWindow that records the messages posted into it.
export function fakeDocument() {
  return {
    createElement(tag) {
      const element = fakeElement(tag);
      if (element.tagName === "IFRAME") {
        element.contentWindow = {
          postMessage(payload, targetOrigin) {
            element.messages.push({ payload, targetOrigin });
          },
        };
      }
      return element;
    },
  };
}

// fakeWindow carries the message listener the host registers.
export function fakeWindow() {
  const listeners = new Map();
  return {
    addEventListener(name, handler) {
      if (!listeners.has(name)) listeners.set(name, []);
      listeners.get(name).push(handler);
    },
    message(event) {
      for (const handler of listeners.get("message") || []) handler(event);
    },
  };
}
