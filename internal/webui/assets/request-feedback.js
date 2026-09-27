// Keep mutation feedback with the invoking form, including forms whose save
// buttons live in a modal footer. A body-level notice is inert behind dialogs.
export function requestFeedback(form) {
  var feedback = document.getElementById("request-feedback");
  if (!feedback) {
    feedback = document.createElement("div");
    feedback.id = "request-feedback";
    feedback.setAttribute("role", "status");
    feedback.setAttribute("aria-live", "polite");
    feedback.hidden = true;
  }
  var footer = form && (form.querySelector(".pellet-create-footer") || form.closest("[data-inspector]")?.querySelector(".dialog-footer"));
  var parent = footer || (form && form.closest(".run-controls")) || form || document.body;
  feedback.toggleAttribute("data-inline", !!form);
  if (feedback.parentElement !== parent) {
    if (footer) parent.prepend(feedback);
    else parent.append(feedback);
  }
  return feedback;
}
