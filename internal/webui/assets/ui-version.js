// A loaded document must never consume markup from another UI build. Reloads
// are explicit, with a bounded, one-use draft handoff in this tab's storage.
export const revision = document.documentElement.dataset.uiRevision || "";
const storageKey = "pellets-ui-reload-drafts";
const maximumBytes = 256 * 1024;
let outdated = false,
  reloading = false,
  pending = 0,
  checking = null;
let lastFocus = null;
const bindings = [
  "request_id",
  "workspace_id",
  "resume_from",
  "resume_pellet",
  "recover_workspace_id",
  "operation",
];

export function headers(extra = {}) {
  return { ...extra, "Pellets-UI-Revision": revision };
}
export function isOutdated() {
  return outdated;
}
export function isReloading() {
  return reloading;
}
export function beginRequest() {
  pending++;
  updateNotice();
}
export function endRequest() {
  pending = Math.max(0, pending - 1);
  updateNotice();
}

function formKey(form) {
  const url = new URL(
    form.getAttribute("action") || location.href,
    location.href,
  );
  return [
    url.pathname,
    form.id,
    ...bindings.map((name) => {
      const field = form.querySelector(`input[type=hidden][name="${name}"]`);
      return field?.value || "";
    }),
  ].join("|");
}
function fields(form) {
  return Array.from(form.elements).filter(
    (field) =>
      field.name &&
      field.matches("input,textarea,select") &&
      !["submit", "button", "reset"].includes(field.type),
  );
}
function modified(field) {
  if (["checkbox", "radio"].includes(field.type))
    return field.checked !== field.defaultChecked;
  if (field.tagName === "SELECT") {
    const options = Array.from(field.options);
    if (field.multiple)
      return options.some(
        (option) => option.selected !== option.defaultSelected,
      );
    const declared = options.findLastIndex((option) => option.defaultSelected);
    const fallback =
      field.size > 1
        ? -1
        : options.findIndex(
            (option) => !option.disabled && !option.parentElement.disabled,
          );
    return field.selectedIndex !== (declared >= 0 ? declared : fallback);
  }
  return field.value !== field.defaultValue;
}
function sensitiveInput() {
  return Array.from(
    document.querySelectorAll("input[type=password],input[type=file]"),
  ).some((field) => field.value);
}
function updateNotice() {
  const notice = document.getElementById("ui-update-notice");
  if (!notice || !outdated) return;
  const secret = sensitiveInput();
  notice.querySelector("[data-ui-reload]").disabled = pending > 0 || secret;
  const message =
    pending > 0
      ? "Waiting for the current request to finish. Your input stays here."
      : secret
        ? "Clear password or file inputs before reloading. They are never stored for an update."
        : "Live updates and saving are paused. Reload to continue and keep your unfinished input.";
  const detail = notice.querySelector("[data-ui-update-detail]");
  if (detail.textContent !== message) detail.textContent = message;
}
function showNotice() {
  for (const form of document.forms)
    for (const control of form.elements) {
      if (control.tagName === "BUTTON" && control.type === "submit")
        control.disabled = true;
    }
  if (!document.getElementById("ui-update-notice")) {
    const notice = document.createElement("section");
    notice.id = "ui-update-notice";
    notice.className = "ui-update-notice";
    notice.setAttribute("role", "status");
    const title = document.createElement("strong");
    title.textContent = "Pellets has been updated";
    const detail = document.createElement("p");
    detail.dataset.uiUpdateDetail = "";
    const button = document.createElement("button");
    button.type = "button";
    button.dataset.uiReload = "";
    button.textContent = "Reload and keep drafts";
    button.addEventListener("click", reloadWithDrafts);
    notice.append(title, detail, button);
    document.body.append(notice);
  }
  // A record dialog is in the top layer. Keep the update notice reachable
  // within it, without taking focus from the current edit.
  const parent = document.querySelector("dialog[open]") || document.body;
  const notice = document.getElementById("ui-update-notice");
  if (notice.parentElement !== parent) parent.append(notice);
  if (typeof notice.showPopover === "function") {
    notice.setAttribute("popover", "manual");
    if (!notice.matches(":popover-open")) notice.showPopover();
  }
  updateNotice();
}
export function acceptRevision(value) {
  if (!value || value === revision) return !outdated;
  outdated = true;
  showNotice();
  document.dispatchEvent(new CustomEvent("pellets-ui-outdated"));
  return false;
}
export function inspectResponse(response) {
  return acceptRevision(response.headers.get("Pellets-UI-Revision"));
}
export async function checkVersion() {
  if (!checking) {
    checking = fetch("/ui-version", {
      cache: "no-store",
      credentials: "same-origin",
    })
      .then((response) => (response.ok ? response.json() : null))
      .then((value) => {
        if (value) acceptRevision(value.revision);
      })
      .catch(() => {})
      .finally(() => {
        checking = null;
      });
  }
  await checking;
  return !outdated;
}
export function watchSource(source) {
  source.addEventListener("pellets-ui-revision", (event) => {
    try {
      if (!acceptRevision(JSON.parse(event.data).revision)) source.close();
    } catch {}
  });
}

