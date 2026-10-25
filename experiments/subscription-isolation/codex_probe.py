#!/usr/bin/env python3
"""Pinned Codex offline native tool inventory via synthetic localhost provider.

No actual model endpoint, authentication or account configuration is used.
"""
import argparse
import http.server
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading

DISABLED_FEATURES = (
    "shell_tool", "shell_snapshot", "unified_exec", "apps", "plugins",
    "remote_plugin", "plugin_sharing", "hooks", "multi_agent", "multi_agent_v2",
    "skill_mcp_dependency_install", "skill_search", "browser_use",
    "browser_use_external", "computer_use", "image_generation", "view_image",
)


def tool_metadata(tool):
    result = {"type": tool.get("type"), "name": tool.get("name")}
    if "tools" in tool:
        result["tools"] = [{"name": nested.get("name"), "type": nested.get("type")} for nested in tool["tools"]]
    return result


def mock_tool_output(server):
    output = []
    if server.mcp and len(server.captured) == 1:
        namespace = next((x for x in server.captured[-1]["tools"] if x.get("type") == "namespace" and x.get("name") == "mcp__group"), None)
        name = "group_exec" if namespace else next(x["name"] for x in server.captured[-1]["tools"] if (x.get("name") or "").endswith("group_exec"))
        output = [{"type": "function_call", "id": "fc_canary", "call_id": "call_canary",
                   "name": name, "arguments": json.dumps({"cmd": "printf codex-mcp-ok > codex-proof.txt; cat codex-proof.txt"}), "status": "completed"}]
        if namespace:
            output[0]["namespace"] = namespace["name"]
    if server.native_attack and len(server.captured) == 1:
        output = [{"type": "function_call", "id": "fc_canary", "call_id": "call_canary",
                   "name": "exec_command", "arguments": json.dumps({"cmd": "printf bypass > native-bypass"}), "status": "completed"}]
    return output


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        # Suppress request/endpoint logs: reports contain only synthetic metadata.
        return

    def do_GET(self):
        body = json.dumps({"object": "list", "data": []}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        request = json.loads(self.rfile.read(length))
        self.server.tool_result_seen = self.server.tool_result_seen or any(
            x.get("type") == "function_call_output" and "codex-mcp-ok" in str(x.get("output"))
            for x in request.get("input", []) if isinstance(x, dict))
        self.server.function_result_seen = self.server.function_result_seen or any(
            x.get("type") == "function_call_output" for x in request.get("input", []) if isinstance(x, dict))
        self.server.captured.append({"path": self.path,
                         "tools": [tool_metadata(x) for x in request.get("tools", [])],
                         "has_authorization": self.headers.get("Authorization") is not None})
        output = mock_tool_output(self.server)
        response = {"id": "resp_offline_canary", "object": "response", "status": "completed",
                    "model": "gpt-5", "output": output, "usage": {"input_tokens": 1, "output_tokens": 0, "total_tokens": 1}}
        frames = [
            {"type": "response.created", "response": {**response, "status": "in_progress"}},
        ]
        if output:
            frames.extend([
                {"type": "response.output_item.added", "output_index": 0, "item": {**output[0], "arguments": "", "status": "in_progress"}},
                {"type": "response.function_call_arguments.delta", "item_id": "fc_canary", "output_index": 0, "delta": output[0]["arguments"]},
                {"type": "response.function_call_arguments.done", "item_id": "fc_canary", "output_index": 0, "arguments": output[0]["arguments"]},
                {"type": "response.output_item.done", "output_index": 0, "item": output[0]},
            ])
        frames.append({"type": "response.completed", "response": response})
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for frame in frames:
            self.wfile.write(("data: " + json.dumps(frame) + "\n\n").encode())


def probe(mcp=False, image="node:22-slim", native_attack=False, missing_mcp=False, http_mcp=False):
    bridge = None

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.captured = []
    server.tool_result_seen = False
    server.function_result_seen = False
    server.mcp = mcp
    server.native_attack = native_attack
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix="gopod-codex-offline-") as root:
            base = Path(root)
            home, work = base / "home", base / "work"
            home.mkdir()
            work.mkdir()
            work.chmod(0o777)
            cli_home = home / ".codex"
            cli_home.mkdir()
            config = [
                'model = "gpt-5"',
                'model_provider = "offline_canary"',
                'approval_policy = "never"',
                'sandbox_mode = "read-only"',
                'web_search = "disabled"',
                'check_for_update_on_startup = false',
                '[model_providers.offline_canary]',
                'name = "Offline synthetic canary"',
                f'base_url = "http://127.0.0.1:{server.server_port}/v1"',
                'wire_api = "responses"',
                'requires_openai_auth = false',
                'supports_websockets = false',
                '[features]',
            ]
            config.extend(f"{key} = false" for key in DISABLED_FEATURES)
            config.extend(['[apps._default]', 'enabled = false'])
            if mcp:
                tool_script = str(Path(__file__).with_name("tool_server.py").resolve())
                if missing_mcp:
                    tool_script = str(base / "missing_server.py")
                import shutil
                transport_config = [
                    'command = ' + json.dumps(shutil.which("python3")),
                    'args = ' + json.dumps([tool_script, "--workspace", str(work), "--image", image]),
                ]
                if http_mcp:
                    from http_tool_bridge import start_bridge
                    bridge, endpoint = start_bridge(str(work), image)
                    transport_config = ['url = ' + json.dumps(endpoint)]
                config.extend([
                    '[mcp_servers.group]',
                    *transport_config,
                    'enabled_tools = ["group_exec"]',
                    'required = true',
                    'tool_timeout_sec = 15',
                    'startup_timeout_sec = 5',
                    '[mcp_servers.group.tools.group_exec]',
                    'approval_mode = "approve"',
                ])
            (cli_home / "config.toml").write_text("\n".join(config) + "\n")
            env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": str(home),
                   "CODEX_HOME": str(cli_home), "TMPDIR": str(base)}
            # The trusted harness fixes the executable and every CLI argument;
            # provider/model input cannot choose a program, path or shell string.
            version = subprocess.check_output(["codex", "--version"], text=True, env=env).strip()
            if version != "codex-cli 0.164.0":
                raise RuntimeError("pinned Codex version required")
            result = subprocess.run(["codex", "exec", "--strict-config", "--ephemeral", "--skip-git-repo-check",
                                     "--json", "Reply with offline-canary only; do not invoke tools."],
                                    cwd=work, env=env, text=True, capture_output=True, timeout=25)
            # Never return raw CLI streams: future versions could include inputs,
            # paths or diagnostics. Only synthetic metadata is reported.
            return {"version": version, "exit_code": result.returncode,
                    "requests": server.captured, "disabled_features": list(DISABLED_FEATURES),
                    "stderr_present": bool(result.stderr), "stdout_lines": len(result.stdout.splitlines()),
                    "mcp_roundtrip": server.tool_result_seen, "function_result_seen": server.function_result_seen,
                    "native_marker_created": (work / "native-bypass").exists(),
                    "external_file_proof": (work / "codex-proof.txt").is_file() and (work / "codex-proof.txt").read_text() == "codex-mcp-ok",
                    "mcp_transport": "http" if http_mcp else "stdio"}
    finally:
        if bridge is not None:
            bridge.close()
        server.shutdown()
        server.server_close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mcp", action="store_true", help="execute synthetic command via real Docker-backed MCP")
    parser.add_argument("--image", default="node:22-slim")
    parser.add_argument("--native-attack", action="store_true", help="mock provider requests disabled native execution")
    parser.add_argument("--missing-mcp", action="store_true", help="qualify fail-closed required MCP startup")
    parser.add_argument("--http-mcp", action="store_true", help="use capability-protected HTTP tool bridge instead of stdio")
    args = parser.parse_args()
    print(json.dumps(probe(mcp=args.mcp or args.missing_mcp or args.http_mcp, image=args.image,
                           native_attack=args.native_attack, missing_mcp=args.missing_mcp,
                           http_mcp=args.http_mcp), indent=2))
