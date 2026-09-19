// Shared by the development-gallery browser suite in Chromium and WebKit.
const assert = require('node:assert/strict');

module.exports = async function checkComponentContracts(page) {
  await page.evaluate(() => {
    const fixture = document.createElement('section');
    fixture.id = 'contract-fixture';
    fixture.innerHTML = `
      <pl-button id="contract-button" disabled><button type="button">Action</button></pl-button>
      <pl-button id="contract-link"><a href="#inputs" tabindex="2">Link</a></pl-button>
      <fieldset id="contract-fieldset" disabled><pl-field><input aria-label="Disabled field"></pl-field><pl-select><select aria-label="Disabled choice"><option>One</option></select></pl-select></fieldset>
      <label><span id="contract-label">Inline label</span><pl-select id="contract-select"><select id="contract-choice"><option value="one">One</option><option value="two">Two</option></select></pl-select></label>
      <pl-menu id="contract-menu"><details><summary>Menu</summary><div role="menu"><button type="button" role="menuitem">First</button><button type="button" role="menuitem" disabled>Disabled</button><button type="button" role="menuitem" hidden>Hidden</button><button type="button" role="menuitem">Last</button></div></details></pl-menu>`;
    document.body.prepend(fixture);
  });
  try {
    assert.deepEqual(await page.evaluate(() => {
      const action = document.getElementById('contract-button');
      action.setAttribute('busy', '');
      action.disabled = false;
      action.removeAttribute('busy');
      const enabledAfterBusy = !action.disabled;
      action.control.disabled = true;
      action.setAttribute('busy', '');
      action.removeAttribute('busy');
      const nativeDisabledPreserved = action.disabled;
      const link = document.getElementById('contract-link');
      let activations = 0;
      link.control.addEventListener('click', event => { event.preventDefault(); activations++; });
      link.setAttribute('busy', '');
      link.control.click();
      const disabledActivations = activations;
      link.removeAttribute('busy');
      const restoredLink = link.control.tabIndex === 2 && !link.control.hasAttribute('aria-disabled') && !link.control.hasAttribute('aria-busy');
      link.control.click();
      return {enabledAfterBusy, nativeDisabledPreserved, disabledActivations, restoredLink, activations};
    }), {enabledAfterBusy: true, nativeDisabledPreserved: true, disabledActivations: 0, restoredLink: true, activations: 1});

    assert.deepEqual(await page.evaluate(() => {
      const fieldset = document.getElementById('contract-fieldset');
      const field = fieldset.querySelector('pl-field'), select = fieldset.querySelector('pl-select');
      const disabled = field.disabled && select.disabled;
      select.open();
      const noMenu = !document.querySelector('.select-popover');
      fieldset.disabled = false;
      const enabled = !field.disabled && !select.disabled && !select.querySelector('button').matches(':disabled');
      return {disabled, noMenu, enabled};
    }), {disabled: true, noMenu: true, enabled: true});

    await page.locator('#contract-label').click();
    assert.equal(await page.locator('#contract-choice-trigger').getAttribute('aria-label'), 'Inline label', 'Labels may contain nested text markup');
    assert.equal(await page.locator('#contract-choice-trigger').evaluate(el => document.activeElement === el), true, 'An implicit label must focus the visible trigger');
    await page.locator('#contract-choice-trigger').click();
    await page.evaluate(() => { document.querySelector('#contract-choice option[value=two]').hidden = true; });
    await page.locator('#contract-choice-listbox').waitFor({state: 'detached'});
    assert.equal(await page.locator('#contract-choice-trigger').evaluate(el => document.activeElement === el), true, 'Updating open options must keep keyboard focus');
    await page.locator('#contract-choice-trigger').click();
    assert.equal(await page.locator('#contract-choice-listbox [role=option]').count(), 1);
    await page.keyboard.press('Escape');
    await page.evaluate(() => { document.querySelector('#contract-choice option').firstChild.data = 'Renamed option'; });
    await page.waitForFunction(() => document.getElementById('contract-choice-trigger').textContent.includes('Renamed option'));
    await page.locator('#contract-choice-trigger').click();
    await page.evaluate(() => {
      document.getElementById('contract-select').innerHTML = '<select id="contract-replacement" aria-label="Replacement"><option>Replacement</option></select>';
    });
    await page.locator('#contract-choice-listbox').waitFor({state: 'detached'});
    await page.locator('#contract-replacement-trigger').waitFor();
    assert.equal(await page.locator('#contract-select .select-trigger').count(), 1);
    await page.evaluate(() => { document.getElementById('contract-replacement').id = 'contract-renamed'; });
    await page.locator('#contract-renamed-trigger').waitFor();
    await page.locator('#contract-renamed-trigger').click();
    assert.equal(await page.locator('#contract-renamed-trigger').getAttribute('aria-controls'), 'contract-renamed-listbox');
    await page.keyboard.press('Escape');

    const summary = page.locator('#contract-menu summary');
    await summary.focus();
    await page.keyboard.press('ArrowUp');
    assert.equal(await page.getByRole('menuitem', {name: 'Last', exact: true}).evaluate(el => el === document.activeElement), true, 'ArrowUp from the menu summary must select the last enabled item');
    await page.keyboard.press('ArrowDown');
    assert.equal(await page.getByRole('menuitem', {name: 'First', exact: true}).evaluate(el => el === document.activeElement), true);
    await page.evaluate(() => {
      const first = document.querySelector('#contract-menu [role=menuitem]');
      first.addEventListener('click', () => { first.dataset.activated = 'true'; });
    });
    await page.keyboard.press('Space');
    assert.equal(await page.getByRole('menuitem', {name: 'First', exact: true}).getAttribute('data-activated'), 'true', 'Space must activate a menu action');
    await page.keyboard.press('Escape');
    assert.equal(await summary.evaluate(el => el === document.activeElement && !el.parentElement.open), true);

    assert.deepEqual(await page.evaluate(() => {
      const fixture = document.getElementById('contract-fixture');
      const form = document.createElement('form');
      form.innerHTML = '<label>First choice<pl-select><select required><option value="">Choose</option><option>One</option></select></pl-select></label><label>Second choice<pl-select><select required><option value="">Choose</option><option>Two</option></select></pl-select></label>';
      fixture.append(form);
      form.reportValidity();
      return {firstInvalidFocused: document.activeElement === form.querySelector('.select-trigger')};
    }), {firstInvalidFocused: true});

    assert.deepEqual(await page.evaluate(() => {
      const resizer = document.createElement('pl-resizer');
      resizer.setAttribute('min', '100');
      resizer.setAttribute('max', '200');
      resizer.setAttribute('value', '300');
      resizer.innerHTML = '<div role="separator" tabindex="0" aria-label="Contract resize"></div>';
      document.getElementById('contract-fixture').append(resizer);
      const bounded = resizer.value === 200 && resizer.control.getAttribute('aria-valuenow') === '200';
      resizer.value = NaN;
      const finite = resizer.value === 100;
      resizer.remove();
      const tabs = document.querySelector('pl-tabs');
      const selected = tabs.querySelector('[aria-selected=true]');
      tabs.select(document.getElementById('contract-link').control);
      return {bounded, finite, foreignTabIgnored: selected.getAttribute('aria-selected') === 'true'};
    }), {bounded: true, finite: true, foreignTabIgnored: true});
  } finally {
    await page.evaluate(() => document.getElementById('contract-fixture')?.remove());
  }
};
