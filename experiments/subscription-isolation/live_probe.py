#!/usr/bin/env python3
"""Opt-in, bounded subscription canary in a disposable trusted CLI container.

Mounts one operator-supplied native auth file read-only, never reads/copies/prints
it. No production chat/context/config is mounted. No API-key fallback is supplied.
"""
import argparse
import json
import os
from pathlib import Path
import selectors
import signal
import stat
import subprocess
import tempfile
import threading
import time
import uuid
from collections import Counter
from dataclasses import dataclass

from claude_policy import SETTINGS, ENV_OVERRIDES
from codex_probe import DISABLED_FEATURES
from http_tool_bridge import start_bridge


CAPTURE_LIMIT = 262144
CLAUDE_ALLOWED_TOOLS = {"mcp__group__group_exec", "EndConversation"}


@dataclass
class Capture:
    returncode: int
    stdout: str
    stderr: str = ""
    stopped: str | None = None


class Cancellation:
    def __init__(self):
        self.signum = None
        self.previous = {}

    def __enter__(self):
        for signum in (signal.SIGTERM, signal.SIGINT):
            self.previous[signum] = signal.signal(signum, self.cancel)
        return self

    def cancel(self, signum, _frame=None):
        self.signum = signum

    def __exit__(self, *_args):
        for signum, handler in self.previous.items():
            signal.signal(signum, handler)


def capture(argv, cancellation, timeout, *, cancelable=True):
    """Bounded in-memory diagnostics; never log native command/output."""
    if cancelable and cancellation.signum is not None:
        return Capture(-1, "", stopped="cancelled")
    proc = subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                            stderr=subprocess.STDOUT)
    deadline = time.monotonic() + timeout
    output = bytearray()
    stopped = None
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(proc.stdout, selectors.EVENT_READ)
            while True:
                if cancelable and cancellation.signum is not None:
                    stopped = "cancelled"
                    break
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    stopped = "deadline"
                    break
                if not selector.select(min(remaining, 0.2)):
                    continue
                data = os.read(proc.stdout.fileno(), min(4096, CAPTURE_LIMIT + 1 - len(output)))
                if not data:
                    break
                output.extend(data)
                if len(output) > CAPTURE_LIMIT:
                    stopped = "output_limit"
                    break
        if stopped:
            proc.kill()
        code = proc.wait(timeout=3)
    finally:
        if proc.poll() is None:
            proc.kill()
            proc.wait()
        proc.stdout.close()
    return Capture(code, bytes(output[:CAPTURE_LIMIT]).decode("utf-8", errors="replace"), stopped=stopped)


