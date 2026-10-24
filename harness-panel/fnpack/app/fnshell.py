#!/usr/bin/env python3
"""harness-panel 壳

职责只有四件事：
  1. 监听飞牛网关给的 Unix socket（$TRIM_APPDEST/harness-panel.sock），
     同时保留一个 TCP 端口（默认 2296）给浏览器/手机直连与排障。
  2. 托管面板前端静态文件（--dist），本应用不跑第二套后端进程。
  3. 把 /api/* 反代到本体控制面 127.0.0.1:2297/app/deepseek-harness/api/*，
     并自动带上控制令牌（读本体写的 control.token），WebSocket 直通。
  4. 访问控制：默认只信局域网网段；非可信网段必须设访问密码才能进，
     连续失败 5 次后按 IP 延后。没设密码又不来自可信网段，直接 401。

面板自身不保存任何会话/模型数据，全部数据在本体那边。
"""
import argparse
import hashlib
import hmac
import http.client
import ipaddress
import json
import os
import secrets
import socket  # noqa: F401  (UnixStreamServer 需要 AF_UNIX)
import socketserver
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, quote, unquote, urlsplit

VERSION = "1.0.0"

HOP_BY_HOP = {
    "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
    "te", "trailers", "transfer-encoding", "upgrade",
}
BODYLESS = {"HEAD"}
MIME = {
    ".html": "text/html; charset=utf-8",
    ".js": "application/javascript; charset=utf-8",
    ".mjs": "application/javascript; charset=utf-8",
    ".css": "text/css; charset=utf-8",
    ".json": "application/json; charset=utf-8",
    ".svg": "image/svg+xml",
    ".png": "image/png",
    ".jpg": "image/jpeg",
    ".jpeg": "image/jpeg",
    ".webp": "image/webp",
    ".ico": "image/x-icon",
    ".woff": "font/woff",
    ".woff2": "font/woff2",
    ".ttf": "font/ttf",
    ".map": "application/json; charset=utf-8",
    ".txt": "text/plain; charset=utf-8",
}

DEFAULT_TRUSTED = ["127.0.0.0/8", "::1/128", "10.0.0.0/8",
                   "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16"]


def log(msg: str) -> None:
    print(f"[panel] {msg}", flush=True)


# ── 配置与访问控制 ─────────────────────────────────────────


class PanelConfig:
    """shell_config.json：访问密码、可信网段、令牌路径。"""

    def __init__(self, path: str):
        self.path = path
        self.password_hash = ""
        self.password_salt = ""
        self.trusted = list(DEFAULT_TRUSTED)
        self.token_file = os.environ.get(
            "DSH_CONTROL_TOKEN_FILE", "/vol1/@appdata/deepseek.harness/control.token")
        self.load()

    def load(self):
        try:
            with open(self.path, "r", encoding="utf-8") as fh:
                data = json.load(fh)
        except FileNotFoundError:
            self.save()
            return
        except Exception as exc:  # 配置坏了不能让面板起不来
            log(f"读取配置失败，使用默认值：{exc}")
            return
        self.password_hash = str(data.get("password_hash") or "")
        self.password_salt = str(data.get("password_salt") or "")
        nets = data.get("trusted_networks")
        if isinstance(nets, list) and nets:
            self.trusted = [str(n) for n in nets]
        self.token_file = str(data.get("token_file") or self.token_file)

    def save(self):
        data = {
            "password_hash": self.password_hash,
            "password_salt": self.password_salt,
            "trusted_networks": self.trusted,
            "token_file": self.token_file,
        }
        try:
            os.makedirs(os.path.dirname(self.path), exist_ok=True)
            tmp = self.path + ".tmp"
            with open(tmp, "w", encoding="utf-8") as fh:
                json.dump(data, fh, ensure_ascii=False, indent=2)
            os.chmod(tmp, 0o600)
            os.replace(tmp, self.path)
        except Exception as exc:
            log(f"写入配置失败：{exc}")

    def set_password(self, password: str):
        if not password:
            self.password_hash = ""
            self.password_salt = ""
        else:
            self.password_salt = secrets.token_hex(8)
            self.password_hash = hashlib.pbkdf2_hmac(
                "sha256", password.encode(), self.password_salt.encode(), 120000).hex()
        self.save()

    def check_password(self, password: str) -> bool:
        if not self.password_hash:
            return False
        calc = hashlib.pbkdf2_hmac(
            "sha256", password.encode(), self.password_salt.encode(), 120000).hex()
        return hmac.compare_digest(calc, self.password_hash)

    def is_trusted(self, ip: str) -> bool:
        try:
            addr = ipaddress.ip_address(ip)
        except ValueError:
            return False
        for net in self.trusted:
            try:
                if addr in ipaddress.ip_network(net, strict=False):
                    return True
            except ValueError:
                continue
        return False


