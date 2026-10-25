#!/usr/bin/env python3
"""Offline CP-020 spike. Trusted stdio MCP adapter; NOT a production service.

The caller fixes the workspace and image. Model input supplies only shell text.
Never mount a real chat or credential directory when running this experiment.
"""
import argparse
import json
import os
from pathlib import Path
import selectors
import signal
import shutil
import subprocess
import sys
import time
import uuid

OUTPUT_LIMIT = 65536
REQUEST_LIMIT = 131072
TOOL = {
    "name": "group_exec",
    "description": "Run a command in the isolated experimental group workspace; no network or account credentials.",
    "inputSchema": {"type": "object", "properties": {"cmd": {"type": "string", "minLength": 1, "maxLength": 16384}},
                    "required": ["cmd"], "additionalProperties": False},
}
ARGUMENT_HINT = ('group_exec requires exactly one field cmd containing a nonempty shell string. '
                 'Example: {"cmd":"printf canary"}. No other fields.')


class Executor:
    def __init__(self, workspace, image, timeout=10):
        source = Path(workspace)
        if not source.is_absolute() or source.is_symlink() or not source.is_dir():
            raise ValueError("workspace must be an existing absolute non-symlink directory")
        self.workspace = source.resolve(strict=True)
        if self.workspace == Path("/") or "," in str(self.workspace):
            raise ValueError("unsafe workspace")
        if not image or image.startswith("-"):
            raise ValueError("explicit image required")
        self.image, self.timeout = image, timeout
        self.docker = shutil.which("docker")
        if not self.docker:
            raise ValueError("docker not found")
        self.cancel_signal = None
        self.last_container_name = None

    def cancel(self, signum, _frame):
        # Do not raise from a signal handler: interrupting docker create can
        # race its daemon-side creation with cleanup. First collect create's
        # acknowledgement, then refuse to start and remove that exact name.
        self.cancel_signal = signum

    def _capture(self, argv, deadline, cancelable=True):
        proc = subprocess.Popen(argv, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                stdin=subprocess.DEVNULL)
        output, reason = bytearray(), None
        try:
            with selectors.DefaultSelector() as selector:
                selector.register(proc.stdout, selectors.EVENT_READ)
                while True:
                    remaining = deadline - time.monotonic()
                    if cancelable and self.cancel_signal is not None:
                        reason = "cancelled"
                        break
                    if remaining <= 0:
                        reason = "timeout"
                        break
                    if not selector.select(min(remaining, 0.2)):
                        continue
                    chunk = os.read(proc.stdout.fileno(), min(4096, OUTPUT_LIMIT + 1 - len(output)))
                    if not chunk:
                        break
                    output.extend(chunk)
                    if len(output) > OUTPUT_LIMIT:
                        reason = "output_limit"
                        break
            if reason:
                proc.kill()
            code = proc.wait(timeout=3)
        finally:
            if proc.poll() is None:
                proc.kill()
                proc.wait()
            proc.stdout.close()
        return code, output[:OUTPUT_LIMIT], reason

    def run(self, arguments):
        if not isinstance(arguments, dict) or set(arguments) != {"command"}:
            raise ValueError("only command is accepted")
        command = arguments["command"]
        if not isinstance(command, str) or not command or len(command) > 16384 or "\0" in command:
            raise ValueError("invalid command")
        if self.cancel_signal is not None:
            raise ValueError("executor is stopping")
        name = "gopod-isolation-spike-" + uuid.uuid4().hex
        self.last_container_name = name
        argv = [self.docker, "create", "--rm", "--pull=never", "--name", name,
                "--network=none", "--read-only", "--cap-drop=ALL",
                "--security-opt=no-new-privileges", "--pids-limit=64",
                "--memory=128m", "--cpus=0.5", "--user=1000:1000",
                "--tmpfs=/tmp:rw,noexec,nosuid,nodev,size=16m,mode=1777",
                "--mount", f"type=bind,src={self.workspace},dst=/workspace/chat",
                "--workdir=/workspace/chat", "--env=HOME=/tmp/group-home",
                "--entrypoint=/bin/sh", self.image, "-c", command]
        # Docker client environment is not passed into the container. No -e NAME
        # inheritance, socket mount, host gateway, privileges or model image.
        output, reason = bytearray(), None
        deadline = time.monotonic() + self.timeout
        try:
            # Create cannot execute the model's command. Let it acknowledge
            # cancellation before cleanup; unlike `run`, there is no late
            # create/start request capable of launching after rm has returned.
            code, create_output, reason = self._capture(argv, deadline, cancelable=False)
            if self.cancel_signal is not None:
                reason = "cancelled"
            if code == 0 and reason is None:
                code, output, reason = self._capture([self.docker, "start", "--attach", name], deadline)
            elif reason is None:
                output = create_output
        finally:
            # Killing docker's client does not kill the container. Remove only
            # the unique container created by this request, including timeout.
            subprocess.run([self.docker, "rm", "-f", name], stdin=subprocess.DEVNULL,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5)
        return {"exit_code": code, "stopped": reason,
                "output": bytes(output[:OUTPUT_LIMIT]).decode("utf-8", errors="replace")}


