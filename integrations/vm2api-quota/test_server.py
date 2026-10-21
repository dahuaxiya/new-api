"""Protect authentication, per-user isolation and official quota semantics."""
import unittest
from unittest.mock import patch

from server import QuotaService, quota_view


class QuotaTests(unittest.TestCase):
    """Use explicit identities and windows; no live secrets are needed."""

    def setUp(self):
        """Create distinct users and snapshots for every independent test."""
        self.service = QuotaService({"users": {
            "2": {"username": "niu", "group": "niu", "slot": "vm-01", "label": "牛"},
            "3": {"username": "chuichui", "group": "chuichui", "slot": "chuichui", "label": "锤"},
        }, "vm_login": {"username": "admin", "password": "test-only"}})
        self.service.snapshots = {
            "vm-01": quota_view([{"id": "7d", "used_percent": 56}], "2026-10-21T13:00:00Z"),
            "chuichui": quota_view([{"id": "5h", "used_percent": 100}], "2026-10-21T13:00:00Z"),
        }
        self.auth = "Bearer " + "x" * 32

    def test_missing_or_malformed_credentials_do_not_call_upstream(self):
        """Reject cookie-only, missing and malformed identity headers before proxying."""
        with patch("server.request_json") as request:
            for auth, uid in [(None, "2"), ("Basic abc", "2"), (self.auth, "../3")]:
                with self.subTest(uid=uid), self.assertRaises(PermissionError):
                    self.service.authenticate(auth, uid)
            request.assert_not_called()

    def test_bearer_only_browser_uses_verified_user_id_without_refresh_loop(self):
        """Current New API browsers omit New-Api-User; valid Bearer sessions must work."""
        with patch("server.request_json", return_value={"success": True, "data": {
            "id": 2, "username": "niu", "group": "niu", "status": 1,
        }}):
            binding = self.service.authenticate(self.auth, None)
        self.assertEqual(binding["slot"], "vm-01")

    def test_spoofed_user_id_cannot_select_another_snapshot(self):
        """A header claiming user 3 must not override user 2 authenticated by New API."""
        with patch("server.request_json", return_value={"success": True, "data": {
            "id": 2, "username": "niu", "group": "niu", "status": 1,
        }}), self.assertRaises(PermissionError):
            self.service.authenticate(self.auth, "3")

    def test_disabled_account_is_rejected(self):
        """New API account disablement takes effect without a local auth cache."""
        with patch("server.request_json", return_value={"success": True, "data": {
            "id": 2, "username": "niu", "group": "niu", "status": 2,
        }}), self.assertRaises(PermissionError):
            self.service.authenticate(self.auth, "2")

    def test_valid_user_gets_only_owned_snapshot(self):
        """Return only the bound account, with no slot ids, credentials or mutable cache."""
        with patch("server.request_json", return_value={"success": True, "data": {
            "id": 2, "username": "niu", "group": "niu", "status": 1,
        }}):
            binding = self.service.authenticate(self.auth, "2")
        view = self.service.get_view(binding)
        self.assertEqual(view["windows"][1]["remaining_percent"], 44)
        self.assertIsNone(view["windows"][0]["used_percent"])
        self.assertNotIn("slot", view)
        view["windows"][1]["used_percent"] = 0
        self.assertEqual(self.service.get_view(binding)["windows"][1]["used_percent"], 56)

    def test_changed_username_or_group_loses_binding(self):
        """An operator account reassignment cannot inherit a previous user's snapshot."""
        with patch("server.request_json", return_value={"success": True, "data": {
            "id": 2, "username": "replacement", "group": "niu", "status": 1,
        }}):
            self.assertIsNone(self.service.authenticate(self.auth, "2"))

    def test_unknown_and_zero_and_fractional_percentage_remain_distinct(self):
        """An actual 0.47 percent stays 0.47; unknown is never converted to zero."""
        view = quota_view([{"id": "7d", "used_percent": 0.47, "reset_at": "invalid"}], None)
        self.assertIsNone(view["windows"][0]["remaining_percent"])
        self.assertEqual(view["windows"][1]["remaining_percent"], 99.53)
        self.assertIsNone(view["windows"][1]["reset_at"])
        self.assertEqual(quota_view([{"id": "5h", "used_percent": 0}], None)["windows"][0]["remaining_percent"], 100)

    def test_probe_failure_preserves_dated_reading_and_reports_error(self):
        """Failed refresh must not look like an empty or freshly replenished quota."""
        def upstream(url, headers=None, data=None, timeout=8):
            """Stub only the external transport for one successful cache read then failure."""
            if url.endswith("/login"):
                return {"token": "test-panel"}
            if url.endswith("/logout"):
                return {"ok": True}
            if url.endswith("/probe"):
                raise OSError("offline")
            return {"data": {"vm": {"codex_usage": {"windows": [{"id": "7d", "used_percent": 56}],
                    "snapshot": {"updated_at": "2026-10-21T13:00:00Z"}}}}}
        with patch("server.request_json", side_effect=upstream):
            self.service.poll_once()
        view = self.service.get_view(self.service.bindings["2"])
        self.assertEqual(view["sync_status"], "error")
        self.assertEqual(view["windows"][1]["used_percent"], 56)
        self.assertEqual(view["observed_at"], "2026-10-21T13:00:00+00:00")


if __name__ == "__main__":
    unittest.main()
