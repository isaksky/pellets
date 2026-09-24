// Real navigation consumers, using the runtime runner's isolated production server.
// PELLETS_NAVIGATION_AUDIT=/absolute/evidence; PELLETS_NAVIGATION_BASELINE=/path/to/pl
const assert = require('node:assert/strict');
const fs = require('node:fs'), path = require('node:path');
module.exports = async ({page, baseline, state}) => {
  const engine = process.env.PLAYWRIGHT_BROWSER || 'chromium';
  const phase = baseline ? 'before' : 'after';
  const output = process.env.PELLETS_NAVIGATION_AUDIT;
  const check = (condition, message) => {if (!baseline) assert.ok(condition, message);};
  const originalSize = page.viewportSize();
  for (const theme of ['gruvbox-light','gruvbox-dark','light','dark','icy']) {
    await page.locator('#theme-select').selectOption(theme, {force:true});
    await page.waitForFunction(theme => document.documentElement.dataset.themeChoice === theme, theme);
    for (const width of [1280,1092,390]) {
      await page.setViewportSize({width,height:900});
      const nav = page.locator('.workspace-link').first();
      const directory = path.join(output, `${engine}-${state.replace(/[^a-z]+/gi,'-')}-${theme}-${width}`);
      fs.mkdirSync(directory,{recursive:true});
      const capture = async (name, selector) => {
        await page.mouse.move(0,0);
        await page.evaluate(() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))));
        const dir = path.join(directory,name);fs.mkdirSync(dir,{recursive:true});
        let clip = await page.locator(selector).boundingBox();
        const old = path.join(dir,'before.json');
        if (!baseline && fs.existsSync(old)) clip = JSON.parse(fs.readFileSync(old)).clip;
        await page.screenshot({path:path.join(dir,phase+'.png'),clip,animations:'disabled',caret:'hide'});
        fs.writeFileSync(path.join(dir,phase+'.json'),JSON.stringify({engine,theme,viewport:page.viewportSize(),scale:1,state,selector,clip,scroll:[0,0]},null,2));
      };
      check((await nav.getAttribute('aria-label') || '').includes(state),'Sidebar accessible name must describe current execution: '+state);
      if(width>600) check((await nav.innerText()).includes({'Waiting for work':'Waiting','Failed · needs attention':'Failed'}[state] || state),'Desktop sidebar must display current execution: '+state);
      check(await nav.locator('.workspace-dot').evaluate(e=>e.classList.contains('busy')) === (state==='Working'),'Navigation dot must represent live working state');
      if (!baseline) {
        const status = nav.locator('.workspace-execution-state');
        assert.equal(await status.getAttribute('title'),state);
        assert.equal(await nav.evaluate(e=>e.scrollWidth<=e.clientWidth+1),true,'Workspace link fits the pane');
      }
      await page.keyboard.press("Tab");await nav.focus();
      assert.equal(await nav.evaluate(e=>document.activeElement===e && getComputedStyle(e).outlineStyle!=='none'),true,'Workspace link retains keyboard focus');
      await capture('sidebar','#project-drawer');
      const projectPath = page.locator('.sidebar-bottom code');
      check(await projectPath.textContent()==='Database folder','Root project identifies its relative base');
      const locationTitle = await projectPath.getAttribute('title');
      check(locationTitle.startsWith('Repository location: ') && path.isAbsolute(locationTitle.slice('Repository location: '.length)),'Full useful project path remains available');
      const summary = page.locator('#view-switcher > summary');
      if(width===390) await summary.tap(); else {await summary.focus();await summary.press('Enter');}
      await page.waitForFunction(()=>document.querySelector('#view-switcher').open);
      const entry = page.locator('#view-switcher .switcher-menu a').last();
      check((await entry.innerText()).includes(state),'Workspace menu must describe current execution: '+state);
      await capture('view-menu','#view-switcher .switcher-menu');
      await summary.focus();await page.keyboard.press('Escape');
      assert.equal(await summary.evaluate(e=>document.activeElement===e),true,'Menu Escape returns focus');
      console.log(`PASS navigation ${engine}/${theme}/${width}/${state}${baseline?' (baseline capture)':''}`);
    }
  }
  await page.setViewportSize(originalSize);
};
