"""启动托管站或中转站的本机预览；两站使用独立 SQLite、密钥和主机名，不接入真实账号或支付。"""

import argparse

import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import sqlite3
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import URLError
from urllib.request import Request, build_opener, ProxyHandler


ROOT = Path(__file__).resolve().parents[1]
SITE = "retail"
STATE = ROOT / ".local-tests/retail-preview"
BINARY = STATE / "new-api"
PORT = "18400"
APP_URL = "http://127.0.0.1:18400"
UPSTREAM_URL = "http://127.0.0.1:18401"
PASSWORD = "LuoboPreview2026!"
REDEMPTION = "luobopreview202611140000000000001"
# 仅对固定回环地址请求；不继承可能将 localhost 请求送往外部的代理设置。
HTTP = build_opener(ProxyHandler({}))


def api(path, body=None, access=None, method=None):
    """调用固定本地实例的管理接口；任何业务错误均停止初始化，避免生成半配置的演示环境。"""
    headers = {"Content-Type": "application/json", "Origin": APP_URL}
    if access:
        headers.update({"Authorization": "Bearer " + access, "New-Api-User": "1"})
    payload = None if body is None else json.dumps(body).encode()
    request = Request(APP_URL + path, data=payload, headers=headers, method=method)
    with HTTP.open(request, timeout=8) as response:
        result = json.load(response)
    if result.get("success") is False:
        raise RuntimeError(f"本地接口 {path} 失败：{result.get('message')}")
    return result


class DemoUpstream(BaseHTTPRequestHandler):
    """返回明确标注的合成文本和固定用量，使真实网关能够展示选路、日志及钱包结算。"""

    def log_message(self, *_args):
        """不记录请求路径或请求体；此服务不需要收集用户输入。"""

    def do_POST(self):
        """只支持三个文本协议与有限请求体；不会根据输入访问其他网络地址。"""
        size = int(self.headers.get("Content-Length", "0"))
        if size < 0 or size > 65536:
            self.send_error(413)
            return
        try:
            request = json.loads(self.rfile.read(size))
        except (ValueError, UnicodeError):
            self.send_error(400)
            return
        if self.path not in ("/v1/chat/completions", "/v1/messages", "/v1/responses"):
            self.send_error(404)
            return
        if request.get("stream"):
            # 该预览只提供同步合成上游；拒绝流式，避免返回伪装成 SSE 的 JSON。
            self.send_error(400, "Local preview upstream supports non-streaming requests only")
            return
        text = "本地演示响应：请求已通过中转站，使用记录和余额由本地后端结算。"
        model = request.get("model", "gpt-4o-mini")
        rid = "demo-" + secrets.token_hex(8)
        if self.path == "/v1/messages":
            result = {"id": rid, "type": "message", "role": "assistant", "model": model,
                      "content": [{"type": "text", "text": text}], "stop_reason": "end_turn",
                      "usage": {"input_tokens": 1200, "output_tokens": 180}}
        elif self.path == "/v1/responses":
            result = {"id": rid, "object": "response", "status": "completed", "model": model,
                      "output": [{"id": "msg-" + rid, "type": "message", "role": "assistant",
                                  "status": "completed", "content": [{"type": "output_text", "text": text}]}],
                      "usage": {"input_tokens": 1200, "output_tokens": 180, "total_tokens": 1380}}
        else:
            result = {"id": rid, "object": "chat.completion", "created": int(time.time()), "model": model,
                      "choices": [{"index": 0, "message": {"role": "assistant", "content": text}, "finish_reason": "stop"}],
                      "usage": {"prompt_tokens": 1200, "completion_tokens": 180, "total_tokens": 1380}}
        payload = json.dumps(result, ensure_ascii=False).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def start_app(log):
    """用最小环境启动本地二进制，不继承生产 DSN、商户或托管配置。"""
    env = {key: os.environ[key] for key in ("PATH", "HOME", "TMPDIR") if key in os.environ}
    env.update({"BIND_ADDRESS": "127.0.0.1", "PORT": PORT, "SITE_MODE": SITE, "GIN_MODE": "release",
                "SQLITE_PATH": str(STATE / "new-api.db"), "SESSION_SECRET": (STATE / "session-secret").read_text(),
                "GPT_HOSTING_ENABLED": "true" if SITE == "hosting" else "false",
                "DEFAULT_LANGUAGE": "zh-CN", "BATCH_UPDATE_ENABLED": "false", "MEMORY_CACHE_ENABLED": "false",
                "VERSION": "luobocode-local-preview", "SHUTDOWN_TIMEOUT_SECONDS": "3"})
    if SITE == "retail":
        env["RETAIL_RELAY_CONFIG"] = str(STATE / "retail.json")
    process = subprocess.Popen([str(BINARY), "--log-dir", str(STATE / "logs")],
                               cwd=STATE, env=env, stdout=log, stderr=log)
    try:
        for _ in range(100):
            if process.poll() is not None:
                raise RuntimeError(f"本地后端启动失败，请查看 {STATE / 'server.log'}")
            try:
                api("/api/setup")
                return process
            except (URLError, TimeoutError):
                time.sleep(0.2)
        raise RuntimeError("本地后端启动超时")
    except BaseException:
        stop_app(process)
        raise


