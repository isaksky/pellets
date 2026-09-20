// npm ci --prefix scripts/mermaid --ignore-scripts
// node scripts/vendor-mermaid.mjs [dependency-directory]
// Dependency installation/build tooling is not required to build or run Pellets.
import fs from "node:fs";
import path from "node:path";
import {createRequire} from "node:module";
import {fileURLToPath} from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const dependencies = path.resolve(process.argv[2] || path.join(root, "scripts/mermaid"));
const require = createRequire(path.join(dependencies, "package.json"));
const {build} = require("esbuild");
const entry = require.resolve("mermaid");
const assets = path.join(root, "internal/webui/assets");
function replaceOnce(text, before, after) {
  if (text.split(before).length !== 2) throw Error("Mermaid CSP patch no longer matches: " + before);
  return text.replace(before, after);
}
const result = await build({
  entryPoints: [entry], bundle: true, format: "esm", minify: true,
  target: ["es2022"], legalComments: "inline", metafile: true,
  outfile: path.join(assets, "mermaid-11.17.2.js"),
  plugins: [{name: "pellets-csp", setup(build) {
    build.onLoad({filter: /mermaid\.core\.mjs$/}, async ({path: filename}) => {
      let contents = fs.readFileSync(filename, "utf8");
      // Ship only the three registered diagram families. No lazy network chunks.
      const registrations = contents.match(/  if \(true\) \{\n    registerLazyLoadedDiagrams\([^;]+;\n  \}\n  registerLazyLoadedDiagrams\([\s\S]+?\n  \);/);
      if (!registrations) throw Error("Mermaid diagram registrations changed");
      contents = replaceOnce(contents, registrations[0], "  registerLazyLoadedDiagrams(sequenceDetector_default, flowDetector_v2_default, flowDetector_default, stateDetector_V2_default, stateDetector_default);");
      // Mermaid's generated CSS is trusted only after our source policy rejects
      // author configuration/CSS. Constructed sheets preserve the existing CSP;
      // never introduce unsafe-inline, a nonce, or a generic HTML insertion hook.
      contents = replaceOnce(contents, '  const style1 = document.createElement("style");\n  style1.innerHTML = rules;\n  svg.insertBefore(style1, firstChild);',
        '  const sheet = new CSSStyleSheet();\n  sheet.replaceSync(rules);\n  document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];\n  try {');
      contents = replaceOnce(contents, '    svg: svgCode,', '    svg: svgCode,\n    css: rules,');
      // Strip inline style before DOMPurify/DOMParser reparses serialized SVG.
      // Retain only inert presentation properties as native SVG attributes.
      contents = replaceOnce(contents, '    let code = root.select(enclosingDivID_selector).node().innerHTML;', `    svgNode.selectAll("[style]").nodes().concat(svgNode.node()).forEach(node => {
      for (const property of ["text-anchor", "dominant-baseline", "font-size", "font-weight", "stroke-width", "stroke-dasharray", "fill", "stroke", "opacity"]) {
        const value = node.style.getPropertyValue(property);
        if (value) node.setAttribute(property, value);
      }
      node.removeAttribute("style");
    });
    let code = root.select(enclosingDivID_selector).node().innerHTML;`);
      contents = replaceOnce(contents, '    bindFunctions: diag.db.bindFunctions\n  };\n}, "render");', '    bindFunctions: diag.db.bindFunctions\n  };\n  } finally { document.adoptedStyleSheets = document.adoptedStyleSheets.filter(value => value !== sheet); }\n}, "render");');
      return {contents, loader: "js"};
    });
    build.onLoad({filter: /d3-selection\/src\/selection\/attr\.js$/}, async ({path: filename}) => {
      let contents = fs.readFileSync(filename, "utf8");
      // Trusted renderer assignments use the style API, not blocked inline
      // attribute parsing. Author CSS never reaches this path.
      contents = replaceOnce(contents, '    this.setAttribute(name, value);', '    if (name === "style") this.style.cssText = value;\n    else this.setAttribute(name, value);');
      contents = replaceOnce(contents, '    else this.setAttribute(name, v);', '    else if (name === "style") this.style.cssText = v;\n    else this.setAttribute(name, v);');
      return {contents, loader: "js"};
    });
  }}],
});

// Include complete notices for every package that contributes bundled code.
const packages = new Set();
for (const input of Object.keys(result.metafile.inputs)) {
  let dir = path.dirname(path.resolve(input));
  while (dir !== path.dirname(dir)) {
    const manifest = path.join(dir, "package.json");
    if (fs.existsSync(manifest) && JSON.parse(fs.readFileSync(manifest)).name) break;
    dir = path.dirname(dir);
  }
  packages.add(dir);
}
let notices = "Locally bundled Mermaid runtime. Full upstream notices follow.\n";
for (const dir of [...packages].sort()) {
  const pkg = JSON.parse(fs.readFileSync(path.join(dir, "package.json")));
  const licenses = fs.readdirSync(dir).filter(name => /^(licen[sc]e|copying|notice)(\.|$)/i.test(name));
  // fastdom's npm tarball puts its complete MIT notice in README.md.
  if (!licenses.length && pkg.name === "fastdom") licenses.push("README.md");
  if (!licenses.length) throw Error("Missing license for " + pkg.name);
  notices += `\n--- ${pkg.name} ${pkg.version} (${pkg.license}) ---\n`;
  for (const name of licenses) {
    const text = fs.readFileSync(path.join(dir, name), "utf8");
    notices += (name === "README.md" ? text.slice(text.lastIndexOf("## License")) : text) + "\n";
  }
}
fs.writeFileSync(path.join(assets, "MERMAID-LICENSES.txt"), notices);
console.log(`Bundled Mermaid with ${packages.size} dependency notices.`);
