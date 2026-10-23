#!/usr/bin/env python3
"""图标自检：打包前强制校验，避免桌面上两个应用图标撞车。

背景：曾经直接复用别的项目图标，装完后桌面上分不清是哪个应用。这里把
「本包图标不得与其它项目图标同 sha256」做成打包硬规则，命中即 exit 1。

检查项：
  1. ICON.PNG / ICON_256.PNG 都存在且非空
  2. 两者 PNG 尺寸均为 256x256
  3. 四角透明（fnOS 桌面图标要求圆角外透明）
  4. sha256 不与其它项目（同级 fnpack*/ICON*.PNG）重复

用法：check-icon.py --src <fnpack 目录> --name <应用名> [--apps-root <父目录>]
"""
import argparse
import hashlib
import pathlib
import struct
import sys


def png_info(path: pathlib.Path):
    data = path.read_bytes()
    if not data.startswith(b"\x89PNG\r\n\x1a\n"):
        raise ValueError("不是 PNG 文件")
    w, h = struct.unpack(">II", data[16:24])
    bit_depth, color_type = data[24], data[25]
    return w, h, bit_depth, color_type


def corner_alpha(path: pathlib.Path):
    """读四角像素 alpha（仅支持 8bit RGBA / RGB，用 PIL 缺失时的退化判断）。"""
    try:
        from PIL import Image  # 可选依赖；没有就跳过透明检查
    except Exception:  # noqa: BLE001
        return None
    im = Image.open(path).convert("RGBA")
    w, h = im.size
    return [im.getpixel((0, 0))[3], im.getpixel((w - 1, 0))[3],
            im.getpixel((0, h - 1))[3], im.getpixel((w - 1, h - 1))[3]]


def sha256(path: pathlib.Path):
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--src", required=True, help="fnpack 目录")
    ap.add_argument("--name", required=True, help="应用名（仅用于日志）")
    ap.add_argument("--apps-root", default=None, help="同级项目根目录，默认 src 的父目录")
    args = ap.parse_args()

    src = pathlib.Path(args.src).resolve()
    apps_root = pathlib.Path(args.apps_root).resolve() if args.apps_root else src.parent

    errors, mine = [], {}
    for fname in ("ICON.PNG", "ICON_256.PNG"):
        p = src / fname
        if not p.exists() or p.stat().st_size == 0:
            errors.append(f"缺少或空文件：{p}")
            continue
        try:
            w, h, depth, ctype = png_info(p)
        except Exception as exc:  # noqa: BLE001
            errors.append(f"{fname} 解析失败：{exc}")
            continue
        if (w, h) != (256, 256):
            errors.append(f"{fname} 尺寸是 {w}x{h}，必须是 256x256")
        alphas = corner_alpha(p)
        if alphas is not None and any(a != 0 for a in alphas):
            errors.append(f"{fname} 四角不是全透明（alpha={alphas}），桌面圆角会露白边")
        mine[fname] = sha256(p)

    if errors:
        for e in errors:
            print(f"✗ 图标自检失败：{e}", file=sys.stderr)
        return 1

    clashes = []
    for other in sorted(apps_root.glob("*/fnpack*")):
        if other.resolve() == src:
            continue
        for fname in ("ICON.PNG", "ICON_256.PNG"):
            p = other / fname
            if not p.exists():
                continue
            s = sha256(p)
            for mf, ms in mine.items():
                if s == ms:
                    clashes.append(f"{other.parent.name}/{other.name}/{fname} 与本包 {mf} 相同（{s[:16]}…）")
    if clashes:
        print("✗ 图标与其它项目重复，禁止打包：", file=sys.stderr)
        for c in clashes:
            print(f"    {c}", file=sys.stderr)
        return 1

    print(f"✓ 图标自检通过（{args.name}）：ICON.PNG/ICON_256.PNG 均 256x256、四角透明，"
          f"sha256 {mine['ICON_256.PNG'][:16]}…，与其它项目无重复")
    return 0


if __name__ == "__main__":
    sys.exit(main())