def stop_app(process):
    """只停止本脚本创建的子进程；先正常退出，超时再终止，避免影响用户其他开发服务。"""
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=8)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def seed_database():
    """仅初始化当前预览目录的新库；通过产品接口创建用户与渠道，再补充明确标注的演示订单。"""
    api("/api/setup", {"username": "previewadmin", "password": PASSWORD, "confirmPassword": PASSWORD,
                       "SelfUseModeEnabled": False, "DemoSiteEnabled": False})
    access = api("/api/user/login", {"username": "previewadmin", "password": PASSWORD})["data"]["access_token"]
    user_id = api("/api/user/", {"username": "preview", "password": PASSWORD,
                               "display_name": "预览用户", "role": 1}, access)["data"]["id"]
    options = {"SystemName": "luobocode · 中转站预览" if SITE == "retail" else "luobocode · 托管站预览", "RegisterEnabled": "false",
               "GroupRatio": json.dumps({"default": 1, "retail": 1}),
               "ModelRatio": json.dumps({"gpt-4o-mini": 0.075, "claude-sonnet-4-5": 1.5}),
               "CompletionRatio": json.dumps({"gpt-4o-mini": 4, "claude-sonnet-4-5": 5}),
               "USDExchangeRate": "7.2", "Price": "7.2", "LogConsumeEnabled": "true",
               "DataExportEnabled": "true",
               # 本地轻量预览按启动时的实际剩余空间检查，避免大容量开发盘的百分比误拦；生产默认值不变。
               "performance_setting.monitor_disk_threshold": "0"}
    for key, value in options.items():
        api("/api/option/", {"key": key, "value": value}, access, method="PUT")
    if SITE == "hosting":
        # 托管预览只展示默认套餐与空账号；不创建供货渠道、充值订单或虚假的已授权账号。
        with sqlite3.connect(STATE / "new-api.db") as db:
            db.execute("UPDATE users SET quota = 0")
            db.execute("UPDATE hosting_plans SET enabled = 1")
        (STATE / "initialized").write_text("luobocode-hosting-preview-v1\n")
        return
    channels = [(1, "自有订阅 · 本地演示", "gpt-4o-mini"), (14, "第三方中转 · 本地演示", "claude-sonnet-4-5")]
    for channel_type, name, models in channels:
        api("/api/channel/", {"mode": "single", "channel": {"type": channel_type, "name": name,
            "key": "local-preview-upstream", "status": 1, "models": models, "group": "retail",
            "base_url": UPSTREAM_URL, "priority": 10, "weight": 10}}, access)
    # 仅访问由本脚本新建的固定路径。所有余额与订单都是演示数据，不从线上复制。
    now = int(time.time())
    with sqlite3.connect(STATE / "new-api.db") as db:
        # 这两个开关仅是隔离演示库的测试夹具，不调用真实声明确认接口，也不配置任何商户。
        for key, value in [("payment_setting.compliance_confirmed", "true"),
                           ("payment_setting.compliance_terms_version", "v1")]:
            db.execute("INSERT INTO options(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", (key, value))
        db.execute("UPDATE users SET quota = ?, display_name = ? WHERE id = ?", (25000000, "预览用户", user_id))
        db.execute("UPDATE users SET display_name = ? WHERE username = ?", ("预览管理员", "previewadmin"))
        db.execute("INSERT INTO redemptions(user_id,key,status,name,quota,created_time,expired_time) VALUES(?,?,?,?,?,?,?)",
                   (1, REDEMPTION, 1, "本地预览充值", 5000000, now, 0))
        for amount, status, offset in [(20, "success", 86400), (10, "pending", 600), (5, "paid_review", 3600)]:
            db.execute("INSERT INTO top_ups(user_id,amount,money,trade_no,payment_method,payment_provider,create_time,complete_time,status) VALUES(?,?,?,?,?,?,?,?,?)",
                       (user_id, amount, amount * 7.2, f"DEMO-{now}-{amount}", "alipay_official", "alipay_official",
                        now-offset, now-offset if status == "success" else 0, status))
        ids = [row[0] for row in db.execute("SELECT id FROM channels ORDER BY id")]
    (STATE / "retail.json").write_text(json.dumps({"enabled": True, "group": "retail", "supplies": [
        {"channel_id": ids[0], "kind": "subscription"}, {"channel_id": ids[1], "kind": "reseller"}]}))
    (STATE / "initialized").write_text("luobocode-local-preview-v1\n")