class FailTracker:
    """连续失败计数：5 次后按 IP 延后（60s 起，翻倍封顶 15 分钟）。"""

    def __init__(self):
        self.lock = threading.Lock()
        self.data = {}

    def blocked_for(self, ip: str) -> int:
        with self.lock:
            until, _ = self.data.get(ip, (0.0, 0))
            return max(0, int(until - time.time())) if until > time.time() else 0

    def fail(self, ip: str) -> int:
        with self.lock:
            until, count = self.data.get(ip, (0.0, 0))
            count += 1
            delay = 0
            if count >= 5:
                delay = min(60 * (2 ** (count - 5)), 900)
                self.data[ip] = (time.time() + delay, count)
            else:
                self.data[ip] = (until, count)
            return delay

    def ok(self, ip: str):
        with self.lock:
            self.data.pop(ip, None)


# ── 控制令牌 ───────────────────────────────────────────────


class TokenCache:
    """读本体写的 control.token（0640，同组可读），带 mtime 缓存。"""

    def __init__(self, path: str):
        self.path = path
        self.value = ""
        self.mtime = 0.0
        self.lock = threading.Lock()

    def get(self) -> str:
        try:
            st = os.stat(self.path)
        except OSError:
            return ""
        with self.lock:
            if st.st_mtime != self.mtime:
                try:
                    with open(self.path, "r", encoding="utf-8") as fh:
                        self.value = fh.read().strip()
                    self.mtime = st.st_mtime
                except OSError as exc:
                    log(f"读取控制令牌失败：{exc}")
                    return ""
            return self.value


# ── 静态文件 ───────────────────────────────────────────────


def guess_mime(path: str) -> str:
    ext = os.path.splitext(path)[1].lower()
    return MIME.get(ext, "application/octet-stream")


# ── 请求处理 ───────────────────────────────────────────────


class ShellHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = f"harness-panel-shell/{VERSION}"
    sys_version = ""

    def log_message(self, fmt, *args):
        pass

    def do_GET(self): self.handle_all()          # noqa: E704
    def do_HEAD(self): self.handle_all()         # noqa: E704
    def do_POST(self): self.handle_all()         # noqa: E704
    def do_PUT(self): self.handle_all()          # noqa: E704
    def do_PATCH(self): self.handle_all()        # noqa: E704
    def do_DELETE(self): self.handle_all()       # noqa: E704
    def do_OPTIONS(self): self.handle_all()      # noqa: E704

    # ── 工具 ────────────────────────────────
    @property
    def cfg(self) -> PanelConfig:
        return self.server.cfg

    def client_ip(self) -> str:
        peer = (self.client_address[0] if self.client_address else "") or ""
        prior = (self.headers.get("X-Real-IP") or "").strip()
        if prior:
            return prior
        xff = (self.headers.get("X-Forwarded-For") or "").strip()
        if xff:
            return xff.split(",")[0].strip()
        # Unix socket 连接没有 IP：那是本机的飞牛网关转发过来的。
        # 不这么兜底的话，网关一旦不送 X-Real-IP，所有外网访问都会被判成
        # "未知来源 + 没设密码" 而 401，面板等于打不开。
        if not peer:
            return "127.0.0.1"
        return peer

    def _send(self, status: int, body: bytes, ctype: str, extra_headers=None):
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        for key, value in (extra_headers or []):
            self.send_header(key, value)
        self.end_headers()
        if self.command not in BODYLESS:
            self.wfile.write(body)

    def _html(self, status: int, html: str, extra_headers=None):
        self._send(status, html.encode("utf-8"), "text/html; charset=utf-8", extra_headers)

    # ── 鉴权 ────────────────────────────────
    def auth_cookie(self) -> str:
        raw = self.headers.get("Cookie") or ""
        for part in raw.split(";"):
            name, _, value = part.strip().partition("=")
            if name == "hp_auth":
                return unquote(value)
        return ""

    def auth_ok(self) -> bool:
        ip = self.client_ip()
        if self.server.fails.blocked_for(ip) > 0:
            return False
        if not self.cfg.password_hash:
            return self.cfg.is_trusted(ip)
        expected = hmac.new(self.cfg.password_salt.encode(),
                            b"hp-session", hashlib.sha256).hexdigest()
        return self.auth_cookie() == expected

    def login_page(self, error: str = "") -> str:
        err = f"<p class='err'>{error}</p>" if error else ""
        return f"""<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Harness 面板 · 登录</title>
<style>
 body{{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;
   font-family:-apple-system,"PingFang SC","Microsoft YaHei",system-ui,sans-serif;background:#f5f7fa;color:#1f2937}}
 @media (prefers-color-scheme:dark){{body{{background:#12141a;color:#e5e7eb}}form{{background:#1b1e25;border-color:#2a2f3a}}}}
 form{{background:#fff;border:1px solid #e5e7eb;border-radius:14px;padding:26px 24px;width:min(92vw,340px);box-shadow:0 1px 2px rgba(16,24,40,.06)}}
 h1{{font-size:16px;margin:0 0 6px}} p{{font-size:12.5px;color:#6b7280;margin:0 0 14px;line-height:1.6}}
 input{{width:100%;box-sizing:border-box;padding:9px 11px;border:1px solid #d1d5db;border-radius:8px;font-size:14px;margin-bottom:12px}}
 button{{width:100%;padding:9px;border:0;border-radius:8px;background:#3b82f6;color:#fff;font-size:14px;cursor:pointer}}
 .err{{color:#dc2626}}
</style></head><body>
<form method="POST" action="{self.server.prefix}/__login">
  <h1>Harness 面板</h1>
  <p>该面板设置了访问密码，请输入后继续。</p>{err}
  <input type="password" name="password" placeholder="访问密码" autofocus>
  <button type="submit">进入面板</button>
</form></body></html>"""

    # ── 主流程 ──────────────────────────────
    def handle_all(self):
        prefix = self.server.prefix
        raw = self.path or "/"
        parts = urlsplit(raw)
        path = parts.path

        # 入口缺尾斜杠 → 301 归位（相对资源靠它才能对）
        if prefix and path == prefix:
            self.send_response(301)
            self.send_header("Location", prefix + "/" + (("?" + parts.query) if parts.query else ""))
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        if prefix and path.startswith(prefix + "/"):
            path = path[len(prefix):]

        ip = self.client_ip()
        wait = self.server.fails.blocked_for(ip)
        if wait > 0:
            self._html(429, f"<h3>尝试过于频繁</h3><p>请在 {wait} 秒后重试。</p>")
            return

        # 登录提交
        if path == "/__login" and self.command == "POST":
            self.handle_login(ip)
            return

        if not self.auth_ok():
            if self.cfg.password_hash:
                self._html(401, self.login_page())
            else:
                self._html(401, f"""<!doctype html><meta charset="utf-8">
<title>Harness 面板</title>
<body style="font:15px/1.8 system-ui;padding:48px;max-width:640px">
<h2>需要授权</h2>
<p>当前来源 <code>{ip}</code> 不在可信网段内，且面板没有设置访问密码，因此被拒绝。</p>
<p>两种解决办法：</p>
<ol><li>在局域网内访问（192.168/10/172.16 网段默认放行）；</li>
<li>在面板数据目录的 <code>shell_config.json</code> 里设置访问密码，或用可信网段内的浏览器打开后设置。</li></ol>
</body>""")
            return

        if path == "/__login":
            self.send_response(302)
            self.send_header("Location", prefix + "/")
            self.send_header("Content-Length", "0")
            self.end_headers()
            return

        if path == "/__panel/info":
            self._send(200, json.dumps({
                "version": VERSION,
                "prefix": prefix,
                "client_ip": ip,
                "trusted": self.cfg.is_trusted(ip),
                "password_set": bool(self.cfg.password_hash),
                "backend": f"{self.server.target_host}:{self.server.target_port}",
                "control_token": bool(self.server.tokens.get()),
            }, ensure_ascii=False).encode(), "application/json; charset=utf-8")
            return

        if path == "/api" or path.startswith("/api/"):
            self.proxy_api(path, parts.query)
            return

        self.serve_static(path)

    def handle_login(self, ip: str):
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length) if length else b""
        password = (parse_qs(body.decode("utf-8", "replace")).get("password") or [""])[0]
        if self.cfg.check_password(password):
            self.server.fails.ok(ip)
            expected = hmac.new(self.cfg.password_salt.encode(),
                                b"hp-session", hashlib.sha256).hexdigest()
            self._html(302, "", [
                ("Location", self.server.prefix + "/"),
                ("Set-Cookie",
                 f"hp_auth={quote(expected)}; Path={self.server.prefix}/; HttpOnly; SameSite=Lax; Max-Age=604800"),
                ("Content-Length", "0"),
            ])
            return
        delay = self.server.fails.fail(ip)
        msg = "密码错误。" + (f"尝试过多，请 {delay} 秒后再试。" if delay else "")
        self._html(401, self.login_page(msg))

    def serve_static(self, path: str):
        dist = self.server.dist
        rel = path.lstrip("/") or "index.html"
        full = os.path.normpath(os.path.join(dist, rel))
        if not full.startswith(os.path.normpath(dist)):
            self._html(403, "<h3>403</h3>")
            return
        if os.path.isdir(full):
            full = os.path.join(full, "index.html")
        if not os.path.isfile(full):
            # SPA 兜底：找不到的静态路径回首页
            full = os.path.join(dist, "index.html")
            if not os.path.isfile(full):
                self._html(404, "<h3>面板资源缺失</h3><p>请重新安装应用。</p>")
                return
        try:
            with open(full, "rb") as fh:
                data = fh.read()
        except OSError as exc:
            self._html(500, f"<h3>读取资源失败</h3><p>{exc}</p>")
            return
        headers = [("Cache-Control", "no-cache")]
        if full.endswith("index.html"):
            headers = [("Cache-Control", "no-store")]
        self._send(200, data, guess_mime(full), headers)

    # ── 控制面反代 ──────────────────────────
    def backend_path(self, path: str) -> str:
        # 面板请求 /api/xxx → 本体 /app/deepseek-harness/api/xxx
        return self.server.backend_base + path

    def read_body(self) -> bytes:
        try:
            length = int(self.headers.get("Content-Length") or 0)
        except ValueError:
            length = 0
        return self.rfile.read(length) if length > 0 else b""

    def proxy_api(self, path: str, query: str):
        token = self.server.tokens.get()
        if not token:
            self._html(503, "<h3>本体未就绪</h3><p>读不到控制令牌，请确认 Hermes 本体（deepseek.harness）"
                            " 已启动。面板需要本机同一 AppUsers 组的读权限。</p>")
            return

        dest = self.backend_path(path) + (("?" + query) if query else "")
        body = self.read_body()

        headers = {"Host": f"{self.server.target_host}:{self.server.target_port}",
                   "X-DSH-Control-Token": token,
                   "X-Real-IP": self.client_ip()}
        for key, value in self.headers.items():
            low = key.lower()
            if low in HOP_BY_HOP or low in ("host", "content-length", "accept-encoding",
                                            "x-dsh-control-token"):
                continue
            headers[key] = value
        if body:
            headers["Content-Length"] = str(len(body))

        # WebSocket：走裸连接隧道
        if (self.headers.get("Upgrade") or "").lower() == "websocket":
            self.tunnel_ws(dest, headers)
            return

        conn = http.client.HTTPConnection(self.server.target_host,
                                          self.server.target_port, timeout=300)
        try:
            conn.request(self.command, dest, body=body or None, headers=headers)
            resp = conn.getresponse()
            self.relay(resp)
        except Exception as exc:
            self._html(502, f"<h3>控制面不可用</h3><p>{exc}</p>"
                            f"<p style='color:#8b949e'>本体可能正在重启，稍后重试。</p>")
        finally:
            conn.close()

    def relay(self, resp):
        prefix = self.server.prefix
        length_hdr = resp.getheader("Content-Length")
        ctype = (resp.getheader("Content-Type") or "").lower()
        is_stream = ("text/event-stream" in ctype) or (length_hdr is None)

        self.send_response(resp.status)
        for key, value in resp.getheaders():
            low = key.lower()
            if low in HOP_BY_HOP or low == "content-length":
                continue
            if low == "location" and value.startswith("/") and prefix:
                value = prefix + value
            self.send_header(key, value)
        if not is_stream and length_hdr is not None:
            self.send_header("Content-Length", length_hdr)
        else:
            self.send_header("Transfer-Encoding", "chunked")
        self.send_header("X-Accel-Buffering", "no")
        if is_stream:
            self.send_header("Cache-Control", "no-cache")
        self.end_headers()

        if self.command in BODYLESS:
            return
        if not is_stream and length_hdr is not None:
            remaining = int(length_hdr)
            while remaining > 0:
                block = resp.read(min(65536, remaining))
                if not block:
                    break
                self.wfile.write(block)
                remaining -= len(block)
            self.wfile.flush()
            return
        while True:
            block = resp.read(4096)
            if not block:
                break
            self.wfile.write(b"%x\r\n" % len(block) + block + b"\r\n")
            self.wfile.flush()
        self.wfile.write(b"0\r\n\r\n")
        self.wfile.flush()

    def tunnel_ws(self, dest: str, headers: dict):
        # 关键：HOP_BY_HOP 里剔掉了 Connection/Upgrade，转发普通请求时是对的，
        # 但 WebSocket 握手恰好需要这两个头。少了它们，Gin/gorilla 会直接
        # 400 Bad Request，前端拿不到状态推送，概览就一直显示"已停止"。
        headers = dict(headers)
        headers["Connection"] = "Upgrade"
        headers["Upgrade"] = "websocket"
        upstream = socket.create_connection(
            (self.server.target_host, self.server.target_port), timeout=300)
        try:
            lines = [f"GET {dest} HTTP/1.1"]
            for key, value in headers.items():
                if key.lower() == "content-length":
                    continue
                lines.append(f"{key}: {value}")
            lines.append("")
            upstream.sendall("\r\n".join(lines).encode("latin-1") + b"\r\n")
            upstream.settimeout(30)
            # 读上游响应头
            buf = b""
            while b"\r\n\r\n" not in buf:
                chunk = upstream.recv(4096)
                if not chunk:
                    break
                buf += chunk
            if b"\r\n\r\n" not in buf:
                self._html(502, "<h3>WebSocket 握手失败</h3>")
                return
            head, rest = buf.split(b"\r\n\r\n", 1)
            status_line = head.split(b"\r\n")[0].decode("latin-1", "replace")
            self.wfile.write(head + b"\r\n\r\n")
            if rest:
                self.wfile.write(rest)
            self.wfile.flush()
            if " 101 " not in status_line:
                return
            upstream.settimeout(None)
            self.connection.settimeout(None)

            def pump(src, dst):
                try:
                    while True:
                        data = src.recv(65536)
                        if not data:
                            break
                        dst.sendall(data)
                except Exception:
                    pass
                finally:
                    try:
                        dst.shutdown(socket.SHUT_WR)
                    except Exception:
                        pass

            t = threading.Thread(target=pump, args=(self.connection, upstream), daemon=True)
            t.start()
            pump(upstream, self.connection)
            t.join(timeout=1)
        finally:
            try:
                upstream.close()
            except Exception:
                pass
            self.close_connection = True


