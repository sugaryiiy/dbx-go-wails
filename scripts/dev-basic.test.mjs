import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, stat } from "node:fs/promises";
import { createServer } from "node:net";
import { createServer as httpServer } from "node:http";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";
import { basicConfig, childEnvironment, localPassword, requireAvailablePort, waitForBackend } from "./dev-basic.mjs";

test("local workbench isolates its files and keeps all listeners on loopback", () => {
  const root = path.join(os.tmpdir(), "dbx-test-project");
  const inherited = { DBX_DATA_DIR: "/existing-data", DBX_BIND_ADDR: "0.0.0.0", DBX_DISABLE_PASSWORD: "1", DBX_DEMO_MODE: "1" };
  const config = basicConfig(root, inherited);
  const env = childEnvironment(config, "local-test-password", inherited);
  assert.equal(env.DBX_DATA_DIR, path.join(root, ".dbx-dev/basic"));
  assert.equal(env.DBX_BIND_ADDR, "127.0.0.1");
  assert.equal(env.DBX_DISABLE_PASSWORD, "0");
  assert.equal(env.DBX_DEMO_MODE, "0");
  assert.equal(inherited.DBX_DATA_DIR, "/existing-data");
});

test("custom ports are validated before starting child processes", () => {
  for (const value of ["abc", "0", "-1", "65536", "12.5"]) {
    assert.throws(() => basicConfig("/tmp", { DBX_BASIC_PORT: value }));
  }
  assert.throws(() => basicConfig("/tmp", { DBX_BASIC_PORT: "4224" }), /different/);
  assert.equal(basicConfig("/tmp", { DBX_BASIC_PORT: "5180" }).frontendUrl, "http://127.0.0.1:5180");
});

test("generated login password persists across restarts and is not overwritten", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "dbx-basic-runner-"));
  try {
    const config = basicConfig(root, {});
    const first = await localPassword(config);
    const second = await localPassword(config);
    assert.ok(first.length >= 24);
    assert.equal(first, second);
    assert.equal(await localPassword(config, "explicit-test-password"), "explicit-test-password");
    assert.equal((await readFile(config.passwordFile, "utf8")).trim(), first);
    if (process.platform !== "win32") assert.equal((await stat(config.passwordFile)).mode & 0o777, 0o600);
    await assert.rejects(localPassword(config, "  "), /empty/);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("occupied backend port fails instead of connecting to another DBX instance", async () => {
  const server = createServer();
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    await assert.rejects(requireAvailablePort(server.address().port), /unavailable/);
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});

test("readiness accepts the authenticated DBX API contract", async () => {
  const server = httpServer((req, res) => {
    assert.equal(req.url, "/api/auth/check");
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify({ authenticated: false, required: true }));
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    await waitForBackend(`http://127.0.0.1:${server.address().port}`, new AbortController().signal, 2000);
  } finally {
    await new Promise((resolve) => server.close(resolve));
  }
});

test("readiness is cancellable while Cargo is still starting", async () => {
  const controller = new AbortController();
  controller.abort();
  await assert.rejects(waitForBackend("http://127.0.0.1:1", controller.signal), { name: "AbortError" });
});
