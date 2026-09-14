(function () {
  "use strict";
  var choice="gruvbox-light", panels={};
  try { choice=localStorage.getItem("pellets-theme")||choice; panels=JSON.parse(localStorage.getItem("pellets-panels")||"{}"); } catch (_) {}
  var settings = JSON.parse(document.documentElement.dataset.settings || "{}");
  choice = settings.theme || choice;
  for(var key of ["navigation","execution"]) if(settings[key+"_visible"] !== undefined) panels[key] = settings[key+"_visible"] === "true";
  document.documentElement.dataset.rightPanelTab = settings.right_panel_tab || "";
  if(choice==="system") choice=matchMedia("(prefers-color-scheme: dark)").matches?"dark":"light";
  if(!["gruvbox-light","gruvbox-dark","light","dark","icy"].includes(choice)) choice="gruvbox-light";
  document.documentElement.dataset.themeChoice=choice;
  document.documentElement.dataset.theme=choice;
  for(var key of ["navigation","execution"]) document.documentElement.classList.toggle(key+"-collapsed",panels[key]===false);
}());