function focusSnapshot(target) {
  const field = target?.matches?.(".select-trigger")
    ? document.getElementById(target.dataset.selectId)
    : target;
  if (!field?.form || !field.name) return null;
  return {
    form: formKey(field.form),
    name: field.name,
    trigger: field !== target,
    start: field.selectionStart,
    end: field.selectionEnd,
    direction: field.selectionDirection,
  };
}
document.addEventListener("focusin", (event) => {
  if (!event.target.closest("#ui-update-notice"))
    lastFocus = focusSnapshot(event.target);
});

function reloadWithDrafts() {
  if (pending || sensitiveInput()) {
    updateNotice();
    return;
  }
  try {
    const focusedForm =
      lastFocus &&
      Array.from(document.forms).find(
        (form) => formKey(form) === lastFocus.form,
      );
    const focusedField =
      focusedForm &&
      fields(focusedForm).find((field) => field.name === lastFocus.name);
    const focus = focusedField
      ? focusSnapshot(
          lastFocus.trigger
            ? document.getElementById(focusedField.id + "-trigger")
            : focusedField,
        )
      : lastFocus;
    const drafts = Array.from(document.forms)
      .filter(
        (form) =>
          form.dataset.dirty === "true" ||
          fields(form).some(
            (field) => field.type !== "hidden" && modified(field),
          ),
      )
      .map((form) => ({
        key: formKey(form),
        label:
          form.querySelector("label")?.textContent.trim().slice(0, 80) ||
          "Unfinished input",
        fields: fields(form)
          .filter(
            (field) =>
              !["password", "file"].includes(field.type) &&
              (field.type !== "hidden" ||
                ["version", "revision"].includes(field.name)),
          )
          .map((field) => ({
            name: field.name,
            type: field.type,
            value: field.value,
            checked: field.checked,
            selected: field.multiple
              ? Array.from(field.selectedOptions, (option) => option.value)
              : null,
          })),
      }));
    const scrolls = Array.from(
      document.querySelectorAll(
        "#main,#execution,.inspector-scroll,.activity-panel",
      ),
    ).map((element) => ({
      id: element.id,
      selector: element.classList.contains("inspector-scroll")
        ? ".inspector-scroll"
        : null,
      top: element.scrollTop,
      left: element.scrollLeft,
    }));
    const saved = JSON.stringify({
      url: location.pathname + location.search,
      created: Date.now(),
      drafts,
      focus,
      scrolls,
      open: Array.from(
        document.querySelectorAll("details[id][open]"),
        (element) => element.id,
      ),
    });
    if (new TextEncoder().encode(saved).length > maximumBytes)
      throw Error(
        "These drafts are too large to carry through a reload. Copy them before reloading; they remain in this page.",
      );
    sessionStorage.setItem(storageKey, saved);
    reloading = true;
    location.reload();
  } catch (error) {
    reloading = false;
    document.querySelector("[data-ui-update-detail]").textContent =
      error.message ||
      "Drafts could not be stored. They remain in this page; copy them before reloading.";
  }
}

