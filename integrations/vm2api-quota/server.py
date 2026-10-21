"""Expose only the authenticated user's official GPT quota, beside New API.

New API remains the session authority. A fixed background poller talks to
vm2api; public requests cannot choose a slot or cause upstream probes.
"""
import copy
import datetime as dt
import json
import math
import os
import re
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

NEW_API = "http://127.0.0.1:3001"
VM_API = "http://127.0.0.1:8787/api/panel"
POLL_SECONDS = 300


class NoRedirect(urllib.request.HTTPRedirectHandler):
    """Never forward bearer credentials to a redirected destination."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        """Reject redirects; both upstream services have fixed local addresses."""
        return None


def request_json(url, headers=None, data=None, timeout=8):
    """Call a fixed service URL with bounded reads; do not log response bodies."""
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    req = urllib.request.Request(
        url, headers={"Content-Type": "application/json", **(headers or {})},
        data=None if data is None else json.dumps(data).encode(),
    )
    with client.open(req, timeout=timeout) as response:
        raw = response.read(1024 * 1024 + 1)
        if len(raw) > 1024 * 1024:
            raise ValueError("Response too large")
        return json.loads(raw)


def iso_now():
    """Return UTC timestamps so the UI can render the viewer's local timezone."""
    return dt.datetime.now(dt.timezone.utc).isoformat()


def timestamp(value):
    """Normalize a valid upstream timestamp; absent or invalid dates stay unknown."""
    if not isinstance(value, str):
        return None
    try:
        parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
        return parsed.astimezone(dt.timezone.utc).isoformat() if parsed.tzinfo else None
    except ValueError:
        return None


def quota_view(usage, observed_at, plan=None):
    """Whitelist official window fields, preserving unknown values and percentage units.

    vm2api windows.used_percent is 0..100, whereas quota.utilization is 0..1.
    Only the former is accepted here to prevent a silent 100-fold scale error.
    """
    rows = {row.get("id"): row for row in usage if isinstance(row, dict)}
    windows = []
    for window_id in ("5h", "7d"):
        row = rows.get(window_id, {})
        used = row.get("used_percent")
        if type(used) not in (int, float) or not math.isfinite(used) or not 0 <= used <= 100:
            used = None
        windows.append({"id": window_id, "used_percent": used,
                        "remaining_percent": None if used is None else round(100 - used, 4),
                        "reset_at": timestamp(row.get("reset_at"))})
    return {"windows": windows, "observed_at": timestamp(observed_at),
            "plan": plan if plan in ("free", "plus", "pro", "team", "business", "enterprise", "edu") else None}


