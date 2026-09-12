(function () {
  "use strict";
  var choice="gruvbox-light", panels={};
  try { choice=localStorage.getItem("pellets-theme")||choice; panels=JSON.parse(localStorage.getItem("pellets-panels")||"{}"); } catch (_) {}
  if(choice==="system") choice=matchMedia("(prefers-color-scheme: dark)").matches?"dark":"light";
  if(!["gruvbox-light","gruvbox-dark","light","dark","icy"].includes(choice)) choice="gruvbox-light";
  document.documentElement.dataset.themeChoice=choice;
  document.documentElement.dataset.theme=choice;
  for(var key of ["navigation","execution"]) document.documentElement.classList.toggle(key+"-collapsed",panels[key]===false);
}());
