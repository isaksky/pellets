// Presentation-only selectors adapted from Workbench v2.
/* Shared select UI. Native controls remain the state and change-event source. */
(() => {
  if (window.Dropdowns) return;

  const selector =
    "#theme-select, #execution select, #inspector-host select, .assignment-form select, .filters select, #planning-panel select";
  let identity = 0;
  const records = new WeakMap();
  const triggerRecords = new WeakMap();
  let active = null;
  let pendingEnhance = false;
  let suppressedPointer = null;
  let typeBuffer = "";
  let typeTime = 0;
  const descriptions = {
    run_one: "Stop after one pellet.",
    drain: "Continue through work assigned to this workspace.",
    watch: "Continue, then wait for new matching work.",
  };

  function svg(className, path) {
    const element = document.createElementNS(
      "http://www.w3.org/2000/svg",
      "svg",
    );
    element.setAttribute("class", className);
    element.setAttribute("viewBox", "0 0 16 16");
    element.setAttribute("fill", "none");
    element.setAttribute("stroke", "currentColor");
    element.setAttribute("stroke-width", "1.4");
    element.setAttribute("stroke-linecap", "round");
    element.setAttribute("stroke-linejoin", "round");
    element.setAttribute("aria-hidden", "true");
    const line = document.createElementNS("http://www.w3.org/2000/svg", "path");
    line.setAttribute("d", path);
    element.append(line);
    return element;
  }

  function optionDisabled(option) {
    return (
      option.disabled ||
      (option.parentElement.tagName === "OPTGROUP" &&
        option.parentElement.disabled)
    );
  }

  function labelFor(select) {
    return (
      select.getAttribute("aria-label") ||
      select.title ||
      select.name ||
      select.id.replaceAll("-", " ")
    );
  }

  function optionLabel(option) {
    return (option?.label || option?.textContent || "")
      .replace(/\s+/g, " ")
      .trim();
  }

  function descriptionFor(select, option) {
    return (
      option.dataset.description ||
      option.dataset.repository ||
      option.dataset.repo ||
      option.dataset.path ||
      (select.name === "mode" ? descriptions[option.value] : "") ||
      ""
    );
  }

  function signature(select) {
    return Array.from(select.options, (option) => [
      option.value,
      optionLabel(option),
      optionDisabled(option),
      descriptionFor(select, option),
      option.parentElement.tagName === "OPTGROUP"
        ? option.parentElement.label
        : "",
    ])
      .map((value) => JSON.stringify(value))
      .join("|");
  }

  function sync(record) {
    const { select, trigger, value } = record;
    const text = (select.dataset.valuePrefix || "") + (
      optionLabel(select.selectedOptions[0]) ||
      select.getAttribute("data-placeholder") ||
      "Select…");
    if (value.textContent !== text) value.textContent = text;
    if (trigger.disabled !== select.disabled)
      trigger.disabled = select.disabled;
    const label = labelFor(select);
    if (trigger.getAttribute("aria-label") !== label)
      trigger.setAttribute("aria-label", label);
    if (select.hasAttribute("aria-labelledby")) {
      const labelledBy = select.getAttribute("aria-labelledby");
      if (trigger.getAttribute("aria-labelledby") !== labelledBy)
        trigger.setAttribute("aria-labelledby", labelledBy);
    } else trigger.removeAttribute("aria-labelledby");
    if (select.title) {
      if (trigger.title !== select.title) trigger.title = select.title;
    } else trigger.removeAttribute("title");
    if (
      active?.select === select &&
      (select.disabled ||
        active.signature !== signature(select) ||
        active.value !== select.value)
    )
      close(false);
  }

  function enhance(root = document) {
    if (
      active &&
      (!active.select.isConnected ||
        !active.trigger.isConnected ||
        !active.menu.isConnected)
    )
      close(false);
    const selects = [
      ...(root.matches?.(selector) ? [root] : []),
      ...root.querySelectorAll(selector),
    ];
    for (const select of selects) {
      if (!select.id) select.id = "workbench-select-" + ++identity;
      let record = records.get(select);
      if (
        record &&
        (!record.wrapper.isConnected ||
          !record.trigger.isConnected ||
          select.parentElement !== record.wrapper)
      )
        record = null;
      if (!record) {
        const wrapper = document.createElement("span");
        wrapper.className = "select-control";
        wrapper.dataset.selectId = select.id;
        const trigger = document.createElement("button");
        trigger.type = "button";
        trigger.id = `${select.id}-trigger`;
        trigger.className = "select-trigger";
        trigger.dataset.selectId = select.id;
        trigger.setAttribute("role", "combobox");
        trigger.setAttribute("aria-haspopup", "listbox");
        trigger.setAttribute("aria-expanded", "false");
        const value = document.createElement("span");
        value.className = "select-value";
        trigger.append(value, svg("select-chevron", "m4 6 4 4 4-4"));
        select.before(wrapper);
        wrapper.append(select, trigger);
        select.classList.add("select-native");
        select.hidden = true;
        select.setAttribute("aria-hidden", "true");
        select.tabIndex = -1;
        record = { select, wrapper, trigger, value };
        records.set(select, record);
        triggerRecords.set(trigger, record);
        select.addEventListener("change", () => sync(record));
      }
      sync(record);
    }
  }

  function close(restoreFocus = false) {
    const current = active;
    active = null;
    typeBuffer = "";
    if (!current) return;
    current.trigger.setAttribute("aria-expanded", "false");
    current.trigger.removeAttribute("aria-controls");
    current.trigger.removeAttribute("aria-activedescendant");
    try {
      current.menu.hidePopover?.();
    } catch {}
    current.menu.remove();
    if (
      restoreFocus &&
      current.trigger.isConnected &&
      !current.trigger.disabled
    )
      current.trigger.focus({ preventScroll: true });
  }

  function place() {
    if (!active) return;
    const { trigger, menu } = active;
    if (!trigger.isConnected || !trigger.getClientRects().length) {
      close(false);
      return;
    }
    const rect = trigger.getBoundingClientRect();
    const width = document.documentElement.clientWidth;
    const height = window.innerHeight;
    if (
      rect.bottom < 0 ||
      rect.top > height ||
      rect.right < 0 ||
      rect.left > width
    ) {
      close(false);
      return;
    }
    menu.style.maxWidth = `${Math.max(0, width - 16)}px`;
    menu.style.maxHeight = `${Math.max(0, Math.min(360, height - 16))}px`;
    const initial = menu.getBoundingClientRect();
    const below = height - rect.bottom - 12;
    const above = rect.top - 12;
    const flip = below < initial.height && above > below;
    menu.style.maxHeight = `${Math.max(0, Math.min(360, flip ? above : below))}px`;
    const size = menu.getBoundingClientRect();
    const left = Math.max(8, Math.min(rect.left, width - size.width - 8));
    const top = Math.max(
      8,
      Math.min(
        flip ? rect.top - size.height - 4 : rect.bottom + 4,
        height - size.height - 8,
      ),
    );
    menu.style.left = `${left}px`;
    menu.style.top = `${top}px`;
    menu.dataset.placement = flip ? "above" : "below";
  }

  function focusOption(index) {
    if (!active || !active.options.length) return;
    const count = active.options.length;
    active.index = (index + count) % count;
    active.options.forEach((option, i) =>
      option.classList.toggle("is-focused", i === active.index),
    );
    const option = active.options[active.index];
    active.trigger.setAttribute("aria-activedescendant", option.id);
    option.focus({ preventScroll: true });
    const menu = active.menu;
    const menuRect = menu.getBoundingClientRect();
    const optionRect = option.getBoundingClientRect();
    if (optionRect.top < menuRect.top + 5)
      menu.scrollTop -= menuRect.top + 5 - optionRect.top;
    else if (optionRect.bottom > menuRect.bottom - 5)
      menu.scrollTop += optionRect.bottom - menuRect.bottom + 5;
  }

  function open(record, initial = "selected") {
    close(false);
    if (record.select.id === "view-switcher")
      window.Navigation?.sync?.(record.select);
    sync(record);
    const { select, trigger } = record;
    if (select.disabled || !trigger.isConnected) return;
    const menu = document.createElement("div");
    menu.className = "select-popover";
    menu.id = `${select.id}-listbox`;
    menu.setAttribute("popover", "manual");
    menu.setAttribute("role", "listbox");
    menu.setAttribute("aria-label", labelFor(select));
    menu.dataset.selectId = select.id;
    const options = [];
    let lastGroup = null;
    Array.from(select.options).forEach((option, index) => {
      if (option.hidden) return;
      const group =
        option.parentElement.tagName === "OPTGROUP"
          ? option.parentElement
          : null;
      if (group && group !== lastGroup) {
        const heading = document.createElement("div");
        heading.className = "select-group-heading";
        heading.setAttribute("role", "presentation");
        heading.textContent = group.label;
        menu.append(heading);
      }
      lastGroup = group;
      const button = document.createElement("button");
      button.type = "button";
      button.id = `${select.id}-option-${index}`;
      button.className = "select-option";
      button.setAttribute("role", "option");
      button.setAttribute("aria-selected", String(option.selected));
      button.dataset.value = option.value;
      button.dataset.optionIndex = String(index);
      button.tabIndex = -1;
      button.disabled = optionDisabled(option);
      const content = document.createElement("span");
      content.className = "select-option-content";
      const label = document.createElement("span");
      label.className = "select-option-label";
      label.textContent = optionLabel(option);
      content.append(label);
      const description = descriptionFor(select, option);
      if (description) {
        const detail = document.createElement("span");
        detail.className = "select-option-description";
        detail.textContent = description;
        content.append(detail);
      }
      button.append(content, svg("select-check", "m3.5 8 3 3 6-6"));
      menu.append(button);
      if (!button.disabled) options.push(button);
    });
    (select.closest("dialog") || document.body).append(menu);
    active = {
      select,
      trigger,
      menu,
      options,
      index: -1,
      signature: signature(select),
      value: select.value,
    };
    trigger.setAttribute("aria-expanded", "true");
    trigger.setAttribute("aria-controls", menu.id);
    try {
      menu.showPopover?.();
    } catch {
      menu.removeAttribute("popover");
    }
    if (typeof menu.showPopover !== "function") menu.removeAttribute("popover");
    place();
    if (!active) return;
    const selected = options.findIndex(
      (option) => option.dataset.optionIndex === String(select.selectedIndex),
    );
    focusOption(
      initial === "first"
        ? 0
        : initial === "last"
          ? options.length - 1
          : Math.max(0, selected),
    );
  }

  function focusReplacement(id, keepDetailsOpen) {
    enhance(document);
    const select = document.getElementById(id);
    const record = select && records.get(select);
    if (!record || record.trigger.disabled) return;
    if (keepDetailsOpen) {
      for (
        let ancestor = record.trigger.parentElement;
        ancestor;
        ancestor = ancestor.parentElement
      ) {
        if (ancestor.tagName === "DETAILS") ancestor.open = true;
      }
    }
    record.trigger.focus({ preventScroll: true });
  }

  function choose(option) {
    if (!active || !option || option.disabled) return;
    const select = active.select;
    const id = select.id;
    const keepDetailsOpen = !!select.closest("details[open]");
    const index = Number(option.dataset.optionIndex);
    const changed = select.selectedIndex !== index;
    close(false);
    if (changed) {
      select.selectedIndex = index;
      select.dispatchEvent(new Event("input", { bubbles: true }));
      select.dispatchEvent(new Event("change", { bubbles: true }));
    }
    focusReplacement(id, keepDetailsOpen);
  }

  function typeahead(character) {
    if (!active?.options.length) return;
    const now = performance.now();
    typeBuffer = now - typeTime > 650 ? character : typeBuffer + character;
    typeTime = now;
    const query = [...typeBuffer].every((value) => value === typeBuffer[0])
      ? typeBuffer[0]
      : typeBuffer;
    const options = active.options;
    const start = query.length === 1 ? active.index + 1 : active.index;
    for (let offset = 0; offset < options.length; offset++) {
      const index = (start + offset + options.length) % options.length;
      if (
        options[index]
          .querySelector(".select-option-label")
          .textContent.toLowerCase()
          .startsWith(query)
      ) {
        focusOption(index);
        return;
      }
    }
  }

  document.addEventListener(
    "pointerdown",
    (event) => {
      suppressedPointer = null;
      if (!active) return;
      if (active.menu.contains(event.target)) {
        event.stopImmediatePropagation();
        return;
      }
      if (active.trigger.contains(event.target)) return;
      suppressedPointer = { x: event.clientX, y: event.clientY };
      event.preventDefault();
      event.stopImmediatePropagation();
      close(true);
    },
    true,
  );

  document.addEventListener(
    "click",
    (event) => {
      if (
        suppressedPointer &&
        Math.abs(suppressedPointer.x - event.clientX) < 5 &&
        Math.abs(suppressedPointer.y - event.clientY) < 5
      ) {
        suppressedPointer = null;
        event.preventDefault();
        event.stopImmediatePropagation();
        return;
      }
      suppressedPointer = null;
      const option = event.target.closest(".select-option");
      if (active?.menu.contains(event.target)) {
        event.preventDefault();
        event.stopImmediatePropagation();
        if (option) choose(option);
        return;
      }
      const trigger = event.target.closest(".select-trigger");
      const record = trigger && triggerRecords.get(trigger);
      if (record) {
        event.preventDefault();
        event.stopImmediatePropagation();
        if (active?.trigger === trigger) close(true);
        else open(record);
        return;
      }
      const label = event.target.closest("label[for]");
      const native = label && document.getElementById(label.htmlFor);
      const targetRecord = native && records.get(native);
      if (targetRecord) {
        event.preventDefault();
        event.stopImmediatePropagation();
        targetRecord.trigger.focus({ preventScroll: true });
      }
    },
    true,
  );

  document.addEventListener(
    "keydown",
    (event) => {
      const trigger = event.target.closest(".select-trigger");
      const record = trigger && triggerRecords.get(trigger);
      const inMenu = active?.menu.contains(event.target);
      if (!record && !inMenu) return;
      if (event.key === "Escape" && !active) return;
      const handled = [
        "ArrowDown",
        "ArrowUp",
        "Home",
        "End",
        "Enter",
        " ",
        "Escape",
      ].includes(event.key);
      if (event.key === "Tab" && active) {
        close(true);
        return;
      }
      if (handled) {
        event.preventDefault();
        event.stopImmediatePropagation();
        if (event.key === "Escape") {
          close(true);
          return;
        }
        if (!active) {
          if (!record || record.select.disabled) return;
          open(
            record,
            event.key === "Home"
              ? "first"
              : event.key === "End"
                ? "last"
                : "selected",
          );
          return;
        }
        if (event.key === "ArrowDown") focusOption(active.index + 1);
        else if (event.key === "ArrowUp") focusOption(active.index - 1);
        else if (event.key === "Home") focusOption(0);
        else if (event.key === "End") focusOption(active.options.length - 1);
        else if (event.key === "Enter" || event.key === " ")
          choose(active.options[active.index]);
        return;
      }
      if (
        event.key.length === 1 &&
        !event.ctrlKey &&
        !event.metaKey &&
        !event.altKey
      ) {
        event.preventDefault();
        event.stopImmediatePropagation();
        if (!active && record) open(record);
        typeahead(event.key.toLowerCase());
      }
    },
    true,
  );

  document.addEventListener(
    "pointermove",
    (event) => {
      const option = event.target.closest(".select-option");
      if (
        !active ||
        !option ||
        !active.menu.contains(option) ||
        option.disabled
      )
        return;
      const index = active.options.indexOf(option);
      if (index >= 0 && index !== active.index) focusOption(index);
    },
    true,
  );

  document.addEventListener(
    "scroll",
    (event) => {
      if (
        !active ||
        event.target === active.menu ||
        active.menu.contains(event.target)
      )
        return;
      // Streaming activity can scroll independently of the menu's anchor.
      if (
        event.target === document ||
        event.target === window ||
        event.target.contains?.(active.trigger)
      )
        place();
    },
    true,
  );
  window.addEventListener("resize", place);
  document.addEventListener(
    "close",
    (event) => {
      if (
        active &&
        event.target.tagName === "DIALOG" &&
        event.target.contains(active.menu)
      )
        close(false);
    },
    true,
  );

  const observer = new MutationObserver(() => {
    if (
      active &&
      (!active.select.isConnected ||
        !active.trigger.isConnected ||
        !active.menu.isConnected)
    )
      close(false);
    if (pendingEnhance) return;
    pendingEnhance = true;
    queueMicrotask(() => {
      pendingEnhance = false;
      enhance(document);
    });
  });
  observer.observe(document.documentElement, {
    subtree: true,
    childList: true,
    attributes: true,
    attributeFilter: [
      "disabled",
      "selected",
      "label",
      "aria-label",
      "aria-labelledby",
      "title",
      "data-description",
    ],
  });

  window.Dropdowns = { enhance, close: () => close(false) };
  enhance(document);
})();
