"""Opt-in tests invoke the pinned CLI with synthetic HOME and localhost provider."""
import os
import unittest

from codex_probe import probe


ALLOWED_NATIVE_CONTROLS = {
    "list_mcp_resources", "list_mcp_resource_templates", "read_mcp_resource",
    "request_user_input", "get_goal", "create_goal", "update_goal",
}


def assert_restricted_inventory(test, result):
    for request in result["requests"]:
        test.assertFalse(request["has_authorization"])
        for tool in request["tools"]:
            if tool["type"] == "namespace":
                test.assertEqual(tool["name"], "mcp__group")
                test.assertEqual(tool["tools"], [{"name": "group_exec", "type": "function"}])
            else:
                test.assertEqual(tool["type"], "function")
                test.assertIn(tool["name"], ALLOWED_NATIVE_CONTROLS)
                test.assertNotIn("tools", tool)


class InventoryGateTests(unittest.TestCase):
    def result(self, tool):
        return {"requests": [{"has_authorization": False, "tools": [tool]}]}

    def test_known_controls_and_exact_group_namespace_allowed(self):
        for name in ALLOWED_NATIVE_CONTROLS:
            assert_restricted_inventory(self, self.result({"name": name, "type": "function"}))
        assert_restricted_inventory(self, self.result({"name": "mcp__group", "type": "namespace",
                                                       "tools": [{"name": "group_exec", "type": "function"}]}))

    def test_unknown_names_types_and_extra_namespace_tools_fail_closed(self):
        cases = [
            {"name": "new_file_reader", "type": "function"},
            {"name": "exec_command", "type": "function"},
            {"name": "get_goal", "type": "custom"},
            {"name": "get_goal", "type": "function", "tools": []},
            {"name": "mcp__personal", "type": "namespace", "tools": [{"name": "group_exec", "type": "function"}]},
            {"name": "mcp__group", "type": "namespace", "tools": [{"name": "group_exec", "type": "function"}, {"name": "read_auth", "type": "function"}]},
        ]
        for tool in cases:
            with self.subTest(tool=tool), self.assertRaises(AssertionError):
                assert_restricted_inventory(self, self.result(tool))


@unittest.skipUnless(os.environ.get("GOPOD_CODEX_OFFLINE_TEST") == "1", "opt-in pinned Codex offline qualification")
class CodexBoundaryTests(unittest.TestCase):
    def assert_no_native_tools(self, result):
        assert_restricted_inventory(self, result)

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
        self.assertTrue(result["external_file_proof"], result)
        self.assertEqual(len(result["requests"]), 2)
        self.assert_no_native_tools(result)

    @unittest.skipUnless(os.environ.get("GOPOD_ISOLATION_DOCKER_TEST") == "1", "real Docker required")
    def test_http_external_tool_roundtrip(self):
        result = probe(mcp=True, http_mcp=True, image=os.environ.get("GOPOD_ISOLATION_TEST_IMAGE", "node:22-slim"))
        self.assertEqual(result["exit_code"], 0, result)
        self.assertTrue(result["mcp_roundtrip"], result)
        self.assertTrue(result["external_file_proof"], result)
        self.assertEqual(len(result["requests"]), 2)
        self.assert_no_native_tools(result)


if __name__ == "__main__":
    unittest.main()