# ── 服务器 ─────────────────────────────────────────────────


if hasattr(socketserver, "UnixStreamServer"):
    BaseUnixServer = socketserver.UnixStreamServer
else:  # 极老 Python 兜底
    class BaseUnixServer(socketserver.TCPServer):
        address_family = getattr(socket, "AF_UNIX", socket.AF_INET)


class ThreadingUnixHTTP(socketserver.ThreadingMixIn, BaseUnixServer):
    request_queue_size = 128
    daemon_threads = True

    def __init__(self, socket_path: str):
        if os.path.exists(socket_path):
            try:
                os.unlink(socket_path)
            except OSError:
                pass
        super().__init__(socket_path, ShellHandler)
        try:
            os.chmod(socket_path, 0o660)  # 收窄：同用户/同组（AppUsers）可连，别开成 0666
        except OSError:
            pass


class ThreadingTCPHTTP(ThreadingHTTPServer):
    request_queue_size = 128
    daemon_threads = True
    allow_reuse_address = True


def attach(server, cfg, tokens, dist, prefix, backend_base, host, port):
    server.cfg = cfg
    server.tokens = tokens
    server.dist = dist
    server.prefix = prefix
    server.backend_base = backend_base
    server.target_host = host
    server.target_port = port
    server.fails = FAILS
    return server


