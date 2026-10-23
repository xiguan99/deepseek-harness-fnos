#!/usr/bin/env python3
"""确定性打包：同一份源码 + 同一份 dist，连跑多次 sha256 必须完全一致。

为什么自己打：fnpack 会把「构建当时的时间」写进 tar 成员头与 gzip 头，
同一份内容每次打出来的字节都不同，装完就没法核对「装的确实是这个包」。

做法：
  - 成员按名字排序后写入，mtime 固定 0，uid/gid 0，uname/gname 空
  - 权限沿用源文件权限（打包内容的一部分，不随构建时间变）
  - 内层 app.tgz 与最外层 fpk 都用 `gzip -9 -n` 压缩（-n 不写文件名与时间戳）
  - manifest 的 checksum 按 md5(app.tgz) 重算，不用构建时间相关值

用法：
  python3 scripts/make-fpk.py --src <fnpack 目录> --out <产物 fpk>
"""
import argparse
import hashlib
import os
import subprocess
import sys
import tarfile
import tempfile

EPOCH = 0


def sorted_entries(root):
    """返回 [(相对路径, 绝对路径)]，父目录在前、同级按名字排序。"""
    out = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames.sort()
        filenames.sort()
        rel_dir = os.path.relpath(dirpath, root)
        if rel_dir != ".":
            out.append((rel_dir.replace(os.sep, "/"), dirpath))
        for name in filenames:
            ap = os.path.join(dirpath, name)
            rp = os.path.relpath(ap, root).replace(os.sep, "/")
            out.append((rp, ap))
    # 保证目录在其子项之前
    out.sort(key=lambda x: (x[0].count("/"), x[0]) if os.path.isdir(x[1]) else (x[0].count("/"), x[0]))
    return out


def add_path(tar, rel, ap):
    st = os.stat(ap)
    ti = tarfile.TarInfo(rel)
    ti.mtime = EPOCH
    ti.uid = 0
    ti.gid = 0
    ti.uname = ""
    ti.gname = ""
    ti.mode = st.st_mode & 0o7777
    if os.path.isdir(ap):
        ti.type = tarfile.DIRTYPE
        ti.size = 0
        tar.addfile(ti)
    else:
        ti.type = tarfile.REGTYPE
        ti.size = st.st_size
        with open(ap, "rb") as fh:
            tar.addfile(ti, fh)


def build_tar_gz(src_dir, out_path, order=None):
    """把 src_dir 打成确定性 tar.gz。order 给出成员顺序（相对路径列表），缺省排序。"""
    plain = out_path + ".tar"
    with tarfile.open(plain, "w", format=tarfile.GNU_FORMAT) as tar:
        if order:
            for rel in order:
                ap = os.path.join(src_dir, rel)
                if os.path.exists(ap):
                    add_path(tar, rel, ap)
        else:
            for rel, ap in sorted_entries(src_dir):
                add_path(tar, rel, ap)
    with open(out_path, "wb") as fh:
        subprocess.run(["gzip", "-9", "-n", "-c", plain], check=True, stdout=fh)
    os.unlink(plain)


def md5(path):
    h = hashlib.md5()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--src", required=True, help="fnpack 目录（含 app/、manifest、cmd/、config/、wizard/）")
    ap.add_argument("--out", required=True)
    args = ap.parse_args()

    src = os.path.abspath(args.src)
    out = os.path.abspath(args.out)

    with tempfile.TemporaryDirectory() as tmp:
        # 1) 内层 app.tgz
        app_tgz = os.path.join(tmp, "app.tgz")
        build_tar_gz(os.path.join(src, "app"), app_tgz)

        # 2) 准备外层骨架：manifest（checksum 重算）+ cmd/config/wizard/图标
        staging = os.path.join(tmp, "pack")
        os.makedirs(staging)
        for name in ("cmd", "config", "wizard"):
            path_in = os.path.join(src, name)
            if os.path.exists(path_in):  # 向导可选（部分测试包没有 wizard/）
                subprocess.run(["cp", "-a", path_in, staging], check=True)
        for name in ("ICON.PNG", "ICON_256.PNG", "LICENSE"):
            p = os.path.join(src, name)
            if os.path.exists(p):
                subprocess.run(["cp", "-a", p, staging], check=True)
        subprocess.run(["cp", "-a", app_tgz, os.path.join(staging, "app.tgz")], check=True)

        man_src = open(os.path.join(src, "manifest"), encoding="utf-8").read().splitlines(keepends=True)
        digest = md5(app_tgz)
        man_out, seen = [], 0
        for line in man_src:
            if line.strip().startswith("checksum") and "=" in line:
                man_out.append("checksum              = %s\n" % digest)
                seen += 1
            else:
                man_out.append(line)
        if seen == 0:
            man_out.append("checksum              = %s\n" % digest)
        with open(os.path.join(staging, "manifest"), "w", encoding="utf-8") as fh:
            fh.write("".join(man_out))

        # 3) 外层顺序与 fnpack 产物保持一致：app.tgz 在最前，manifest 在 wizard 之前
        order = ["app.tgz", "cmd", "cmd/config_callback", "cmd/config_init",
                 "cmd/install_callback", "cmd/install_init", "cmd/main",
                 "cmd/uninstall_callback", "cmd/uninstall_init",
                 "cmd/upgrade_callback", "cmd/upgrade_init",
                 "config", "config/privilege", "config/resource",
                 "ICON.PNG", "ICON_256.PNG", "LICENSE", "manifest",
                 "wizard", "wizard/install", "wizard/uninstall"]
        build_tar_gz(staging, out, order=order)

    print(f"✓ {out}（md5(app.tgz)={digest}）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
