// Document-local navigation. The native source field and form still own edits.
// One reader owns its observers; streamed removal/reconnection cleans them up.
const wideScreen = "(min-width: 960px)";
const headingSelector = "[data-markdown-heading]";
const slug = text => text.normalize("NFKC").toLowerCase()
  .replace(/[^\p{L}\p{N}\p{M}]+/gu, "-").replace(/^-|-$/g, "") || "section";

class DescriptionReader extends HTMLElement {
  connectedCallback() {
    this.abort?.abort();
    this.abort = new AbortController();
    const options = {signal: this.abort.signal};
    this.addEventListener("click", event => {
      const link = event.target.closest("[data-description-anchor]");
      if (!link) return;
      event.preventDefault(); // Never change the workbench URL/hash or its scroll.
      this.navigate(link.dataset.descriptionAnchor);
    }, options);
    this.addEventListener("scroll", () => this.schedule(), {...options, capture: true, passive: true});
    this.media = matchMedia(wideScreen);
    this.media.addEventListener("change", () => this.schedule(), options);
    this.resize = new ResizeObserver(() => this.schedule());
    if (this.saved) this.refresh(this.saved);
  }
  disconnectedCallback() {
    this.abort.abort();
    this.resize.disconnect();
    cancelAnimationFrame(this.frame);
    this.frame = null;
    if (this.toggle) this.toggle.onclick = null;
  }
  refresh(saved, changed = false) {
    this.saved = saved;
    this.view = this.querySelector(".markdown-body");
    if (!this.isConnected || !this.view) return;
    const host = this.closest("[data-description]");
    this.toggle = host.querySelector("[data-description-contents]");
    if (!this.toggle) {
      this.toggle = document.createElement("button");
      this.toggle.type = "button";
      this.toggle.dataset.descriptionContents = "";
      this.toggle.textContent = "Contents";
      host.querySelector(".description-toolbar").append(this.toggle);
    }
    this.toggle.onclick = () => {
      saved[this.media.matches ? "contentsWide" : "contentsCompact"] = this.nav.hidden;
      this.layout();
    };
    if (changed || !this.nav) this.build(host.dataset.descriptionKey);
    this.toggle.setAttribute("aria-controls", this.nav.id);
    this.resize.disconnect();
    // Observing the fixed-height viewport alone misses growing diagrams. Each
    // top-level block also covers layout changes within lists/quotes/diagrams.
    for (const node of [this.view, ...this.view.children]) this.resize.observe(node);
    if (changed || !this.restored) this.restoreSection = saved.section;
    this.restored = true;
    this.layout();
    this.schedule(); // The dialog may still be closed during initialization.
  }
  build(key) {
    this.nav?.remove();
    this.nav = document.createElement("nav");
    this.nav.className = "description-contents";
    this.nav.id = "description-contents-" + key;
    this.nav.setAttribute("aria-label", "Description contents");
    const title = document.createElement("strong");
    title.textContent = "On this page";
    const list = document.createElement("ol");
    this.nav.append(title, list);
    this.prepend(this.nav);
    this.headings = Array.from(this.view.querySelectorAll(headingSelector));
    this.links = [];
    const used = new Set(), stack = [];
    for (const heading of this.headings) {
      const text = heading.textContent.trim().replace(/\s+/g, " ") || "Untitled section";
      const base = "description-" + encodeURIComponent(key) + "-" + slug(text);
      let id = base, suffix = 2;
      while (used.has(id)) id = base + "-" + suffix++;
      used.add(id);
      heading.id = id;
      heading.tabIndex = -1;
      const level = Number(heading.dataset.markdownHeading);
      const item = document.createElement("li"), link = document.createElement("a");
      item.dataset.headingLevel = String(level);
      link.textContent = text;
      link.href = "#" + encodeURIComponent(id);
      link.dataset.descriptionAnchor = id;
      link.id = "contents-link-" + id;
      item.append(link);
      while (stack.length && stack.at(-1).level >= level) stack.pop();
      const parent = stack.at(-1);
      if (parent && !parent.list) {
        parent.list = document.createElement("ol");
        parent.item.append(parent.list);
      }
      (parent?.list || list).append(item);
      stack.push({level, item});
      this.links.push(link);
    }
    this.destination = null;
    this.activeID = null;
  }
  schedule() {
    if (this.frame != null) return;
    this.frame = requestAnimationFrame(() => { this.frame = null; this.layout(); });
  }
  layout() {
    if (!this.nav) return;
    const visible = !this.view.hidden && this.view.clientHeight > 0;
    this.toggle.hidden = !visible || !this.headings.length;
    const overflow = this.view.scrollHeight > this.view.clientHeight + 1;
    const expanded = visible && this.headings.length > 0 && (this.media.matches
      ? this.saved.contentsWide ?? (this.headings.length >= 2 && overflow)
      : this.saved.contentsCompact === true);
    this.nav.hidden = !expanded;
    this.dataset.contentsOpen = String(expanded);
    this.toggle.setAttribute("aria-expanded", String(expanded));
    if (!visible) return;
    if (this.restoreSection) {
      const heading = this.headings.find(node => node.id === this.restoreSection.id);
      if (heading) this.view.scrollTop += heading.getBoundingClientRect().top
        - this.view.getBoundingClientRect().top - this.restoreSection.offset;
      this.restoreSection = null;
    }
    this.track();
  }
  current() {
    const top = this.view.getBoundingClientRect().top + this.view.clientTop + 12;
    let current = this.headings[0];
    for (const heading of this.headings) {
      if (heading.getBoundingClientRect().top > top + 1) break;
      current = heading;
    }
    return current;
  }
  capture(saved) {
    if (!this.view || this.view.hidden || !this.view.clientHeight) return;
    const current = this.current();
    if (current) saved.section = {id: current.id,
      offset: current.getBoundingClientRect().top - this.view.getBoundingClientRect().top};
    else delete saved.section;
  }
  track() {
    let current = this.current();
    if (this.destination) {
      const target = this.headings.find(node => node.id === this.destination.id);
      // A diagram can move the target while WebKit keeps scrollTop unchanged.
      // Do not retain a clicked section whose destination no longer matches.
      if (!target || Math.abs(this.position(target) - this.destination.top) > 1) this.destination = null;
      else if (Math.abs(this.view.scrollTop - this.destination.top) <= 1) {
        current = target;
        this.destination.arrived = true;
      } else if (this.destination.arrived) this.destination = null;
    }
    for (const link of this.links) {
      if (link.dataset.descriptionAnchor === current?.id) link.setAttribute("aria-current", "location");
      else link.removeAttribute("aria-current");
    }
    if (current?.id !== this.activeID && !this.nav.hidden && !this.nav.contains(document.activeElement)) {
      const link = this.links.find(node => node.dataset.descriptionAnchor === current?.id);
      if (link) {
        const box = link.getBoundingClientRect(), rail = this.nav.getBoundingClientRect();
        if (box.top < rail.top) this.nav.scrollTop += box.top - rail.top - 6;
        else if (box.bottom > rail.bottom) this.nav.scrollTop += box.bottom - rail.bottom + 6;
      }
    }
    this.activeID = current?.id;
  }
  position(heading) {
    return Math.max(0, Math.min(this.view.scrollHeight - this.view.clientHeight,
      this.view.scrollTop + heading.getBoundingClientRect().top
        - this.view.getBoundingClientRect().top - this.view.clientTop - 12));
  }
  navigate(id) {
    const heading = this.headings.find(node => node.id === id);
    if (!heading) return;
    if (!this.media.matches) {
      this.saved.contentsCompact = false;
      this.layout();
    }
    // Chrome (the dialog header and display toolbar) is outside this scroller.
    // A 12px inset leaves breathing room without scrolling any ancestor.
    const top = this.position(heading);
    this.destination = {id, top};
    heading.focus({preventScroll: true});
    this.view.scrollTo({top, behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "instant" : "smooth"});
    this.schedule();
  }
}
customElements.define("pl-description-reader", DescriptionReader);
