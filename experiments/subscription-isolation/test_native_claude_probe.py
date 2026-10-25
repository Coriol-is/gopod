import os
import unittest

from native_claude_probe import qualify


@unittest.skipUnless(os.environ.get("GOPOD_CLAUDE_OFFLINE_TEST") == "1", "opt-in pinned Claude/Docker synthetic API qualification")
class NativeClaudeTests(unittest.TestCase):
    def test_native_cli_external_tool_roundtrip_and_inventory(self):
        result = qualify()
        self.assertTrue(result["passed"], result)
        self.assertEqual(result["requests"], 2)
        self.assertEqual(result["tools"], ["mcp__group__group_exec"])
        self.assertTrue(result["mcp_result_returned"])
        self.assertTrue(result["workspace_proof"])


if __name__ == "__main__":
    unittest.main()
