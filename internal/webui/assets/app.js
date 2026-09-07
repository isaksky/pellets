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
  document.addEventListener("click", function (event) {
    var sorter = event.target.closest(".task-sort");
    if (sorter) sortOpenerID = sorter.id;
    var opener = event.target.closest(".row-link, .memory-card > a");
    if (!opener) return;
    inspectorOpener = opener;
    inspectorOpenerHref = opener.getAttribute("href") || "";
    var table = opener.closest(".table-scroll");
    tableScrollLeft = table ? table.scrollLeft : 0;
  });

  // These small application actions delegate transport and DOM patches to Datastar.
  // Request ownership is per destination, so an old poll cannot replace a newer
  // navigation and independent fragments at the same URL do not cancel each other.
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
    var target = document.getElementById(targetID);
    if (!target || (automatic && (document.hidden || protectedTarget(target)))) return;
    if (automatic && targetID === "inspector-host" && target.querySelector(".conflict-state, .error-state")) return;
    var editing = dirtyInspector();
    if (targetID === "inspector-host" && editing) {
      if (automatic) return;
      if (!el.matches("form.dirty-track") && !confirmDiscard()) return;
    }
    // Do not let a poll interrupt a mutation/navigation or submit the same form twice.
    var previous = pending.get(targetID);
    if (previous && (automatic || (mutation && previous.mutation))) return;
    if (mutation && !el.checkValidity()) { el.reportValidity(); return; }
    if (previous) previous.controller.abort();
    if (!automatic) routeRevision += 1;
    var state = {el: el, targetID: targetID, automatic: automatic, mutation: mutation,
      revision: editRevision, route: routeRevision, controller: new AbortController(), applied: false};
    pending.set(targetID, state);
    requests.set(el, state);
    var url = automatic ? el.dataset.refreshUrl : mutation || kind === "filter" ? el.action : el.href;
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
    var options = {headers: {"Pellets-Target": targetID}, requestCancellation: state.controller,
      openWhenHidden: true, retry: "never", retryMaxCount: 0, filterSignals: {include: /^$/}};
    if (mutation || kind === "filter") options.contentType = "form";
    try {
      await actions[mutation ? "post" : "get"](ctx, url, options);
    } finally {
      if (pending.get(targetID) === state) pending.delete(targetID);
      if (requests.get(el) === state) requests.delete(el);
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
      var target = document.getElementById(state.targetID);
      if (state.controller.signal.aborted || pending.get(state.targetID) !== state || state.route !== routeRevision ||
          !state.el.isConnected || (state.automatic && protectedTarget(target)) ||
          (state.targetID === "inspector-host" && ((state.automatic && dirtyInspector()) || state.revision !== editRevision))) {
        event.stopImmediatePropagation();
        return;
      }
      state.applied = true;
      queueMicrotask(function () { afterPatch(document.getElementById(state.targetID) || document); });
    } else if (detail.type === "datastar-patch-signals" && state.applied) {
      var result = JSON.parse(detail.argsRaw.signals)._webResult;
      if (!result) return;
      if (!state.automatic && result.status < 400 && result.url) {
        var next = new URL(result.url, location.href);
        if (next.origin === location.origin && next.pathname + next.search !== location.pathname + location.search) {
          currentHistoryIndex += 1;
          history.pushState({[historyIndexKey]: currentHistoryIndex}, "", next.pathname + next.search);
        }
        document.querySelectorAll("[data-refresh-url]").forEach(function (region) {
          region.dataset.refreshUrl = next.pathname + next.search;
        });
      }
      if (result.refresh || (!state.automatic && result.status < 400)) refreshRegions();
    }
  }, true);

  window.addEventListener("beforeunload", function (event) {
    if (!dirtyInspector()) return;
    event.preventDefault();
    event.returnValue = "";
  });

  var previousRows = new Map();
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
    if (narrow) {
      var focusable = inspector.querySelector("button, [href], input, select, textarea, [tabindex]:not([tabindex='-1'])");
      if (focusable) focusable.focus({preventScroll: true});
    }
  }

  var inspectorMedia = window.matchMedia ? window.matchMedia("(max-width: 760px)") : null;
  if (inspectorMedia) inspectorMedia.addEventListener("change", function () { configureInspector(document); });

  function initialize(scope) {
    applyTheme(root.dataset.themeChoice || "system");
    rememberAndMarkRows(scope);
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
      Array.prototype.some.call(document.querySelectorAll(".row-link, .memory-card > a"), function (candidate) {
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
