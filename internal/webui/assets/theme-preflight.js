(function () {
  "use strict";
  var choice = "system";
  try { choice = localStorage.getItem("pellets-theme") || "system"; } catch (_) {}
  if (choice !== "light" && choice !== "dark") choice = "system";
  var dark = window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;
  document.documentElement.dataset.themeChoice = choice;
  document.documentElement.dataset.theme = choice === "system" ? (dark ? "dark" : "light") : choice;
}());