FAILS = FailTracker()


def main() -> int:
    ap = argparse.ArgumentParser(description="harness-panel 壳")
    ap.add_argument("--socket", default="", help="飞牛网关 Unix socket 路径")
    ap.add_argument("--prefix", default="/app/harness-panel", help="飞牛网关前缀")
    ap.add_argument("--dist", default="", help="面板前端静态文件目录")
    ap.add_argument("--backend", default="127.0.0.1:2297", help="本体控制面地址")
    ap.add_argument("--backend-base", default="/app/deepseek-harness",
                    help="本体控制面路径前缀")
    ap.add_argument("--tcp", type=int, default=2296, help="直连端口（0 表示不监听）")
    ap.add_argument("--tcp-host", default="0.0.0.0", help="直连端口绑定地址")
    ap.add_argument("--state", default=os.environ.get("TRIM_PKGVAR", ""),
                    help="面板数据目录（shell_config.json 所在）")
    args = ap.parse_args()

    if not args.dist or not os.path.isdir(args.dist):
        log(f"静态目录不可用：{args.dist}")
        return 1

    host, _, port = args.backend.rpartition(":")
    host = host or "127.0.0.1"
    port = int(port)

    state = args.state or "/tmp"
    cfg = PanelConfig(os.path.join(state, "shell_config.json"))
    tokens = TokenCache(cfg.token_file)
    prefix = args.prefix.rstrip("/")

    servers = []
    if args.socket:
        uds = ThreadingUnixHTTP(args.socket)
        attach(uds, cfg, tokens, args.dist, prefix, args.backend_base, host, port)
        servers.append(("unix", args.socket, uds))
        log(f"网关 socket 已监听 {args.socket}（前缀 {prefix or '/'}）")

    if args.tcp:
        tcp = ThreadingTCPHTTP((args.tcp_host, args.tcp), ShellHandler)
        attach(tcp, cfg, tokens, args.dist, prefix, args.backend_base, host, port)
        servers.append(("tcp", f"{args.tcp_host}:{args.tcp}", tcp))
        log(f"直连端口已监听 {args.tcp_host}:{args.tcp}")

    if not servers:
        log("没有任何监听目标，退出")
        return 1

    log(f"静态资源 {args.dist}")
    log(f"控制面 {host}:{port}{args.backend_base}（令牌 {cfg.token_file}）")
    log(f"可信网段 {', '.join(cfg.trusted)}；访问密码{'已设置' if cfg.password_hash else '未设置'}")

    for kind, addr, srv in servers[1:]:
        threading.Thread(target=srv.serve_forever, name=f"{kind}-{addr}", daemon=True).start()

    try:
        servers[0][2].serve_forever()
    except KeyboardInterrupt:
        log("收到中断")
    finally:
        for _, _, srv in servers:
            try:
                srv.server_close()
            except Exception:
                pass
        if args.socket and os.path.exists(args.socket):
            try:
                os.unlink(args.socket)
            except OSError:
                pass
    return 0


if __name__ == "__main__":
    sys.exit(main())
