#!/usr/bin/env python3
"""本体 fpk 的包纹自检（对外交付前逐项核对）。

七项口径（与验收清单一一对应）：
  1 manifest version 原样，不带 v 前缀
  2 两个包除 manifest 的 version 行外逐字节一致（给出成员级对照）
  3 app/.version 与运行时 tar 的声明版本、实物版本都等于 RUNTIME_VERSION
  4 ui/config 只剩一个 type=url / port=2299 / url=/ 入口
  5 图标 256x256、四角透明、sha 与其它项目不同
  6 全包（含 app.tgz 内部）grep yuexps 命中 0
  7 manifest description 与 README 都写了版本血缘

用法：
  python3 scripts/fingerprint.py --root <源码根> --rt 0.2.1-rc.2 \
      --main dist/deepseek.harness_v0.2.1-rc.3.1_x86.fpk \
      [--alt dist/deepseek.harness_v0.4.2_x86.fpk] \
      [--apps-root /vol1/1000/work/deepseek/fnos-apps]
退出码非 0 表示有检查项不通过。
"""
import argparse
import pathlib
import hashlib
import io
import json
import os
import re
import stat
import struct
import sys
import tarfile
import tempfile


def read_member(tf, name):
    """容忍 ./ 前缀差异读 tar 成员。"""
    for cand in (name, "./" + name.lstrip("./")):
        try:
            return tf.extractfile(cand).read()
        except KeyError:
            continue
    raise KeyError(f"tar 里找不到成员 {name}")


def sha_bytes(b):
    return hashlib.sha256(b).hexdigest()


