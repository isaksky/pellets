import { action, actions } from "./datastar-1.0.3.js";

(function () {
  "use strict";

  var root = document.documentElement;
  var media = window.matchMedia ? window.matchMedia("(prefers-color-scheme: dark)") : null;

  function applyTheme(choice) {
    if (choice !== "light" && choice !== "dark") choice = "system";
    root.dataset.themeChoice = choice;
    root.dataset.theme = choice === "system" ? (media && media.matches ? "dark" : "light") : choice;
    var selector = document.getElementById("theme-select");
    if (selector) selector.value = choice;
  }

  function rememberTheme(choice) {
    try { localStorage.setItem("pellets-theme", choice); } catch (_) {}
    applyTheme(choice);
  }

  document.addEventListener("change", function (event) {
    if (event.target && event.target.id === "theme-select") rememberTheme(event.target.value);
  });

  // Schedule controls intentionally use the small JSON receipt endpoints.
  // A successful receipt is followed by an authoritative page refresh; browser
  // state never guesses a run state or retains a stale receipt after reconnect.
  document.addEventListener("submit", async function (event) {
    var form = event.target;
    if (!form || !form.matches("form[data-schedule], form[data-run-action]")) return;
    event.preventDefault();
    if (form.dataset.schedulePending === "true") return;
    var submitter = event.submitter;
    if (submitter && submitter.disabled) return;
    var fields = new FormData(form);
    if (submitter && submitter.name) fields.set(submitter.name, submitter.value);
    // Keep the explicit filter scope aligned with the current control value;
    // an old rendered "value" marker must never turn a cleared group into an
    // accidental wildcard. Ungrouped is intentionally server-rejected.
    if (fields.has("group_scope") && fields.get("group_scope") !== "ungrouped") {
      fields.set("group_scope", fields.get("group") ? "value" : "any");
    }
    if (form.matches("form[data-schedule]")) fields.set("admission", "interactive");
    var body = new URLSearchParams();
    fields.forEach(function (value, key) {
      // Empty filter controls mean "unfiltered". Omit them so the server can
      // distinguish that from an invalid explicitly empty exact filter.
      if ((key === "external_id" || key === "group") && value === "") return;
      body.append(key, value);
    });
    var feedback = document.getElementById("request-feedback");
    var buttons = Array.from(form.querySelectorAll("button"));
    var confirmed = false;
    delete form.dataset.admissionChoice;
    form.dataset.schedulePending = "true";
    buttons.forEach(function (button) { button.disabled = true; });
    feedback.hidden = false;
    feedback.classList.remove("request-failed");
    feedback.textContent = form.matches("form[data-schedule]") ? "Checking the runtime, sign-in, workspace, and saved run…" : "Updating run controls…";
    try {
      var response = await fetch(form.action, {method: "POST", credentials: "same-origin", headers: {"Content-Type": "application/x-www-form-urlencoded"}, body: body.toString()});
      if (!response.ok) {
        var problem;
        if ((response.headers.get("Content-Type") || "").includes("application/json")) problem = await response.json();
        if (problem && problem.error) {
          form.dataset.admissionChoice = "true";
          feedback.classList.add("request-failed");
          feedback.textContent = problem.error.message;
          var retry = function (field) {
            if (field) {
              var input = form.querySelector('input[name="' + field + '"]');
              if (!input) { input = document.createElement("input"); input.type = "hidden"; input.name = field; form.appendChild(input); }
              input.value = "true";
            }
            form.requestSubmit(submitter || undefined);
          };
          (problem.choices || []).forEach(function (choice) {
            if (!["fresh_conversation", "use_managed_runtime"].includes(choice.field)) return;
            var button = document.createElement("button");
            button.type = "button"; button.textContent = choice.label; button.title = choice.description;
            button.addEventListener("click", function () { retry(choice.field); });
            feedback.appendChild(button);
          });
          var retryButton = document.createElement("button");
          retryButton.type = "button"; retryButton.textContent = "Check again";
          retryButton.addEventListener("click", function () { retry(); });
          feedback.appendChild(retryButton);
          var cancelButton = document.createElement("button");
          cancelButton.type = "button"; cancelButton.textContent = "Cancel";
          cancelButton.addEventListener("click", function () { delete form.dataset.admissionChoice; feedback.hidden = true; refreshRegions(); });
          feedback.appendChild(cancelButton);
          return;
        }
        throw new Error("run action rejected");
      }
      confirmed = true;
      delete form.dataset.schedulePending;
      delete form.dataset.dirty;
      feedback.hidden = true;
      refreshRegions();
    } catch (_) {
      feedback.classList.add("request-failed");
      feedback.textContent = "Run control could not be confirmed. Reload to view authoritative status.";
    } finally {
      // Success remains locked until the authoritative dashboard patch replaces
      // this form. This closes the window where an old receipt could admit a
      // duplicate click before the refreshed state arrives.
      if (!confirmed) {
        delete form.dataset.schedulePending;
        buttons.forEach(function (button) { button.disabled = false; });
      }
    }
  });
  if (media) media.addEventListener("change", function () {
    if (root.dataset.themeChoice === "system") applyTheme("system");
  });

  function markDirty(form) {
    editRevision += 1;
    form.dataset.dirty = "true";
    var protectedRegion = form.closest("[data-protect-dirty]");
    if (protectedRegion) protectedRegion.classList.add("is-dirty");
  }
  document.addEventListener("input", function (event) {
    var form = event.target && event.target.closest("form.dirty-track");
    if (form) markDirty(form);
  });
  document.addEventListener("change", function (event) {
    var form = event.target && event.target.closest("form.dirty-track");
    if (form) markDirty(form);
  });

  function dirtyInspector() { return document.querySelector("[data-inspector].is-dirty"); }
  function confirmDiscard() { return !dirtyInspector() || window.confirm("Discard unsaved inspector changes?"); }

  var historyIndexKey = "pelletsHistoryIndex";
  var currentHistoryIndex = history.state && Number.isInteger(history.state[historyIndexKey]) ? history.state[historyIndexKey] : 0;
  history.replaceState(Object.assign({}, history.state || {}, {[historyIndexKey]: currentHistoryIndex}), "", location.href);
  var replayingHistory = false;
  window.addEventListener("popstate", function (event) {
    var targetIndex = event.state && Number.isInteger(event.state[historyIndexKey]) ? event.state[historyIndexKey] : currentHistoryIndex - 1;
    if (replayingHistory) { replayingHistory = false; currentHistoryIndex = targetIndex; return; }
    if (confirmDiscard()) {
      // Reload authoritative HTML for Back/Forward; never restore stale form drafts.
      document.querySelectorAll("[data-inspector].is-dirty").forEach(function (el) { el.classList.remove("is-dirty"); });
      window.location.reload();
      return;
    }
    replayingHistory = true;
    history.go(targetIndex < currentHistoryIndex ? 1 : -1);
  });

  var inspectorOpener = null;
  var inspectorOpenerHref = "";
  var sortOpenerID = "";
  var tableScrollLeft = 0;
  // Forward clicks in non-interactive cells to that row's native link. A
  // positioned overlay on <tr> is not a reliable hit target across browsers.
  document.addEventListener("click", function (event) {
    var row = event.target.closest(".task-row");
    if (!row || event.defaultPrevented || event.button !== 0 ||
        event.target.closest("a, button, input, select, textarea, summary, [contenteditable]")) return;
    if (window.getSelection && !window.getSelection().isCollapsed) return;
    var link = row.querySelector(".row-link");
    if (!link) return;
    if (event.metaKey || event.ctrlKey || event.shiftKey) {
      window.open(link.href, "_blank", "noopener");
      return;
    }
    link.dispatchEvent(new MouseEvent("click", {
      bubbles: true, cancelable: true, view: window,
      ctrlKey: event.ctrlKey, metaKey: event.metaKey, shiftKey: event.shiftKey, altKey: event.altKey
    }));
  });

  document.addEventListener("click", function (event) {
    var sorter = event.target.closest(".task-sort");
    if (sorter) sortOpenerID = sorter.id;
    var opener = event.target.closest(".row-link, .task-title, .memory-card > a");
    if (!opener) return;
    inspectorOpener = opener;
    inspectorOpenerHref = opener.getAttribute("href") || "";
    var table = opener.closest(".table-scroll");
    tableScrollLeft = table ? table.scrollLeft : 0;
  });

  // These small application actions delegate transport and DOM patches to Datastar.
  // One foreground request and one background refresh own complete update bundles.
  // Foreground work supersedes refreshes; a pending save cannot be interrupted.
  var pending = new Map();
  var requests = new WeakMap();
  var editRevision = 0;
  var routeRevision = 0;
  var refreshTimer;

  function refreshRegions() {
    clearTimeout(refreshTimer);
    refreshTimer = setTimeout(function () {
      document.dispatchEvent(new CustomEvent("pellets-refresh"));
    }, 120);
  }

  function protectedTarget(target) {
    return target && ((target.id === "project-drawer" && target.classList.contains("open")) ||
      (target.id === "run-dashboard" && target.querySelector("form[data-no-run-resume][data-dirty='true'], form[data-schedule-pending='true'], form[data-admission-choice='true']")) ||
      (target.id === "project-record" && target.open));
  }

  async function request(ctx, targetID, kind) {
    var el = ctx.el;
    var automatic = kind === "refresh";
    var mutation = kind === "submit";
    if (kind === "navigate") {
      var evt = ctx.evt;
      if (evt && (evt.button !== 0 || evt.metaKey || evt.ctrlKey || evt.shiftKey || evt.altKey)) return;
      if (evt) evt.preventDefault();
    }
    var target = targetID === "live" ? document.body : document.getElementById(targetID);
    if (automatic && Array.from(pending.values()).some(function (state) { return !state.automatic; })) return;
    if (!target || (automatic && (document.hidden || protectedTarget(target)))) return;
    if (automatic && targetID === "inspector-host" && target.querySelector(".conflict-state, .error-state")) return;
    var editing = dirtyInspector();
    var discard = false;
    if (targetID === "inspector-host" && editing) {
      if (automatic) return;
      if (!el.matches("form.dirty-track")) {
        if (!confirmDiscard()) return;
        discard = true;
      }
    }
    // Do not let a poll interrupt a mutation/navigation or submit the same form twice.
    var slot = automatic ? "background" : "foreground";
    var previous = pending.get(slot);
    if (previous && automatic) return;
    if (previous && previous.mutation) {
      // Keep only the latest navigation/filter intent while the save completes.
      if (!mutation) previous.next = {ctx: ctx, targetID: targetID, kind: kind};
      return;
    }
    if (mutation && !el.checkValidity()) { el.reportValidity(); return; }
    if (previous) previous.controller.abort();
    if (!automatic) {
      routeRevision += 1;
      var background = pending.get("background");
      if (background) background.controller.abort();
    }
    var state = {slot: slot, el: el, targetID: targetID, automatic: automatic, mutation: mutation, navigation: kind === "navigate",
      revision: editRevision, route: routeRevision, controller: new AbortController(), applied: false, discard: discard};
    pending.set(slot, state);
    requests.set(el, state);
    var url = automatic ? location.pathname + location.search : mutation || kind === "filter" ? el.action : el.href;
    if (kind === "filter") {
      // Sort/filter values are successful form controls; avoid duplicate query keys.
      url = location.pathname;
    } else if (kind === "navigate" && targetID === "tasks-area") {
      // The list may still contain links rendered before the inspector closed.
      var sorted = new URL(url, location.href);
      sorted.pathname = location.pathname;
      url = sorted.pathname + sorted.search;
    } else if (kind === "navigate" && el.matches("[aria-label='Close inspector']")) {
      url = new URL(url, location.href).pathname + location.search;
    }
    if (mutation) url += location.search;
    var options = {headers: {"Pellets-Target": targetID}, requestCancellation: state.controller,
      openWhenHidden: true, retry: "never", retryMaxCount: 0, filterSignals: {include: /^$/}};
    if (mutation || kind === "filter") options.contentType = "form";
    var feedback = document.getElementById("request-feedback");
    var buttons = mutation ? Array.from(el.querySelectorAll("button[type=submit], button:not([type])")).filter(function (button) { return !button.disabled; }) : [];
    if (!automatic) {
      feedback.hidden = false;
      feedback.textContent = mutation ? "Saving…" : "Loading…";
      feedback.classList.remove("request-failed");
      el.setAttribute("aria-busy", "true");
    }
    try {
      // Datastar serializes successful controls before disabling the submitter.
      var work = actions[mutation ? "post" : "get"](ctx, url, options);
      buttons.forEach(function (button) { button.disabled = true; });
      await work;
    } catch (_) {
      // Missing completion is reported below; drafts remain in the DOM.
    } finally {
      buttons.forEach(function (button) { button.disabled = false; });
      if (requests.get(el) === state) el.removeAttribute("aria-busy");
      if (!automatic && pending.get(slot) === state && state.route === routeRevision && !state.controller.signal.aborted) {
        feedback.hidden = !!state.completed;
        if (!state.completed) {
          feedback.classList.add("request-failed");
          feedback.textContent = mutation ? "Save could not be confirmed. Your draft is preserved. Check the current record before retrying." : "Could not load updates. Please try again.";
        }
      }
      if (pending.get(slot) === state) pending.delete(slot);
      if (requests.get(el) === state) requests.delete(el);
      if (state.next && state.next.ctx.el.isConnected && state.completed) {
        request(state.next.ctx, state.next.targetID, state.next.kind);
      }
    }
  }

  action({name: "navigate", apply: function (ctx, target) { return request(ctx, target, "navigate"); }});
  action({name: "submit", apply: function (ctx) { return request(ctx, "inspector-host", "submit"); }});
  action({name: "filter", apply: function (ctx) { return request(ctx, "task-list", "filter"); }});
  action({name: "refresh", apply: function (ctx, target) { return request(ctx, target || ctx.el.id, "refresh"); }});

  // Capture precedes Datastar's patch watcher, including responses already in flight
  // when a user opens a disclosure or starts editing.
  document.addEventListener("datastar-fetch", function (event) {
    var detail = event.detail;
    var state = requests.get(detail.el);
    if (!state) return;
    if (detail.type === "datastar-patch-elements") {
      var patchID = (detail.argsRaw.selector || "").replace(/^#/, "");
      var target = document.getElementById(patchID);
      var inspector = patchID === "inspector-host";
      // A navigation bundle describes one destination. If newer edits reject its
      // first (inspector) patch, its list selection must not move there either.
      if (inspector && state.navigation && state.revision !== editRevision) state.rejected = true;
      if (state.rejected || state.controller.signal.aborted || pending.get(state.slot) !== state || state.route !== routeRevision ||
          (!state.applied && !state.el.isConnected) ||
          (protectedTarget(target) && (state.automatic || patchID !== state.targetID)) ||
          (inspector && ((state.automatic && (dirtyInspector() || target.querySelector(".conflict-state, .error-state"))) || state.revision !== editRevision))) {
        event.stopImmediatePropagation();
        return;
      }
      // Morphing preserves live values when server defaults are unchanged. Apply
      // confirmed discard only once a current response can replace the inspector,
      // so failed requests and edits made in flight retain their drafts.
      if (inspector && state.discard) {
        target.querySelectorAll("form.dirty-track").forEach(function (form) { form.reset(); });
        state.discard = false;
      }
      state.applied = true;
      if (patchID === state.targetID) state.primaryApplied = true;
      if (patchID === "app-content") state.bootstrapApplied = true;
      queueMicrotask(function () { afterPatch(document.getElementById(patchID) || document); });
    } else if (detail.type === "datastar-patch-signals") {
      if (!state.applied || state.controller.signal.aborted || state.route !== routeRevision) {
        event.stopImmediatePropagation();
        return;
      }
      state.completed = true;
      var result = JSON.parse(detail.argsRaw.signals)._webResult;
      if (!result) return;
      if (state.el.matches && state.el.matches("form[data-checkpoint-form]") && result.status < 400) {
        // The add receipt intentionally retains this value after uncertain
        // failures, but a confirmed creation must not make the next distinct
        // selection replay this completed request.
        var requestID = state.el.querySelector("input[name=request_id]");
        if (requestID) requestID.value = "";
        clearCheckpointSelection();
      }
      if (state.automatic && state.bootstrapApplied && result.status < 400 && result.url) {
        var destination = new URL(result.url, location.href);
        if (destination.origin === location.origin) {
          history.replaceState(history.state, "", destination.pathname + destination.search);
          var heading = document.querySelector(".project-heading h1");
          document.title = heading ? heading.textContent + " · Pellets" : "Pellets";
        }
      }
      // Related regions can refresh even when newer edits reject the inspector.
      // Advance history only if the requested destination was actually accepted.
      if (!state.automatic && state.primaryApplied && result.status < 400 && result.url) {
        var next = new URL(result.url, location.href);
        if (next.origin === location.origin && next.pathname + next.search !== location.pathname + location.search) {
          currentHistoryIndex += 1;
          history.pushState({[historyIndexKey]: currentHistoryIndex}, "", next.pathname + next.search);
        }
      }
    }
  }, true);

  window.addEventListener("beforeunload", function (event) {
    if (!dirtyInspector()) return;
    event.preventDefault();
    event.returnValue = "";
  });

  var previousRows = new Map();
  // Selection is deliberately keyed by immutable Pellet references rather than
  // a row position. The table may be sorted by any display column and live
  // invalidations may replace its DOM while a human is composing a checkpoint.
  var checkpointSelection = new Map();
  // Composer inputs are replaced by live HTML patches. Keep an explicit user
  // override outside that DOM so a poll cannot silently restore an inferred
  // filter/common value over the value they chose.
  var checkpointMetadataOverrides = {};

  function checkpointRowSnapshot(row) {
    return {
      reference: row.dataset.rowId,
      version: row.dataset.rowVersion,
      kind: row.dataset.checkpointKind,
      priority: row.dataset.checkpointPriority,
      external: row.dataset.checkpointExternal || "",
      externalSet: row.dataset.checkpointExternalSet === "true",
      group: row.dataset.checkpointGroup || "",
      groupSet: row.dataset.checkpointGroupSet === "true",
      problem: ""
    };
  }

  function newCheckpointRequestID() {
    if (window.crypto && window.crypto.randomUUID) return window.crypto.randomUUID();
    if (window.crypto && window.crypto.getRandomValues) {
      var bytes = new Uint8Array(16);
      window.crypto.getRandomValues(bytes);
      return Array.prototype.map.call(bytes, function (byte) { return byte.toString(16).padStart(2, "0"); }).join("");
    }
    return "checkpoint-" + Date.now() + "-" + Math.random().toString(36).slice(2);
  }

  function clearCheckpointSelection() {
    checkpointSelection.clear();
    checkpointMetadataOverrides = {};
    synchronizeCheckpointSelection();
  }

  function updateCheckpointComposer() {
    var composer = document.querySelector("[data-checkpoint-composer]");
    if (!composer) return;
    var selected = Array.from(checkpointSelection.values()).sort(function (a, b) { return a.reference.localeCompare(b.reference, undefined, {numeric: true}); });
    var form = composer.querySelector("form[data-checkpoint-form]");
    var targets = form.querySelector("input[name=review_targets]");
    var versions = form.querySelector("input[name=review_target_versions]");
    var button = form.querySelector("button[type=submit]");
    var summary = composer.querySelector("[data-checkpoint-summary]");
    var warning = composer.querySelector("[data-checkpoint-warning]");
    var invalid = selected.filter(function (item) { return item.problem || item.kind !== "ordinary"; });
    var tooMany = selected.length > 1000;
    composer.hidden = selected.length === 0;
    targets.value = selected.map(function (item) { return item.reference; }).join(",");
    versions.value = selected.map(function (item) { return item.reference + ":" + item.version; }).join(",");
    if (!selected.length) return;

    var active = selected.filter(function (item) { return /^\d+$/.test(item.priority); });
    active.sort(function (a, b) { return Number(a.priority) - Number(b.priority) || a.reference.localeCompare(b.reference); });
    var placement = active.length ? "after " + active[active.length - 1].reference + " in authoritative priority order" : "at the active queue tail (the selection has no active queue position)";
    summary.textContent = selected.length + " selected: " + selected.map(function (item) { return item.reference; }).join(", ") + ". Insert " + placement + "; the displayed table sort does not affect placement.";

    function metadata(name, filterSet, filterValue) {
      if (filterSet) return {value: filterValue, source: "the exact displayed " + name + " filter", mixed: false};
      var values = new Set(selected.map(function (item) { return item[name] + "\u0000" + (item[name + "Set"] ? "set" : "unset"); }));
      if (values.size === 1) {
        var first = selected[0];
        return {value: first[name], source: "common selected metadata", mixed: false};
      }
      return {value: "", source: "mixed selected metadata", mixed: true};
    }
    var external = metadata("external", composer.dataset.filterExternalSet === "true", composer.dataset.filterExternal || "");
    var group = metadata("group", composer.dataset.filterGroupSet === "true", composer.dataset.filterGroup || "");
    [ ["external", external], ["group", group] ].forEach(function (entry) {
      var input = form.querySelector("[data-checkpoint-metadata='" + entry[0] + "']");
      input.value = Object.prototype.hasOwnProperty.call(checkpointMetadataOverrides, entry[0]) ? checkpointMetadataOverrides[entry[0]] : entry[1].value;
    });
    var metadataNotice = "External ID: " + external.source + "; Group: " + group.source + ".";
    if (composer.dataset.filterGroupUngrouped === "true") metadataNotice += " The exact Ungrouped filter remains ungrouped and cannot be scheduled until it is removed.";
    if (external.mixed || group.mixed) metadataNotice += " Mixed values default to no exact filter, so this checkpoint will not disappear from a guessed runner filter.";

    if (invalid.length || tooMany) {
      warning.hidden = false;
      warning.textContent = tooMany ? "Select at most 1000 ordinary Pellets for one checkpoint." : "Selection changed and was not submitted: " + invalid.map(function (item) { return item.reference + " (" + (item.problem || "not an ordinary Pellet") + ")"; }).join(", ") + ". Reselect each affected target after refreshing it.";
      button.disabled = true;
    } else {
      warning.hidden = false;
      warning.textContent = metadataNotice;
      button.disabled = false;
    }
    var requestID = form.querySelector("input[name=request_id]");
    if (!requestID.value) requestID.value = newCheckpointRequestID();
  }

  function synchronizeCheckpointSelection() {
    var rows = new Map();
    document.querySelectorAll("#task-list .task-row[data-checkpoint-kind]").forEach(function (row) { rows.set(row.dataset.rowId, row); });
    checkpointSelection.forEach(function (selected, reference) {
      var row = rows.get(reference);
      if (!row) {
        selected.problem = "no longer appears in this filtered queue";
        return;
      }
      var current = checkpointRowSnapshot(row);
      if (current.version !== selected.version) selected.problem = "changed since selection";
    });
    document.querySelectorAll("#task-list [data-checkpoint-select]").forEach(function (checkbox) {
      var row = checkbox.closest(".task-row");
      checkbox.checked = checkpointSelection.has(row.dataset.rowId);
    });
    document.querySelectorAll("[data-checkpoint-select-all]").forEach(function (control) {
      var selectable = Array.prototype.filter.call(document.querySelectorAll("[data-checkpoint-select]:not([disabled])"), function (checkbox) { return checkbox.closest(".task-row"); });
      control.checked = selectable.length > 0 && selectable.every(function (checkbox) { return checkbox.checked; });
      control.indeterminate = selectable.some(function (checkbox) { return checkbox.checked; }) && !control.checked;
    });
    updateCheckpointComposer();
  }

  document.addEventListener("change", function (event) {
    var checkbox = event.target && event.target.closest("[data-checkpoint-select]");
    if (checkbox) {
      var row = checkbox.closest(".task-row");
      if (checkbox.checked) checkpointSelection.set(row.dataset.rowId, checkpointRowSnapshot(row));
      else checkpointSelection.delete(row.dataset.rowId);
      synchronizeCheckpointSelection();
      return;
    }
    var all = event.target && event.target.closest("[data-checkpoint-select-all]");
    if (all) {
      document.querySelectorAll("[data-checkpoint-select]:not([disabled])").forEach(function (candidate) {
        var row = candidate.closest(".task-row");
        candidate.checked = all.checked;
        if (all.checked) checkpointSelection.set(row.dataset.rowId, checkpointRowSnapshot(row));
        else checkpointSelection.delete(row.dataset.rowId);
      });
      synchronizeCheckpointSelection();
      return;
    }
    var metadataInput = event.target && event.target.closest("[data-checkpoint-metadata]");
    if (metadataInput) checkpointMetadataOverrides[metadataInput.dataset.checkpointMetadata] = metadataInput.value;
  });

  document.addEventListener("input", function (event) {
    var metadataInput = event.target && event.target.closest("[data-checkpoint-metadata]");
    if (metadataInput) checkpointMetadataOverrides[metadataInput.dataset.checkpointMetadata] = metadataInput.value;
  });

  document.addEventListener("click", function (event) {
	var clear = event.target && event.target.closest("[data-checkpoint-clear]");
	if (!clear) return;
	event.preventDefault();
	clearCheckpointSelection();
  });

  document.addEventListener("submit", function (event) {
    var form = event.target && event.target.closest("form[data-checkpoint-form]");
    if (!form) return;
    var requestID = form.querySelector("input[name=request_id]");
    if (!requestID.value) requestID.value = newCheckpointRequestID();
  }, true);
  function rememberAndMarkRows(scope) {
    var current = new Map();
    scope.querySelectorAll("[data-row-id][data-row-version]").forEach(function (row) {
      var id = row.dataset.rowId;
      var version = row.dataset.rowVersion;
      current.set(id, version);
      if (previousRows.has(id) && previousRows.get(id) !== version) row.classList.add("state-changed");
      if (!previousRows.has(id) && previousRows.size) row.classList.add("state-changed");
    });
    current.forEach(function (version, id) { previousRows.set(id, version); });
  }

  function configureInspector(scope) {
    var inspector = scope.matches && scope.matches("[data-inspector]") ? scope :
      (scope.querySelector ? scope.querySelector("[data-inspector]") : null);
    var host = document.getElementById("inspector-host");
    var shell = document.querySelector(".app-shell");
    var hasInspector = !!document.querySelector("#inspector-host [data-inspector], #inspector-host .error-state");
    if (host) host.classList.toggle("has-inspector", hasInspector);
    if (shell) shell.classList.toggle("has-inspector", hasInspector);
    if (!inspector) return;
    if (!inspectorOpener) {
      inspectorOpener = document.querySelector(".task-row.selected .row-link, .memory-card.selected > a");
      inspectorOpenerHref = inspectorOpener ? inspectorOpener.getAttribute("href") || "" : "";
    }
    var narrow = window.matchMedia && window.matchMedia("(max-width: 760px)").matches;
    inspector.setAttribute("aria-modal", narrow ? "true" : "false");
    if (narrow && !inspector.contains(document.activeElement)) {
      var focusable = inspector.querySelector("button, [href], input, select, textarea, [tabindex]:not([tabindex='-1'])");
      if (focusable) focusable.focus({preventScroll: true});
    }
  }

  var inspectorMedia = window.matchMedia ? window.matchMedia("(max-width: 760px)") : null;
  if (inspectorMedia) inspectorMedia.addEventListener("change", function () { configureInspector(document); });

  function initialize(scope) {
    applyTheme(root.dataset.themeChoice || "system");
    rememberAndMarkRows(scope);
    synchronizeCheckpointSelection();
    configureInspector(scope);
  }
  initialize(document);
  function afterPatch(scope) {
    initialize(scope);
    if (sortOpenerID) {
      var sorter = document.getElementById(sortOpenerID);
      if (sorter) sorter.focus({preventScroll: true});
      sortOpenerID = "";
    }

    if (document.querySelector("#inspector-host [data-inspector], #inspector-host .error-state") || (!inspectorOpener && !inspectorOpenerHref)) return;
    var table = document.querySelector(".table-scroll");
    if (table) table.scrollLeft = tableScrollLeft;
    if ((!inspectorOpener || !document.contains(inspectorOpener)) && inspectorOpenerHref) {
      Array.prototype.some.call(document.querySelectorAll(".row-link, .task-title, .memory-card > a"), function (candidate) {
        if (candidate.getAttribute("href") !== inspectorOpenerHref) return false;
        inspectorOpener = candidate;
        return true;
      });
    }
    var focusTarget = inspectorOpener && document.contains(inspectorOpener) ? inspectorOpener :
      document.querySelector("#task-list a, #memory-list a, #main");
    if (focusTarget) focusTarget.focus({preventScroll: true});
    inspectorOpener = null;
    inspectorOpenerHref = "";
  }

  function closeDrawer(restoreFocus) {
    var drawer = document.getElementById("project-drawer");
    if (drawer) {
      drawer.classList.remove("open");
      drawer.setAttribute("role", "navigation");
      drawer.setAttribute("aria-modal", "false");
    }
    var scrim = document.querySelector(".drawer-scrim");
    if (scrim) scrim.hidden = true;
    var toggle = document.querySelector("[data-drawer-toggle]");
    if (toggle) {
      toggle.setAttribute("aria-expanded", "false");
      if (restoreFocus) toggle.focus();
    }
  }

  document.addEventListener("keydown", function (event) {
    var inspector = document.querySelector("[data-inspector]");
    var drawer = document.getElementById("project-drawer");
    if (!inspector && drawer && drawer.classList.contains("open")) {
      if (event.key === "Escape") {
        event.preventDefault();
        closeDrawer(true);
        return;
      }
      if (event.key === "Tab") {
        var drawerFocusable = Array.prototype.slice.call(drawer.querySelectorAll("button:not([disabled]), [href], [tabindex]:not([tabindex='-1'])"));
        if (drawerFocusable.length) {
          var drawerFirst = drawerFocusable[0], drawerLast = drawerFocusable[drawerFocusable.length - 1];
          if (event.shiftKey && document.activeElement === drawerFirst) { event.preventDefault(); drawerLast.focus(); }
          else if (!event.shiftKey && document.activeElement === drawerLast) { event.preventDefault(); drawerFirst.focus(); }
        }
      }
      return;
    }
    if (!inspector) return;
    if (event.key === "Escape") {
      var close = inspector.querySelector("[aria-label='Close inspector']");
      if (close) { event.preventDefault(); close.click(); }
      return;
    }
    if (event.key !== "Tab" || inspector.getAttribute("aria-modal") !== "true") return;
    var focusable = Array.prototype.slice.call(inspector.querySelectorAll("button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex='-1'])"));
    if (!focusable.length) return;
    var first = focusable[0], last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
  });

  document.addEventListener("click", function (event) {
    var toggle = event.target.closest("[data-drawer-toggle]");
    var close = event.target.closest("[data-drawer-close]");
    var drawer = document.getElementById("project-drawer");
    if (!drawer || (!toggle && !close)) return;
    var open = toggle ? !drawer.classList.contains("open") : false;
    drawer.classList.toggle("open", open);
    drawer.setAttribute("role", open ? "dialog" : "navigation");
    drawer.setAttribute("aria-modal", open ? "true" : "false");
    var scrim = document.querySelector(".drawer-scrim");
    if (scrim) scrim.hidden = !open;
    if (toggle) toggle.setAttribute("aria-expanded", open ? "true" : "false");
    if (open) {
      var firstLink = drawer.querySelector("a");
      if (firstLink) firstLink.focus();
    } else {
      closeDrawer(true);
    }
  });

  document.addEventListener("click", function (event) {
    var projectDetails = document.getElementById("project-record");
    if (projectDetails && projectDetails.open && !projectDetails.contains(event.target)) {
      projectDetails.open = false;
    }
  });

  if (window.EventSource) {
    var source = new EventSource("/events");
    source.addEventListener("open", refreshRegions);
    source.addEventListener("pellets-invalidate", function () {
      refreshRegions();
    });
  }
  document.addEventListener("visibilitychange", function () {
    if (!document.hidden) refreshRegions();
  });
}());

document.addEventListener("click", function (event) {
 if (!event.target.closest("[data-create-memory]")) return;
 var form = document.querySelector(".create-popover form[action$='/memories']");
 if (form) { form.closest("details").open = true; form.querySelector("textarea").focus(); }
});
