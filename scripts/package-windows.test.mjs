import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFile } from "node:child_process";
import { mkdtemp, mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { promisify } from "node:util";
import { collectBinaries, defaultTarget, isSystemDll, packageWindows, parseArgs, parsePe } from "./package-windows.mjs";

const execute = promisify(execFile);

// Structural PE fixtures only; these are never advertised as runnable binaries.
function peFixture({ imports = [], delayImports = [], dll = false, machine = 0x8664 } = {}) {
  const data = Buffer.alloc(8192);
  data.write("MZ");
  data.writeUInt32LE(0x80, 0x3c);
  data.write("PE\0\0", 0x80);
  data.writeUInt16LE(machine, 0x84);
  data.writeUInt16LE(1, 0x86);
  data.writeUInt16LE(240, 0x94);
  data.writeUInt16LE(dll ? 0x2022 : 0x22, 0x96);
  const optional = 0x98;
  data.writeUInt16LE(0x20b, optional);
  data.writeBigUInt64LE(0x140000000n, optional + 24);
  data.writeUInt32LE(0x200, optional + 60);
  data.writeUInt32LE(16, optional + 108);
  const section = optional + 240;
  data.write(".rdata", section);
  data.writeUInt32LE(data.length - 0x200, section + 8);
  data.writeUInt32LE(0x1000, section + 12);
  data.writeUInt32LE(data.length - 0x200, section + 16);
  data.writeUInt32LE(0x200, section + 20);
  let namePosition = 0x900;
  for (const [names, index, stride, tablePosition] of [[imports, 1, 20, 0x200], [delayImports, 13, 32, 0x500]]) {
    if (!names.length) continue;
    data.writeUInt32LE(tablePosition + 0xe00, optional + 112 + index * 8);
    data.writeUInt32LE((names.length + 1) * stride, optional + 116 + index * 8);
    names.forEach((name, number) => {
      const descriptor = tablePosition + number * stride;
      if (index === 13) data.writeUInt32LE(1, descriptor);
      data.writeUInt32LE(namePosition + 0xe00, descriptor + (index === 13 ? 4 : 12));
      data.write(`${name}\0`, namePosition, "latin1");
      namePosition += Buffer.byteLength(name, "latin1") + 1;
    });
  }
  return data;
}

async function fixtureRoot(t, { target = defaultTarget, imports = ["KERNEL32.dll"], delayImports = [], loaderImports = [] } = {}) {
  const root = await mkdtemp(path.join(os.tmpdir(), "dbx-package-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const output = path.join(root, "target", target, "release");
  const vendor = path.join(root, "vendor", "webview2-com-sys", "x64");
  await mkdir(output, { recursive: true });
  await mkdir(vendor, { recursive: true });
  await writeFile(path.join(output, "dbx.exe"), peFixture({ imports, delayImports }));
  await writeFile(path.join(vendor, "WebView2Loader.dll"), peFixture({ dll: true, imports: loaderImports }));
  await writeFile(path.join(root, "package.json"), JSON.stringify({ version: "0.0.0-test-fixture" }));
  await writeFile(path.join(root, "LICENSE"), "Packaging test fixture license\n");
  return root;
}

test("reads x64 regular and delay imports, and distinguishes system libraries", () => {
  assert.deepEqual(parsePe(peFixture({ imports: ["KERNEL32.dll", "libc++.dll"], delayImports: ["extra.dll"] })).imports,
    ["extra.dll", "KERNEL32.dll", "libc++.dll"]);
  assert.equal(isSystemDll("KERNEL32.dll"), true);
  assert.equal(isSystemDll("api-ms-win-crt-runtime-l1-1-0.dll"), true);
  for (const name of ["WebView2Loader.dll", "libc++.dll", "VCRUNTIME140.dll", "msvcp140.dll"]) assert.equal(isSystemDll(name), false);
});

test("rejects non-PE, wrong architecture, wrong binary kind, and malformed import tables", () => {
  assert.throws(() => parsePe(Buffer.alloc(100)), /missing DOS header/);
  assert.throws(() => parsePe(peFixture({ machine: 0xaa64 })), /expected x64/);
  assert.throws(() => parsePe(peFixture({ dll: true })), /received a DLL/);
  assert.throws(() => parsePe(peFixture(), "fake.dll", { dll: true }), /expected a DLL/);
  assert.throws(() => parsePe(peFixture().subarray(0, 600)), /truncated PE/);
  const malformed = peFixture({ imports: ["extra.dll"] });
  malformed.writeUInt32LE(20, 0x98 + 116 + 8);
  assert.throws(() => parsePe(malformed), /not terminated/);
  assert.throws(() => parsePe(peFixture({ imports: ["../extra.dll"] })), /unsafe or invalid/);
});

test("recursively includes required runtime DLLs with case-insensitive resolution", async (t) => {
  const root = await fixtureRoot(t, { imports: ["WebView2Loader.dll", "LIBC++.DLL"], delayImports: ["helper.dll"], loaderImports: ["KERNEL32.dll"] });
  const runtimeDir = path.join(root, "runtime");
  await mkdir(runtimeDir);
  await writeFile(path.join(runtimeDir, "libc++.dll"), peFixture({ dll: true, imports: ["libunwind.dll"] }));
  await writeFile(path.join(runtimeDir, "libunwind.dll"), peFixture({ dll: true, imports: ["ucrtbase.dll"] }));
  await writeFile(path.join(runtimeDir, "helper.dll"), peFixture({ dll: true, imports: ["libc++.dll"] }));
  await writeFile(path.join(runtimeDir, "unrelated.dll"), "not a PE; must not be copied");
  const binaries = await collectBinaries({ root, target: defaultTarget, runtimeDir });
  assert.deepEqual(binaries.map((binary) => binary.name.toLowerCase()).sort(),
    ["database-workbench.exe", "helper.dll", "libc++.dll", "libunwind.dll", "webview2loader.dll"]);
});

test("GNU includes WebView2Loader even without a direct import; MSVC needs no loader", async (t) => {
  const root = await fixtureRoot(t);
  const binaries = await collectBinaries({ root, target: defaultTarget });
  assert.equal(binaries.some((binary) => binary.name === "WebView2Loader.dll"), true);
  await rm(path.join(root, "vendor", "webview2-com-sys", "x64", "WebView2Loader.dll"));
  await assert.rejects(collectBinaries({ root, target: defaultTarget }), /ENOENT/);
  const msvcRoot = await fixtureRoot(t, { target: "x86_64-pc-windows-msvc" });
  await rm(path.join(msvcRoot, "vendor"), { recursive: true });
  assert.equal((await collectBinaries({ root: msvcRoot, target: "x86_64-pc-windows-msvc" })).length, 1);
});

test("missing or foreign-architecture runtime dependencies cannot replace a prior package", async (t) => {
  const root = await fixtureRoot(t, { imports: ["runtime.dll"] });
  const previous = path.join(root, "target", "windows", "Database-Workbench-windows-x64");
  await mkdir(previous, { recursive: true });
  await writeFile(path.join(previous, "preserved.txt"), "prior package");
  await assert.rejects(packageWindows({ root }), /runtime.dll.*--runtime-dir/);
  assert.equal(await readFile(path.join(previous, "preserved.txt"), "utf8"), "prior package");
  const runtimeDir = path.join(root, "runtime");
  await mkdir(runtimeDir);
  await writeFile(path.join(runtimeDir, "runtime.dll"), peFixture({ dll: true, machine: 0x14c }));
  await assert.rejects(packageWindows({ root, runtimeDir }), /expected x64/);
  assert.equal(await readFile(path.join(previous, "preserved.txt"), "utf8"), "prior package");
});

test("a missing built executable is an error and creates no output", async (t) => {
  const root = await fixtureRoot(t);
  await rm(path.join(root, "target", defaultTarget, "release", "dbx.exe"));
  await assert.rejects(packageWindows({ root }), /Complete a successful Windows build first/);
  await assert.rejects(readdir(path.join(root, "target", "windows")), /ENOENT/);
});

test("packages structural fixtures with truthful validation metadata and verifiable checksums", async (t) => {
  const root = await fixtureRoot(t);
  await mkdir(path.join(root, "docs"));
  await writeFile(path.join(root, "docs", "windows-package.md"), "Fixture documentation\n");
  await writeFile(path.join(root, "NOTICE"), "Fixture notice\n");
  const result = await packageWindows({ root });
  assert.equal(result.manifest.windowsRuntimeTested, false);
  assert.equal(result.manifest.target, defaultTarget);
  assert.equal(result.manifest.version, "0.0.0-test-fixture");
  assert.equal(result.manifest.validation.includesDelayImports, true);
  const checksums = (await readFile(path.join(result.directory, "SHA256SUMS"), "utf8")).trim().split("\n");
  for (const line of checksums) {
    const [digest, name] = line.split("  ");
    assert.equal(createHash("sha256").update(await readFile(path.join(result.directory, name))).digest("hex"), digest);
  }
  assert.equal(await readFile(path.join(result.directory, "portable.dbx"), "utf8"), "");
  const archiveBytes = await readFile(result.archive);
  assert.equal(archiveBytes.readUInt32LE(0), 0x04034b50);
  assert.equal(createHash("sha256").update(archiveBytes).digest("hex"), result.archiveSha256);
  assert.match(await readFile(`${result.archive}.sha256`, "utf8"), new RegExp(`^${result.archiveSha256}  Database-Workbench-windows-x64.zip`));
  const manifestOnDisk = JSON.parse(await readFile(path.join(result.directory, "build-manifest.json"), "utf8"));
  assert.deepEqual(manifestOnDisk, result.manifest);
  assert.equal(manifestOnDisk.files.some((file) => file.name === "NOTICE"), true);
  assert.equal(manifestOnDisk.files.some((file) => file.name === "windows-package.md"), true);
  assert.deepEqual((await readdir(path.dirname(result.directory))).sort(), ["Database-Workbench-windows-x64", "Database-Workbench-windows-x64.zip", "Database-Workbench-windows-x64.zip.sha256"]);
  // Confirm replacement remains complete on repeated packaging.
  const repeated = await packageWindows({ root });
  assert.equal(repeated.directory, result.directory);
  assert.equal(JSON.parse(await readFile(path.join(result.directory, "build-manifest.json"), "utf8")).windowsRuntimeTested, false);
});

test("CLI help, supported targets, and invalid arguments are explicit", async () => {
  assert.deepEqual(parseArgs([]), { target: defaultTarget });
  for (const target of [defaultTarget, "x86_64-pc-windows-gnu", "x86_64-pc-windows-msvc"]) assert.equal(parseArgs(["--target", target]).target, target);
  assert.throws(() => parseArgs(["--target"]), /Missing value/);
  assert.throws(() => parseArgs(["--target", "arm64"]), /Unsupported target/);
  assert.throws(() => parseArgs(["--unknown"]), /Unknown argument/);
  const help = await execute(process.execPath, [path.join(import.meta.dirname, "package-windows.mjs"), "--help"]);
  assert.match(help.stdout, /does not build or download/);
  assert.match(help.stdout, /windowsRuntimeTested=false/);
});
