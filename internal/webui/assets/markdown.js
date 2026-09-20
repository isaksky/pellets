import { lexer } from "./marked-18.0.13.js";
import { highlightCode } from "./code-highlight.js";
import { createDiagram } from "./diagrams.js";

const highlightedLanguages = new Set([
  "go", "golang", "js", "javascript", "jsx", "ts", "typescript", "tsx",
  "python", "py", "json", "c", "cpp", "java", "rust", "sh", "bash", "diff",
]);
const node = (tag, text) => {
  const el = document.createElement(tag);
  if (text !== undefined) el.textContent = text;
  return el;
};
// Decode only individual character references, never markup. URL validation
// runs AFTER decoding, including numeric references for colons/control bytes.
function entities(text) {
  return String(text).replace(/&(?:#\d+|#x[\da-f]+|[a-z][\da-z]+);?/gi, value => {
    const decoder = document.createElement("textarea");
    decoder.innerHTML = value;
    return decoder.value;
  });
}
function safeLink(value) {
  const href = entities(value).trim();
  if (/[\u0000-\u0020\u007f-\u009f]/.test(href)) return null;
  try {
    const url = new URL(href, location.href);
    return ["http:", "https:", "mailto:"].includes(url.protocol) ? href : null;
  } catch { return null; }
}

function appendTokens(parent, tokens, loose = false, budget = {diagrams: 0}) {
  for (const token of tokens || []) {
    let el;
    switch (token.type) {
      case "space": case "def": continue;
      case "heading":
        el = node("h" + Math.min(6, Math.max(1, token.depth)));
        // Only actual Markdown heading nodes participate in document outlines.
        el.dataset.markdownHeading = String(token.depth);
        appendTokens(el, token.tokens, false, budget);
        break;
      case "paragraph": case "strong": case "em": case "del": case "blockquote":
        el = node(token.type === "paragraph" ? "p" : token.type);
        appendTokens(el, token.tokens, false, budget);
        break;
      case "hr": case "br": el = node(token.type); break;
      case "codespan": el = node("code", token.text); break;
      case "code": {
        el = node("pre");
        el.tabIndex = 0;
        const language = (token.lang || "").trim().split(/\s+/)[0].toLowerCase();
        if (language === "mermaid") {
          el = createDiagram(token.text, ++budget.diagrams > 8);
          break;
        }
        el.dataset.language = language;
        el.setAttribute("aria-label", language ? language + " code" : "Code");
        const code = node("code");
        if (highlightedLanguages.has(language)) {
          const highlighted = highlightCode(token.text, language === "diff");
          Array.from(highlighted.children).forEach((line, index) => {
            if (index) code.append("\n");
            code.append(line);
          });
        } else code.textContent = token.text;
        el.append(code);
        break;
      }
      case "list":
        el = node(token.ordered ? "ol" : "ul");
        if (token.ordered) el.start = token.start;
        for (const item of token.items) {
          const li = node("li");
          if (item.task) li.className = "markdown-task";
          appendTokens(li, item.tokens, token.loose, budget);
          el.append(li);
        }
        break;
      case "checkbox":
        el = node("input");
        el.type = "checkbox";
        el.disabled = true;
        el.checked = token.checked;
        el.setAttribute("aria-label", token.checked ? "Completed task" : "Incomplete task");
        break;
      case "link": {
        const href = safeLink(token.href);
        el = node(href === null ? "span" : "a");
        if (href !== null) {
          el.setAttribute("href", href);
          el.target = "_blank";
          el.rel = "noopener noreferrer";
        }
        if (token.title) el.title = entities(token.title);
        appendTokens(el, token.tokens, false, budget);
        break;
      }
      case "table": {
        el = node("div");
        el.className = "markdown-table";
        el.tabIndex = 0;
        el.setAttribute("role", "region");
        el.setAttribute("aria-label", "Table (scroll horizontally)");
        const table = node("table"), head = node("thead"), body = node("tbody");
        for (const [index, cells] of [token.header, ...token.rows].entries()) {
          const row = node("tr");
          for (const cell of cells) {
            const td = node(index === 0 ? "th" : "td");
            if (index === 0) td.scope = "col";
            if (["left", "center", "right"].includes(cell.align)) td.className = "align-" + cell.align;
            appendTokens(td, cell.tokens, false, budget);
            row.append(td);
          }
          (index === 0 ? head : body).append(row);
        }
        table.append(head, body);
        el.append(table);
        break;
      }
      case "text":
        if (token.tokens) {
          if (loose) {
            el = node("p");
            appendTokens(el, token.tokens, false, budget);
          } else { appendTokens(parent, token.tokens, false, budget); continue; }
        } else el = document.createTextNode(entities(token.text));
        break;
      // Images remain readable alt text without offline/network side effects.
      // Raw HTML and all unknown tokens are literal text, never parsed as HTML.
      case "image": el = document.createTextNode(entities(token.text)); break;
      case "escape": case "html":
      default: el = document.createTextNode(token.text || token.raw || "");
    }
    parent.append(el);
  }
}

// Independent of forms/state. Original source stays with the caller. Unknown
// languages retain escaped source and language metadata. Mermaid presentation
// owns its asynchronous lifecycle; it never rewrites the caller's source.
export function renderMarkdown(source) {
  const fragment = document.createDocumentFragment();
  try { appendTokens(fragment, lexer(String(source), { gfm: true })); }
  catch { fragment.replaceChildren(node("p", String(source))); }
  return fragment;
}
