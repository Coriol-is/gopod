import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from tool_server import Executor, OUTPUT_LIMIT, dispatch


class ProtocolTests(unittest.TestCase):
    def test_only_fixed_tool_is_advertised(self):
        result = dispatch({"id": 1, "method": "tools/list"}, None)
        self.assertEqual([x["name"] for x in result["result"]["tools"]], ["group_exec"])
        self.assertFalse(result["result"]["tools"][0]["inputSchema"]["additionalProperties"])

    def test_unknown_method_and_tool(self):
        self.assertEqual(dispatch({"id": 1, "method": "host/read"}, None)["error"]["code"], -32601)
        with self.assertRaises(ValueError):
            dispatch({"id": 2, "method": "tools/call", "params": {"name": "host_exec"}}, None)


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
        result = self.executor.run({"command": "yes x"})
        self.assertEqual(result["stopped"], "output_limit")
        self.assertEqual(len(result["output"].encode()), OUTPUT_LIMIT)
        # No canary container remains after timeout/output cancellation.
        names = subprocess.check_output([self.executor.docker, "ps", "-a", "--format", "{{.Names}}"], text=True)
        self.assertFalse(any(x.startswith("gopod-isolation-spike-") for x in names.splitlines()))

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