def main():
    """按固定站点选择隔离目录和端口；同一二进制可运行两站，Cookie 由主机名隔离。"""
    global SITE, STATE, APP_URL, PORT
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--site", choices=("hosting", "retail"), default="retail")
    SITE = parser.parse_args().site
    if SITE == "hosting":
        STATE = ROOT / ".local-tests/hosting-preview"
        PORT = "18410"
        # Cookie 不按端口隔离，因此两个站不能只使用同一主机名加不同端口。
        APP_URL = "http://localhost:18410"
    os.umask(0o077)
    STATE.mkdir(parents=True, exist_ok=True)
    if shutil.disk_usage(STATE).free < 512 * 1024 * 1024:
        raise SystemExit("本地预览至少需要 512 MiB 可用磁盘空间，请先清理开发盘。")
    if not BINARY.is_file():
        raise SystemExit("先执行 go build -ldflags='-s -w' -o .local-tests/retail-preview/new-api .")
    fresh = not (STATE / "new-api.db").exists()
    if not fresh and not (STATE / "initialized").is_file():
        raise SystemExit("发现未初始化完成的本地库；请检查该预览目录，不自动覆盖已有数据。")
    if fresh:
        (STATE / "session-secret").write_text(secrets.token_hex(32))
        (STATE / "retail.json").write_text('{"enabled":false,"supplies":[]}')
    upstream = None
    if SITE == "retail":
        upstream = ThreadingHTTPServer(("127.0.0.1", 18401), DemoUpstream)
        threading.Thread(target=upstream.serve_forever, daemon=True).start()
    process = None
    def interrupt(_signum, _frame):
        """把系统退出信号转成统一清理路径，避免遗留后台演示服务。"""
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupt)
    try:
        with (STATE / "server.log").open("a") as log:
            process = start_app(log)
            if fresh:
                seed_database()
                # 重新读取刚写入的演示余额与渠道配置，避免旧的用户缓存影响第一次试用。
                stop_app(process)
                process = start_app(log)
            # 仅更新此脚本拥有的名称和回环地址，保留已产生的请求记录、余额和密钥。
            # 文档从 ServerAddress 生成客户端地址，不能沿用默认的 3000 端口。
            access = api("/api/user/login", {"username": "previewadmin", "password": PASSWORD})["data"]["access_token"]
            name = "luobocode · 中转站预览" if SITE == "retail" else "luobocode · 托管站预览"
            api("/api/option/", {"key": "SystemName", "value": name}, access, method="PUT")
            api("/api/option/", {"key": "ServerAddress", "value": APP_URL}, access, method="PUT")
            print(f"{name}已启动：{APP_URL}/dashboard/overview", flush=True)
            print(f"用户：preview；管理员：previewadmin；演示密码：{PASSWORD}", flush=True)
            if SITE == "retail":
                print(f"演示兑换码：{REDEMPTION}（一次有效）；支付宝未配置，订单记录均为演示数据。", flush=True)
            else:
                print("空托管账号与演示套餐；未连接真实账号、节点或支付。", flush=True)
            process.wait()
    except KeyboardInterrupt:
        pass
    finally:
        if process is not None:
            stop_app(process)
        if upstream is not None:
            upstream.shutdown()
            upstream.server_close()


if __name__ == "__main__":
    main()
