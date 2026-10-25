import json
import os
from pathlib import Path
import subprocess
import signal
import shutil
import tempfile
import time
import unittest
from unittest import mock

from tool_server import ARGUMENT_HINT, Executor, OUTPUT_LIMIT, REQUEST_LIMIT, dispatch


class ProtocolTests(unittest.TestCase):
    def test_only_fixed_tool_is_advertised(self):
        result = dispatch({"id": 1, "method": "tools/list"}, None)
        self.assertEqual([x["name"] for x in result["result"]["tools"]], ["group_exec"])
        self.assertFalse(result["result"]["tools"][0]["inputSchema"]["additionalProperties"])

    def test_unknown_method_and_tool(self):
        self.assertEqual(dispatch({"id": 1, "method": "host/read"}, None)["error"]["code"], -32601)
        with self.assertRaises(ValueError):
            dispatch({"id": 2, "method": "tools/call", "params": {"name": "host_exec"}}, None)

    def test_invalid_arguments_are_actionable_tool_errors_without_docker(self):
        executor = mock.Mock()
        # Exercise the real strict validator, which returns before any self or
        # Docker access on invalid input. Hints never echo untrusted arguments.
        executor.run.side_effect = lambda arguments: Executor.run(None, arguments)
        for arguments in ({'cmd': 'private-argument'}, {'command': ''}, {'command': 'true', 'private-key': 'private-value'}):
            with mock.patch('tool_server.subprocess.Popen') as spawn:
                response = dispatch({'id': 7, 'method': 'tools/call',
                                     'params': {'name': 'group_exec', 'arguments': arguments}}, executor)
                spawn.assert_not_called()
            self.assertEqual(response['result'], {'content': [{'type': 'text', 'text': ARGUMENT_HINT}], 'isError': True})
            self.assertNotIn('private-', json.dumps(response))
        executor.run.side_effect = ValueError('executor is stopping')
        with self.assertRaises(ValueError):
            dispatch({'id': 8, 'method': 'tools/call', 'params': {'name': 'group_exec', 'arguments': {}}}, executor)

    @unittest.skipUnless(shutil.which("docker"), "requires Docker client executable, not a running daemon")
    def test_idle_sigterm_and_oversized_frame(self):
        with tempfile.TemporaryDirectory(prefix="gopod-protocol-") as workspace:
            argv = ["python3", str(Path(__file__).with_name("tool_server.py")),
                    "--workspace", str(Path(workspace).resolve()), "--image", "unused:offline"]
            proc = subprocess.Popen(argv, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            try:
                proc.stdin.write(json.dumps({"id": 1, "method": "ping"}) + "\n")
                proc.stdin.flush()
                self.assertEqual(json.loads(proc.stdout.readline())["result"], {})
                proc.send_signal(signal.SIGTERM)
                _, stderr = proc.communicate(timeout=3)
                self.assertEqual(proc.returncode, 128 + signal.SIGTERM)
                self.assertEqual(stderr, "")
            finally:
                if proc.poll() is None:
                    proc.kill()
                    proc.communicate()
            result = subprocess.run(argv, input="x" * (REQUEST_LIMIT + 1), text=True,
                                    capture_output=True, timeout=3)
            self.assertEqual(result.returncode, 2)
            self.assertEqual(result.stdout, "")
            self.assertEqual(result.stderr, "")


@unittest.skipUnless(os.environ.get("GOPOD_ISOLATION_DOCKER_TEST") == "1", "opt-in real Docker qualification")
class DockerBoundaryTests(unittest.TestCase):
    def setUp(self):
        self.root = tempfile.TemporaryDirectory(prefix="gopod-isolation-canary-")
        self.addCleanup(self.root.cleanup)
        base = Path(self.root.name)
        self.workspace = base / "group"
        self.workspace.mkdir(mode=0o777)
        self.workspace.chmod(0o777)
        self.secret = base / "personal-auth-canary"
        self.secret.write_text("synthetic-personal-value")
        (self.workspace / "escape").symlink_to(self.secret)
        self.executor = Executor(str(self.workspace), os.environ.get("GOPOD_ISOLATION_TEST_IMAGE", "node:22-slim"), timeout=4)

    def test_working_files_allowed_but_personal_authority_absent(self):
        # Synthetic environment only. Never read actual credential stores.
        old = os.environ.get("OPENAI_API_KEY")
        os.environ["OPENAI_API_KEY"] = "synthetic-env-canary"
        self.addCleanup(lambda: os.environ.pop("OPENAI_API_KEY", None) if old is None else os.environ.__setitem__("OPENAI_API_KEY", old))
        command = """
set -eu
printf group-ok > allowed.txt
test "$(cat allowed.txt)" = group-ok
test -z "${OPENAI_API_KEY-}"
test -z "${CLAUDE_CODE_OAUTH_TOKEN-}"
test ! -e /var/run/docker.sock
test ! -e /workspace/vault
test ! -e /home/node/.codex/auth.json
test ! -e /home/node/.claude/.credentials.json
! cat escape >/dev/null 2>&1
! command -v claude >/dev/null 2>&1
! command -v codex >/dev/null 2>&1
! touch /etc/gopod-canary 2>/dev/null
node -e 'const fs=require("fs");const n=fs.readdirSync("/sys/class/net");if(n.some(x=>x!=="lo")) process.exit(1)'
printf boundary-ok
"""
        value = self.executor.run({"command": command})
        self.assertEqual(value["exit_code"], 0, value)
        self.assertIsNone(value["stopped"])
        self.assertEqual(value["output"], "boundary-ok")
        self.assertEqual((self.workspace / "allowed.txt").read_text(), "group-ok")

    def test_tool_arguments_cannot_select_other_workspace_or_image(self):
        for extra in [{"workspace": "/"}, {"image": "personal"}, {"env": {"TOKEN": "x"}}]:
            with self.assertRaises(ValueError):
                self.executor.run({"command": "true", **extra})

    def test_timeout_and_output_limit(self):
        self.assertEqual(self.executor.run({"command": "sleep 30"})["stopped"], "timeout")
        own_names = [self.executor.last_container_name]
        result = self.executor.run({"command": "yes x"})
        own_names.append(self.executor.last_container_name)
        self.assertEqual(result["stopped"], "output_limit")
        self.assertEqual(len(result["output"].encode()), OUTPUT_LIMIT)
        self.assert_containers_absent(own_names)

    def assert_containers_absent(self, names):
        remaining = subprocess.check_output([self.executor.docker, "ps", "-a", "--format", "{{.Names}}"], text=True).splitlines()
        for name in names:
            self.assertNotIn(name, remaining)

    def test_sigterm_cleans_running_request(self):
        # Name discovery is scoped by the unique mounted synthetic workspace,
        # never by every other concurrent experiment's name prefix.
        proc = subprocess.Popen(["python3", str(Path(__file__).with_name("tool_server.py")),
                                 "--workspace", str(self.workspace), "--image", self.executor.image],
                                stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        def stop_server():
            if proc.poll() is None:
                proc.terminate()
                proc.communicate(timeout=8)
        self.addCleanup(stop_server)
        request = {"id": 1, "method": "tools/call", "params": {"name": "group_exec", "arguments": {"command": "touch started; sleep 30"}}}
        proc.stdin.write(json.dumps(request) + "\n")
        proc.stdin.flush()
        deadline = time.monotonic() + 5
        while not (self.workspace / "started").exists() and time.monotonic() < deadline:
            time.sleep(0.05)
        self.assertTrue((self.workspace / "started").exists())
        own_names = subprocess.check_output([self.executor.docker, "ps", "--filter", f"volume={self.executor.workspace}", "--format", "{{.Names}}"], text=True).splitlines()
        self.assertEqual(len(own_names), 1)
        proc.send_signal(signal.SIGTERM)
        stdout, stderr = proc.communicate(timeout=8)
        self.assertEqual(proc.returncode, 128 + signal.SIGTERM)
        result = json.loads(json.loads(stdout)["result"]["content"][0]["text"])
        self.assertEqual(result["stopped"], "cancelled")
        self.assertEqual(stderr, "")
        self.assert_containers_absent(own_names)

    def test_cancel_during_create_never_starts_command(self):
        original = self.executor._capture
        def cancel_after_create(argv, deadline, cancelable=True):
            value = original(argv, deadline, cancelable)
            if argv[1] == "create":
                self.executor.cancel(signal.SIGTERM, None)
            return value
        with mock.patch.object(self.executor, "_capture", side_effect=cancel_after_create):
            result = self.executor.run({"command": "touch must-not-start"})
        self.assertEqual(result["stopped"], "cancelled")
        self.assertFalse((self.workspace / "must-not-start").exists())
        self.assert_containers_absent([self.executor.last_container_name])

    def test_stdio_protocol(self):
        frames = [
            {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-03-26"}},
            {"jsonrpc": "2.0", "method": "notifications/initialized"},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "group_exec", "arguments": {"command": "printf mcp-ok"}}},
        ]
        result = subprocess.run(["python3", str(Path(__file__).with_name("tool_server.py")),
                                 "--workspace", str(self.workspace), "--image", self.executor.image],
                                input="".join(json.dumps(x) + "\n" for x in frames), text=True,
                                capture_output=True, timeout=15, check=True)
        responses = [json.loads(x) for x in result.stdout.splitlines()]
        self.assertEqual(len(responses), 2)
        self.assertEqual(responses[0]["result"]["protocolVersion"], "2025-03-26")
        self.assertEqual(json.loads(responses[1]["result"]["content"][0]["text"])["output"], "mcp-ok")
        self.assertEqual(result.stderr, "")


if __name__ == "__main__":
    unittest.main()
