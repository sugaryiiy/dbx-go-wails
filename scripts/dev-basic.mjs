import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { mkdir, readFile, writeFile, access } from "node:fs/promises";
import { createServer } from "node:net";
import path from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath } from "node:url";

const repositoryRoot = path.resolve(import.meta.dirname, "..");

function port(value, fallback, name) {
  const parsed = value === undefined || value === "" ? fallback : Number(value);
  if (!Number.isInteger(parsed) || parsed < 1 || parsed > 65535) {
    throw new Error(`${name} must be an integer between 1 and 65535.`);
  }
  return parsed;
}

export function basicConfig(root = repositoryRoot, env = process.env) {
  const backendPort = port(env.DBX_BASIC_BACKEND_PORT, 4224, "DBX_BASIC_BACKEND_PORT");
  const frontendPort = port(env.DBX_BASIC_PORT, 5173, "DBX_BASIC_PORT");
  if (backendPort === frontendPort) throw new Error("Frontend and backend ports must be different.");
  const dataDir = path.resolve(root, env.DBX_BASIC_DATA_DIR || ".dbx-dev/basic");
  return {
    root,
    backendPort,
    frontendPort,
    dataDir,
    passwordFile: path.join(dataDir, "login-password"),
    backendUrl: `http://127.0.0.1:${backendPort}`,
    frontendUrl: `http://127.0.0.1:${frontendPort}`,
  };
}

export async function localPassword(config, suppliedPassword) {
  await mkdir(config.dataDir, { recursive: true, mode: 0o700 });
  if (suppliedPassword !== undefined) {
    if (!suppliedPassword.trim()) throw new Error("DBX_PASSWORD must not be empty.");
    return suppliedPassword;
  }
  try {
    await writeFile(config.passwordFile, `${randomBytes(18).toString("base64url")}\n`, {
      flag: "wx",
      mode: 0o600,
    });
  } catch (error) {
    if (error.code !== "EEXIST") throw error;
  }
  const password = (await readFile(config.passwordFile, "utf8")).trim();
  if (!password) throw new Error(`The local password file is empty: ${config.passwordFile}`);
  return password;
}

export function childEnvironment(config, password, env = process.env) {
  return {
    ...env,
    DBX_DATA_DIR: config.dataDir,
    DBX_BACKUP_ROOT: path.join(config.dataDir, "backups"),
    DBX_BIND_ADDR: "127.0.0.1",
    DBX_PORT: String(config.backendPort),
    DBX_BACKEND_URL: config.backendUrl,
    DBX_PASSWORD: password,
    DBX_DISABLE_PASSWORD: "0",
    DBX_DEMO_MODE: "0",
    DBX_PUBLIC_BASE_PATH: "",
    VITE_DBX_BASE_PATH: "",
    TAURI_DEV_HOST: "",
    TAURI_ENV_ARCH: "",
    RUST_LOG: env.RUST_LOG || "dbx_web=info",
  };
}

export async function requireAvailablePort(listenPort) {
  await new Promise((resolve, reject) => {
    const server = createServer();
    server.once("error", (error) => {
      reject(new Error(`Port ${listenPort} is unavailable (${error.code}). Change DBX_BASIC_PORT or DBX_BASIC_BACKEND_PORT.`));
    });
    server.listen({ host: "127.0.0.1", port: listenPort, exclusive: true }, () => server.close(resolve));
  });
}

export async function waitForBackend(baseUrl, signal, timeoutMs = 30 * 60 * 1000) {
  const started = Date.now();
  while (!signal.aborted && Date.now() - started < timeoutMs) {
    try {
      const response = await fetch(`${baseUrl}/api/auth/check`, {
        signal: AbortSignal.any([signal, AbortSignal.timeout(1500)]),
      });
      if (response.ok) {
        const result = await response.json();
        if (typeof result.authenticated === "boolean" && result.required === true) return;
      }
    } catch {
      // Cargo may still be compiling. Child exit and signals abort this wait.
    }
    await delay(Math.min(500, Math.max(1, timeoutMs - (Date.now() - started))), undefined, { signal });
  }
  signal.throwIfAborted();
  throw new Error("The DBX backend did not become ready. Check the Cargo output above.");
}