def proof_matches(workspace, name, marker):
    """Never follow an agent-created symlink/FIFO or read an unbounded file."""
    if not name or Path(name).name != name:
        return False
    directory = None
    descriptor = None
    try:
        directory = os.open(workspace, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        descriptor = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
        metadata = os.fstat(descriptor)
        expected = marker.encode()
        if not stat.S_ISREG(metadata.st_mode) or metadata.st_nlink != 1 or metadata.st_size != len(expected):
            return False
        return os.read(descriptor, len(expected) + 1) == expected
    except (OSError, ValueError):
        return False
    finally:
        if descriptor is not None:
            os.close(descriptor)
        if directory is not None:
            os.close(directory)


class ToolAudit:
    """Trusted bounded execution evidence; never retain arguments or output."""
    MAX_CALLS = 8

    def __init__(self, executor, workspace, marker, cancellation):
        self.executor = executor
        self.workspace = workspace
        self.marker = marker
        self.cancellation = cancellation
        self.original_run = executor.run
        self.lock = threading.Lock()
        self.count = 0
        self.over_budget = False
        self.expected = None
        self.proof = None
        self.records = []
        executor.run = self.run

    def configure(self, expected, proof):
        with self.lock:
            self.expected, self.proof = expected, proof
            self.records = []

    def snapshot(self):
        with self.lock:
            return {'calls': [dict(x) for x in self.records], 'total_calls': self.count,
                    'call_limit': self.MAX_CALLS, 'call_limit_exceeded': self.over_budget}

    def run(self, arguments):
        with self.lock:
            if self.count >= self.MAX_CALLS or self.expected is None:
                self.over_budget = True
                self.executor.cancel_signal = signal.SIGTERM
                self.cancellation.cancel(signal.SIGTERM)
                raise ValueError('tool call budget unavailable')
            self.count += 1
            expected, proof = self.expected, self.proof
            record = {'command_matches_expected': isinstance(arguments, dict) and arguments.get('command') == expected,
                      'exit_code': None, 'stopped': 'execution_unavailable',
                      'marker_returned': False, 'proof_matches_after': False}
            self.records.append(record)
        try:
            result = self.original_run(arguments)
            code, stopped, output = result.get('exit_code'), result.get('stopped'), result.get('output')
            with self.lock:
                record['exit_code'] = code if type(code) is int else None
                record['stopped'] = stopped if stopped in (None, 'timeout', 'output_limit', 'cancelled') else 'other'
                record['marker_returned'] = isinstance(output, str) and self.marker in output
            return result
        finally:
            matched = proof_matches(self.workspace, proof, self.marker)
            with self.lock:
                record['proof_matches_after'] = matched


def sanitized_events(output, provider):
    event_types, item_types, tool_names = Counter(), Counter(), Counter()
    inventory_seen, inventory_valid, unexpected = False, True, 0
    events = []
    known_events = {"system", "assistant", "user", "result", "thread.started", "turn.started", "turn.completed",
                    "turn.failed", "item.started", "item.updated", "item.completed", "error"}
    known_items = {"command_execution", "mcp_tool_call", "agent_message", "reasoning", "file_change",
                   "web_search", "todo_list", "error"}
    for line in output.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if not isinstance(event, dict):
            continue
        events.append(event)
        kind = event.get("type")
        event_types[kind if isinstance(kind, str) and kind in known_events else "other"] += 1
        item = event.get("item", {})
        if isinstance(item, dict) and item:
            kind = item.get("type")
            item_types[kind if isinstance(kind, str) and kind in known_items else "other"] += 1
            if kind == "mcp_tool_call":
                tool = item.get("tool")
                if isinstance(tool, str) and tool in {"group_exec", "mcp__group__group_exec"}:
                    tool_names[tool] += 1
                else:
                    tool_names["other"] += 1
        if provider == "claude" and event.get("type") == "system" and event.get("subtype") == "init":
            inventory_seen = True
            tools = event.get("tools")
            if not isinstance(tools, list):
                inventory_valid = False
                continue
            for tool in tools:
                if isinstance(tool, str) and tool in CLAUDE_ALLOWED_TOOLS:
                    tool_names[tool] += 1
                else:
                    unexpected += 1
    needles = {"approval": "approval", "mcp": "mcp", "unsupported": "unsupported", "not_found": "not found",
               "denied": "denied", "disabled": "disabled", "unavailable": "unavailable", "connection": "connect",
               "auth": "not logged in", "quota": "usage limit", "expired": "expired", "readonly": "read-only file system"}
    lower = output.lower()
    report = {"event_types": dict(event_types), "item_types": dict(item_types), "known_tool_counts": dict(tool_names),
              "diagnostic_flags": {label: needle in lower for label, needle in needles.items()},
              "inventory_observed": inventory_seen, "unexpected_tool_count": unexpected,
              "inventory_allowed": inventory_seen and inventory_valid and unexpected == 0}
    # Parsed events stay in-process only; never return raw payloads in a report.
    return report, events


def cleanup(name, server):
    try:
        subprocess.run(['docker', 'rm', '-f', name], capture_output=True, timeout=15)
    finally:
        server.close()


def run(options):
    with Cancellation() as cancellation:
        return _run(options, cancellation)


def _run(options, cancellation):
    auth = Path(options.auth_file)
    if not auth.is_absolute() or auth.is_symlink() or not auth.is_file():
        raise ValueError("operator must supply an existing absolute native auth file")
    image = options.cli_image
    if not image or image.startswith("-"):
        raise ValueError("explicit trusted CLI image required")
    name = "gopod-trusted-cli-canary-" + uuid.uuid4().hex
    marker = "gopod_subscription_canary"
    report = {"provider": options.provider, "scope": "synthetic new/resume; existing native subscription auth",
              "auth_mounted_readonly": True, "auth_copied": False, "api_key_fallback": False,
              "roundtrip_passed": False, "isolation_qualified": False, "turns": []}
    with tempfile.TemporaryDirectory(prefix="gopod-live-canary-") as root:
        root = Path(root)
        work, policy = root / "group", root / "policy"
        work.mkdir(mode=0o777)
        work.chmod(0o777)
        policy.mkdir()
        server, url = start_bridge(str(work), options.tool_image, bind="0.0.0.0")
        audit = ToolAudit(server.executor, work, marker, cancellation)
        endpoint = url.replace("127.0.0.1", "host.docker.internal", 1)
        try:
            if options.provider == "claude":
                target = "/home/node/.claude/.credentials.json"
                (policy / "mcp.json").write_text(json.dumps({"mcpServers": {"group": {"type": "http", "url": endpoint}}}))
            else:
                target = "/home/node/.codex/auth.json"
                lines = ['approval_policy = "never"', 'sandbox_mode = "read-only"', 'web_search = "disabled"',
                         'check_for_update_on_startup = false', 'model_reasoning_effort = "low"', '[features]']
                lines += [key + " = false" for key in DISABLED_FEATURES]
                lines += ['[apps._default]', 'enabled = false', '[mcp_servers.group]',
                          'url = ' + json.dumps(endpoint), 'required = true', 'enabled_tools = ["group_exec"]',
                          'startup_timeout_sec = 10', 'tool_timeout_sec = 15',
                          '[mcp_servers.group.tools.group_exec]', 'approval_mode = "approve"']
                (policy / "config.toml").write_text("\n".join(lines) + "\n")
            command = ['docker', 'create', '--rm', '--pull=never', '--init', '--name', name,
                       '--read-only', '--cap-drop=ALL', '--security-opt=no-new-privileges',
                       '--pids-limit=128', '--memory=768m', '--cpus=1', '--user=1000:1000',
                       '--add-host=host.docker.internal:host-gateway',
                       '--tmpfs=/tmp:rw,nosuid,nodev,size=64m,mode=1777',
                       '--tmpfs=/home/node:rw,nosuid,nodev,size=96m,uid=1000,gid=1000,mode=700',
                       '--tmpfs=/home/node/.claude:rw,nosuid,nodev,size=32m,uid=1000,gid=1000,mode=700',
                       '--tmpfs=/home/node/.codex:rw,nosuid,nodev,size=32m,uid=1000,gid=1000,mode=700',
                       '--tmpfs=/workspace/chat:rw,nosuid,nodev,size=16m,uid=1000,gid=1000,mode=700',
                       '--mount', f'type=bind,src={auth},dst={target},readonly',
                       '--mount', f'type=bind,src={policy},dst=/qualification,readonly',
                       '--workdir=/workspace/chat', '--env=HOME=/home/node']
            if options.provider == "codex":
                command += ['--mount', f'type=bind,src={policy / "config.toml"},dst=/home/node/.codex/config.toml,readonly']
            command += ['--entrypoint=/bin/sleep', image, 'infinity']
            # A create request cannot execute a command. Wait for its bounded
            # acknowledgement even on cancellation, then never start if stopped.
            created = capture(command, cancellation, timeout=20, cancelable=False)
            if created.returncode != 0 or created.stopped or cancellation.signum is not None:
                report['failure'] = 'cancelled' if cancellation.signum is not None else 'create_unavailable'
                return report
            started = capture(['docker', 'start', name], cancellation, timeout=10)
            if started.returncode != 0 or started.stopped:
                report['failure'] = started.stopped or 'start_unavailable'
                return report
            version_result = capture(['docker', 'exec', name, options.provider, '--version'], cancellation, timeout=10)
            version = version_result.stdout.strip()
            expected = '2.1.310 (Claude Code)' if options.provider == "claude" else 'codex-cli 0.164.0'
            report['version_match'] = version == expected
            if version != expected:
                return report
            session = str(uuid.uuid4()) if options.provider == "claude" else None
            for turn in ("new", "resume"):
                proof = f"{options.provider}-{turn}.txt"
                expected_command = f"printf {marker} > {proof}; cat {proof}"
                audit.configure(expected_command, proof)
                prompt = (f"This is an isolation canary, not a user chat. Use only the group MCP tool group_exec once "
                          f"to run exactly: {expected_command}. "
                          "Do not inspect accounts, environment, other files, network or any other tools. "
                          f"Then reply exactly {marker}. No planning or extra work.")
                cli = ['docker', 'exec']
                if options.provider == "claude":
                    for key, value in ENV_OVERRIDES.items():
                        cli += ['--env', key + '=' + value]
                    cli += [name, 'claude', '--restricted', '--tools', '', '--strict-mcp-config',
                            '--mcp-config', '/qualification/mcp.json', '--settings', json.dumps(SETTINGS),
                            '--setting-sources', '', '--disable-slash-commands', '--permission-mode', 'dontAsk',
                            '--allowedTools', 'mcp__group__group_exec', '--system-prompt-snapshot', 'off',
                            '--system-prompt', 'Use only the supplied isolated group MCP tool for the synthetic canary.',
                            '--model', 'claude-haiku-4-5', '--max-turns', '3',
                            '--session-id' if turn == "new" else '--resume', session,
                            '--output-format', 'stream-json', '--verbose', '-p', prompt]
                else:
                    cli += ['--env', 'CODEX_HOME=/home/node/.codex', name, 'codex', 'exec']
                    if turn == "resume":
                        cli += ['resume', session]
                    cli += ['--strict-config', '--skip-git-repo-check', '--json', prompt]
                result = capture(cli, cancellation, timeout=90)
                if result.stopped:
                    report['turns'].append({'turn': turn, 'passed': False, 'failure': result.stopped,
                                            'tool_execution': audit.snapshot()})
                    break
                metadata, events = sanitized_events(result.stdout, options.provider)
                if options.provider == "codex":
                    for event in events:
                        if event.get('type') == 'thread.started':
                            try:
                                session = str(uuid.UUID(event.get('thread_id')))
                            except (ValueError, TypeError, AttributeError):
                                session = None
                external_proof = result.returncode == 0 and proof_matches(work, proof, marker)
                inventory_allowed = options.provider != 'claude' or metadata['inventory_allowed']
                passed = external_proof and inventory_allowed
                entry = {'turn': turn, 'exit_code': result.returncode, 'external_tool_proof': external_proof,
                         'roundtrip_passed': external_proof, 'passed': passed, 'metadata': metadata,
                         'tool_execution': audit.snapshot()}
                if not passed:
                    combined = (result.stdout + result.stderr).lower()
                    entry['failure'] = next((label for needle, label in [
                        ('rate limit', 'quota_or_rate_limit'), ('usage limit', 'quota_or_rate_limit'),
                        ('not logged in', 'auth_unavailable'), ('unauthorized', 'auth_unavailable'),
                        ('expired', 'auth_refresh_unavailable'), ('requires approval', 'tool_approval'),
                        ('failed to connect', 'connection'), ('read-only file system', 'readonly_state')]
                        if needle in combined), 'unclassified_no_raw_output')
                    if not inventory_allowed:
                        entry['failure'] = 'live_inventory_unqualified'
                report['turns'].append(entry)
                if not passed or not session:
                    break
            report['roundtrip_passed'] = (len(report['turns']) == 2
                                          and all(x.get('roundtrip_passed') for x in report['turns']))
            report['passed'] = len(report['turns']) == 2 and all(x['passed'] for x in report['turns'])
            return report
        finally:
            # A timed-out docker exec may leave a CLI process. Remove the entire
            # disposable CLI container; never target an existing gopod runtime.
            cleanup(name, server)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--provider', required=True, choices=['claude', 'codex'])
    parser.add_argument('--auth-file', required=True)
    parser.add_argument('--cli-image', required=True)
    parser.add_argument('--tool-image', required=True)
    options = parser.parse_args()
    try:
        report = run(options)
    except Exception:
        # Do not expose native CLI streams, endpoint capabilities or credentials.
        report = {'provider': options.provider, 'passed': False, 'roundtrip_passed': False,
                  'isolation_qualified': False, 'failure': 'qualification_unavailable_no_raw_output'}
    print(json.dumps(report, indent=2))
    raise SystemExit(0 if report.get('passed') else 1)
