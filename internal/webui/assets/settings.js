import * as uiVersion from './ui-version.js';

// Serialize writes so rapid choices cannot arrive at SQLite out of order.
// Only initial migration uses insert-if-absent; later explicit choices win.
const root = document.documentElement;
const initial = JSON.parse(root.dataset.settings || '{}');
const pending = new Map();
let running = false;
let notice;
export function saveSetting(key, value, onlyIfUnset = false) {
  pending.set(key, {key, value, only_if_unset: onlyIfUnset});
  void flush();
}
async function flush() {
  if (running || uiVersion.isOutdated()) return;
  running = true;
  try {
    while (pending.size) {
      const [key, input] = pending.entries().next().value;
      const response = await fetch('/settings', {
        method: 'POST', credentials: 'same-origin',
        headers: uiVersion.headers({'Content-Type': 'application/json'}),
        body: JSON.stringify({...input, _csrf: root.dataset.csrf}),
      });
      if (!uiVersion.inspectResponse(response)) return;
      if (!response.ok) throw new Error('Could not save presentation settings.');
      if (pending.get(key) === input) pending.delete(key);
    }
    notice?.remove(); notice = null;
  } catch (_) {
    if (!notice) {
      notice = document.createElement('div');
      notice.className = 'settings-save-notice'; notice.setAttribute('role', 'alert');
      notice.append('Settings are not saved. ');
      const retry = document.createElement('button'); retry.type = 'button'; retry.textContent = 'Retry';
      retry.addEventListener('click', () => void flush()); notice.append(retry); document.body.append(notice);
    }
  } finally { running = false; }
}
// Import existing browser preferences once per database, never overwriting a
// preference already saved by another tab or browser.
window.addEventListener('DOMContentLoaded', () => {
  let tab = 'execution';
  try { if (localStorage.getItem('pellets-right-panel-tab') === 'plan') tab = 'plan'; } catch (_) {}
  const defaults = {
    theme: root.dataset.themeChoice,
    navigation_visible: String(!root.classList.contains('navigation-collapsed')),
    execution_visible: String(!root.classList.contains('execution-collapsed')),
    right_panel_tab: root.dataset.rightPanelTab || tab,
  };
  for (const [key, value] of Object.entries(defaults)) if (!(key in initial) && !pending.has(key)) saveSetting(key, value, true);
});
