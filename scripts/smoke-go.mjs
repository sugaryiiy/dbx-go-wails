#!/usr/bin/env node
// Build and test a disposable Go server. Never use an existing DBX data directory.
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../", import.meta.url));
const temporary = await mkdtemp(join(tmpdir(), "dbx-go-smoke-"));
const binary = join(temporary, process.platform === "win32" ? "dbx-web.exe" : "dbx-web");
const data = join(temporary, "data");
let backend;
let backendDone;

function run(command, args, env = process.env) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd: root, env, stdio: "inherit" });
    child.once("error", reject);
    child.once("exit", (code, signal) => code === 0 ? resolve() : reject(new Error(`${command} exited (${code ?? signal})`)));
  });
}

try {
  await run("go", ["build", "-mod=mod", "-o", binary, "./cmd/dbx-web"]);
  const base = await new Promise((resolve, reject) => {
    let log = "";
    const timer = setTimeout(() => reject(new Error("Disposable Go server did not start")), 30_000);
    backend = spawn(binary, ["--bind=127.0.0.1", "--port=0", `--data-dir=${data}`], {
      cwd: root,
      env: { ...process.env, DBX_PASSWORD: "" },
      stdio: ["ignore", "ignore", "pipe"],
    });
    backendDone = new Promise((done) => backend.once("exit", done));
    backend.once("error", (error) => { clearTimeout(timer); reject(error); });
    backend.once("exit", (code) => { clearTimeout(timer); reject(new Error(`Disposable Go server exited: ${code}`)); });
    backend.stderr.on("data", (chunk) => {
      log = (log + chunk.toString()).slice(-8192);
      const ready = log.match(/DBX Go backend ready at (http:\/\/127\.0\.0\.1:\d+);/);
      if (ready) { clearTimeout(timer); resolve(ready[1]); }
    });
  });
  await run(process.execPath, ["scripts/smoke-basic-workbench.mjs"], {
    ...process.env,
    DBX_SMOKE_ISOLATED: "1",
    DBX_SMOKE_BASE_URL: base,
    DBX_SMOKE_PASSWORD: "",
    DBX_SMOKE_PASSWORD_FILE: join(data, "login-password"),
  });
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
} finally {
  if (backend?.pid) {
    backend.kill("SIGTERM");
    const timer = setTimeout(() => backend.kill("SIGKILL"), 5000);
    await backendDone;
    clearTimeout(timer);
  }
  await rm(temporary, { recursive: true, force: true });
}