def sha_file(p):
    h = hashlib.sha256()
    with open(p, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def norm(name):
    """只剥掉一次前导 "./"，不能用 lstrip —— 它会把 ".version" 的 "." 也吃掉。"""
    return name[2:] if name.startswith("./") else name


def members(fp):
    """返回 [(name, bytes)]，名字规范化去掉 ./ 前缀，顺序保持原样。"""
    out = []
    with tarfile.open(fp) as t:
        for m in t.getmembers():
            if not m.isfile():
                continue
            out.append((norm(m.name), t.extractfile(m).read()))
    return out


def manifest_fields(text):
    """按 ini 语义解析 manifest：; 与 # 开头是注释，key = value。"""
    fields = {}
    for line in text.splitlines():
        line = line.strip()
        if not line or line[0] in ";#":
            continue
        if "=" in line:
            k, v = line.split("=", 1)
            fields[k.strip()] = v.strip()
    return fields


def png_info(data):
    w, h = struct.unpack(">II", data[16:24])
    return w, h


def corner_alpha(data):
    """用 zlib 手工解码 PNG 太啰嗦；这里只做「有 PIL 就精确验、没有就跳过」。"""
    try:
        from PIL import Image  # type: ignore
    except Exception:
        return None
    im = Image.open(io.BytesIO(data)).convert("RGBA")
    return [im.getpixel(p)[3] for p in ((0, 0), (im.width - 1, 0), (0, im.height - 1), (im.width - 1, im.height - 1))]


def check_one(fp, rt, root, apps_root, want_version=None):
    """单项包纹检查，返回 (failures, facts)。"""
    fails, facts = [], {}
    with tempfile.TemporaryDirectory() as d:
        with tarfile.open(fp) as t:
            t.extractall(d)
        man_text = open(os.path.join(d, "manifest"), encoding="utf-8").read()
        man = manifest_fields(man_text)

        # 1) version 原样、不带 v
        ver = man.get("version", "")
        facts["version"] = ver
        if not ver or ver.startswith("v"):
            fails.append(f"manifest version={ver!r} 不该为空或以 v 开头")
        if want_version and ver != want_version:
            fails.append(f"manifest version={ver!r} 期望 {want_version!r}")

        # 3) .version + 运行时 tar（声明与实物）
        with tarfile.open(os.path.join(d, "app.tgz")) as t:
            inner = {norm(m.name): t.extractfile(m).read()
                     for m in t.getmembers() if m.isfile()}
        dot_ver = inner.get(".version", b"").decode().strip()
        facts["dot_version"] = dot_ver
        if dot_ver != rt:
            fails.append(f"app/.version={dot_ver!r} 期望 {rt!r}")

        # 运行时 tar 在 app.tgz 内部，直接从字节流读，不要落盘
        rt_decl = rt_real = None
        rt_bytes = inner.get("deepseek-harness.tar.gz")
        if rt_bytes:
            with tarfile.open(fileobj=io.BytesIO(rt_bytes), mode="r:gz") as rt_t:
                for m in rt_t.getmembers():
                    n = norm(m.name)
                    if n == "package.json":
                        pkg = json.loads(rt_t.extractfile(m).read())
                        deps = pkg.get("dependencies") or {}
                        rt_decl = deps.get("@deepseek-ai/dsh")
                    elif n.endswith("node_modules/@deepseek-ai/dsh/package.json"):
                        rt_real = json.loads(rt_t.extractfile(m).read()).get("version")
                    if rt_decl and rt_real:
                        break
        facts["tar_declared"] = rt_decl
        facts["tar_actual"] = rt_real
        if rt_decl != rt or rt_real != rt:
            fails.append(f"运行时 tar 声明={rt_decl!r} 实物={rt_real!r}，期望都是 {rt!r}")

        # 4) ui/config 单 url 入口
        cfg = json.loads(inner["ui/config"].decode())
        urls = cfg.get(".url", {})
        facts["ui_entries"] = {k: (v.get("type"), v.get("port"), v.get("url")) for k, v in urls.items()}
        if len(urls) != 1:
            fails.append(f"ui/config 入口数={len(urls)}，应为 1")
        else:
            e = next(iter(urls.values()))
            if e.get("type") != "url" or int(e.get("port") or 0) != 2299 or e.get("url") != "/":
                fails.append(f"ui/config 入口不对：{e}")
            if "gatewaySocket" in e or "gatewayPrefix" in e:
                fails.append("ui/config 残留网关 socket 字段")

        # 5) 图标
        icon = inner.get("ICON_256.PNG") or open(os.path.join(d, "ICON_256.PNG"), "rb").read()
        w, h = png_info(icon)
        facts["icon"] = f"{w}x{h} sha256={sha_bytes(icon)[:16]}…"
        if (w, h) != (256, 256):
            fails.append(f"ICON_256 尺寸 {w}x{h}")
        ca = corner_alpha(icon)
        facts["icon_corners"] = ca
        # 权限：应用中心按 /var/apps/<app>/ICON.PNG 取图，若以组身份读取，
        # 组无读权限（如 705）会退回默认占位图 —— harness-panel 1.0.0 装机踩过
        for nm in ("ICON.PNG", "ICON_256.PNG"):
            ip = os.path.join(d, nm)
            if os.path.exists(ip):
                mode = stat.S_IMODE(os.stat(ip).st_mode)
                facts[f"icon_mode_{nm}"] = oct(mode)
                if not (mode & 0o040):
                    fails.append(f"{nm} 权限 {oct(mode)} 组不可读，应用中心会显示占位图")
        if ca is not None and any(a != 0 for a in ca):
            fails.append(f"图标四角不透明：{ca}")
        if apps_root and os.path.isdir(apps_root):
            for other in sorted(os.listdir(apps_root)):
                for sub in ("fnpack", "fnpack-panel"):
                    for nm in ("ICON.PNG", "ICON_256.PNG"):
                        p = os.path.join(apps_root, other, sub, nm)
                        if os.path.exists(p):
                            s = sha_file(p)
                            if s == sha_bytes(icon):
                                fails.append(f"图标与 {other}/{sub}/{nm} 完全相同")

        # 6) yuexps 残留
        hits = []
        if b"yuexps" in man_text.encode():
            hits.append("manifest")
        for name, data in inner.items():
            if len(data) < 8 << 20 and b"yuexps" in data:
                hits.append(f"app.tgz:{name}")
        for nm in os.listdir(d):
            p = os.path.join(d, nm)
            if os.path.isfile(p) and nm != "app.tgz":
                if b"yuexps" in open(p, "rb").read():
                    hits.append(nm)
        facts["yuexps"] = hits
        if hits:
            fails.append(f"yuexps 命中 {hits[:4]}")

        # 7) desc 血缘 + manufacturer 信息
        desc = man.get("desc", "")
        facts["desc_len"] = len(desc)
        facts["desc_semicolon"] = ";" in desc
        facts["distributor"] = man.get("distributor")
        if "本仓双包改造第 1 号补丁" not in desc:
            fails.append("manifest desc 缺少版本血缘说明")
        if ";" in desc:
            fails.append("manifest description 含分号（ini 注释会截断）")
        if man.get("distributor") != "xiguan99":
            fails.append(f"distributor={man.get('distributor')!r} 期望 xiguan99")

    # 8) install_callback 必须保证装完自启（停着卸载的用户靠这条才不至于打不开）
    cbt = None
    try:
        with tarfile.open(fp) as t:
            cbt = t.extractfile(t.getmember("cmd/install_callback")).read().decode("utf-8", "replace")
    except KeyError:
        fails.append("缺少 cmd/install_callback")
    if cbt is not None:
        has_cfg = "config.json" in cbt and "last_run_state" in cbt and "running" in cbt
        facts["install_autostart"] = has_cfg
        if not has_cfg:
            fails.append("cmd/install_callback 未预置 last_run_state=running（停着卸载后重装不会自启）")

    readme = os.path.join(root, "README.md")
    readme_text = open(readme, encoding="utf-8").read() if os.path.exists(readme) else ""
    facts["readme_lineage"] = "本仓双包改造第 1 号补丁" in readme_text
    if not facts["readme_lineage"]:
        fails.append("README 缺少版本血缘说明")
    facts["readme_root_run"] = bool(re.search(r"以\s*`?root`?\s*运行", readme_text))
    if not facts["readme_root_run"]:
        fails.append("README 未如实说明「本体以 root 运行」")
    return fails, facts


def check_panel(fpk, root, apps_root=None, expect_version=None):
    """面板包 self-contained 包纹（七类检查）。"""
    fails, facts = [], {}
    with tempfile.TemporaryDirectory() as tmp:
        tarfile.open(fpk).extractall(tmp)
        man = open(os.path.join(tmp, "manifest"), encoding="utf-8").read()
        kv = {}
        for line in man.splitlines():
            if "=" in line and not line.strip().startswith((";", "#")):
                k, v = line.split("=", 1)
                kv[k.strip()] = v.strip()
        facts["appname"] = kv.get("appname")
        facts["version"] = kv.get("version")
        if kv.get("appname") != "harness-panel":
            fails.append(f"appname 应为 harness-panel，实际 {kv.get('appname')}")
        if expect_version and kv.get("version") != expect_version:
            fails.append(f"version 应为 {expect_version}，实际 {kv.get('version')}")
        if kv.get("version", "").lower().startswith("v"):
            fails.append("version 不应带 v 前缀")

        desc = kv.get("desc", "")
        facts["desc_len"] = len(desc)
        facts["desc_semicolon"] = ";" in desc
        if ";" in desc:
            fails.append("desc 含 ASCII 分号，ini 会截断")
        if "<NAS>" in desc or "&lt;" in desc:
            fails.append("desc 含 <NAS> 或坏实体")
        if "共 7 个页面" not in desc:
            fails.append("desc 未写明实际页面数（共 7 个页面）")
        facts["distributor"] = kv.get("distributor")
        if kv.get("distributor") != "xiguan99":
            fails.append("distributor 不是 xiguan99")

        # 图标
        icon = os.path.join(tmp, "ICON_256.PNG")
        if not os.path.exists(icon):
            fails.append("缺少 ICON_256.PNG")
        else:
            data = open(icon, "rb").read()
            w, h = struct.unpack(">II", data[16:24])
            facts["icon"] = f"{w}x{h} sha256={sha_bytes(data)[:16]}…"
            if (w, h) != (256, 256):
                fails.append(f"图标尺寸 {w}x{h}，应为 256x256")
            if apps_root:
                mine = sha_bytes(data)
                for other in sorted(pathlib.Path(apps_root).glob("*/fnpack*/ICON_256.PNG")):
                    if sha_bytes(open(other, "rb").read()) == mine:
                        fails.append(f"图标与 {other} 重复")

            # 权限：应用中心按 /var/apps/<app>/ICON.PNG 取图，若它以组身份读取，
            # 组无读权限（如 705）会退回默认占位图 —— harness-panel 1.0.0 装机踩过
            for nm in ("ICON.PNG", "ICON_256.PNG"):
                ip = os.path.join(tmp, nm)
                if os.path.exists(ip):
                    mode = stat.S_IMODE(os.stat(ip).st_mode)
                    facts[f"icon_mode_{nm}"] = oct(mode)
                    if not (mode & 0o040):
                        fails.append(f"{nm} 权限 {oct(mode)} 组不可读，应用中心会显示占位图")

        # app.tgz 内：ui/config + fnshell + 前端产物
        app_tgz = os.path.join(tmp, "app.tgz")
        with tarfile.open(app_tgz) as at:
            uic = json.loads(read_member(at, "ui/config").decode())
            entries = uic.get(".url", {})
            facts["ui_entries"] = {k: (v.get("type"), v.get("gatewayPrefix")) for k, v in entries.items()}
            if len(entries) != 1:
                fails.append(f"ui/config 入口数 {len(entries)}，应为 1")
            else:
                (k, v), = entries.items()
                if v.get("type") != "iframe":
                    fails.append("面板入口类型应为 iframe")
                if v.get("gatewayPrefix") != "/app/harness-panel":
                    fails.append("gatewayPrefix 应为 /app/harness-panel")
                if v.get("gatewaySocket") != "harness-panel.sock":
                    fails.append("gatewaySocket 应为 harness-panel.sock")
            shell = read_member(at, "fnshell.py").decode()
            facts["shell_token"] = "X-DSH-Control-Token" in shell
            facts["shell_ws_fix"] = 'headers["Connection"] = "Upgrade"' in shell or "headers['Connection'] = 'Upgrade'" in shell
            if not facts["shell_token"]:
                fails.append("fnshell.py 未注入控制令牌")
            if not facts["shell_ws_fix"]:
                fails.append("fnshell.py 缺 WebSocket 升级头修正")
            js = [m for m in at.getnames() if m.endswith(".js") and "panel_dist/assets/" in m]
            body = "".join(at.extractfile(m).read().decode("utf-8", "ignore") for m in js)
            facts["ws_relative"] = "location.pathname" in body and "app/deepseek-harness/api/ws" not in body
            if not facts["ws_relative"]:
                fails.append("前端产物 WS 地址非相对路径（仍写死网关前缀）")
            # 整包 grep yuexps
            hits = []
            for m in at.getmembers():
                if m.isfile() and m.size < 4 << 20:
                    try:
                        if b"yuexps" in at.extractfile(m).read():
                            hits.append(m.name)
                    except Exception:
                        pass
            facts["yuexps"] = hits
            if hits:
                fails.append(f"app.tgz 内命中 yuexps：{hits}")
        # wizard 必须非空：空数组会让应用中心空指针（code 10111 / APP_INSTALL_FAILED_PKG_EXCEPTION）
        wz = {}
        for nm in ("install", "uninstall"):
            mp = os.path.join(tmp, "wizard", nm)
            if not os.path.exists(mp):
                fails.append(f"缺少 wizard/{nm}")
                continue
            size = os.path.getsize(mp)
            wz[nm] = size
            if size <= 100:
                fails.append(f"wizard/{nm} 只有 {size} 字节，必须是真实配置（不能是空数组）")
                continue
            try:
                arr = json.load(open(mp, encoding="utf-8"))
            except Exception as exc:  # noqa: BLE001
                fails.append(f"wizard/{nm} 不是合法 JSON：{exc}")
                continue
            if not isinstance(arr, list) or not arr:
                fails.append(f"wizard/{nm} 必须是非空 JSON 数组")
        facts["wizard_bytes"] = wz

        return fails, facts


def compare(main_fp, alt_fp):
    """两个包成员级对照：应当只有 manifest 不同。"""
    a = dict(members(main_fp))
    b = dict(members(alt_fp))
    only_a = sorted(set(a) - set(b))
    only_b = sorted(set(b) - set(a))
    diff = [n for n in sorted(set(a) & set(b)) if a[n] != b[n]]
    return only_a, only_b, diff


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", required=True)
    ap.add_argument("--rt", required=True)
    ap.add_argument("--main")
    ap.add_argument("--alt")
    ap.add_argument("--apps-root", default=None)
    ap.add_argument("--main-version", default=None)
    ap.add_argument("--alt-version", default=None)
    ap.add_argument("--panel", default=None)
    ap.add_argument("--panel-version", default=None)
    args = ap.parse_args()

    all_fails = []

    if args.main:
        print("═" * 72)
        print(f"主选包 {os.path.basename(args.main)}")
        print("═" * 72)
        fails, facts = check_one(args.main, args.rt, args.root, args.apps_root, args.main_version)
        for k, v in facts.items():
            print(f"  {k:16} {v}")
        print(f"  文件大小          {os.path.getsize(args.main):,} B")
        print(f"  fpk sha256        {sha_file(args.main)}")
        print(f"  ✓ 七项全过" if not fails else f"  ✗ 不通过：{fails}")
        all_fails += fails

    if args.panel:
        print()
        print("═" * 72)
        print(f"面板包 {os.path.basename(args.panel)}")
        print("═" * 72)
        pfails, pfacts = check_panel(args.panel, args.root, args.apps_root, args.panel_version)
        for k, v in pfacts.items():
            print(f"  {k:16} {v}")
        print(f"  文件大小          {os.path.getsize(args.panel):,} B")
        print(f"  fpk sha256        {sha_file(args.panel)}")
        print(f"  ✓ 面板包纹全过" if not pfails else f"  ✗ 不通过：{pfails}")
        all_fails += pfails

    if args.alt:
        print()
        print("═" * 72)
        print(f"备选包 {os.path.basename(args.alt)}")
        print("═" * 72)
        fails2, facts2 = check_one(args.alt, args.rt, args.root, args.apps_root, args.alt_version)
        for k, v in facts2.items():
            print(f"  {k:16} {v}")
        print(f"  文件大小          {os.path.getsize(args.alt):,} B")
        print(f"  fpk sha256        {sha_file(args.alt)}")
        print(f"  ✓ 七项全过" if not fails2 else f"  ✗ 不通过：{fails2}")
        all_fails += fails2

        print()
        print("── 两包成员级对照（2）──")
        only_a, only_b, diff = compare(args.main, args.alt)
        print(f"  只在主选包里：{only_a or '无'}")
        print(f"  只在备选包里：{only_b or '无'}")
        print(f"  内容不同的成员：{diff}")
        if only_a or only_b or diff != ["manifest"]:
            all_fails.append(f"两包差异不是「只有 manifest」：{diff}")

    print()
    if all_fails:
        print("✗ 包纹自检未通过：")
        for f in all_fails:
            print(f"    - {f}")
        return 1
    print("✓ 包纹自检全部通过")
    return 0


if __name__ == "__main__":
    sys.exit(main())