function launch(command, args, env, cwd) {
  const child = spawn(command, args, { cwd, env, stdio: "inherit", detached: process.platform !== "win32" });
  const completion = new Promise((resolve) => {
    child.once("error", (error) => resolve({ code: 1, error }));
    child.once("exit", (code, signal) => resolve({ code: code ?? (signal ? 1 : 0) }));
  });
  return { child, completion };
}

function terminate(child, signal) {
  if (!child.pid) return;
  try {
    if (process.platform === "win32") {
      // Cargo launches dbx-web as a child; ending cargo.exe alone leaks it.
      const killer = spawn("taskkill", ["/PID", String(child.pid), "/T", ...(signal === "SIGKILL" ? ["/F"] : [])], { stdio: "ignore" });
      killer.once("error", (error) => console.error(`Could not stop the development process tree: ${error.message}`));
    }
    else process.kill(-child.pid, signal);
  } catch (error) {
    if (error.code !== "ESRCH") console.error(error.message);
  }
}

export async function main() {
  const config = basicConfig();
  await access(path.join(config.root, "node_modules/vite/bin/vite.js"));
  await Promise.all([requireAvailablePort(config.backendPort), requireAvailablePort(config.frontendPort)]);
  const password = await localPassword(config, process.env.DBX_PASSWORD);
  const env = childEnvironment(config, password);
  const controller = new AbortController();
  const children = [];
  let requestedStop = false;
  let stopTimer;
  function stop() {
    controller.abort();
    for (const entry of children) terminate(entry.child, "SIGTERM");
    stopTimer ??= setTimeout(() => {
      for (const entry of children) terminate(entry.child, "SIGKILL");
    }, 5000);
    stopTimer.unref();
  }
  function onSignal() {
    requestedStop = true;
    stop();
  }
  process.once("SIGINT", onSignal);
  process.once("SIGTERM", onSignal);
  console.log(`DBX basic workbench · data: ${config.dataDir}`);
  console.log("Starting the web frontend and Rust backend. The first compilation can take several minutes.");
  try {
    children.push(launch("cargo", ["run", "-p", "dbx-web", "--no-default-features", "--features", "dbx-core/sqlite-bundled,system-fonts"], env, config.root));
    children.push(launch(process.execPath, ["node_modules/vite/bin/vite.js", "--config", "apps/desktop/vite.config.ts", "--mode", "web", "--host", "127.0.0.1", "--port", String(config.frontendPort), "--strictPort"], env, config.root));
    const firstExit = Promise.race(children.map((entry) => entry.completion));
    const ready = await Promise.race([
      waitForBackend(config.backendUrl, controller.signal).then(() => ({ ready: true })),
      firstExit,
    ]);
    if (!ready.ready) {
      if (ready.error) console.error(ready.error.message);
      return requestedStop ? 0 : (ready.code || 1);
    }
    console.log(`\nOpen ${config.frontendUrl}`);
    console.log(process.env.DBX_PASSWORD !== undefined
      ? "Log in using the DBX_PASSWORD you supplied."
      : `Login password is saved in ${config.passwordFile}`);
    console.log("Choose Open sample database on the welcome screen to try the complete basic workflow.");
    const result = await firstExit;
    if (result.error) console.error(result.error.message);
    return requestedStop ? 0 : (result.code || 1);
  } catch (error) {
    if (requestedStop) return 0;
    throw error;
  } finally {
    stop();
    await Promise.all(children.map((entry) => entry.completion));
    clearTimeout(stopTimer);
    process.removeListener("SIGINT", onSignal);
    process.removeListener("SIGTERM", onSignal);
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().then((code) => { process.exitCode = code; }).catch((error) => {
    console.error(error.message);
    process.exitCode = 1;
  });
}
