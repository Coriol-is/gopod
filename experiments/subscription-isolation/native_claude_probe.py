#!/usr/bin/env python3
"""Synthetic Claude CLI -> local fake Anthropic API -> MCP -> real Docker.

Never loads real account credentials or makes a real inference request.
"""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def qualify():
    cli = shutil.which("claude")
    if not cli:
        raise RuntimeError("Claude CLI required")
    version = subprocess.check_output([cli, "--version"], text=True).strip()
    if not version.startswith("2.1.310 "):
        raise RuntimeError("requalify changed Claude version")
    inventories, calls = [], []
    marker = "gopod-claude-mcp-canary"
    tool_name = "mcp__group__group_exec"

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_POST(self):
            data = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            if self.path.startswith("/v1/messages/count_tokens"):
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(b'{"input_tokens":10}')
                return
            if not self.path.startswith("/v1/messages"):
                self.send_error(404)
                return
            tools = [x["name"] for x in data.get("tools", [])]
            inventories.append(tools)
            results = [b for m in data.get("messages", []) if isinstance(m.get("content"), list)
                       for b in m["content"] if b.get("type") == "tool_result"]
            calls.extend(results)
            if not results:
                content = {"type": "tool_use", "id": "toolu_synthetic", "name": tool_name,
                           "input": {"command": f"printf {marker} > claude-proof.txt; cat claude-proof.txt"}}
                reason = "tool_use"
            else:
                content = {"type": "text", "text": "SYNTHETIC_ROUNDTRIP_OK"}
                reason = "end_turn"
            message = {"id": "msg_synthetic", "type": "message", "role": "assistant",
                       "model": data.get("model", "claude-test"), "content": [],
                       "stop_reason": None, "stop_sequence": None,
                       "usage": {"input_tokens": 10, "output_tokens": 1}}
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Connection", "close")
            self.end_headers()
            def event(kind, value):
                self.wfile.write(("event: " + kind + "\ndata: " + json.dumps(value) + "\n\n").encode())
            event("message_start", {"type": "message_start", "message": message})
            start = dict(content)
            if reason == "tool_use":
                start["input"] = {}
                delta = {"type": "input_json_delta", "partial_json": json.dumps(content["input"])}
            else:
                start["text"] = ""
                delta = {"type": "text_delta", "text": content["text"]}
            event("content_block_start", {"type": "content_block_start", "index": 0, "content_block": start})
            event("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": delta})
            event("content_block_stop", {"type": "content_block_stop", "index": 0})
            event("message_delta", {"type": "message_delta", "delta": {"stop_reason": reason, "stop_sequence": None}, "usage": {"output_tokens": 5}})
            event("message_stop", {"type": "message_stop"})
            self.wfile.flush()

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix="gopod-claude-native-") as root:
            root = Path(root)
            home, control, workspace = (root / x for x in ("home", "control", "group"))
            for path in (home, control, workspace):
                path.mkdir(mode=0o777)
                path.chmod(0o777)
            config = root / "mcp.json"
            # Docker client context may live in the operator HOME. Only the
            # trusted MCP process gets that directory reference; it never mounts
            # it into the command container. No provider auth is read or copied.
            mcp_env = {k: os.environ[k] for k in ("HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "PATH") if k in os.environ}
            config.write_text(json.dumps({"mcpServers": {"group": {
                "command": sys.executable,
                "args": [str(Path(__file__).with_name("tool_server.py").resolve()),
                         "--workspace", str(workspace), "--image", "node:22-slim"],
                "env": mcp_env,
            }}}))
            env = {"PATH": os.environ["PATH"], "HOME": str(home),
                   "CLAUDE_CONFIG_DIR": str(home / ".claude"),
                   "ANTHROPIC_API_KEY": "synthetic-not-a-real-key",
                   "ANTHROPIC_BASE_URL": f"http://127.0.0.1:{server.server_port}",
                   "DISABLE_NONESSENTIAL_TRAFFIC": "1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
                   "ENABLE_CLAUDEAI_MCP_SERVERS": "false", "CI": "1"}
            args = [cli, "--restricted", "--tools", "", "--strict-mcp-config", "--mcp-config", str(config),
                    "--settings", '{"disableClaudeAiConnectors":true,"disableAllHooks":true,"autoMemoryEnabled":false}',
                    "--setting-sources", "", "--disable-slash-commands", "--permission-mode", "dontAsk",
                    "--allowedTools", tool_name, "--no-session-persistence", "--output-format", "stream-json",
                    "--verbose", "--model", "claude-sonnet-4-6", "-p", "Run the synthetic group canary using the approved MCP tool."]
            result = subprocess.run(args, cwd=control, env=env, capture_output=True, text=True, timeout=50)
            # Emit only synthetic evidence, never raw CLI diagnostics or headers.
            observed = sorted({x for tools in inventories for x in tools})
            allowed = {tool_name, "EndConversation"}
            proof = workspace / "claude-proof.txt"
            report = {"provider": "claude", "version": version, "api": "localhost synthetic only",
                      "exit_code": result.returncode, "requests": len(inventories), "tools": observed,
                      "unexpected_tools": sorted(set(observed) - allowed),
                      "mcp_result_returned": any(marker in json.dumps(x) for x in calls),
                      "workspace_proof": proof.is_file() and proof.read_text() == marker,
                      "final_marker": "SYNTHETIC_ROUNDTRIP_OK" in result.stdout}
            report["passed"] = (result.returncode == 0 and not report["unexpected_tools"]
                                and report["mcp_result_returned"] and report["workspace_proof"] and report["final_marker"])
            return report
    finally:
        server.shutdown()
        server.server_close()
        thread.join()


if __name__ == "__main__":
    report = qualify()
    print(json.dumps(report, indent=2))
    raise SystemExit(0 if report["passed"] else 1)
