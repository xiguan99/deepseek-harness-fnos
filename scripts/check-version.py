#!/usr/bin/env python3
"""版本自检：应用版本与运行时版本必须处处一致。

真相来源只有两个文件（都在仓库根）：
  VERSION          → 应用版本（写进 fnpack/manifest）
  RUNTIME_VERSION  → 钉死的 @deepseek-ai/dsh 版本（写进包内 app/.version）

检查项：
  1. fnpack/manifest 的 version == VERSION
  2. README.md 的版本小节里出现了 VERSION
  3. 若传入 --runtime-dir：其 package.json 里 @deepseek-ai/dsh 必须精确等于 RUNTIME_VERSION
     （不接受 ^ / ~ / latest，必须钉死）
  4. 若传入 --tar：包内 tar 的 package.json 依赖同样必须精确等于 RUNTIME_VERSION
"""
import argparse
import json
import pathlib
import re
import subprocess
import sys
import tarfile


def read(p):
    return pathlib.Path(p).read_text(encoding="utf-8").strip()


def manifest_version(path):
    for line in pathlib.Path(path).read_text(encoding="utf-8").splitlines():
        m = re.match(r"^version\s*=\s*(\S+)", line)
        if m:
            return m.group(1)
    return None


def dep_version(pkg_json_path):
    data = json.loads(pathlib.Path(pkg_json_path).read_text(encoding="utf-8"))
    return (data.get("dependencies") or {}).get("@deepseek-ai/dsh")


def tar_dep_version(tar_path):
    with tarfile.open(tar_path, "r:gz") as tf:
        for member in tf:
            if member.name in ("./package.json", "package.json"):
                data = json.loads(tf.extractfile(member).read().decode("utf-8"))
                return (data.get("dependencies") or {}).get("@deepseek-ai/dsh")
    return None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", required=True, help="仓库根（dsh-src）")
    ap.add_argument("--runtime-dir", default=None, help="运行时 npm 工程目录")
    ap.add_argument("--tar", default=None, help="已打好的运行时 tar.gz")
    args = ap.parse_args()

    root = pathlib.Path(args.root).resolve()
    app_ver = read(root / "VERSION")
    rt_ver = read(root / "RUNTIME_VERSION")
    errors = []

    mv = manifest_version(root / "fnpack" / "manifest")
    if mv != app_ver:
        errors.append(f"fnpack/manifest version={mv} 与 VERSION={app_ver} 不一致")

    readme = (root / "README.md").read_text(encoding="utf-8")
    if app_ver not in readme:
        errors.append(f"README.md 里没有出现应用版本 {app_ver}")

    if args.runtime_dir:
        dv = dep_version(pathlib.Path(args.runtime_dir) / "package.json")
        if dv != rt_ver:
            errors.append(f"运行时 package.json 依赖 @deepseek-ai/dsh={dv!r}，必须是精确的 {rt_ver}")
        lock = pathlib.Path(args.runtime_dir) / "package-lock.json"
        if not lock.exists():
            errors.append("运行时缺 package-lock.json（必须随包，保证可复现）")

    if args.tar:
        tv = tar_dep_version(args.tar)
        if tv != rt_ver:
            errors.append(f"运行时 tar 内依赖 @deepseek-ai/dsh={tv!r}，必须是精确的 {rt_ver}")

    if errors:
        for e in errors:
            print(f"✗ 版本自检失败：{e}", file=sys.stderr)
        return 1

    print(f"✓ 版本自检通过：应用 {app_ver}（manifest/README 一致），运行时钉死 {rt_ver}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
