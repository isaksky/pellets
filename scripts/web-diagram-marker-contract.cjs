const assert = require('node:assert/strict');

// Resolve the actual painted markers, so inline/viewer agreement cannot hide
// both surfaces losing Mermaid's theme rules in the same way.
module.exports = async ({page, view, fresh, ready, screenshot, noDuplicateIDs, source, fence}) => {
  const examples = [
    ['sequence', 'sequenceDiagram\nautonumber\nAgent->>Pellets: Save\nPellets--xAgent: Reply'],
    ['state-v2', 'stateDiagram-v2\n[*] --> Open\nOpen --> Closed\nClosed --> [*]'],
    ['state', 'stateDiagram\n[*] --> Open\nOpen --> Closed\nClosed --> [*]'],
  ];
  const themes = ['gruvbox-light', 'gruvbox-dark', 'light', 'dark', 'icy'];
  await fresh([...examples, ...examples].map(([, text]) => fence(text)).join('\n\n'));
  await ready(view, 6);
  // Matching suffixes outside any diagram must not pick up its generated CSS.
  await page.evaluate(() => {
    const make = name => document.createElementNS('http://www.w3.org/2000/svg', name);
    const svg = make('svg'), defs = make('defs');
    svg.id = 'marker-scope-test'; svg.setAttribute('width', '0'); svg.setAttribute('height', '0');
    svg.setAttribute('aria-hidden', 'true'); svg.append(defs);
    for (const suffix of ['arrowhead', 'crosshead', 'sequencenumber', 'barbEnd']) {
      const marker = make('marker'); marker.id = 'marker-scope-' + suffix;
      marker.setAttribute('fill', '#010203'); marker.setAttribute('stroke', '#040506');
      marker.append(make('path')); defs.append(marker);
    }
    document.body.append(svg);
  });
  const modal = page.getByRole('dialog', {name:'Diagram viewer'});
  const check = async (svg, label) => {
    const result = await svg.evaluate(svg => {
      // Resolve the palette through a native color property to normalize hex
      // tokens to the same computed format as SVG fill/stroke in both engines.
      const sample = document.createElement('span');
      sample.style.color = 'var(--ink)'; document.body.append(sample);
      const ink = getComputedStyle(sample).color; sample.remove();
      const failures = [], markers = new Set();
      for (const node of svg.querySelectorAll('[marker-start],[marker-mid],[marker-end]')) {
        for (const attr of ['marker-start', 'marker-mid', 'marker-end']) {
          const value = node.getAttribute(attr);
          if (!value || value === 'none') continue;
          const id = /^url\(#([\w-]+)\)$/.exec(value)?.[1];
          const marker = id && document.getElementById(id);
          if (!marker || marker.localName !== 'marker' || !svg.contains(marker)) {
            failures.push(`Marker reference escapes its SVG or is unresolved: ${value}`);
            continue;
          }
          markers.add(marker);
        }
      }
      for (const marker of markers) {
        const shapes = [...marker.querySelectorAll('path,circle,polygon,polyline')];
        if (!shapes.length) failures.push(`Marker has no geometry: ${marker.id}`);
        for (const shape of shapes) {
          const styles = getComputedStyle(shape);
          // Sequence-number circles have no outline; arrows and crosses use
          // signal/transition colors for both fill and stroke.
          const stroke = shape.localName === 'circle' ? 'none' : ink;
          if (styles.fill !== ink || styles.stroke !== stroke)
            failures.push(`${marker.id}: fill ${styles.fill}, stroke ${styles.stroke}; expected ${ink}, ${stroke}`);
        }
      }
      return {failures, markers: markers.size};
    });
    assert.ok(result.markers > 0, label + ': exercise rendered marker references');
    assert.deepEqual(result.failures, [], label);
  };
  for (let index = 0; index < examples.length; index++) {
    const [family] = examples[index], diagram = view.locator('pl-diagram').nth(index);
    await diagram.locator('.mermaid-activate').click(); await modal.waitFor();
    await modal.evaluate(node => { window.markerViewer = node; });
    for (const theme of themes) {
      await page.evaluate(theme => window.Workbench.applyTheme(theme), theme);
      await ready(view, 6);
      assert.equal(await modal.evaluate(node => node === window.markerViewer), true, 'Theme change retains the open viewer');
      await noDuplicateIDs();
      for (let repeated = 0; repeated < 6; repeated++)
        await check(view.locator('.mermaid-canvas > svg').nth(repeated), `${theme} inline ${examples[repeated % 3][0]} ${repeated}`);
      await check(modal.locator('svg > svg'), `${theme} viewer ${family}`);
      assert.deepEqual(await page.locator('#marker-scope-test path').evaluateAll(nodes => nodes.map(node => {
        const style = getComputedStyle(node); return [style.fill, style.stroke];
      })), Array.from({length:4}, () => ['rgb(1, 2, 3)', 'rgb(4, 5, 6)']), 'Marker CSS remains scoped to its SVG');
      // Capture both diagram families in the real application at every theme.
      if (index < 2) await screenshot(`markers-viewer-${family}-${theme}.png`);
    }
    await page.keyboard.press('Escape');
    await modal.waitFor({state:'detached'});
  }
  await page.locator('#marker-scope-test').evaluate(node => node.remove());
  await fresh(source); await ready();
  console.log('PASS sequence/state marker theme fill/stroke, open-viewer theme changes, repeated IDs and local references in all five themes');
};
