// The event stream the BFF repeats (KOSD-C-13, KOSD-C-64). A stream that
// cannot read keeps the last observation and marks it stale instead of
// claiming there are no events.

// subscribeStream listens for the two events the BFF emits and returns the
// function that closes the subscription.
export function subscribeStream(url, { EventSource, onEvents, onStale }) {
  if (typeof EventSource !== "function") return () => {};
  const source = new EventSource(url);
  source.addEventListener("events", (event) => {
    const read = parse(event.data);
    if (read) onEvents(read);
  });
  source.addEventListener("stale", (event) => {
    onStale(parse(event.data) || { reason: "事件流已断开" });
  });
  source.addEventListener("error", () => {
    // A reconnect gap is an unknown interval, so the last observation is
    // stale until the stream delivers again.
    onStale({ reason: "事件流已断开" });
  });
  return () => source.close();
}

// parse decodes one server-sent payload; a malformed frame is ignored.
function parse(data) {
  try {
    return JSON.parse(data);
  } catch {
    return null;
  }
}