export function restoreDrafts() {
  let saved;
  try {
    const raw = sessionStorage.getItem(storageKey);
    sessionStorage.removeItem(storageKey);
    if (!raw || raw.length > maximumBytes) return;
    saved = JSON.parse(raw);
    if (
      saved.url !== location.pathname + location.search ||
      Date.now() - saved.created > 30 * 60 * 1000
    )
      return;
  } catch {
    return;
  }
  const forms = Array.from(document.forms);
  const unmatched = [];
  let filtersToApply = null;
  for (const draft of saved.drafts || []) {
    const form = forms.find((candidate) => formKey(candidate) === draft.key);
    if (!form) {
      unmatched.push(draft);
      continue;
    }
    let missing = false;
    for (const value of draft.fields) {
      const field = fields(form).find(
        (candidate) =>
          candidate.name === value.name &&
          candidate.type === value.type &&
          (!["checkbox", "radio"].includes(value.type) ||
            candidate.value === value.value),
      );
      if (
        !field ||
        (field.tagName === "SELECT" &&
          !Array.from(field.options).some(
            (option) => option.value === value.value,
          ))
      ) {
        missing = true;
        continue;
      }
      if (["checkbox", "radio"].includes(value.type))
        field.checked = value.checked;
      else if (value.selected)
        for (const option of field.options)
          option.selected = value.selected.includes(option.value);
      else field.value = value.value;
    }
    // Keep the original optimistic version. A concurrent change must still
    // conflict when a restored edit is submitted. CSRF always stays fresh.
    form.dataset.dirty = "true";
    form.closest("[data-protect-dirty]")?.classList.add("is-dirty");
    if (form.matches(".filters") && fields(form).some(modified))
      filtersToApply = form;
    if (missing) unmatched.push(draft);
  }
  for (const id of saved.open || []) {
    const details = document.getElementById(id);
    if (details?.tagName === "DETAILS") details.open = true;
  }
  // Restored choices can reveal dependent fields (for example, explicit group
  // assignments). Reconcile presentation before returning focus and scroll.
  window.Workbench?.initialize();
  window.Dropdowns?.enhance();
  for (const scroll of saved.scrolls || []) {
    const element = scroll.id
      ? document.getElementById(scroll.id)
      : document.querySelector(scroll.selector);
    if (element) {
      element.scrollTop = scroll.top;
      element.scrollLeft = scroll.left;
    }
  }
  if (saved.focus) {
    const form = forms.find(
      (candidate) => formKey(candidate) === saved.focus.form,
    );
    const field =
      form &&
      fields(form).find((candidate) => candidate.name === saved.focus.name);
    const target =
      saved.focus.trigger && field
        ? document.getElementById(field.id + "-trigger")
        : field;
    if (target?.getClientRects().length) {
      target.focus({ preventScroll: true });
      try {
        if (!saved.focus.trigger)
          target.setSelectionRange(
            saved.focus.start,
            saved.focus.end,
            saved.focus.direction,
          );
      } catch {}
    }
  }
  if (unmatched.length) {
    const recovered = document.createElement("details");
    recovered.id = "ui-recovered-drafts";
    recovered.className = "ui-update-notice";
    recovered.open = true;
    const summary = document.createElement("summary");
    summary.textContent =
      "Some drafts could not be restored to their original controls";
    const explanation = document.createElement("p");
    explanation.textContent =
      "The available records or actions changed. Copy the saved input below into the appropriate record; nothing was submitted.";
    recovered.append(summary, explanation);
    for (const draft of unmatched)
      for (const field of draft.fields.filter(
        (field) => field.type !== "hidden",
      )) {
        const label = document.createElement("label");
        label.textContent = `${draft.label} · ${field.name}`;
        const text = document.createElement("textarea");
        text.readOnly = true;
        text.value = ["checkbox", "radio"].includes(field.type)
          ? `${field.value}: ${field.checked ? "selected" : "not selected"}`
          : field.value;
        label.append(text);
        recovered.append(label);
      }
    (document.querySelector("dialog[open]") || document.body).append(recovered);
  }
  // Apply a search typed while updates were paused to the queue's read-only
  // endpoint, so restored filter controls and the visible rows agree.
  if (filtersToApply) queueMicrotask(() => filtersToApply.requestSubmit());
}

document.addEventListener(
  "datastar-fetch",
  (event) => {
    if (outdated && event.detail.type.startsWith("datastar-patch-"))
      event.stopImmediatePropagation();
  },
  true,
);
document.addEventListener("input", () => {
  if (outdated) updateNotice();
});
document.addEventListener("change", () => {
  if (outdated) updateNotice();
});
document.addEventListener(
  "toggle",
  () => {
    if (outdated) showNotice();
  },
  true,
);
document.addEventListener(
  "submit",
  (event) => {
    if (outdated && event.target.tagName === "FORM") {
      event.preventDefault();
      event.stopImmediatePropagation();
      showNotice();
    }
  },
  true,
);
