// Mermaid is presentation only. Native description fields remain authoritative.
// The locally bundled renderer is loaded once, only when a diagram is present.
import { openDiagramViewer, refreshDiagramViewer, closeDiagramViewer } from "./diagram-viewer.js";
let runtime, serial = 0, draining = false;
const queue = new Map(), connected = new Set();
const maximumText = 4096;
const element = (name, text) => {
  const node = document.createElement(name);
  if (text !== undefined) node.textContent = text;
  return node;
};

function sourceError(source) {
  if (source.length > maximumText || source.split(/\n|;/).length > 80 ||
      (source.match(/[\p{L}\p{N}_]+|[^\s]/gu) || []).length > 700)
    return "Diagram is too large. Use at most 4,096 characters, 80 statements and 700 tokens; split larger diagrams.";
  // Reject configuration before Mermaid's preprocessor sees it, including
  // frontmatter and spaced directives. No author styles, HTML, math, callbacks,
  // links, images, icons, or new shape metadata can enter the layout DOM.
  if (/%%\s*\{|^\s*---|<\s*[!/?a-z]|&(?:#|[a-z]+;)|#(?:\d+|[a-z]+);|@\s*\{|\$\$|`|\b(?:click|link|links|href|callback|call|style|classDef|class|linkStyle|image|icon)\b|(?:https?|javascript|data|file):|url\s*\(/im.test(source))
    return "This diagram uses unsupported configuration, HTML, styling, links or resources. Use plain Mermaid labels and connections.";
  const code = source.replace(/%%[^\n]*/g, "").trim();
  if (!/^(?:flowchart\s+(?:TB|TD|BT|RL|LR)\b|graph\s+(?:TB|TD|BT|RL|LR)\b|sequenceDiagram\b|stateDiagram(?:-v2)?\b)/.test(code))
    return "Unsupported diagram. Use flowchart, graph, sequenceDiagram or stateDiagram-v2.";
  // Conservative limits before parsing/layout, including graph expansion and
  // nested blocks. The renderer also enforces its own edge/text limits.
  if (source.includes("&") || (code.match(/[-=.]{2,}/g) || []).length > 60 ||
      (code.match(/\b(?:subgraph|state|loop|alt|opt|par|critical|break|rect)\b/g) || []).length > 12 ||
      (code.match(/[{}]/g) || []).length > 24)
    return "Diagram is too complex. Use at most 60 connections and 12 groups; split larger diagrams. Grouped & connections are unsupported.";
  return "";
}

function palette() {
  const styles = getComputedStyle(document.documentElement);
  return Object.fromEntries(["ink", "bg", "pane", "muted"].map(key => [key, styles.getPropertyValue("--" + key).trim()]));
}

// Only basic SVG geometry and text can cross from the renderer into the view.
// Rebuild, rather than insert an HTML string. ID references are rewritten even
// for Mermaid's fixed sequence marker IDs (which repeat between renders).
export function diagramSVG(markup, prefix) {
  if (markup.length > 500000) throw Error("Diagram output is too large; simplify the diagram.");
  // Our vendor serializer removes styles. Reject unexpected inline CSS before
  // XML parsing, which itself reports CSP violations even in an inert document.
  if (/<(?:[\w-]+:)?style\b|\s(?:[\w-]+:)?style\s*=/i.test(markup)) throw Error("Unsupported diagram styling.");
  const parsed = new DOMParser().parseFromString(markup, "image/svg+xml");
  if (parsed.querySelector("parsererror") || parsed.documentElement.localName !== "svg") throw Error("The diagram could not be rendered.");
  const tags = new Set(["svg", "g", "defs", "marker", "path", "rect", "circle", "ellipse", "line", "polyline", "polygon", "text", "tspan", "title", "desc", "clipPath"]);
  const attributes = new Set(["id", "class", "viewBox", "width", "height", "x", "y", "x1", "x2", "y1", "y2", "dx", "dy", "cx", "cy", "r", "rx", "ry", "d", "points", "transform", "fill", "fill-opacity", "stroke", "stroke-width", "stroke-dasharray", "stroke-linecap", "stroke-linejoin", "stroke-opacity", "opacity", "text-anchor", "dominant-baseline", "font-size", "font-weight", "marker-start", "marker-mid", "marker-end", "markerWidth", "markerHeight", "markerUnits", "refX", "refY", "orient", "clip-path", "preserveAspectRatio", "role", "aria-roledescription", "aria-labelledby", "aria-describedby"]);
  // Mermaid colors markers with suffix selectors such as [id$="-arrowhead"].
  // Keep safe original IDs after the unique prefix/index so those rules still
  // match, without carrying arbitrary source characters into CSS references.
  const ids = new Map([...parsed.querySelectorAll("[id]")].map((node, i) =>
    [node.id, `${prefix}-${i}${/^[\w-]+$/.test(node.id) ? "-" + node.id : ""}`]));
  function copy(node) {
    if (node.nodeType === Node.TEXT_NODE) return document.createTextNode(node.textContent);
    if (node.nodeType !== Node.ELEMENT_NODE || !tags.has(node.localName)) return null;
    const safe = document.createElementNS("http://www.w3.org/2000/svg", node.localName);
    for (const {name, value} of node.attributes) {
      if (!attributes.has(name)) continue;
      let text = value;
      if (name === "id") text = ids.get(value);
      else if (name === "aria-labelledby" || name === "aria-describedby") text = value.split(/\s+/).map(id => ids.get(id)).filter(Boolean).join(" ");
      else if (["fill", "stroke", "marker-start", "marker-mid", "marker-end", "clip-path"].includes(name)) {
        const reference = /^url\(#([\w-]+)\)$/.exec(value);
        if (reference && ids.has(reference[1])) text = `url(#${ids.get(reference[1])})`;
        else if (!/^(?:none|currentColor|transparent|#[\da-f]{3,8}|[a-z]+|rgba?\([\d.,%\s]+\))$/i.test(value)) continue;
      }
      safe.setAttribute(name, text);
    }
    for (const child of node.childNodes) { const result = copy(child); if (result) safe.append(result); }
    return safe;
  }
  const svg = copy(parsed.documentElement);
  const box = svg.getAttribute("viewBox")?.trim().split(/[\s,]+/).map(Number);
  if (!box || box.length !== 4 || !box.every(Number.isFinite) || box[2] <= 0 || box[3] <= 0 || box[2] > 20000 || box[3] > 20000)
    throw Error("Diagram dimensions are unsupported; simplify the diagram.");
  svg.setAttribute("width", box[2]);
  svg.setAttribute("height", box[3]);
  svg.setAttribute("role", "img");
  if (!svg.hasAttribute("aria-labelledby")) svg.setAttribute("aria-label", "Mermaid diagram. Read the Mermaid source below for a text alternative.");
  return {svg, ids};
}

async function drain() {
  if (draining) return;
  draining = true;
  try {
    while (queue.size) {
      // Yield between bounded diagrams; coalesce changes before expensive work.
      await new Promise(resolve => setTimeout(resolve, 30));
      if (!queue.size) break; // All pending surfaces may have disconnected.
      const [host, version] = queue.entries().next().value;
      queue.delete(host);
      if (!host.isConnected || host.version !== version) continue;
      let stage;
      try {
        runtime ||= import("./mermaid-11.17.2.js");
        const {default: mermaid} = await runtime;
        if (!host.isConnected || host.version !== version) continue;
        const colors = palette();
        const configuration = {
          startOnLoad: false, securityLevel: "strict", suppressErrorRendering: true,
          maxTextSize: maximumText, maxEdges: 60, htmlLabels: false,
          fontFamily: "Arial, sans-serif", theme: "base", layout: "dagre", look: "classic",
          flowchart: {htmlLabels: false, useMaxWidth: false, defaultRenderer: "dagre-wrapper"},
          sequence: {useMaxWidth: false}, state: {useMaxWidth: false},
          themeVariables: {primaryColor: colors.pane, primaryTextColor: colors.ink, primaryBorderColor: colors.muted,
            secondaryColor: colors.pane, tertiaryColor: colors.bg, lineColor: colors.ink, textColor: colors.ink,
            mainBkg: colors.pane, nodeTextColor: colors.ink, actorBkg: colors.pane, actorTextColor: colors.ink,
            actorBorder: colors.muted, actorLineColor: colors.ink, signalColor: colors.ink, signalTextColor: colors.ink,
            labelBoxBkgColor: colors.pane, labelBoxBorderColor: colors.muted, labelTextColor: colors.ink,
            loopTextColor: colors.ink, noteBkgColor: colors.pane, noteTextColor: colors.ink, noteBorderColor: colors.muted,
            edgeLabelBackground: colors.bg, clusterBkg: colors.bg, clusterBorder: colors.muted,
            background: colors.bg, fontSize: "16px"},
        };
        configuration.secure = Object.keys(configuration).concat("secure", "themeCSS", "dompurifyConfig");
        mermaid.initialize(configuration);
        const parsed = await mermaid.mermaidAPI.getDiagramFromText(host.source);
        const graph = parsed.db.getData?.();
        if ((graph?.nodes?.length || parsed.db.getActors?.().size || 0) > 60 ||
            (graph?.edges?.length || parsed.db.getMessages?.().length || 0) > 60)
          throw Error("Diagram is too complex. Use at most 60 nodes and 60 connections/messages.");
        const parents = new Map((graph?.nodes || []).map(node => [node.id, node.parentId]));
        for (const node of parents.keys()) {
          let parent = parents.get(node), depth = 0;
          while (parent) {
            if (++depth > 4) throw Error("Diagram groups are too deeply nested or cyclic. Use at most four levels.");
            parent = parents.get(parent);
          }
        }
        if (!host.isConnected || host.version !== version) continue;
        stage = element("div"); stage.className = "mermaid-stage";
        stage.setAttribute("aria-hidden", "true"); stage.inert = true;
        document.body.append(stage);
        const id = "pellets-mermaid-" + ++serial;
        const result = await mermaid.render(id, host.source, stage);
        if (!host.isConnected || host.version !== version) continue;
        const {svg, ids} = diagramSVG(result.svg, id);
        // Generated stylesheet only: author CSS/configuration is rejected before
        // rendering. Still reject resource-bearing CSS at this final boundary.
        if (/@import|url\s*\(|\\/i.test(result.css.replace(/url\(#[\w-]+\)/g, ""))) throw Error("Unsupported diagram styling.");
        let css = result.css;
        for (const [oldID, newID] of ids) if (oldID === id) css = css.replaceAll("#" + oldID, "#" + newID);
        host.finish(svg, css);
      } catch (error) {
        if (host.isConnected && host.version === version) {
          // Parser diagnostics are text, bounded and local to this fence.
          const message = String(error?.message || "Unable to load the local diagram renderer.").slice(0, 500);
          host.fail("Could not render Mermaid: " + message);
        }
      } finally { stage?.remove(); }
    }
  } finally { draining = false; }
}

class PelletDiagram extends HTMLElement {
  version = 0;
  connectedCallback() { connected.add(this); this.schedule(); }
  disconnectedCallback() { closeDiagramViewer(this); connected.delete(this); queue.delete(this); this.version++; this.clearSheet(); }
  clearSheet() {
    if (this.sheet) document.adoptedStyleSheets = document.adoptedStyleSheets.filter(sheet => sheet !== this.sheet);
    this.sheet = null;
  }
  schedule() {
    refreshDiagramViewer(this);
    this.version++;
    const error = this.limited ? "Only the first eight Mermaid diagrams in a description are rendered. Split this description to view more." : sourceError(this.source);
    if (error) { this.fail(error); return; }
    if (queue.size >= 32 && !queue.has(this)) { this.fail("Too many diagrams are waiting. Retry after the other diagrams finish.", true); return; }
    this.dataset.state = "pending";
    this.retry.hidden = true;
    this.status.textContent = "Rendering diagram…";
    this.status.hidden = false;
    queue.set(this, this.version);
    drain();
  }
  fail(message, retry = false) {
    closeDiagramViewer(this);
    this.clearSheet(); this.canvas.replaceChildren(); this.activate.hidden = true;
    this.canvas.hidden = true; this.retry.hidden = !retry;
    this.dataset.state = "error"; this.status.hidden = false;
    this.status.textContent = message; this.details.open = true;
  }
  finish(svg, css) {
    this.clearSheet();
    this.sheet = new CSSStyleSheet(); this.sheet.replaceSync(css);
    document.adoptedStyleSheets = [...document.adoptedStyleSheets, this.sheet];
    this.canvas.replaceChildren(svg); this.canvas.hidden = false; this.activate.hidden = false;
    // A button's descendants are presentational to assistive technology. Keep
    // Mermaid's title/description available on the activation controls too.
    const title = svg.querySelector("title")?.textContent.trim();
    this.canvas.setAttribute("aria-label", "Open diagram viewer" + (title ? ": " + title : ""));
    this.activate.setAttribute("aria-label", "View larger" + (title ? ": " + title : ""));
    for (const control of [this.canvas, this.activate]) {
      const description = svg.getAttribute("aria-describedby");
      if (description) control.setAttribute("aria-describedby", description);
      else control.removeAttribute("aria-describedby");
    }
    this.status.hidden = true; this.dataset.state = "ready";
    this.css = css;
    refreshDiagramViewer(this, svg);
  }
}
customElements.define("pl-diagram", PelletDiagram);

export function createDiagram(source, limited = false) {
  const host = element("pl-diagram");
  host.source = source; host.limited = limited; host.className = "mermaid-diagram";
  host.status = element("p", "Rendering diagram…"); host.status.className = "mermaid-status";
  host.status.setAttribute("role", "status");
  host.canvas = element("div"); host.canvas.className = "mermaid-canvas";
  host.canvas.hidden = true;
  host.canvas.tabIndex = 0; host.canvas.setAttribute("role", "button");
  host.canvas.setAttribute("aria-label", "Open diagram viewer");
  host.canvas.setAttribute("aria-haspopup", "dialog");
  host.canvas.title = "Open diagram viewer to zoom and pan";
  host.activate = element("button", "View larger"); host.activate.type = "button";
  host.activate.className = "mermaid-activate"; host.activate.hidden = true;
  host.activate.setAttribute("aria-haspopup", "dialog");
  host.retry = element("button", "Retry diagram"); host.retry.type = "button";
  host.retry.className = "mermaid-retry"; host.retry.hidden = true;
  host.retry.addEventListener("click", () => host.schedule());
  const activate = trigger => {
    if (!host.dispatchEvent(new CustomEvent("pellets-diagram-activate", {
      bubbles: true, cancelable: true, detail: {source: host.source, svg: host.canvas.querySelector("svg"), trigger},
    }))) return;
    openDiagramViewer(host, trigger);
  };
  host.activate.addEventListener("click", () => activate(host.activate));
  host.canvas.addEventListener("click", () => activate(host.canvas));
  host.canvas.addEventListener("keydown", event => {
    if (event.key !== "Enter" && event.key !== " ") return;
    event.preventDefault();
    if (!event.repeat) activate(host.canvas);
  });
  host.details = element("details"); host.details.className = "mermaid-source";
  const pre = element("pre"); pre.tabIndex = 0; pre.dataset.language = "mermaid";
  pre.setAttribute("aria-label", "Mermaid source"); pre.append(element("code", source));
  host.details.append(element("summary", "Mermaid source"), pre);
  host.append(host.status, host.canvas, host.activate, host.retry, host.details);
  return host;
}

// One observer for the theme, never one listener per refresh/diagram. A queued
// version replaces older results, including a theme change during await render.
let theme = document.documentElement.dataset.theme;
new MutationObserver(() => {
  if (theme === document.documentElement.dataset.theme) return;
  theme = document.documentElement.dataset.theme;
  for (const host of connected) host.schedule();
})
  .observe(document.documentElement, {attributes: true, attributeFilter: ["data-theme"]});
