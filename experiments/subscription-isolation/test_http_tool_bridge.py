from contextlib import redirect_stderr
import http.client
import io
import json
import socket
import tempfile
import unittest
from unittest.mock import patch
from urllib.parse import urlsplit

from http_tool_bridge import start_bridge
from tool_server import REQUEST_LIMIT


class BridgeTests(unittest.TestCase):
    def setUp(self):
        self.workspace = tempfile.TemporaryDirectory()
        self.addCleanup(self.workspace.cleanup)
        self.server, self.url = start_bridge(self.workspace.name, "node:22-slim")
        self.addCleanup(self.server.close)
        self.parsed = urlsplit(self.url)

    def request(self, method="POST", path=None, body=None, headers=None):
        connection = http.client.HTTPConnection("127.0.0.1", self.parsed.port, timeout=5)
        self.addCleanup(connection.close)
        if body is None:
            body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
        connection.request(method, path or self.parsed.path, body=body,
                           headers={"Content-Type": "application/json", **(headers or {})})
        response = connection.getresponse()
        self.last_headers = dict(response.getheaders())
        return response.status, response.read()

    def test_mcp_inventory_and_notification(self):
        status, body = self.request()
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(body)["result"]["tools"][0]["name"], "group_exec")
        status, body = self.request(body='{"jsonrpc":"2.0","method":"notifications/initialized"}')
        self.assertEqual((status, body), (202, b""))

    def test_bad_capability_and_origin_do_not_execute_or_log(self):
        capture = io.StringIO()
        with redirect_stderr(capture), patch.object(self.server.executor, "run") as run:
            self.assertEqual(self.request(path="/mcp/wrong")[0], 404)
            self.assertEqual(self.request(headers={"Origin": "https://evil.example"})[0], 403)
            self.assertEqual(self.request(headers={"Host": "evil.example"})[0], 403)
            run.assert_not_called()
        self.assertEqual(capture.getvalue(), "")

    def test_capability_randomness_and_matching_local_origin(self):
        self.assertEqual(len(self.parsed.path.removeprefix("/mcp/")), 43)
        self.assertEqual(self.request(headers={"Origin": f"http://localhost:{self.parsed.port}"})[0], 200)
        self.assertEqual(self.request(headers={"Origin": f"http://localhost:{self.parsed.port + 1}"})[0], 403)
        self.assertEqual(self.request(headers={"Host": f"host.docker.internal:{self.parsed.port}"})[0], 200)

    def test_body_limits_and_invalid_framing(self):
        self.assertEqual(self.request(body="x" * (REQUEST_LIMIT + 1))[0], 413)
        self.assertEqual(self.request(body="[]")[0], 400)
        self.assertEqual(self.request(headers={"Transfer-Encoding": "chunked"})[0], 400)
        self.assertEqual(self.request(headers={"Content-Type": "text/plain"})[0], 400)
        self.assertEqual(self.request(method="GET")[0], 405)

    def test_json_response_is_nosniff_and_cannot_render_markup(self):
        identity = '<script>alert("canary")</script>&'
        status, body = self.request(body=json.dumps({"jsonrpc": "2.0", "id": identity, "method": "unknown"}))
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(body)["id"], identity)
        self.assertNotIn(b"<script>", body)
        self.assertNotIn(b"&", body)
        self.assertIn(b"\\u003c", body)
        self.assertEqual(self.last_headers["X-Content-Type-Options"], "nosniff")
        self.assertEqual(self.last_headers["Content-Type"], "application/json; charset=utf-8")
        self.assertIn("sandbox", self.last_headers["Content-Security-Policy"])

    def test_ids_and_request_structure_are_bounded_before_dispatch(self):
        invalid = [
            {"id": {"secret": "not-reflected"}}, {"id": [1]}, {"id": True},
            {"id": "x" * 129}, {"id": 9007199254740992}, {"id": 1.5},
            {"params": []}, {"method": ["tools/list"]}, {"extra": "not-reflected"},
        ]
        for fields in invalid:
            request = {"jsonrpc": "2.0", "id": 1, "method": "tools/list", **fields}
            with self.subTest(fields=fields), patch.object(self.server.executor, "run") as run:
                status, body = self.request(body=json.dumps(request))
                self.assertEqual(status, 400)
                self.assertIsNone(json.loads(body)["id"])
                self.assertNotIn(b"not-reflected", body)
                run.assert_not_called()

    def test_fixed_executor_and_safe_errors(self):
        request = {"jsonrpc": "2.0", "id": 8, "method": "tools/call",
                   "params": {"name": "group_exec", "arguments": {"cmd": "printf yes"}}}
        with patch.object(self.server.executor, "run", return_value={"exit_code": 0, "stopped": None, "output": "yes"}) as run:
            status, body = self.request(body=json.dumps(request))
            run.assert_called_once_with({"command": "printf yes"})
            self.assertEqual(status, 200)
            self.assertFalse(json.loads(body)["result"]["isError"])
        with patch.object(self.server.executor, "run", side_effect=RuntimeError("secret-path-do-not-render")):
            status, body = self.request(body=json.dumps(request))
            self.assertEqual(status, 400)
            self.assertNotIn(b"secret-path", body)

    def test_bind_is_explicit(self):
        with self.assertRaises(ValueError):
            start_bridge(self.workspace.name, "node:22-slim", bind="192.0.2.1")

    def test_idle_saturation_is_bounded_and_recovers(self):
        sockets = []
        self.addCleanup(lambda: [connection.close() for connection in sockets])
        for count in range(1, self.server.max_workers + 1):
            connection = socket.create_connection(("127.0.0.1", self.parsed.port), timeout=2)
            sockets.append(connection)
            with self.server.worker_condition:
                self.assertTrue(self.server.worker_condition.wait_for(
                    lambda: self.server.active_workers == count, timeout=2))
        overflow = socket.create_connection(("127.0.0.1", self.parsed.port), timeout=2)
        self.addCleanup(overflow.close)
        try:
            self.assertEqual(overflow.recv(1), b"")
        except ConnectionResetError:
            pass
        self.assertEqual(self.server.active_workers, self.server.max_workers)
        sockets.pop().close()
        with self.server.worker_condition:
            self.assertTrue(self.server.worker_condition.wait_for(
                lambda: self.server.active_workers < self.server.max_workers, timeout=2))
        self.assertEqual(self.request()[0], 200)

    def test_idle_timeout_releases_admission(self):
        self.server.request_timeout = 0.2
        idle = socket.create_connection(("127.0.0.1", self.parsed.port), timeout=2)
        self.addCleanup(idle.close)
        with self.server.worker_condition:
            self.assertTrue(self.server.worker_condition.wait_for(
                lambda: self.server.active_workers == 1, timeout=2))
            self.assertTrue(self.server.worker_condition.wait_for(
                lambda: self.server.active_workers == 0, timeout=2))
        self.assertEqual(idle.recv(1), b"")
        self.assertEqual(self.request()[0], 200)


if __name__ == "__main__":
    unittest.main()
