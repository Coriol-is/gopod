"""Opt-in tests invoke the pinned CLI with synthetic HOME and localhost provider."""
import os
import unittest

from codex_probe import probe


@unittest.skipUnless(os.environ.get("GOPOD_CODEX_OFFLINE_TEST") == "1", "opt-in pinned Codex offline qualification")
class CodexBoundaryTests(unittest.TestCase):
    def assert_no_native_tools(self, result):
        forbidden = {"exec_command", "write_stdin", "shell", "shell_command", "apply_patch", "view_image",
                     "read_file", "list_dir", "grep_files", "spawn_agent", "web_search", "computer", "browser"}
        for request in result["requests"]:
            self.assertFalse(request["has_authorization"])
            for tool in request["tools"]:
                self.assertNotIn(tool["name"], forbidden)
                if tool["type"] == "namespace":
                    self.assertEqual(tool["name"], "mcp__group")
                    self.assertEqual(tool["tools"], [{"name": "group_exec", "type": "function"}])

    def test_native_inventory(self):
        result = probe()
        self.assertEqual(result["exit_code"], 0, result)
        self.assertEqual(len(result["requests"]), 1)
        self.assert_no_native_tools(result)

    def test_disabled_native_call_rejected(self):
        result = probe(native_attack=True)
        self.assertEqual(result["exit_code"], 0, result)
        self.assertTrue(result["function_result_seen"], result)
        self.assertFalse(result["native_marker_created"], result)
        self.assert_no_native_tools(result)

    def test_required_mcp_failure_stops_before_provider(self):
        result = probe(mcp=True, missing_mcp=True)
        self.assertNotEqual(result["exit_code"], 0, result)
        self.assertEqual(result["requests"], [])

    @unittest.skipUnless(os.environ.get("GOPOD_ISOLATION_DOCKER_TEST") == "1", "real Docker required")
    def test_external_tool_roundtrip(self):
        result = probe(mcp=True, image=os.environ.get("GOPOD_ISOLATION_TEST_IMAGE", "node:22-slim"))
        self.assertEqual(result["exit_code"], 0, result)
        self.assertTrue(result["mcp_roundtrip"], result)
        self.assertEqual(len(result["requests"]), 2)
        self.assert_no_native_tools(result)

    @unittest.skipUnless(os.environ.get("GOPOD_ISOLATION_DOCKER_TEST") == "1", "real Docker required")
    def test_http_external_tool_roundtrip(self):
        result = probe(mcp=True, http_mcp=True, image=os.environ.get("GOPOD_ISOLATION_TEST_IMAGE", "node:22-slim"))
        self.assertEqual(result["exit_code"], 0, result)
        self.assertTrue(result["mcp_roundtrip"], result)
        self.assertEqual(len(result["requests"]), 2)
        self.assert_no_native_tools(result)


if __name__ == "__main__":
    unittest.main()
