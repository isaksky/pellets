// Keep native numeric validation and keyboard support; theme only the buttons.
// Delegation also handles controls replaced by streamed page updates.
document.addEventListener("click", (event) => {
  const button = event.target.closest("button[data-number-step]");
  if (!button) return;
  const input = button.closest(".number-control")?.querySelector('input[type="number"]');
  if (!input || input.matches(":disabled") || input.readOnly || button.disabled) return;
  const previous = input.value;
  if (button.dataset.numberStep === "up") input.stepUp();
  else input.stepDown();
  input.focus({ preventScroll: true });
  if (input.value !== previous) {
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new Event("change", { bubbles: true }));
  }
});
