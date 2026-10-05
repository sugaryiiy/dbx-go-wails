import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";
import { buildEnvironment, cachedBuild, frontendEnvironmentDigest, treeDigest } from "./build-macos-mcp.mjs";

async function fixture(testBody) {
  const directory = await mkdtemp(path.join(os.tmpdir(), "dbx-macos-cache-"));
  try { await testBody(directory); }
  finally { await rm(directory, { recursive: true, force: true }); }
}

test("unchanged source reuses output; content edits and missing outputs rebuild", () => fixture(async (base) => {
  await writeFile(path.join(base, "source"), "first");
  let builds = 0;
  const options = {
    manifestPath: path.join(base, "manifest.json"),
    inputs: () => treeDigest(base, ["source"]),
    outputs: () => treeDigest(base, ["output"]),
    build: async () => { builds++; await writeFile(path.join(base, "output"), await readFile(path.join(base, "source"))); },
  };
  assert.equal((await cachedBuild(options)).cached, false);
  assert.equal((await cachedBuild(options)).cached, true);
  assert.equal(builds, 1);
  await writeFile(path.join(base, "source"), "second");
  assert.equal((await cachedBuild(options)).cached, false);
  await rm(path.join(base, "output"));
  assert.equal((await cachedBuild(options)).cached, false);
  assert.equal(builds, 3);
  assert.equal(await readFile(path.join(base, "output"), "utf8"), "second");
}));

test("tampered output and malformed cache cannot silently ship a stale binary", () => fixture(async (base) => {
  let builds = 0;
  const options = {
    manifestPath: path.join(base, "manifest.json"), inputs: async () => "same-source",
    outputs: () => treeDigest(base, ["output"]),
    build: async () => { builds++; await writeFile(path.join(base, "output"), "valid"); },
  };
  await cachedBuild(options);
  await writeFile(path.join(base, "output"), "modified");
  assert.equal((await cachedBuild(options)).cached, false);
  await writeFile(options.manifestPath, "not-json");
  assert.equal((await cachedBuild(options)).cached, false);
  assert.equal(builds, 3);
}));

test("frontend-only edits leave the backend cache intact", () => fixture(async (base) => {
  let frontendBuilds = 0;
  let backendBuilds = 0;
  await writeFile(path.join(base, "ui.vue"), "initial");
  await writeFile(path.join(base, "server.rs"), "stable");
  const options = (name, source, onBuild) => ({
    manifestPath: path.join(base, `${name}.json`), inputs: () => treeDigest(base, [source]),
    outputs: () => treeDigest(base, [`${name}.out`]),
    build: async () => { onBuild(); await writeFile(path.join(base, `${name}.out`), await readFile(path.join(base, source))); },
  });
  const frontend = options("frontend", "ui.vue", () => frontendBuilds++);
  const backend = options("backend", "server.rs", () => backendBuilds++);
  await cachedBuild(frontend); await cachedBuild(backend);
  await writeFile(path.join(base, "ui.vue"), "new interface");
  await cachedBuild(frontend); await cachedBuild(backend);
  assert.equal(frontendBuilds, 2);
  assert.equal(backendBuilds, 1);
}));

test("failed or concurrently edited builds are never marked reusable", () => fixture(async (base) => {
  const manifestPath = path.join(base, "manifest.json");
  const options = { manifestPath, inputs: async () => "initial", outputs: async () => "result" };
  await assert.rejects(cachedBuild({ ...options, build: async () => { throw new Error("compiler failed"); } }), /compiler failed/);
  await assert.rejects(readFile(manifestPath), { code: "ENOENT" });
  let input = "before";
  await assert.rejects(cachedBuild({ ...options, inputs: async () => input, build: async () => { input = "edited during build"; } }), /inputs changed/);
  await assert.rejects(readFile(manifestPath), { code: "ENOENT" });
}));

test("force rebuild bypasses a valid cache without deleting compiler artifacts", () => fixture(async (base) => {
  let builds = 0;
  await writeFile(path.join(base, "compiler-cache"), "preserve");
  const options = { manifestPath: path.join(base, "manifest.json"), inputs: async () => "source", outputs: async () => "out", build: async () => { builds++; } };
  await cachedBuild(options);
  await cachedBuild({ ...options, force: true });
  assert.equal(builds, 2);
  assert.equal(await readFile(path.join(base, "compiler-cache"), "utf8"), "preserve");
}));

test("fast packaging cannot inherit a cross target or frontend context path", () => {
  const inherited = { CARGO_BUILD_TARGET: "x86_64-pc-windows-msvc", RUSTFLAGS: "-C lto=yes", TAURI_DEV_HOST: "other-host", VITE_DBX_BASE_PATH: "/other-app/", CARGO_PROFILE_DEV_DEBUG: "2" };
  const result = buildEnvironment(inherited);
  assert.equal(result.CARGO_BUILD_TARGET, undefined);
  assert.equal(result.RUSTFLAGS, undefined);
  assert.equal(result.CARGO_PROFILE_DEV_DEBUG, "0");
  assert.equal(result.VITE_DBX_BASE_PATH, "");
  assert.equal(result.TAURI_DEV_HOST, "");
  assert.equal(inherited.CARGO_PROFILE_DEV_DEBUG, "2");
});

test("changing a Vite compile-time option rebuilds frontend with unchanged source", () => fixture(async (base) => {
  let env = buildEnvironment({ VITE_DBX_RESULT_CACHE_BACKEND: "http" });
  let builds = 0;
  const options = {
    manifestPath: path.join(base, "frontend.json"),
    inputs: async () => frontendEnvironmentDigest(env),
    outputs: () => treeDigest(base, ["frontend.out"]),
    build: async () => { builds++; await writeFile(path.join(base, "frontend.out"), env.VITE_DBX_RESULT_CACHE_BACKEND || "default"); },
  };
  await cachedBuild(options);
  env = buildEnvironment({});
  assert.equal((await cachedBuild(options)).cached, false);
  assert.equal(builds, 2);
  assert.equal(await readFile(path.join(base, "frontend.out"), "utf8"), "default");
}));
