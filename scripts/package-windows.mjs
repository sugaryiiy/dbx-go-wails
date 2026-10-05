#!/usr/bin/env node
// Package an existing release binary. This command never compiles or downloads.
import { execFile } from "node:child_process";
import { createHash } from "node:crypto";
import { createReadStream } from "node:fs";
import { copyFile, mkdir, mkdtemp, readFile, readdir, rename, rm, stat, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const execute = promisify(execFile);
const projectRoot = path.resolve(import.meta.dirname, "..");
export const defaultTarget = "x86_64-pc-windows-gnullvm";
const targets = new Set([defaultTarget, "x86_64-pc-windows-gnu", "x86_64-pc-windows-msvc"]);
const packageName = "Database-Workbench-windows-x64";
const executableName = "Database-Workbench.exe";

// Windows 10/11 system components. Redistributables (VC++, MinGW, WebView2)
// deliberately are NOT system DLLs and must be supplied beside the executable.
const systemDlls = new Set(`advapi32 authz avrt bcrypt bcryptprimitives cabinet cfgmgr32
  clbcatq combase comctl32 comdlg32 credui crypt32 cryptbase cryptnet cryptsp d2d1
  d3d11 d3d12 d3d9 dcomp dbgcore dbghelp dinput8 dnsapi dsound dwmapi dwrite dxgi
  dxva2 faultrep fwpuclnt gdi32 gdi32full glu32 hid imm32 iphlpapi kernel32
  kernelbase ksuser mf mfplat mfreadwrite mfuuid mmdevapi mpr msacm32 msimg32
  msvcrt mswsock ncrypt netapi32 normaliz ntdll ntmarta ole32 oleacc oleaut32 opengl32
  pdh powrprof profapi propsys psapi rasapi32 rpcrt4 rstrtmgr secur32 security setupapi
  shcore shell32 shlwapi sspicli urlmon user32 userenv uxtheme ucrtbase version
  wevtapi windowscodecs winhttp wininet winmm winspool wintrust winusb wldap32
  wlanapi ws2_32 wsock32 wtsapi32`.split(/\s+/).filter(Boolean).map((name) => `${name}.dll`));

export function isSystemDll(name) {
  const normalized = name.toLowerCase();
  return systemDlls.has(normalized) || /^(api-ms-win-|ext-ms-win-)[a-z0-9_-]+\.dll$/.test(normalized);
}

/** Read x64 PE import and delay-import tables without needing platform tools. */
export function parsePe(data, label = "PE file", { dll = false } = {}) {
  const fail = (reason) => { throw new Error(`${label}: ${reason}`); };
  const bounds = (offset, size) => {
    if (!Number.isSafeInteger(offset) || offset < 0 || offset + size > data.length) fail("truncated PE file");
  };
  const u16 = (offset) => { bounds(offset, 2); return data.readUInt16LE(offset); };
  const u32 = (offset) => { bounds(offset, 4); return data.readUInt32LE(offset); };
  bounds(0, 64);
  if (data.toString("ascii", 0, 2) !== "MZ") fail("missing DOS header; expected a Windows .exe/.dll");
  const pe = u32(0x3c);
  bounds(pe, 24);
  if (!data.subarray(pe, pe + 4).equals(Buffer.from([0x50, 0x45, 0, 0]))) fail("missing PE signature");
  if (u16(pe + 4) !== 0x8664) fail("expected x64 PE machine type (0x8664)");
  const sectionCount = u16(pe + 6);
  if (!sectionCount || sectionCount > 96) fail("invalid PE section count");
  const characteristics = u16(pe + 22);
  if (!(characteristics & 2)) fail("PE is not executable");
  if (Boolean(characteristics & 0x2000) !== dll) fail(dll ? "expected a DLL" : "expected an executable, received a DLL");
  const optional = pe + 24;
  const optionalSize = u16(pe + 20);
  bounds(optional, optionalSize);
  if (optionalSize < 112 || u16(optional) !== 0x20b) fail("expected a PE32+ optional header");
  const directoryCount = u32(optional + 108);
  if (directoryCount > Math.floor((optionalSize - 112) / 8)) fail("truncated PE data directories");
  const headersSize = u32(optional + 60);
  if (headersSize > data.length) fail("truncated PE headers");
  const imageBase = data.readBigUInt64LE(optional + 24);
  const sectionTable = optional + optionalSize;
  bounds(sectionTable, sectionCount * 40);
  const sections = Array.from({ length: sectionCount }, (_, index) => {
    const offset = sectionTable + index * 40;
    const entry = { virtual: u32(offset + 12), virtualSize: u32(offset + 8), raw: u32(offset + 20), size: u32(offset + 16) };
    bounds(entry.raw, entry.size);
    return entry;
  });
  const rvaOffset = (rva, size = 1) => {
    if (rva < headersSize && rva + size <= headersSize) { bounds(rva, size); return rva; }
    for (const section of sections) {
      const delta = rva - section.virtual;
      if (delta >= 0 && delta + size <= section.size) return section.raw + delta;
    }
    fail(`PE RVA 0x${rva.toString(16)} is not backed by file data`);
  };
  const dllName = (rva) => {
    const offset = rvaOffset(rva);
    const end = data.indexOf(0, offset);
    if (end < 0 || end - offset > 255) fail("invalid DLL import name");
    rvaOffset(rva, end - offset + 1);
    const name = data.toString("latin1", offset, end);
    if (!/^[a-zA-Z0-9_+.-]+\.dll$/i.test(name)) fail(`unsafe or invalid DLL import name: ${JSON.stringify(name)}`);
    return name;
  };
  const readTable = (index, stride, delay) => {
    if (index >= directoryCount) return [];
    const directory = optional + 112 + index * 8;
    const rva = u32(directory);
    const size = u32(directory + 4);
    if (!rva && !size) return [];
    if (!rva || size < stride) fail("invalid PE import directory");
    const imported = [];
    for (let position = 0; position + stride <= size; position += stride) {
      const offset = rvaOffset(rva + position, stride);
      if (data.subarray(offset, offset + stride).every((value) => value === 0)) return imported;
      let nameRva = u32(offset + (delay ? 4 : 12));
      if (delay) {
        const attributes = u32(offset);
        if (attributes > 1) fail("unsupported delay-import attributes");
        if (!attributes) {
          const relative = BigInt(nameRva) - imageBase;
          if (relative < 0 || relative > 0xffffffffn) fail("invalid delay-import address");
          nameRva = Number(relative);
        }
      }
      if (!nameRva) fail("PE import descriptor has no DLL name");
      imported.push(dllName(nameRva));
    }
    fail("PE import table is not terminated within its directory");
  };
  const canonical = new Map();
  for (const name of [...readTable(1, 20, false), ...readTable(13, 32, true)]) canonical.set(name.toLowerCase(), name);
  return { machine: "x64", format: "PE32+", imports: [...canonical.values()].sort((a, b) => a.localeCompare(b)) };
}

async function fileDigest(filename) {
  const hash = createHash("sha256");
  for await (const part of createReadStream(filename)) hash.update(part);
  return hash.digest("hex");
}

async function exists(filename) {
  try { await stat(filename); return true; }
  catch (error) { if (error.code === "ENOENT") return false; throw error; }
}

export async function collectBinaries({ root, target, runtimeDir }) {
  const exePath = path.join(root, "target", target, "release", "dbx.exe");
  if (!await exists(exePath)) throw new Error(`Built release executable not found: ${exePath}. Complete a successful Windows build first.`);
  const binaries = new Map();
  const addBinary = async (filename, name, dll) => {
    const metadata = parsePe(await readFile(filename), filename, { dll });
    binaries.set(name.toLowerCase(), { name, source: filename, ...metadata });
  };
  await addBinary(exePath, executableName, false);
  if (target !== "x86_64-pc-windows-msvc") {
    await addBinary(path.join(root, "vendor", "webview2-com-sys", "x64", "WebView2Loader.dll"), "WebView2Loader.dll", true);
  }
  const runtimeFiles = new Map();
  if (runtimeDir) {
    for (const entry of await readdir(runtimeDir, { withFileTypes: true })) {
      if (!entry.isFile() && !entry.isSymbolicLink()) continue;
      if (!entry.name.toLowerCase().endsWith(".dll")) continue;
      const key = entry.name.toLowerCase();
      if (runtimeFiles.has(key)) throw new Error(`Ambiguous runtime DLL name in ${runtimeDir}: ${entry.name}`);
      runtimeFiles.set(key, path.join(runtimeDir, entry.name));
    }
  }
  // Map iteration visits newly-added dependencies, so this validates the entire
  // non-system DLL graph, including the loader and runtime DLLs themselves.
  for (const binary of binaries.values()) {
    for (const imported of binary.imports) {
      const key = imported.toLowerCase();
      if (isSystemDll(imported) || binaries.has(key)) continue;
      const filename = runtimeFiles.get(key);
      if (!filename) throw new Error(`${binary.name} requires ${imported}; supply its x64 DLL with --runtime-dir <directory>. No package was published.`);
      await addBinary(filename, imported, true);
    }
  }
  return [...binaries.values()];
}

async function makeZip(directory, destination) {
  const python = `import pathlib, sys, zipfile
root = pathlib.Path(sys.argv[1])
with zipfile.ZipFile(sys.argv[2], 'w', compression=zipfile.ZIP_DEFLATED, compresslevel=6) as archive:
    for item in sorted(root.rglob('*')):
        if item.is_file():
            archive.write(item, item.relative_to(root.parent).as_posix())
with zipfile.ZipFile(sys.argv[2]) as archive:
    if archive.testzip() is not None:
        raise RuntimeError('ZIP verification failed')
`;
  for (const [command, prefix] of process.platform === "win32" ? [["py", ["-3"]], ["python", []], ["python3", []]] : [["python3", []], ["python", []]]) {
    try { await execute(command, [...prefix, "-c", python, directory, destination], { maxBuffer: 1024 * 1024 }); return; }
    catch (error) { if (error.code !== "ENOENT") throw error; }
  }
  if (process.platform === "win32") {
    const quote = (value) => `'${value.replaceAll("'", "''")}'`;
    const script = `$ErrorActionPreference = 'Stop'; Compress-Archive -LiteralPath ${quote(directory)} -DestinationPath ${quote(destination)} -CompressionLevel Optimal`;
    await execute("powershell.exe", ["-NoProfile", "-NonInteractive", "-EncodedCommand", Buffer.from(script, "utf16le").toString("base64")]);
    return;
  }
  throw new Error("ZIP creation requires Python 3 (python3 or python). No package was published.");
}

const readme = `# Database Workbench · Windows x64 便携版

完整解压 ZIP 到当前用户可写的目录，然后双击 Database-Workbench.exe。
请保留同目录的 DLL 和 portable.dbx；数据保存在程序旁的 data 目录。
首次启动可能导入此电脑上已有的 DBX 数据。DBX_DATA_DIR 可指定其他数据目录。

系统要求：64 位 Windows 10/11，以及 Microsoft Edge WebView2 Runtime。
WebView2Loader.dll 只是加载器，并不包含 WebView2 Runtime；若系统尚未安装运行时，请从微软官网安装：
https://developer.microsoft.com/microsoft-edge/webview2/

这是本地构建的未签名便携包。PE 格式和 DLL 依赖已静态检查，但尚未完成 Windows 实机启动及功能验证。
构建来源和校验值见 build-manifest.json、SHA256SUMS。SHA256 校验用于检查文件完整性，不代表代码签名。
`;

/** Only completed, validated staging output can replace an existing package. */
export async function packageWindows({ root = projectRoot, target = defaultTarget, runtimeDir } = {}) {
  if (!targets.has(target)) throw new Error(`Unsupported target: ${target}`);
  const binaries = await collectBinaries({ root, target, runtimeDir });
  const version = JSON.parse(await readFile(path.join(root, "package.json"), "utf8")).version;
  const outputRoot = path.join(root, "target", "windows");
  await mkdir(outputRoot, { recursive: true });
  const lock = path.join(outputRoot, ".package.lock");
  try { await writeFile(lock, `${process.pid}\n`, { flag: "wx" }); }
  catch (error) { if (error.code === "EEXIST") throw new Error(`Packaging lock already exists: ${lock}. Check for another running packager before removing it.`); throw error; }
  let temporary;
  let preserveTemporary = false;
  try {
    temporary = await mkdtemp(path.join(outputRoot, ".package-"));
    const directory = path.join(temporary, packageName);
    await mkdir(directory);
    for (const binary of binaries) {
      await copyFile(binary.source, path.join(directory, binary.name));
      // Reject a build artifact or runtime that changed during packaging.
      const copied = parsePe(await readFile(path.join(directory, binary.name)), binary.name, { dll: binary.name !== executableName });
      if (JSON.stringify(copied.imports) !== JSON.stringify(binary.imports)
        || await fileDigest(binary.source) !== await fileDigest(path.join(directory, binary.name))) {
        throw new Error(`${binary.name} changed during packaging; finish the build before packaging again.`);
      }
    }
    await copyFile(path.join(root, "LICENSE"), path.join(directory, "LICENSE"));
    if (await exists(path.join(root, "NOTICE"))) await copyFile(path.join(root, "NOTICE"), path.join(directory, "NOTICE"));
    if (runtimeDir && binaries.some((binary) => path.resolve(path.dirname(binary.source)) === path.resolve(runtimeDir))) {
      // The LLVM-MinGW distribution keeps its licenses at these known paths.
      // Generic runtime directories need not have this layout; do not scan them.
      const toolchainLicense = path.resolve(runtimeDir, "..", "..", "LICENSE.TXT");
      if (await exists(toolchainLicense)) await copyFile(toolchainLicense, path.join(directory, "LICENSE-llvm-mingw.txt"));
      const mingwLicenses = path.resolve(runtimeDir, "..", "share", "mingw32");
      if (await exists(mingwLicenses)) {
        for (const entry of await readdir(mingwLicenses, { withFileTypes: true })) {
          if (entry.isFile() && entry.name.startsWith("COPYING")) {
            const name = entry.name === "COPYING" ? "COPYING-mingw32.txt" : entry.name;
            await copyFile(path.join(mingwLicenses, entry.name), path.join(directory, name));
          }
        }
      }
    }
    await writeFile(path.join(directory, "portable.dbx"), "");
    await writeFile(path.join(directory, "README-运行说明.md"), readme);
    const sourceDocs = path.join(root, "docs", "windows-package.md");
    if (await exists(sourceDocs)) await copyFile(sourceDocs, path.join(directory, "windows-package.md"));

    const files = [];
    for (const name of (await readdir(directory)).sort()) {
      const filename = path.join(directory, name);
      files.push({ name, bytes: (await stat(filename)).size, sha256: await fileDigest(filename) });
    }
    let revision = null;
    try { revision = (await execute("git", ["rev-parse", "HEAD"], { cwd: root })).stdout.trim(); } catch { /* A source archive may have no Git metadata. */ }
    const manifest = {
      schemaVersion: 1, product: "Database Workbench", version, target, architecture: "x64",
      executable: executableName, packagedAt: new Date().toISOString(),
      packagingHost: { platform: process.platform, architecture: process.arch },
      crossCompiled: process.platform !== "win32" || process.arch !== "x64",
      windowsRuntimeTested: false, codeSigningVerified: false, revision,
      suppliedBuildMetadata: {
        command: process.env.DBX_WINDOWS_BUILD_COMMAND || null,
        releaseProfileOverrides: Object.fromEntries(["CARGO_PROFILE_RELEASE_OPT_LEVEL", "CARGO_PROFILE_RELEASE_LTO", "CARGO_PROFILE_RELEASE_CODEGEN_UNITS"]
          .filter((key) => process.env[key] !== undefined).map((key) => [key, process.env[key]])),
      },
      sourceExecutable: path.relative(root, binaries[0].source).split(path.sep).join("/"),
      validation: { peFormat: "PE32+", machine: "AMD64", dependencyImportsChecked: true, includesDelayImports: true },
      binaries: binaries.map(({ name, imports }) => ({ name, imports })), files,
      notes: ["Packaging validates PE structure and DLL imports only; it does not run the Windows application.", "crossCompiled is inferred from the packaging host; no compiler provenance is asserted.", "Build command and release profile overrides are recorded only when supplied in the packaging environment; they are not independently verified.", "Microsoft Edge WebView2 Runtime must be installed on Windows.", "Version and revision describe the packaging workspace; source modifications may be present."],
    };
    await writeFile(path.join(directory, "build-manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
    const checksums = [...files.map(({ name, sha256 }) => `${sha256}  ${name}`), `${await fileDigest(path.join(directory, "build-manifest.json"))}  build-manifest.json`];
    await writeFile(path.join(directory, "SHA256SUMS"), `${checksums.join("\n")}\n`);
    const zipName = `${packageName}.zip`;
    await makeZip(directory, path.join(temporary, zipName));
    const archiveSha256 = await fileDigest(path.join(temporary, zipName));
    await writeFile(path.join(temporary, `${zipName}.sha256`), `${archiveSha256}  ${zipName}\n`);

    // Back up every prior output before publishing any new one, and restore
    // those outputs if any rename fails. Unrelated target/windows files stay put.
    const names = [packageName, zipName, `${zipName}.sha256`];
    const backedUp = [];
    const published = [];
    try {
      for (const name of names) {
        const destination = path.join(outputRoot, name);
        if (await exists(destination)) { await rename(destination, path.join(temporary, `${name}.previous`)); backedUp.push(name); }
      }
      for (const name of names) { await rename(path.join(temporary, name), path.join(outputRoot, name)); published.push(name); }
    } catch (error) {
      try {
        for (const name of published.reverse()) await rm(path.join(outputRoot, name), { recursive: true, force: true });
        for (const name of backedUp.reverse()) await rename(path.join(temporary, `${name}.previous`), path.join(outputRoot, name));
      } catch (restoreError) {
        preserveTemporary = true;
        throw new Error(`Publishing failed (${error.message}); restoring prior output also failed (${restoreError.message}). Recovery files have been preserved in ${temporary}.`);
      }
      throw error;
    }
    return { directory: path.join(outputRoot, packageName), archive: path.join(outputRoot, zipName), archiveSha256, manifest };
  } finally {
    if (temporary && !preserveTemporary) await rm(temporary, { recursive: true, force: true });
    await rm(lock, { force: true });
  }
}

export function parseArgs(args) {
  const options = { target: defaultTarget };
  for (let index = 0; index < args.length; index++) {
    const arg = args[index];
    if (arg === "--help" || arg === "-h") { options.help = true; continue; }
    if (arg !== "--target" && arg !== "--runtime-dir") throw new Error(`Unknown argument: ${arg}. Use --help.`);
    const value = args[++index];
    if (!value || value.startsWith("--")) throw new Error(`Missing value for ${arg}.`);
    if (arg === "--target") options.target = value;
    else options.runtimeDir = path.resolve(value);
  }
  if (!targets.has(options.target)) throw new Error(`Unsupported target: ${options.target}. Use --help.`);
  return options;
}

export async function main(args = process.argv.slice(2)) {
  const options = parseArgs(args);
  if (options.help) {
    console.log(`Usage: node scripts/package-windows.mjs [--target <triple>] [--runtime-dir <directory>]

Packages target/<triple>/release/dbx.exe; does not build or download anything.
Targets: ${[...targets].join(", ")}
Default: ${defaultTarget}
--runtime-dir: directory containing any non-system x64 runtime DLLs.
GNU targets always include vendor/webview2-com-sys/x64/WebView2Loader.dll.
Output: target/windows/${packageName}/ and ${packageName}.zip
The package records windowsRuntimeTested=false; it has not been run on Windows.`);
    return;
  }
  const result = await packageWindows(options);
  console.log(`Packaged: ${result.directory}\nArchive: ${result.archive}\nSHA256: ${result.archiveSha256}\nWindows runtime verification: not performed.`);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => { console.error(`Windows packaging failed: ${error.message}`); process.exitCode = 1; });
}
