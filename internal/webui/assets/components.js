import { connectSelect, disconnectSelect, refreshSelect, closeSelect } from "./dropdowns.js";

// Light DOM keeps native form ownership, labels, validation, and Datastar's
// keyed controls intact. Components never copy a control's value into JS state.
class Component extends HTMLElement {
  connectedCallback() {
    this.events?.abort();
    this.disconnect?.();
    this.events = new AbortController();
    this.connect();
  }
  disconnectedCallback() {
    this.events?.abort();
    this.disconnect?.();
  }
  listen(target, type, listener, options = {}) {
    target.addEventListener(type, listener, {...options, signal: this.events.signal});
  }
  connect() {}
  emit(type, detail = {}, cancelable = false) {
    return this.dispatchEvent(new CustomEvent(type, {bubbles: true, composed: true, cancelable, detail}));
  }
}
class Control extends Component {
  get control() { return this.querySelector("button, input, textarea, select, a"); }
  focus(options) { this.control?.focus(options); }
  get disabled() { return !!this.control?.matches(":disabled"); }
  set disabled(value) { if (this.control) this.control.disabled = Boolean(value); }
  get value() { return this.control?.value ?? ""; }
  set value(value) { if (this.control) this.control.value = value; }
  get form() { return this.control?.form ?? null; }
  checkValidity() { return this.control?.checkValidity?.() ?? true; }
  reportValidity() { return this.control?.reportValidity?.() ?? true; }
  setCustomValidity(message) { this.control?.setCustomValidity?.(message); }
}
class Button extends Control {
  static observedAttributes = ["variant", "disabled", "busy"];
  connect() {
    this.refresh();
    this.listen(this, "click", event => {
      if (this.control?.localName === "a" && this.disabled) {
        event.preventDefault(); event.stopImmediatePropagation();
      }
    }, {capture: true});
  }
  get disabled() { return this.hasAttribute("disabled") || this.hasAttribute("busy") || super.disabled; }
  set disabled(value) { this.toggleAttribute("disabled", Boolean(value)); }
  refresh() {
    const button = this.control;
    if (!button) return;
    if (this.hasAttribute("variant") || this.appliedVariant) {
      this.appliedVariant = this.hasAttribute("variant");
      for (const [variant, className] of Object.entries({primary: "primary-button", quiet: "quiet", icon: "icon-button"})) button.classList.toggle(className, this.getAttribute("variant") === variant);
    }
    // Native disabled state can also be managed by an application request.
    if (this.hasAttribute("disabled")) button.disabled = true;
    if (this.hasAttribute("busy")) {
      if (!this.busy) this.wasDisabled = button.disabled;
      this.busy = true;
      button.disabled = true;
      button.setAttribute("aria-busy", "true");
    } else if (this.busy) {
      this.busy = false;
      button.disabled = this.hasAttribute("disabled") || this.wasDisabled;
      button.removeAttribute("aria-busy");
    }
    if (button.localName === "a") {
      if (this.disabled) {
        if (!this.linkDisabled) this.linkTabIndex = button.getAttribute("tabindex");
        this.linkDisabled = true;
        button.setAttribute("aria-disabled", "true");
        button.tabIndex = -1;
      } else if (this.linkDisabled) {
        this.linkDisabled = false;
        button.removeAttribute("aria-disabled");
        if (this.linkTabIndex === null) button.removeAttribute("tabindex");
        else button.setAttribute("tabindex", this.linkTabIndex);
      }
    }
  }
  attributeChangedCallback(name, previous, value) {
    if (name === "disabled" && value === null && this.control) {
      this.control.disabled = this.hasAttribute("busy");
      if (this.busy) this.wasDisabled = false;
    }
    if (this.isConnected) this.refresh();
  }
}
class Field extends Control {
  get readOnly() { return !!this.control?.readOnly; }
  set readOnly(value) { if (this.control) this.control.readOnly = Boolean(value); }
}
class Checkbox extends Field {
  get checked() { return !!this.control?.checked; }
  set checked(value) { if (this.control) this.control.checked = Boolean(value); }
  get indeterminate() { return !!this.control?.indeterminate; }
  set indeterminate(value) { if (this.control) this.control.indeterminate = Boolean(value); }
}
class Select extends Control {
  get control() { return this.querySelector("select"); }
  connect() {
    if (this.hasAttribute("native")) return;
    connectSelect(this);
    this.observer = new MutationObserver(() => this.refresh());
    this.observer.observe(this, {subtree: true, childList: true, characterData: true, attributes: true,
      attributeFilter: ["id", "value", "hidden", "disabled", "selected", "label", "aria-label", "aria-labelledby", "aria-describedby", "aria-invalid", "required", "title", "data-description", "data-repository", "data-repo", "data-path", "data-placeholder", "data-value-prefix"]});
    this.listen(this, "change", () => this.refresh());
    this.listen(this, "invalid", event => {
      event.preventDefault();
      const firstInvalid = Array.from(this.form?.elements || []).find(control => control.willValidate && !control.validity.valid);
      if (!firstInvalid || firstInvalid === this.control) this.focus();
      this.validationControl = this.control;
      this.control?.setAttribute("aria-invalid", "true");
      this.refresh();
    }, {capture: true});
    this.listen(document, "reset", event => {
      if (event.target === this.control?.form) requestAnimationFrame(() => { if (this.isConnected) this.refresh(); });
    });
  }
  disconnect() { this.observer?.disconnect(); disconnectSelect(this); }
  refresh() {
    if (this.validationControl === this.control && this.control?.validity.valid) {
      this.control.removeAttribute("aria-invalid");
      this.validationControl = null;
    }
    if (!this.hasAttribute("native")) refreshSelect(this);
  }
  focus(options) { (this.querySelector(".select-trigger") || this.control)?.focus(options); }
  set value(value) { if (this.control) this.control.value = value; this.refresh(); }
  get value() { return super.value; }
  open() { this.querySelector(".select-trigger")?.click(); }
  close() { closeSelect(this); }
}
class NumberField extends Field {
  get control() { return this.querySelector('input[type="number"]'); }
  connect() {
    this.listen(this, "click", event => {
      const button = event.target.closest("button[data-number-step]");
      const input = this.control;
      if (!button || !input || input.matches(":disabled") || input.readOnly || button.disabled) return;
      const previous = input.value;
      if (button.dataset.numberStep === "up") input.stepUp(); else input.stepDown();
      input.focus({preventScroll: true});
      if (input.value !== previous) {
        input.dispatchEvent(new Event("input", {bubbles: true}));
        input.dispatchEvent(new Event("change", {bubbles: true}));
      }
    });
  }
}
class Disclosure extends Component {
  get control() { return this.querySelector("details"); }
  get open() { return !!this.control?.open; }
  set open(value) { if (this.control) this.control.open = Boolean(value); }
  close(restoreFocus = false) {
    this.open = false;
    if (restoreFocus) this.control?.querySelector("summary")?.focus();
  }
}
class Menu extends Disclosure {
  connect() {
    this.typed = "";
    this.listen(document, "click", event => {
      if (this.open && !this.contains(event.target) && !event.target.closest(".select-popover, [data-edit-assignment], [data-project-settings]")) this.close();
    });
    this.listen(this, "keydown", event => {
      if (event.defaultPrevented || event.target.closest("pl-menu") !== this) return;
      if (event.key === "Escape" && this.open) {
        event.preventDefault(); event.stopPropagation(); this.close(true); return;
      }
      if (event.target.matches("input, textarea, select") || event.ctrlKey || event.metaKey || event.altKey) return;
      const navigation = ["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key);
      // Space and Enter retain native button/link activation.
      if (!navigation && (event.key.length !== 1 || event.key === " ")) return;
      const choices = Array.from(this.querySelectorAll('[role="menuitem"], .row-popover button, .assignment-form button, .recipient-form button, .record-actions-panel button, .record-actions-panel a'))
        .filter(choice => !choice.matches(':disabled, [aria-disabled="true"]') && !choice.closest("[hidden]") && choice.closest("pl-menu") === this);
      if (!choices.length) return;
      event.preventDefault(); this.open = true;
      let index = choices.indexOf(document.activeElement);
      if (event.key === "Home") index = 0;
      else if (event.key === "End") index = choices.length - 1;
      else if (event.key === "ArrowDown") index = (index + 1) % choices.length;
      else if (event.key === "ArrowUp") index = index <= 0 ? choices.length - 1 : index - 1;
      else {
        if (Date.now() - this.typedAt > 700) this.typed = "";
        this.typedAt = Date.now(); this.typed += event.key.toLowerCase();
        const match = choices.findIndex(choice => choice.textContent.trim().toLowerCase().startsWith(this.typed));
        if (match >= 0) index = match;
      }
      choices[Math.max(0, index)].focus();
    });
  }
}
class Dialog extends Component {
  get control() { return this.querySelector("dialog"); }
  get open() { return !!this.control?.open; }
  showModal(opener = document.activeElement) {
    if (this.open) return;
    this.opener = opener;
    this.previousFocus = document.activeElement;
    this.control?.showModal();
  }
  close(value) { this.control?.close(value); }
  requestClose(reason = "dismiss") {
    if (this.hasAttribute("locked")) return;
    if (this.emit("pl-close-request", {reason}, true)) this.close();
  }
  connect() {
    const dialog = this.control;
    if (!dialog) return;
    let backdropPress = false;
    const outside = event => {
      const bounds = dialog.getBoundingClientRect();
      return event.target === dialog && (event.clientX < bounds.left || event.clientX > bounds.right || event.clientY < bounds.top || event.clientY > bounds.bottom);
    };
    this.listen(dialog, "pointerdown", event => { backdropPress = event.button === 0 && outside(event); });
    this.listen(dialog, "pointercancel", () => { backdropPress = false; });
    this.listen(dialog, "click", event => {
      if (backdropPress && outside(event)) this.requestClose("backdrop");
      backdropPress = false;
      if (event.target.closest("[data-dialog-dismiss]")) this.requestClose("dismiss");
    });
    this.listen(dialog, "cancel", event => {
      if (this.hasAttribute("locked") || !this.emit("pl-close-request", {reason: "escape"}, true)) event.preventDefault();
    });
    this.listen(dialog, "close", () => {
      // Safari does not focus a button on pointer activation. Remember the
      // explicit opener, while respecting deliberate focus moves after close.
      const focused = document.activeElement;
      if (this.opener?.isConnected && (focused === document.body || focused === this.previousFocus || dialog.contains(focused))) this.opener.focus({preventScroll: true});
      this.opener = null;
      this.emit("pl-close", {returnValue: dialog.returnValue});
    });
  }
}
class Tabs extends Component {
  get tabs() { return Array.from(this.querySelectorAll('[role="tab"]')).filter(tab => tab.closest("pl-tabs") === this); }
  select(tab, focus = false) {
    if (!this.tabs.includes(tab) || tab.matches(':disabled, [aria-disabled="true"]') || !this.emit("pl-tab-change", {tab, panel: tab.getAttribute("aria-controls")}, true)) return;
    if (!this.hasAttribute("manual")) for (const item of this.tabs) {
      const selected = item === tab;
      item.setAttribute("aria-selected", String(selected)); item.tabIndex = selected ? 0 : -1;
      const panel = document.getElementById(item.getAttribute("aria-controls"));
      if (panel) panel.hidden = !selected;
    }
    if (focus) tab.focus();
  }
  connect() {
    this.listen(this, "click", event => {
      const tab = event.target.closest('[role="tab"]');
      if (tab?.closest("pl-tabs") === this) this.select(tab);
    });
    this.listen(this, "keydown", event => {
      const tabs = this.tabs.filter(tab => !tab.matches(':disabled, [aria-disabled="true"]'));
      const index = tabs.indexOf(event.target);
      if (index < 0 || !["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
      event.preventDefault();
      this.select(tabs[event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : (index + (event.key === "ArrowRight" ? 1 : -1) + tabs.length) % tabs.length], true);
    });
  }
}
class Badge extends Component {}
class Notice extends Component {
  show(message) {
    const content = this.querySelector("[data-notice-content]") || this.firstElementChild;
    if (content) { content.textContent = message; content.hidden = false; }
    this.hidden = false;
  }
  dismiss() { this.hidden = true; this.emit("pl-dismiss"); }
}
class Resizer extends Component {
  static observedAttributes = ["value", "min", "max", "orientation"];
  get control() { return this.querySelector('[role="separator"]'); }
  number(name, fallback) {
    const value = Number(this.getAttribute(name));
    return this.hasAttribute(name) && Number.isFinite(value) ? value : fallback;
  }
  get min() { return this.number("min", 0); }
  get max() { return Math.max(this.min, this.number("max", 1000)); }
  get value() { return Math.max(this.min, Math.min(this.max, this.number("value", this.min))); }
  set value(value) {
    this.setAttribute("value", String(Math.max(this.min, Math.min(this.max, Number.isFinite(Number(value)) ? Math.round(Number(value)) : this.min))));
  }
  attributeChangedCallback() { if (this.isConnected) this.refresh(); }
  refresh() {
    const handle = this.control;
    if (!handle) return;
    handle.setAttribute("aria-valuemin", this.min);
    handle.setAttribute("aria-valuemax", this.max);
    handle.setAttribute("aria-valuenow", this.value);
    handle.setAttribute("aria-orientation", this.getAttribute("orientation") || "vertical");
  }
  change(value, phase) { this.value = value; this.emit("pl-resize", {value: this.value, phase}); }
  disconnect() {
    if (this.drag) { const value = this.drag.value; this.drag = null; this.change(value, "cancel"); }
  }
  connect() {
    const handle = this.control;
    if (!handle) return;
    this.refresh();
    const horizontal = () => this.getAttribute("orientation") === "horizontal";
    const coordinate = event => horizontal() ? event.clientY : event.clientX;
    const direction = () => Number(this.getAttribute("direction") || 1);
    this.listen(handle, "pointerdown", event => {
      if (event.button !== 0 || this.drag) return;
      event.preventDefault(); handle.focus();
      this.drag = {pointer: event.pointerId, start: coordinate(event), value: this.value};
      handle.setPointerCapture(event.pointerId);
      this.emit("pl-resize", {value: this.value, phase: "start"});
    });
    this.listen(handle, "pointermove", event => {
      if (this.drag?.pointer === event.pointerId) this.change(this.drag.value + (coordinate(event) - this.drag.start) * direction(), "input");
    });
    const finish = event => {
      if (this.drag?.pointer !== event.pointerId) return;
      const original = this.drag.value;
      this.drag = null;
      const canceled = event.type !== "pointerup";
      this.change(canceled ? original : this.value, canceled ? "cancel" : "commit");
    };
    this.listen(handle, "pointerup", finish);
    this.listen(handle, "pointercancel", finish);
    this.listen(handle, "lostpointercapture", finish);
    this.listen(handle, "keydown", event => {
      const backwards = horizontal() ? "ArrowUp" : "ArrowLeft";
      const forwards = horizontal() ? "ArrowDown" : "ArrowRight";
      if (![backwards, forwards, "Home", "End"].includes(event.key)) return;
      event.preventDefault();
      const step = Number(this.getAttribute("step") || 10) * (event.shiftKey ? 4 : 1);
      this.change(event.key === "Home" ? this.min : event.key === "End" ? this.max : this.value + (event.key === forwards ? 1 : -1) * direction() * step, "commit");
    });
    this.listen(handle, "dblclick", () => this.emit("pl-resize", {value: this.value, phase: "reset"}));
  }
}

class Icon extends Component {
  static observedAttributes = ["label"];
  connect() { this.refresh(); }
  attributeChangedCallback() { if (this.isConnected) this.refresh(); }
  refresh() {
    const svg = this.querySelector("svg");
    if (!svg) return;
    const label = this.getAttribute("label");
    if (label) { svg.setAttribute("role", "img"); svg.setAttribute("aria-label", label); svg.removeAttribute("aria-hidden"); }
    else { svg.setAttribute("aria-hidden", "true"); svg.removeAttribute("aria-label"); svg.removeAttribute("role"); }
  }
}
for (const [name, definition] of Object.entries({button: Button, field: Field, checkbox: Checkbox, select: Select, number: NumberField, menu: Menu, disclosure: Disclosure, dialog: Dialog, tabs: Tabs, badge: Badge, notice: Notice, icon: Icon, resizer: Resizer})) {
  customElements.define(`pl-${name}`, definition);
}

// Explicit adapter for nodes constructed by application code. The returned
// wrapper owns lifecycle; native IDs, names, events and form receipts stay put.
export function wrapControl(control) {
  const tag = control.tagName.toLowerCase();
  const name = tag === "button" || tag === "a" ? "button" : tag === "select" ? "select" : tag === "dialog" ? "dialog" : tag === "details" ? (control.matches(".switcher, .create-popover, .filter-options, .assignment-popover, .record-actions, .row-menu, .project-record") ? "menu" : "disclosure") : tag === "svg" ? "icon" : control.type === "checkbox" ? "checkbox" : "field";
  if (control.parentElement?.localName === `pl-${name}`) return control.parentElement;
  const wrapper = document.createElement(`pl-${name}`);
  wrapper.append(control);
  return wrapper;
}
export function refreshComponents(scope = document) {
  for (const select of scope.querySelectorAll("pl-select")) select.refresh();
}

export function wrapNotice(content) {
  if (content.parentElement?.localName === "pl-notice") return content.parentElement;
  const notice = document.createElement("pl-notice");
  notice.append(content);
  return notice;
}
