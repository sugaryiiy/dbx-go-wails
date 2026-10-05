#!/usr/bin/env node
// Fast local .app packaging: Swift shell, web assets and the shared HTTP/MCP
// backend are independent build products. No frontend embedding or Tauri link.
import { spawn, execFile } from "node:child_process";
import { promisify } from "node:util";
import { createHash } from "node:crypto";
import { createReadStream, openSync, closeSync } from "node:fs";
import { access, chmod, copyFile, cp, mkdir, readFile, readdir, rename, rm, stat, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const execute = promisify(execFile);
const root = path.resolve(import.meta.dirname, "..");
const schemaVersion = 1;
const ignoredDirectories = new Set(["node_modules", ".git", "target", "__tests__", ".DS_Store"]);

async function fileDigest(filename) {
  const hash = createHash("sha256");
  for await (const chunk of createReadStream(filename)) hash.update(chunk);
  return hash.digest("hex");
}

/** Content, names, modes and deletions count; mtimes deliberately do not. */
export async function treeDigest(base, entries, { sources = false } = {}) {
  const hash = createHash("sha256");
  async function visit(relative) {
    const filename = path.join(base, relative);
    let metadata;
    try { metadata = await stat(filename); }
    catch (error) {
      if (error.code !== "ENOENT") throw error;
      hash.update(`missing:${relative}\0`);
      return;
    }
    if (metadata.isDirectory()) {
      hash.update(`dir:${relative}\0`);
      for (const entry of (await readdir(filename)).sort()) {
        if (entry === ".DS_Store") continue;
        if (sources && (ignoredDirectories.has(entry) || /\.(test|spec)\.[cm]?[jt]sx?$/.test(entry))) continue;
        await visit(path.join(relative, entry));
      }
    } else if (metadata.isFile()) {
      hash.update(`file:${relative}:${metadata.mode & 0o111}:${await fileDigest(filename)}\0`);
    } else {
      throw new Error(`Unsupported build input: ${relative}`);
    }
  }
  for (const entry of [...entries].sort()) await visit(entry);
  return hash.digest("hex");
}

export function digestConfiguration(value) {
  return createHash("sha256").update(JSON.stringify(value)).digest("hex");
}

async function readJson(filename) {
  try { return JSON.parse(await readFile(filename, "utf8")); }
  catch (error) {
    if (error.code === "ENOENT" || error instanceof SyntaxError) return null;
    throw error;
  }
}

async function writeJson(filename, value) {
  const temporary = `${filename}.${process.pid}.tmp`;
  await writeFile(temporary, `${JSON.stringify(value, null, 2)}\n`);
  await rename(temporary, filename);
}

/** A cache hit requires both identical inputs and intact outputs. */
export async function cachedBuild({ manifestPath, inputs, outputs, build, force = false }) {
  const inputDigest = await inputs();
  const cached = await readJson(manifestPath);
  if (!force && cached?.schemaVersion === schemaVersion && cached.inputDigest === inputDigest
    && cached.outputDigest === await outputs()) return { cached: true, ...cached };
  await build();
  if (await inputs() !== inputDigest) throw new Error("Build inputs changed while building; run the command again.");
  const entry = { schemaVersion, inputDigest, outputDigest: await outputs() };
  await writeJson(manifestPath, entry);
  return { cached: false, ...entry };
}

export function buildEnvironment(source = process.env) {
  const env = { ...source };
  // These change the intended target/profile or select an embedded frontend.
  // Use a dedicated command for other variants; do not accidentally ship them.
  delete env.CARGO_BUILD_TARGET;
  delete env.CARGO_ENCODED_RUSTFLAGS;
  delete env.RUSTFLAGS;
  delete env.CARGO_PROFILE_DEV_OPT_LEVEL;
  delete env.CARGO_PROFILE_DEV_LTO;
  env.CARGO_PROFILE_DEV_DEBUG = "0";
  env.CARGO_BUILD_JOBS ||= "4";
  env.TAURI_DEV_HOST = "";
  env.TAURI_ENV_ARCH = "";
  env.VITE_DBX_BASE_PATH = "";
  env.DBX_PUBLIC_BASE_PATH = "";
  return env;
}

export function frontendEnvironmentDigest(env) {
  return digestConfiguration(Object.fromEntries(Object.entries(env)
    .filter(([key]) => /^(VITE_|TAURI_|DBX_PUBLIC_BASE_PATH$|NODE_ENV$)/.test(key))
    .sort(([a], [b]) => a.localeCompare(b))));
}

function plist(version) {
  const escape = (text) => text.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll('"', "&quot;");
  return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>DBXMCP</string>
<key>CFBundleIdentifier</key><string>local.dbx.mcpdesktop</string>
<key>CFBundleName</key><string>Database Workbench</string>
<key>CFBundleDisplayName</key><string>Database Workbench</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>${escape(version)}</string>
<key>CFBundleVersion</key><string>${escape(version)}</string>
<key>CFBundleIconFile</key><string>app.icns</string>
<key>LSMinimumSystemVersion</key><string>13.0</string>
<key>LSApplicationCategoryType</key><string>public.app-category.developer-tools</string>
<key>NSHighResolutionCapable</key><true/>
<key>NSAppTransportSecurity</key><dict><key>NSAllowsLocalNetworking</key><true/></dict>
</dict></plist>\n`;
}

export async function main(args = process.argv.slice(2)) {
  if (args.includes("--help")) {
    console.log("Usage: node scripts/build-macos-mcp.mjs [--force] [--zip]\nBuilds a native macOS .app using the cached development backend. --zip also creates a distributable local-test archive. --force rechecks/rebuilds each layer without deleting Cargo caches.");
    return;
  }
  if (args.some((arg) => !["--force", "--zip"].includes(arg))) throw new Error("Unknown argument; use --help.");
  if (process.platform !== "darwin") throw new Error("This packager requires macOS and Xcode Command Line Tools.");
  const architecture = process.arch === "arm64" ? "arm64" : process.arch === "x64" ? "x86_64" : null;
  if (!architecture) throw new Error(`Unsupported native architecture: ${process.arch}`);
  const started = performance.now();
  const env = buildEnvironment();
  const targetRoot = path.resolve(root, env.CARGO_TARGET_DIR || "target");
  const outputRoot = path.join(root, "target", "macos-mcp");
  const cache = path.join(outputRoot, "cache");
  const logs = path.join(outputRoot, "logs");
  await mkdir(cache, { recursive: true });
  await mkdir(logs, { recursive: true });
  const lockPath = path.join(outputRoot, "build.lock");
  try { await writeFile(lockPath, String(process.pid), { flag: "wx" }); }
  catch (error) {
    if (error.code !== "EEXIST") throw error;
    const owner = Number(await readFile(lockPath, "utf8"));
    let alive = true;
    try { process.kill(owner, 0); } catch (probe) { if (probe.code === "ESRCH") alive = false; }
    if (!Number.isInteger(owner) || owner < 1 || alive) throw new Error(`Another build owns ${lockPath}.`);
    await rm(lockPath);
    await writeFile(lockPath, String(process.pid), { flag: "wx" });
  }

  let runningChild;
  let interrupted = false;
  const interrupt = () => {
    interrupted = true;
    if (runningChild?.pid) {
      try { process.kill(-runningChild.pid, "SIGTERM"); }
      catch (error) { if (error.code !== "ESRCH") console.error(error.message); }
    }
  };
  process.once("SIGINT", interrupt);
  process.once("SIGTERM", interrupt);
  const run = async (command, commandArgs, logName) => {
    if (interrupted) throw new Error("Packaging interrupted.");
    const logfile = path.join(logs, `${logName}.log`);
    const fd = openSync(logfile, "w");
    try {
      await new Promise((resolve, reject) => {
        const child = spawn(command, commandArgs, { cwd: root, env, detached: true, stdio: ["ignore", fd, fd] });
        runningChild = child;
        child.once("error", reject);
        child.once("exit", (code, signal) => code === 0 ? resolve() : reject(new Error(`${logName} failed (${signal || code}); see ${logfile}`)));
      });
    } finally { runningChild = undefined; closeSync(fd); }
  };
  const capture = async (command, commandArgs) => (await execute(command, commandArgs, { cwd: root, env })).stdout.trim();
  const report = { architecture, profile: "dev (debug=0, cached; no release LTO)", stages: {} };
  const stage = async (name, options) => {
    if (interrupted) throw new Error("Packaging interrupted.");
    console.log(`Checking ${name}…`);
    const start = performance.now();
    const result = await cachedBuild({ manifestPath: path.join(cache, `${name}.json`), force: args.includes("--force"), ...options });
    report.stages[name] = { cached: result.cached, seconds: Number(((performance.now() - start) / 1000).toFixed(2)) };
    console.log(`${result.cached ? "CACHE" : "BUILT"} ${name} · ${report.stages[name].seconds}s`);
    return result;
  };
  try {
    await access(path.join(root, "node_modules/vite/bin/vite.js"));
    await run(process.execPath, ["scripts/sync-connection-types.mjs"], "connection-types");
    const [rust, swift, sdkPath, sdkVersion, clang] = await Promise.all([
      capture("rustc", ["-vV"]), capture("xcrun", ["swiftc", "--version"]),
      capture("xcrun", ["--sdk", "macosx", "--show-sdk-path"]),
      capture("xcrun", ["--sdk", "macosx", "--show-sdk-version"]), capture("xcrun", ["clang", "--version"]),
    ]);
    const web = path.join(cache, "web-dist");
    const sourceFingerprint = (entries, configuration) => async () => digestConfiguration({
      tree: await treeDigest(root, entries, { sources: true }), configuration,
    });
    const scriptVersion = await fileDigest(fileURLToPath(import.meta.url));
    const frontend = await stage("frontend", {
      inputs: sourceFingerprint(["apps/desktop", "packages/mongo-shell/src", "plugins/connection-types", "plugins/dialects", "package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", "scripts/sync-connection-types.mjs"], { node: process.version, mode: "web", base: "./", environmentHash: frontendEnvironmentDigest(env), scriptVersion }),
      outputs: () => treeDigest(cache, ["web-dist"]),
      build: () => run(process.execPath, ["node_modules/vite/bin/vite.js", "build", "--config", "apps/desktop/vite.config.ts", "--mode", "web", "--outDir", web], "frontend"),
    });
    const backendFile = path.join(targetRoot, "debug", "dbx-web");
    const cargoArgs = ["build", "--locked", "-p", "dbx-web", "--no-default-features", "--features", "dbx-core/sqlite-bundled,system-fonts"];
    const buildVariables = Object.fromEntries(Object.entries(env).filter(([key]) => /^(CARGO_|RUST|CC|CXX|CFLAGS|CXXFLAGS|AR|SDKROOT|MACOSX_DEPLOYMENT_TARGET|LIBSQLITE3_FLAGS)/.test(key)).sort(([a], [b]) => a.localeCompare(b)));
    const backend = await stage("backend", {
      inputs: sourceFingerprint(["crates", "vendor", "plugins/connection-types", "plugins/dialects", "plugins/sdk/rust", "src-tauri/Cargo.toml", "Cargo.toml", "Cargo.lock", ".cargo"], { rust, clang, sdkVersion, cargoArgs, architecture, environmentHash: digestConfiguration(buildVariables), scriptVersion }),
      outputs: () => treeDigest(targetRoot, ["debug/dbx-web"]),
      build: () => run("cargo", cargoArgs, "backend"),
    });
    const swiftFile = path.join(cache, "DBXMCP");
    const swiftArgs = ["swiftc", "-O", "-target", `${architecture}-apple-macosx13.0`, "-sdk", sdkPath,
      "-framework", "AppKit", "-framework", "WebKit", "-framework", "Security", "apps/macos-mcp/DBXMCPApp.swift", "-o", swiftFile];
    const native = await stage("native", {
      inputs: sourceFingerprint(["apps/macos-mcp/DBXMCPApp.swift"], { swift, sdkVersion, swiftArgs, scriptVersion }),
      outputs: () => treeDigest(cache, ["DBXMCP"]),
      build: () => run("xcrun", swiftArgs, "native"),
    });
    const iconFile = path.join(cache, "workbench.icns");
    const icon = await stage("icon", {
      inputs: sourceFingerprint(["apps/desktop/public/workbench.svg", "scripts/generate-workbench-icon.swift"], { swift, sdkVersion, scriptVersion }),
      outputs: () => treeDigest(cache, ["workbench.icns"]),
      build: () => run("xcrun", ["swift", "scripts/generate-workbench-icon.swift", "apps/desktop/public/workbench.svg", iconFile], "icon"),
    });
    const version = JSON.parse(await readFile(path.join(root, "package.json"), "utf8")).version;
    const appName = "Database Workbench.app";
    const appPath = path.join(outputRoot, appName);
    const bundle = await stage("bundle", {
      inputs: sourceFingerprint(["LICENSE", "NOTICE"], { icon: icon.outputDigest, frontend: frontend.outputDigest, backend: backend.outputDigest, native: native.outputDigest, info: plist(version), scriptVersion }),
      outputs: () => treeDigest(outputRoot, [appName]),
      build: async () => {
        const staging = path.join(outputRoot, `staging-${process.pid}`, appName);
        const resources = path.join(staging, "Contents", "Resources");
        const binaries = path.join(staging, "Contents", "MacOS");
        await mkdir(resources, { recursive: true });
        await mkdir(binaries, { recursive: true });
        try {
          await copyFile(backendFile, path.join(resources, "dbx-web"));
          await copyFile(swiftFile, path.join(binaries, "DBXMCP"));
          await chmod(path.join(resources, "dbx-web"), 0o755);
          await chmod(path.join(binaries, "DBXMCP"), 0o755);
          await copyFile(iconFile, path.join(resources, "app.icns"));
          await copyFile(path.join(root, "LICENSE"), path.join(resources, "LICENSE"));
          try { await copyFile(path.join(root, "NOTICE"), path.join(resources, "NOTICE")); }
          catch (error) { if (error.code !== "ENOENT") throw error; }
          await cp(web, path.join(resources, "web-dist"), { recursive: true });
          await writeFile(path.join(staging, "Contents", "Info.plist"), plist(version));
          await writeJson(path.join(resources, "build-manifest.json"), { version, architecture, profile: report.profile, frontend: frontend.outputDigest, backend: backend.outputDigest, native: native.outputDigest });
          await run("/usr/bin/strip", ["-S", "-x", path.join(resources, "dbx-web")], "strip");
          await run("/usr/bin/codesign", ["--force", "--sign", "-", "--timestamp=none", path.join(resources, "dbx-web")], "sign-backend");
          await run("/usr/bin/codesign", ["--force", "--sign", "-", "--timestamp=none", staging], "sign-app");
          await run("/usr/bin/codesign", ["--verify", "--deep", "--strict", staging], "verify-signature");
          if (interrupted) throw new Error("Packaging interrupted before publication.");
          // A failed build never removes the last good app. Publish only after validation.
          const old = path.join(outputRoot, `previous-${process.pid}.app`);
          let replaced = false;
          try { await rename(appPath, old); replaced = true; } catch (error) { if (error.code !== "ENOENT") throw error; }
          try { await rename(staging, appPath); }
          catch (error) { if (replaced) await rename(old, appPath); throw error; }
          if (replaced) await rm(old, { recursive: true, force: true });
        } finally { await rm(path.dirname(staging), { recursive: true, force: true }); }
      },
    });
    if (args.includes("--zip")) {
      const archiveName = `Database-Workbench-macos-${process.arch}.zip`;
      await stage("archive", {
        inputs: async () => bundle.outputDigest,
        outputs: () => treeDigest(outputRoot, [archiveName]),
        build: () => run("/usr/bin/ditto", ["-c", "-k", "--keepParent", appPath, path.join(outputRoot, archiveName)], "archive"),
      });
      report.archive = path.join(outputRoot, archiveName);
    }
    report.app = appPath;
    report.totalSeconds = Number(((performance.now() - started) / 1000).toFixed(2));
    await writeJson(path.join(outputRoot, "build-report.json"), report);
    console.log(`\nReady: ${appPath}\nTotal: ${report.totalSeconds}s · ${architecture} · local ad-hoc signature\nBuild report: ${path.join(outputRoot, "build-report.json")}`);
  } finally {
    process.removeListener("SIGINT", interrupt);
    process.removeListener("SIGTERM", interrupt);
    await rm(lockPath, { force: true });
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => { console.error(error.message); process.exitCode = 1; });
}
