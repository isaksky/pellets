const assert = require('node:assert/strict');

// Run inside the real record dialog, with its dirty draft and live updates.
module.exports = async ({page, view, field, fresh, ready, refresh, screenshot, noDuplicateIDs, source, fence, engine}) => {
  const modal = page.getByRole('dialog', {name:'Diagram viewer'});
  const canvas = modal.getByRole('region', {name:'Diagram canvas'});
  const zoom = async () => Number((await modal.locator('output').textContent()).replace('%',''));
  const bounds = () => canvas.evaluate(node => {
    const rect = node.getBoundingClientRect(), svg = node.querySelector('svg > svg'), box = svg.viewBox.baseVal;
    // SVG getBoundingClientRect measures painted geometry, excluding Mermaid's
    // own padding. Map the full vector viewport through the browser's CTM.
    const first = new DOMPoint(box.x,box.y).matrixTransform(svg.getScreenCTM());
    const last = new DOMPoint(box.x+box.width,box.y+box.height).matrixTransform(svg.getScreenCTM());
    return {left:first.x-rect.left-node.clientLeft, top:first.y-rect.top-node.clientTop,
      width:last.x-first.x, height:last.y-first.y, availableWidth:node.clientWidth, availableHeight:node.clientHeight};
  });
  const trigger = view.locator('.mermaid-canvas').first();
  const open = async key => {
    await trigger.scrollIntoViewIfNeeded();
    await trigger.focus(); await page.keyboard.press(key);
    await modal.waitFor();
  };
  // Both keyboard keys activate the diagram itself. Native Tab stays modal.
  for (const key of ['Enter', 'Space']) {
    await open(key);
    for (let i=0;i<10;i++) {
      await page.keyboard.press('Tab');
      // Native dialogs may cycle through browser chrome (activeElement=body),
      // but must never place focus on an inert background control.
      assert.equal(await modal.evaluate(node => node.contains(document.activeElement) || document.activeElement === document.body), true,
        await page.evaluate(() => document.activeElement.outerHTML.slice(0,400)));
    }
    await canvas.focus();
    await field.evaluate(node => node.focus());
    assert.equal(await canvas.evaluate(node => node === document.activeElement),true);
    await page.keyboard.press('Escape');
    await modal.waitFor({state:'detached'});
    assert.equal(await trigger.evaluate(node => node === document.activeElement), true);
    assert.equal(await page.locator('#record-dialog').evaluate(node => node.open), true);
  }
  // Accessibility labels, source, unique IDs/markers and cancelable activation.
  const sequence = view.locator('.mermaid-canvas').nth(1);
  assert.equal(await view.getByRole('button',{name:'Open diagram viewer: Local sequence',exact:true}).count(),1);
  assert.equal(await view.getByRole('button',{name:'View larger: Local sequence',exact:true}).count(),1);
  await sequence.click(); await noDuplicateIDs();
  assert.equal(await modal.getByRole('img', {name:'Local sequence'}).count(), 1);
  await modal.locator('summary').click();
  assert.match(await modal.locator('pre').textContent(), /participant Pellets/);
  assert.equal(await modal.evaluate(node => [...node.querySelectorAll('[aria-labelledby],[aria-describedby]')]
    .every(el => ['aria-labelledby','aria-describedby'].every(attr => !el.hasAttribute(attr) ||
      el.getAttribute(attr).split(/\s+/).every(id => !!document.getElementById(id))))), true);
  await page.keyboard.press('Escape');
  await trigger.evaluate(node => node.closest('pl-diagram').addEventListener('pellets-diagram-activate', event => event.preventDefault(), {once:true}));
  await trigger.click(); assert.equal(await modal.count(), 0);

  const large = direction => 'flowchart ' + direction + '\naccTitle: Large diagram\naccDescr: All eighteen stages can be inspected.\n' +
    Array.from({length:17}, (_,i) => `N${i}[Stage ${i}] --> N${i+1}[Stage ${i+1}]`).join('\n');
  const draft = 'Reading before diagram.\n\n'.repeat(30) + fence(large('LR')) + '\n\nUnsaved diagram draft.\n\n' + 'Reading position.\n\n'.repeat(30);
  await fresh(draft); await ready(view,1);
  await trigger.scrollIntoViewIfNeeded();
  const scroll = () => page.evaluate(() => [...document.querySelectorAll('.markdown-body,.inspector-scroll,html,body')].map(node => [node.scrollLeft,node.scrollTop]));
  const beforeScroll = await scroll();
  assert.ok(beforeScroll.some(([,top]) => top > 0),'Exercise a nonzero description reading position');
  await trigger.click();
  let fitZoom = await zoom();
  assert.ok(fitZoom < 100 && fitZoom > 0);
  let box = await bounds();
  assert.ok(box.left >= 23 && box.width <= box.availableWidth - 46, JSON.stringify(box));
  await modal.getByRole('button', {name:'Zoom in',exact:true}).click(); assert.ok(await zoom() > fitZoom);
  await modal.getByRole('button', {name:'Zoom out',exact:true}).click(); assert.ok(Math.abs(await zoom()-fitZoom) < .2);
  await modal.getByRole('button', {name:'Reset zoom to 100%'}).click(); assert.equal(await zoom(),100);
  // Pointer-anchored zoom preserves the same vector coordinate below the mouse.
  box = await bounds();
  const canvasBox = await canvas.boundingBox(), px = canvasBox.width * .68, py = canvasBox.height / 2;
  const vectorX = (px - 1 - box.left) / box.width;
  await page.mouse.move(canvasBox.x + px, canvasBox.y + py); await page.mouse.wheel(0,-120);
  await page.waitForTimeout(100);
  const after = await bounds();
  assert.ok(Math.abs((px - 1 - after.left) / after.width - vectorX) < .002, 'Wheel moved the anchor');
  assert.ok(await zoom() > 100);
  assert.deepEqual(await scroll(),beforeScroll);
  // Drag beyond both pan bounds. Every node at either extreme remains reachable.
  const drag = async dx => {
    await page.mouse.move(canvasBox.x + 100, canvasBox.y + canvasBox.height/2);
    await page.mouse.down(); await page.mouse.move(canvasBox.x + 100 + dx, canvasBox.y + canvasBox.height/2,{steps:8}); await page.mouse.up();
  };
  await drag(10000); box = await bounds(); assert.ok(Math.abs(box.left-24)<1, JSON.stringify(box));
  await drag(-10000); box = await bounds(); assert.ok(Math.abs(box.left+box.width-box.availableWidth+24)<1, JSON.stringify(box));
  assert.equal(await page.locator('#record-dialog').evaluate(node => node.open),true);
  await canvas.focus();
  const left = box.left; await page.keyboard.press('ArrowLeft'); assert.ok((await bounds()).left > left);
  await page.keyboard.press('0'); assert.equal(await zoom(),100);
  await page.keyboard.press('+'); assert.equal(await zoom(),125);
  await page.keyboard.press('-'); assert.equal(await zoom(),100);
  await page.keyboard.press('f'); assert.ok(Math.abs(await zoom()-fitZoom)<.2);
  await refresh();
  await page.keyboard.press('Escape'); await modal.waitFor({state:'detached'});
  assert.deepEqual(await scroll(),beforeScroll,'Closing must restore the exact reading position');
  assert.equal(await field.inputValue(),draft,'A live refresh must retain the dirty source');
  await open('Enter'); await canvas.focus();
  // Clamp both zoom limits; fit remains available at the extremes.
  for(let i=0;i<35;i++) await page.keyboard.press('+');
  assert.equal(await zoom(),800); assert.equal(await modal.getByRole('button',{name:'Zoom in',exact:true}).isDisabled(),true);
  for(let i=0;i<45;i++) await page.keyboard.press('-');
  assert.equal(await zoom(),.1); assert.equal(await modal.getByRole('button',{name:'Zoom out',exact:true}).isDisabled(),true);
  await page.keyboard.press('f');
  await page.setViewportSize({width:678,height:700}); await page.waitForTimeout(100);
  assert.ok(await zoom() < fitZoom, 'Fit should follow viewport resize');
  await modal.getByRole('button',{name:'Reset zoom to 100%'}).click();
  await page.setViewportSize({width:390,height:740}); await page.waitForTimeout(100);
  assert.equal(await zoom(),100,'An intentional zoom survives resize');
  await modal.getByRole('button',{name:'Fit diagram to view'}).click();
  await screenshot('viewer-large-phone.png');
  // Zoom shortcuts outside the canvas retain browser accessibility behavior.
  assert.equal(await modal.getByRole('button',{name:'Close diagram viewer'}).evaluate(node =>
    node.dispatchEvent(new KeyboardEvent('keydown',{key:'+',ctrlKey:true,bubbles:true,cancelable:true}))),true);
  assert.equal(await canvas.evaluate(node =>
    node.dispatchEvent(new KeyboardEvent('keydown',{key:'+',metaKey:true,bubbles:true,cancelable:true}))),true);
  assert.equal(await canvas.evaluate(node =>
    node.dispatchEvent(new WheelEvent('wheel',{deltaX:100,bubbles:true,cancelable:true}))),false,'Horizontal wheel gestures must stay inside the canvas');
  assert.equal(await modal.getByRole('button',{name:'Close diagram viewer'}).evaluate(node =>
    node.dispatchEvent(new WheelEvent('wheel',{deltaY:10,ctrlKey:true,bubbles:true,cancelable:true}))),true,'Browser pinch outside the canvas must remain available');
  // Theme rerenders replace the exact vector/style without resetting the view.
  await modal.getByRole('button',{name:'Reset zoom to 100%'}).click();
  await page.evaluate(() => window.Workbench.applyTheme('dark')); await ready(view,1);
  assert.equal(await zoom(),100); await noDuplicateIDs();
  assert.equal(await modal.locator('svg svg text').first().evaluate(node => getComputedStyle(node).fill),
    await view.locator('svg text').first().evaluate(node => getComputedStyle(node).fill));
  await page.keyboard.press('Escape');
  assert.equal(await field.inputValue(),draft);
  assert.equal(await trigger.evaluate(node => node === document.activeElement),true);
  await page.setViewportSize({width:1280,height:850});
  // Tall diagrams can reach top and bottom using the keyboard alone.
  await fresh(fence(large('TD'))); await ready(view,1); await open('Enter');
  await canvas.focus(); await page.keyboard.press('0');
  for(let i=0;i<50;i++) await page.keyboard.press('Shift+ArrowDown');
  box = await bounds(); assert.ok(Math.abs(box.top+box.height-box.availableHeight+24)<1, JSON.stringify(box));
  for(let i=0;i<50;i++) await page.keyboard.press('Shift+ArrowUp');
  assert.ok(Math.abs((await bounds()).top-24)<1);
  await page.keyboard.press('Escape');
  // Source changes/removal close the exact viewer and release its stylesheet.
  for(let i=0;i<3;i++) {
    await fresh(fence(large('LR'))); await ready(view,1); await open('Enter');
    await view.evaluate((node,i) => {
      const host = node.querySelector('pl-diagram');
      if(i === 0) { host.source = 'flowchart TD\nA[Changed source]-->B'; host.schedule(); }
      else host.remove();
    },i);
    assert.equal(await modal.count(),0);
    assert.equal(await page.locator('#record-dialog').evaluate(node => node.open),true);
    await fresh(source + '\nCycle '+i); await ready();
    assert.equal(await page.evaluate(() => document.adoptedStyleSheets.length),3);
  }
  // Native touch input at phone width in Chromium; desktop WebKit also gets
  // coverage for its separate trackpad GestureEvent path below.
  await page.setViewportSize({width:390,height:740});
  await fresh(fence(large('LR'))); await ready(view,1); await open('Enter');
  await modal.getByRole('button',{name:'Reset zoom to 100%'}).click();
  if (engine.name() === 'chromium') {
    const session = await page.context().newCDPSession(page);
    const rect = await canvas.boundingBox(), cx = rect.x+rect.width/2, cy = rect.y+rect.height/2;
    const touch = (type, points) => session.send('Input.dispatchTouchEvent',{type,touchPoints:points.map(([id,x,y]) => ({id,x,y}))});
    await touch('touchStart',[[0,cx-50,cy],[1,cx+50,cy]]);
    await touch('touchMove',[[0,cx-100,cy],[1,cx+100,cy]]);
    await touch('touchEnd',[]); await page.waitForTimeout(50);
    assert.ok(await zoom() > 150,'Native touch pinch must zoom');
    const before = await bounds();
    await touch('touchStart',[[0,cx,cy]]); await touch('touchMove',[[0,cx+50,cy+20]]); await touch('touchEnd',[]);
    assert.ok((await bounds()).left > before.left,'Native touch drag must pan');
    await session.detach();
  }
  const gestureZoom = await zoom();
  await canvas.evaluate(node => {
    const rect = node.getBoundingClientRect();
    for (const [type,scale] of [['gesturestart',1],['gesturechange',1.2],['gestureend',1.2]]) {
      const event = new Event(type,{bubbles:true,cancelable:true});
      Object.assign(event,{scale,clientX:rect.x+rect.width/2,clientY:rect.y+rect.height/2});
      if (node.dispatchEvent(event)) throw Error('Canvas gesture was not contained');
    }
  });
  assert.ok(Math.abs(await zoom()-gestureZoom*1.2)<.2,'Trackpad gestures must change zoom');
  // Dispatch on a real captured mouse pointer to test cancel/restart in both engines.
  const rect = await canvas.boundingBox();
  await page.mouse.move(rect.x+100,rect.y+100); await page.mouse.down();
  await canvas.dispatchEvent('pointercancel',{pointerId:1,pointerType:'mouse'}); await page.mouse.up();
  await page.keyboard.press('Escape');
  await page.setViewportSize({width:1280,height:850});
  await fresh(source); await ready();
  assert.equal(await page.evaluate(() => document.adoptedStyleSheets.length),3);
  console.log('PASS diagram viewer keyboard/click, modal focus, vector/ARIA IDs, zoom anchors/limits, drag/pan bounds, fit/reset/resize, theme refresh, drafts, removal and touch');
};
