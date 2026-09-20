// Shared offline activity/description highlighting. All source becomes text nodes.
function element(tag, className, text) {
  const el = document.createElement(tag);
  if (className) el.className = className;
  if (text !== undefined) el.textContent = text;
  return el;
}
export function highlightCode(text, diff = false) {
  const pre = element("pre");
  pre.tabIndex = 0;
  const keywords =
    /\b(?:func|package|import|return|if|else|for|range|var|const|let|type|struct|interface|select|case|switch|break|continue|go|defer|async|await|function|class|new|throw|try|catch|export|from|true|false|nil|null|undefined)\b/;
  for (const line of String(text).split("\n")) {
    const row = element(
      "span",
      "source-line" +
        (diff && line.startsWith("+") && !line.startsWith("+++")
          ? " diff-added"
          : diff && line.startsWith("-") && !line.startsWith("---")
            ? " diff-removed"
            : ""),
    );
    const pattern =
      /(\/\/.*$|#.*$|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|`[^`]*`|\b\d+(?:\.\d+)?\b|\b[A-Za-z_$][\w$]*\b)/g;
    let last = 0;
    for (const match of line.matchAll(pattern)) {
      row.append(document.createTextNode(line.slice(last, match.index)));
      const value = match[0],
        kind = /^(\/\/|#)/.test(value)
          ? "comment"
          : /^["'`]/.test(value)
            ? "string"
            : /^\d/.test(value)
              ? "number"
              : keywords.test(value)
                ? "keyword"
                : "";
      row.append(
        kind
          ? element("span", "code-" + kind, value)
          : document.createTextNode(value),
      );
      last = match.index + value.length;
    }
    row.append(document.createTextNode(line.slice(last)));
    pre.append(row);
  }
  return pre;
}
