import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import { readFileSync } from "node:fs";
import path from "node:path";

const root = fileURLToPath(new URL("../", import.meta.url));
const config = JSON.parse(readFileSync(path.join(root, "wails.json"), "utf8"));
const command = process.argv[2] ?? "build";
if (!["build", "dev", "doctor"].includes(command)) {
  throw new Error("Usage: node scripts/wails.mjs [build|dev|doctor] [Wails options]");
}

// The repository still contains legacy Rust vendor sources. Go must resolve
// modules rather than interpreting that directory as Go's vendor tree.
// Wails' standard dev server exposes HTTP routes. Keep the password-free
// desktop API inside the native WebView by building and launching locally.
const args = ["run", "-mod=mod", "github.com/wailsapp/wails/v2/cmd/wails@v2.16.0", command === "dev" ? "build" : command];
if (command !== "doctor") args.push("-tags", "desktop");
if (command === "dev") args.push("-debug");
args.push(...process.argv.slice(3));
let child = spawn("go", args, {
  cwd: root,
  stdio: "inherit",
  env: { ...process.env, GOFLAGS: `${process.env.GOFLAGS ?? ""} -mod=mod`.trim() },
});
child.on("error", (error) => { console.error(error.message); process.exitCode = 1; });
for (const signal of ["SIGINT", "SIGTERM"]) process.on(signal, () => child.kill(signal));
child.on("exit", (code) => {
  if (code !== 0 || command !== "dev") { process.exitCode = code ?? 1; return; }
  const executable = process.platform === "darwin"
    ? path.join(root, `build/wails/bin/${config.name}.app/Contents/MacOS/${config.outputfilename}`)
    : path.join(root, "build/wails/bin", process.platform === "win32" ? `${config.outputfilename}.exe` : config.outputfilename);
  child = spawn(executable, [], { cwd: root, stdio: "inherit", env: process.env });
  child.on("error", (error) => { console.error(error.message); process.exitCode = 1; });
  child.on("exit", (appCode) => { process.exitCode = appCode ?? 1; });
});
