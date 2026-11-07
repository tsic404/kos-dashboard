// One escape function for every value the shell writes into the document. All
// answered values pass through it, so a manifest or a page can never turn its
// text into document structure.

// escapeHtml keeps a value out of the document's markup.
export function escapeHtml(value) {
  return String(value ?? "").replace(/[&<>"']/g, (character) => {
    switch (character) {
      case "&":
        return "&amp;";
      case "<":
        return "&lt;";
      case ">":
        return "&gt;";
      case '"':
        return "&quot;";
      default:
        return "&#39;";
    }
  });
}
