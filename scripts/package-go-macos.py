#!/usr/bin/env python3
"""Package a committed Go/Wails macOS build and verify the mounted application."""

import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import platform
import plistlib
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parent.parent


def run(*args):
    return subprocess.check_output(args, cwd=ROOT)


def digest(path):
    checksum = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            checksum.update(chunk)
    return checksum.hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if platform.system() != "Darwin":
        parser.error("Packaging requires macOS and hdiutil")
    if run("git", "status", "--porcelain").strip():
        parser.error("Commit all source changes before packaging")
    commit = run("git", "rev-parse", "HEAD").decode().strip()
    config = json.loads((ROOT / "wails.json").read_text())
    name = config["name"] + ".app"
    built = ROOT / config["build:dir"] / "bin" / name
    binary = Path("Contents/MacOS") / config["outputfilename"]
    info = plistlib.loads((built / "Contents/Info.plist").read_bytes())
    arch = run("lipo", "-archs", str(built / binary)).decode().strip()
    expected_digest = digest(built / binary)
    output = args.output if args.output.is_absolute() else ROOT / args.output
    checksum = Path(str(output) + ".sha256")
    manifest = Path(str(output) + ".manifest.json")
    if output.suffix != ".dmg":
        parser.error("Output must have a .dmg extension")
    if any(path.exists() for path in (output, checksum, manifest)):
        parser.error("Output or verification files already exist")
    output.parent.mkdir(parents=True, exist_ok=True)

    def verify_app(app):
        run("codesign", "--verify", "--deep", "--strict", str(app))
        if digest(app / binary) != expected_digest:
            raise RuntimeError("Packaged application differs from the build")
        if run("lipo", "-archs", str(app / binary)).decode().strip() != arch:
            raise RuntimeError("Packaged application architecture differs from the build")

    verify_app(built)
    with tempfile.TemporaryDirectory(prefix="dbx-go-release-") as temporary:
        stage = Path(temporary) / "stage"
        stage.mkdir()
        run("ditto", str(built), str(stage / name))
        verify_app(stage / name)
        os.symlink("/Applications", stage / "Applications")
        (stage / "安装说明.txt").write_text(
            f"将 {name} 拖到 Applications 即可安装。\n"
            f"预览版本：{args.tag}\n源码提交：{commit}\n"
            f"平台：macOS {arch}，最低 macOS {info['LSMinimumSystemVersion']}。\n\n"
            "本版修复 PostgreSQL 旧版本的表详情和查询补全，支持表名及字段提示。\n"
            "Go 迁移仍在进行，完整功能范围见仓库 MIGRATION.md。\n"
            "本预览采用 ad-hoc 签名，尚未公证；macOS 可能要求在隐私与安全性中允许打开。\n"
            "安装包不包含连接配置或凭据，应用使用原有 Go 数据目录。\n",
            encoding="utf-8",
        )
        run("hdiutil", "create", "-volname", config["name"], "-srcfolder", str(stage),
            "-fs", "HFS+", "-format", "UDZO", "-imagekey", "zlib-level=9", str(output))
    run("hdiutil", "verify", str(output))
    attached = plistlib.loads(run("hdiutil", "attach", "-readonly", "-nobrowse", "-plist", str(output)))
    mount = next(Path(item["mount-point"]) for item in attached["system-entities"] if "mount-point" in item)
    try:
        verify_app(mount / name)
    finally:
        run("hdiutil", "detach", str(mount))
    sha = digest(output)
    checksum.write_text(f"{sha}  {output.name}\n", encoding="utf-8")
    report = {
        "tag": args.tag, "commit": commit, "application": name,
        "version": info["CFBundleShortVersionString"],
        "minimumMacOS": info["LSMinimumSystemVersion"], "architecture": arch,
        "signature": "ad-hoc", "notarized": False,
        "package": output.name, "packageBytes": output.stat().st_size,
        "packageSha256": sha, "executableSha256": expected_digest,
        "packagedAt": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "checks": {"builtSignature": "passed", "dmgIntegrity": "passed",
                   "mountedSignature": "passed", "binaryMatch": "passed", "architectureMatch": "passed"},
    }
    manifest.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
