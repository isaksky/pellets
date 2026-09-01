(function () {
  "use strict";

  var root = document.documentElement;
  var media = window.matchMedia ? window.matchMedia("(prefers-color-scheme: dark)") : null;

  // Keep HTTP error semantics while rendering the two application responses
  // that intentionally contain actionable inspector fragments. Every other
  // client/server error retains HTMX's safe no-swap default.
  if (window.htmx) {
    window.htmx.config.responseHandling = [
      {code: "204", swap: false},
      {code: "409", swap: true, error: true},
      {code: "422", swap: true, error: true},
      {code: "[23]..", swap: true},
      {code: "[45]..", swap: false, error: true}
    ];
  }

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
  document.body.addEventListener("htmx:pushedIntoHistory", function () {
    currentHistoryIndex += 1;
    history.replaceState(Object.assign({}, history.state || {}, {[historyIndexKey]: currentHistoryIndex}), "", location.href);
  });
  var replayingHistory = false;
  window.addEventListener("popstate", function (event) {
    var targetIndex = event.state && Number.isInteger(event.state[historyIndexKey]) ? event.state[historyIndexKey] : currentHistoryIndex - 1;
    if (replayingHistory) { replayingHistory = false; currentHistoryIndex = targetIndex; return; }
    if (!dirtyInspector() || window.confirm("Discard unsaved inspector changes?")) { currentHistoryIndex = targetIndex; return; }
    event.stopImmediatePropagation();
    replayingHistory = true;
    history.go(targetIndex < currentHistoryIndex ? 1 : -1);
  }, true);

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

  document.body.addEventListener("htmx:beforeRequest", function (event) {
    if (event.detail.elt && ((event.detail.elt.id === "project-drawer" && event.detail.elt.classList.contains("open")) || (event.detail.elt.id === "project-record" && event.detail.elt.open))) {
      event.preventDefault();
      return;
    }
    var region = event.detail.elt && event.detail.elt.closest("[data-protect-dirty]");
    if (region && region.classList.contains("is-dirty") && !event.detail.elt.closest("form.dirty-track")) {
      // Never interrupt the user for an automatic refresh. An explicit close
      // or secondary action may proceed after one discard confirmation.
      if (event.detail.elt === region || !confirmDiscard()) event.preventDefault();
      return;
    }
    var target = event.detail.target;
    if (target && target.id === "inspector-host" && dirtyInspector() && !event.detail.elt.closest("form") && !confirmDiscard()) {
      event.preventDefault();
    }
  });
  // A refresh can begin while a disclosure is closed and finish after the
  // user opens it. Guard the swap as well as the request so that an in-flight
  // response cannot discard the open drawer/details state or strand the
  // drawer scrim over the page.
  document.body.addEventListener("htmx:beforeSwap", function (event) {
    var target = event.detail && event.detail.target;
    if (!target) return;
    if ((target.id === "project-drawer" && target.classList.contains("open")) ||
        (target.id === "project-record" && target.open)) {
      event.preventDefault();
    }
  });
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
    var hasInspector = !!document.querySelector("[data-inspector], .error-state");
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
  document.body.addEventListener("htmx:afterSwap", function (event) {
    initialize(event.detail.target || document);
    if (sortOpenerID) {
      var sorter = document.getElementById(sortOpenerID);
      if (sorter) sorter.focus({preventScroll: true});
      sortOpenerID = "";
    }
  });
  document.body.addEventListener("htmx:afterSwap", function () {
    if (document.querySelector("[data-inspector], .error-state") || (!inspectorOpener && !inspectorOpenerHref)) return;
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
  });
  document.body.addEventListener("htmx:historyRestore", function () { initialize(document); });
  document.body.addEventListener("htmx:afterRequest", function (event) {
    if (event.detail.successful && event.detail.elt && event.detail.elt.matches("form.dirty-track")) {
      event.detail.elt.dataset.dirty = "false";
    }
  });

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
    source.addEventListener("pellets-invalidate", function () {
      document.body.dispatchEvent(new CustomEvent("pellets:refresh", {bubbles: true}));
    });
  }
}());