def request_lines(executor):
    """Bounded framing, also responsive to shutdown while stdin stays open."""
    pending = bytearray()
    with selectors.DefaultSelector() as selector:
        selector.register(sys.stdin.buffer, selectors.EVENT_READ)
        while executor.cancel_signal is None:
            if not selector.select(0.2):
                continue
            chunk = os.read(sys.stdin.fileno(), 4096)
            if not chunk:
                if pending:
                    yield bytes(pending)
                return
            pending.extend(chunk)
            while b"\n" in pending:
                line, _, rest = pending.partition(b"\n")
                pending = bytearray(rest)
                if len(line) > REQUEST_LIMIT:
                    yield None
                    return
                yield bytes(line)
                if executor.cancel_signal is not None:
                    return
            if len(pending) > REQUEST_LIMIT:
                yield None
                return


def mcp_command(arguments):
    """Strict external cmd schema; the internal Executor API stays command."""
    if not isinstance(arguments, dict) or set(arguments) != {"cmd"}:
        raise ValueError("invalid command")
    command = arguments["cmd"]
    if not isinstance(command, str) or not command or len(command) > 16384 or "\0" in command:
        raise ValueError("invalid command")
    return command


def call_tool(params, executor):
    if params.get("name") != "group_exec":
        raise ValueError("unknown tool")
    try:
        value = executor.run({"command": mcp_command(params.get("arguments"))})
    except ValueError as error:
        if str(error) not in {"only command is accepted", "invalid command"}:
            raise
        return {"content": [{"type": "text", "text": ARGUMENT_HINT}], "isError": True}
    return {"content": [{"type": "text", "text": json.dumps(value)}],
            "isError": value["stopped"] is not None or value["exit_code"] != 0}


def dispatch(request, executor):
    method = request.get("method")
    if "id" not in request:
        return None
    result = None
    if method == "initialize":
        version = request.get("params", {}).get("protocolVersion", "2024-11-05")
        if version not in {"2024-11-05", "2025-03-26", "2025-06-18"}:
            version = "2024-11-05"
        result = {"protocolVersion": version, "capabilities": {"tools": {}},
                  "serverInfo": {"name": "gopod-isolation-spike", "version": "0.1"}}
    elif method == "ping":
        result = {}
    elif method == "tools/list":
        result = {"tools": [TOOL]}
    elif method == "tools/call":
        result = call_tool(request.get("params", {}), executor)
    else:
        return {"jsonrpc": "2.0", "id": request["id"],
                "error": {"code": -32601, "message": "Method not found"}}
    return {"jsonrpc": "2.0", "id": request["id"], "result": result}


def response_for(line, executor):
    request = None
    try:
        request = json.loads(line)
        if not isinstance(request, dict):
            raise ValueError("object required")
        return dispatch(request, executor)
    except Exception:
        # Never echo malformed input, host paths or Docker diagnostics.
        identifier = request.get("id") if isinstance(request, dict) else None
        return {"jsonrpc": "2.0", "id": identifier,
                "error": {"code": -32602, "message": "Request rejected or execution unavailable"}}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workspace", required=True)
    parser.add_argument("--image", required=True, help="trusted preloaded tool-only image, preferably digest")
    options = parser.parse_args()
    executor = Executor(options.workspace, options.image)
    for signum in (signal.SIGTERM, signal.SIGINT):
        signal.signal(signum, executor.cancel)
    for line in request_lines(executor):
        if line is None:
            return 2
        response = response_for(line, executor)
        if response is not None:
            print(json.dumps(response), flush=True)
    return 128 + executor.cancel_signal if executor.cancel_signal is not None else 0


if __name__ == "__main__":
    raise SystemExit(main())