class QuotaService:
    """Keep per-slot snapshots private and resolve every viewer through New API."""

    def __init__(self, config):
        """Accept only operator-controlled mappings, never paths supplied by a browser."""
        self.config = config
        self.bindings = config["users"]
        for user_id, binding in self.bindings.items():
            if not user_id.isdigit() or not re.fullmatch(r"[A-Za-z0-9_-]+", binding["slot"]):
                raise ValueError("Invalid configured user mapping")
        self.snapshots = {}
        self.lock = threading.Lock()

    def authenticate(self, authorization, claimed_id):
        """Validate the live session and match its verified identity to a fixed binding.

        New-Api-User is optional in recent New API versions and is never proof
        of identity. A supplied hint must match; absent hints use the verified
        response id. Disabled accounts and changed bindings always fail closed.
        No auth cache is used, so revocation follows New API immediately.
        """
        if not re.fullmatch(r"Bearer [A-Za-z0-9._~-]{20,8192}", authorization or ""):
            raise PermissionError("Authentication required")
        if claimed_id is not None and not re.fullmatch(r"[1-9][0-9]{0,12}", claimed_id):
            raise PermissionError("Authentication required")
        try:
            result = request_json(NEW_API + "/api/user/self", {
                "Authorization": authorization,
            })
        except urllib.error.HTTPError as error:
            if error.code in (401, 403):
                raise PermissionError("Authentication required") from None
            raise
        user = result.get("data") or {}
        if not result.get("success") or not isinstance(user.get("id"), int) or user.get("status") != 1:
            raise PermissionError("Authentication required")
        if claimed_id is not None and str(user["id"]) != claimed_id:
            raise PermissionError("Authentication required")
        binding = self.bindings.get(str(user["id"]))
        if not binding or user.get("username") != binding["username"] or user.get("group") != binding["group"]:
            return None
        return binding

    def get_view(self, binding):
        """Return a detached snapshot for one verified user, without upstream identifiers."""
        if binding is None:
            return {"bound": False}
        with self.lock:
            view = copy.deepcopy(self.snapshots.get(binding["slot"]))
        if view is None:
            view = {**quota_view([], None), "sync_status": "pending", "last_checked_at": None}
        observed = timestamp(view.get("observed_at"))
        age = None if observed is None else time.time() - dt.datetime.fromisoformat(observed).timestamp()
        view.update(bound=True, account_label=binding["label"],
                    stale=age is None or age > 600, poll_seconds=POLL_SECONDS)
        return view

    def poll_once(self):
        """Refresh each configured slot once; failures retain dated last-known readings.

        The worker is the only holder of vm2api credentials. Even an authenticated
        user cannot invoke arbitrary panel APIs or amplify expensive official probes.
        """
        token = None
        try:
            token = request_json(VM_API + "/login", data=self.config["vm_login"])["token"]
            headers = {"Authorization": "Bearer " + token}
            for slot in dict.fromkeys(binding["slot"] for binding in self.bindings.values()):
                with self.lock:
                    previous = copy.deepcopy(self.snapshots.get(slot))
                plan = previous.get("plan") if previous else None
                try:
                    # Read the persisted official view first, including the plan.
                    vm = request_json(VM_API + "/vms/" + slot, headers)["data"]["vm"]
                    usage = vm.get("codex_usage") or {}
                    plan = vm.get("plan_type")
                    previous = quota_view(usage.get("windows") or [], (usage.get("snapshot") or {}).get("updated_at"), plan)
                    result = request_json(VM_API + "/vms/" + slot + "/probe", headers, {}, timeout=55)
                    if not result.get("ok") or not result.get("data", {}).get("ok"):
                        raise ValueError("Probe failed")
                    data = result["data"]
                    view = quota_view(data.get("windows") or [], data.get("probed_at"), plan)
                    view["sync_status"] = "ok"
                except (OSError, ValueError, KeyError, TypeError):
                    view = previous or quota_view([], None, plan)
                    view["sync_status"] = "error"
                view["last_checked_at"] = iso_now()
                with self.lock:
                    self.snapshots[slot] = view
        except (OSError, ValueError, KeyError, TypeError):
            # Do not leak admin login responses or replace existing readings with zeroes.
            with self.lock:
                for slot in dict.fromkeys(binding["slot"] for binding in self.bindings.values()):
                    view = self.snapshots.setdefault(slot, quota_view([], None))
                    view.update(sync_status="error", last_checked_at=iso_now())
        finally:
            if token:
                try:
                    request_json(VM_API + "/logout", {"Authorization": "Bearer " + token}, {})
                except (OSError, ValueError):
                    pass

    def poll_forever(self):
        """Serialize batches; polling never overlaps even when upstreams are slow."""
        while True:
            started = time.monotonic()
            self.poll_once()
            time.sleep(max(1, POLL_SECONDS - (time.monotonic() - started)))


class Handler(BaseHTTPRequestHandler):
    """Serve the one read-only quota endpoint; no generic panel proxy exists."""

    def do_GET(self):
        """Reject alternate paths and query selectors before looking up the current user."""
        if self.path != "/api/gpt-quota/self":
            self.respond(404, {"success": False, "message": "Not found"})
            return
        try:
            binding = self.server.service.authenticate(self.headers.get("Authorization"), self.headers.get("New-Api-User"))
            self.respond(200, {"success": True, "data": self.server.service.get_view(binding)})
        except PermissionError:
            self.respond(401, {"success": False, "message": "Authentication required"})
        except (OSError, ValueError, KeyError, TypeError):
            self.respond(503, {"success": False, "message": "Quota service unavailable"})

    def respond(self, status, body):
        """Send only whitelisted JSON with no browser or intermediary caching."""
        raw = json.dumps(body, ensure_ascii=False, allow_nan=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Cache-Control", "no-store, private")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def log_message(self, format, *args):
        """Suppress request/header logging because requests carry session credentials."""
        return


class Server(ThreadingHTTPServer):
    """Loopback server with bounded concurrency to protect the small VPS."""
    daemon_threads = True

    def __init__(self, address, service):
        """Bind the listener and permit at most 16 active authentication requests."""
        self.service = service
        self.slots = threading.BoundedSemaphore(16)
        super().__init__(address, Handler)

    def process_request(self, request, client_address):
        """Refuse excess work rather than creating unbounded worker threads."""
        request.settimeout(10)
        if not self.slots.acquire(blocking=False):
            self.shutdown_request(request)
            return
        try:
            super().process_request(request, client_address)
        except Exception:
            self.slots.release()
            raise

    def process_request_thread(self, request, client_address):
        """Always release the capacity permit after the framework closes the socket."""
        try:
            super().process_request_thread(request, client_address)
        finally:
            self.slots.release()


if __name__ == "__main__":
    config_path = Path(os.environ.get("CREDENTIALS_DIRECTORY", "/etc/new-api-quota")) / "config.json"
    service = QuotaService(json.loads(config_path.read_text()))
    threading.Thread(target=service.poll_forever, daemon=True).start()
    Server(("127.0.0.1", 3002), service).serve_forever()
