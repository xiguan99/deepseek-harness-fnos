#!/usr/bin/env python3
"""版本与端口自检：声明和实物必须一致。

真相来源只有两个文件（都在仓库根）：
  VERSION          → 应用版本（写进 fnpack/manifest）
  RUNTIME_VERSION  → 钉死的 @deepseek-ai/dsh 版本（写进包内 app/.version）

为什么要有"实物层"：装机版就吃过这个亏——包内声明 0.2.1-rc.1，实际跑的是
在线升级来的 0.2.1-rc.2。只比 package.json/lock 里的声明不够，必须把 tar
解开、读 node_modules 里那份真 package.json。

检查项：
  1. fnpack/manifest 的 version == VERSION
  2. README.md 的版本小节里出现了 VERSION
  3. --runtime-dir：package.json 依赖精确等于 RUNTIME_VERSION（不接受 ^ ~ latest），
     且必须有 package-lock.json
  4. --tar：tar 内 package.json 依赖精确等于 RUNTIME_VERSION
  5. --tar：tar 内 node_modules/@deepseek-ai/dsh/package.json 的 version 必须
     精确等于 RUNTIME_VERSION（实物层，防声明/实物双轨）
  6. 端口钉死：config.go 的默认端口必须是 2298（后端）/ 2299（反代）/ 2297（控制面），
     前端 fnpack/app/ui/config 只能有一个 url 入口，端口 2299、路径 /
  7. manifest 里不得残留上游维护者标识（yuexps）
"""
import argparse
import json
import pathlib
import re
import sys
import tarfile

EXPECTED_SERVER_PORT = 2298
EXPECTED_PROXY_PORT = 2299
EXPECTED_CONTROL_PORT = 2297
GATEWAY_PREFIX = "/app/deepseek-harness"


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


def tar_versions(tar_path):
    """一次遍历 tar，同时取声明依赖与实际安装版本。"""
    declared = None
    actual = None
    with tarfile.open(tar_path, "r:gz") as tf:
        for member in tf:
            name = member.name[2:] if member.name.startswith("./") else member.name
            if name == "package.json":
                data = json.loads(tf.extractfile(member).read().decode("utf-8"))
                declared = (data.get("dependencies") or {}).get("@deepseek-ai/dsh")
            elif name == "node_modules/@deepseek-ai/dsh/package.json":
                data = json.loads(tf.extractfile(member).read().decode("utf-8"))
                actual = data.get("version")
            if declared is not None and actual is not None:
                break
    return declared, actual


def check_ports(root: pathlib.Path):
    """端口钉死 + 桌面入口形态 + 上游标识清理。"""
    errors = []
    cfg_go = (root / "config.go").read_text(encoding="utf-8")
    if f"ServerPort:" not in cfg_go or f"{EXPECTED_SERVER_PORT}" not in cfg_go:
        errors.append(f"config.go 里找不到后端端口 {EXPECTED_SERVER_PORT}")
    if f"ProxyPort:" not in cfg_go or f"{EXPECTED_PROXY_PORT}" not in cfg_go:
        errors.append(f"config.go 里找不到反代端口 {EXPECTED_PROXY_PORT}")
    main_go = (root / "main.go").read_text(encoding="utf-8")
    if str(EXPECTED_CONTROL_PORT) not in main_go:
        errors.append(f"main.go 里找不到控制面端口 {EXPECTED_CONTROL_PORT}")

    ui_path = root / "fnpack" / "app" / "ui" / "config"
    entries = json.loads(ui_path.read_text(encoding="utf-8")).get(".url", {})
    if len(entries) != 1:
        errors.append(f"桌面入口应为 1 个（本体直连），实际 {len(entries)} 个")
    for key, val in entries.items():
        if val.get("type") != "url":
            errors.append(f"{key} 的 type 应是 url（实际 {val.get('type')!r}）")
        if int(val.get("port") or 0) != EXPECTED_PROXY_PORT:
            errors.append(f"{key} 的端口应是 {EXPECTED_PROXY_PORT}（实际 {val.get('port')!r}）")
        if val.get("url") != "/":
            errors.append(f"{key} 的 url 应是 /（实际 {val.get('url')!r}）")
        if "gatewaySocket" in val or "gatewayPrefix" in val:
            errors.append(f"{key} 还残留网关 socket 形态字段")

    manifest_text = (root / "fnpack" / "manifest").read_text(encoding="utf-8")
    if "yuexps" in manifest_text:
        errors.append("manifest 里还残留上游维护者标识 yuexps")
    if re.search(r"^version\s*=\s*v", manifest_text, re.M):
        errors.append("manifest version 不能带 v 前缀（可能影响应用中心比较）")
    return errors


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", required=True, help="源码树根（deepseek.harness/src）")
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
        declared, actual = tar_versions(args.tar)
        if declared != rt_ver:
            errors.append(f"运行时 tar 内声明依赖 @deepseek-ai/dsh={declared!r}，必须是精确的 {rt_ver}")
        if actual is None:
            errors.append("运行时 tar 里找不到 node_modules/@deepseek-ai/dsh/package.json（实物层缺失）")
        elif actual != rt_ver:
            errors.append(
                f"运行时 tar 内实际安装版本是 {actual!r}，与钉死的 {rt_ver} 不一致"
                "（声明与实物双轨，正是线上遇到过的坑）"
            )

    errors.extend(check_ports(root))

    if errors:
        for e in errors:
            print(f"✗ 自检失败：{e}", file=sys.stderr)
        return 1

    tail = ""
    if args.tar:
        declared, actual = tar_versions(args.tar)
        tail = f"；tar 内声明 {declared} / 实物 {actual}"
    print(f"✓ 自检通过：应用 {app_ver}（manifest/README 一致），运行时钉死 {rt_ver}{tail}；"
          f"端口钉死 {EXPECTED_SERVER_PORT}/{EXPECTED_PROXY_PORT}/控制面 {EXPECTED_CONTROL_PORT}，"
          f"桌面入口 1 个（url → 2299）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
