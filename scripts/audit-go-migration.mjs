import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../", import.meta.url));
const normalize = (route) => route.replace(/\{[^}]+\}|:[A-Za-z_]+/g, "{parameter}").replace(/\/$/, "");
async function files(directory) {
  const result = [];
  for (const entry of await fs.readdir(directory, { withFileTypes: true })) {
    const full = path.join(directory, entry.name);
    if (entry.isDirectory()) result.push(...await files(full));
    else if (entry.name.endsWith(".go") && !entry.name.endsWith("_test.go")) result.push(full);
  }
  return result;
}
const legacy = await fs.readFile(path.join(root, "crates/dbx-web/src/main.rs"), "utf8");
const apiSource=legacy.slice(legacy.indexOf("let api = Router::new()"),legacy.indexOf("let mut app = Router::new()"));
const routes = [...new Set([...apiSource.matchAll(/\.route\s*\(\s*"([^"\n]+)"/g)].map((match) => normalize(`/api${match[1]}`)))].sort();
const sources = await Promise.all((await files(path.join(root, "internal"))).map(async (file) => ({ file: path.relative(root, file), text: await fs.readFile(file, "utf8") })));
const declarations = new Map();
for (const { file, text } of sources) {
  for (const match of text.matchAll(/"(\/api\/[^"\n]*)"/g)) {
    const route = normalize(match[1]);
    if (!declarations.has(route)) declarations.set(route, []);
    declarations.get(route).push(file);
  }
  // Schema handlers select the suffix after resolving the live SQL pool.
  if (/schema(?:_advanced)?\.go$/.test(file)) {
    for (const match of text.matchAll(/case\s+((?:"[^"\n]+"(?:\s*,\s*)?)+)\s*:/g)) {
      for (const value of match[1].matchAll(/"([^"\n]+)"/g)) {
        if (["sqlite", "mysql", "postgres"].includes(value[1])) continue;
        const route = normalize(`/api/schema/${value[1]}`);
        if (!declarations.has(route)) declarations.set(route, []);
        declarations.get(route).push(file);
      }
    }
  }
}
const inventory = routes.map((route) => ({ route, declarationFound: declarations.has(route), goFiles: [...new Set(declarations.get(route) ?? [])] }));
const report = {
  note: "Static interface inventory only. A declaration does not prove implementation, database capability, safety, frontend parity or acceptance. Dynamic families require manual review.",
  legacyRouteCount: inventory.length,
  declarationFoundCount: inventory.filter((item) => item.declarationFound).length,
  declarationMissingCount: inventory.filter((item) => !item.declarationFound).length,
  inventory,
};
if (process.argv.includes("--write")) await fs.writeFile(path.join(root, "docs/go-migration-inventory.json"), `${JSON.stringify(report, null, 2)}\n`);
console.log(JSON.stringify({ ...report, inventory: undefined }, null, 2));
if (process.argv.includes("--missing")) for (const item of inventory) if (!item.declarationFound) console.log(item.route);
