import json
import unittest
import uuid

from claude_policy import ENV_OVERRIDES, SETTINGS, build_command, mcp_config


class ClaudePolicyTests(unittest.TestCase):
    def test_fresh_has_no_builtin_tools_or_bypass(self):
        command = build_command("http://127.0.0.1:8123/mcp", "synthetic")
        self.assertEqual(command[command.index("--tools") + 1], "")
        for flag in ["--restricted", "--strict-mcp-config", "--disable-slash-commands", "--no-session-persistence"]:
            self.assertIn(flag, command)
        self.assertNotIn("--dangerously-skip-permissions", command)
        self.assertNotIn("--bare", command)
        self.assertEqual(command[command.index("--permission-mode") + 1], "dontAsk")
        self.assertEqual(command[command.index("--allowedTools") + 1], "mcp__group__group_exec")
        config = json.loads(command[command.index("--mcp-config") + 1])
        self.assertEqual(list(config["mcpServers"]), ["group"])

    def test_explicit_sessions_keep_same_policy(self):
        sid = str(uuid.uuid4())
        fresh = build_command("http://[::1]:8123/mcp", "synthetic", session_id=sid)
        resumed = build_command("http://[::1]:8123/mcp", "synthetic", session_id=sid, resume=True)
        self.assertEqual(fresh[:-4], resumed[:-4])
        self.assertIn("--session-id", fresh)
        self.assertIn("--resume", resumed)
        self.assertNotIn("--no-session-persistence", resumed)
        self.assertEqual(resumed[resumed.index("--system-prompt-snapshot") + 1], "off")
        with self.assertRaises(ValueError):
            build_command("http://127.0.0.1:8123/mcp", "synthetic", resume=True)

    def test_no_remote_or_credential_endpoint(self):
        for url in ["https://127.0.0.1:8123/mcp", "http://example.com:80/mcp", "http://127.0.0.1/mcp", "http://x:y@127.0.0.1:8123/mcp", "http://127.0.0.1:8123/mcp?token=x", "http://127.0.0.1:8123/mcp#x"]:
            with self.subTest(url=url), self.assertRaises(ValueError):
                mcp_config(url)

    def test_connectors_hooks_memory_disabled(self):
        self.assertTrue(SETTINGS["disableClaudeAiConnectors"])
        self.assertTrue(SETTINGS["disableAllHooks"])
        self.assertFalse(SETTINGS["autoMemoryEnabled"])
        self.assertEqual(ENV_OVERRIDES["ENABLE_CLAUDEAI_MCP_SERVERS"], "false")


if __name__ == "__main__":
    unittest.main()
