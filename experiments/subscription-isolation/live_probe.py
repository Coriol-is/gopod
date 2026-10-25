#!/usr/bin/env python3
"""Opt-in, bounded subscription canary in a disposable trusted CLI container.

Mounts one operator-supplied native auth file read-only, never reads/copies/prints
it. No production chat/context/config is mounted. No API-key fallback is supplied.
"""
import argparse
import json
import os
import re
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
from enum import Enum, auto

from claude_policy import SETTINGS, ENV_OVERRIDES
from codex_probe import DISABLED_FEATURES
from http_tool_bridge import start_bridge


CAPTURE_LIMIT = 262144
INPUT_LIMIT = 16384
CLAUDE_ALLOWED_TOOLS = {"mcp__group__group_exec", "EndConversation"}
EXPECTED_VERSIONS = {'claude': '2.1.310 (Claude Code)', 'codex': 'codex-cli 0.164.0'}


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


def input_payload(input_text):
    if input_text is None:
        return b''
    if not isinstance(input_text, str):
        raise ValueError('input must be text')
    payload = input_text.encode('utf-8')
    if len(payload) > INPUT_LIMIT:
        raise ValueError('input exceeds limit')
    return payload


class CaptureIO:
    """Nonblocking bounded stdin/stdout; never wait on a child input reader."""
    def __init__(self, proc, payload, cancellation, deadline, cancelable):
        self.proc, self.payload = proc, payload
        self.cancellation, self.deadline, self.cancelable = cancellation, deadline, cancelable
        self.position, self.output, self.stopped = 0, bytearray(), None
        self.reader_done = False

    def write_input(self, selector):
        try:
            count = os.write(self.proc.stdin.fileno(), self.payload[self.position:self.position + 4096])
            self.position += count
        except BrokenPipeError:
            self.position = len(self.payload)
        except BlockingIOError:
            return
        if self.position == len(self.payload):
            selector.unregister(self.proc.stdin)
            self.proc.stdin.close()

    def read_output(self, selector):
        data = os.read(self.proc.stdout.fileno(), min(4096, CAPTURE_LIMIT + 1 - len(self.output)))
        if not data:
            selector.unregister(self.proc.stdout)
            self.reader_done = True
            return
        self.output.extend(data)
        if len(self.output) > CAPTURE_LIMIT:
            self.stopped = 'output_limit'

    def stop_reason(self):
        if self.cancelable and self.cancellation.signum is not None:
            return 'cancelled'
        if time.monotonic() >= self.deadline:
            return 'deadline'
        return self.stopped

    def complete(self):
        return self.reader_done and (self.proc.stdin is None or self.proc.stdin.closed)

    def run(self):
        with selectors.DefaultSelector() as selector:
            selector.register(self.proc.stdout, selectors.EVENT_READ, self.read_output)
            if self.proc.stdin is not None:
                os.set_blocking(self.proc.stdin.fileno(), False)
                selector.register(self.proc.stdin, selectors.EVENT_WRITE, self.write_input)
            while not self.complete():
                self.stopped = self.stop_reason()
                if self.stopped:
                    break
                for key, _events in selector.select(max(0, min(self.deadline - time.monotonic(), 0.2))):
                    key.data(selector)
                    if self.stopped:
                        break
        return self.output[:CAPTURE_LIMIT], self.stopped


def _collect_process(proc, payload, cancellation, timeout, *, cancelable=True):
    """Collect an already-created process; this helper cannot execute commands."""
    try:
        output, stopped = CaptureIO(proc, payload, cancellation, time.monotonic() + timeout, cancelable).run()
        if stopped:
            proc.kill()
        code = proc.wait(timeout=3)
    finally:
        if proc.poll() is None:
            proc.kill()
            proc.wait()
        proc.stdout.close()
        if proc.stdin is not None and not proc.stdin.closed:
            proc.stdin.close()
    return Capture(code, bytes(output).decode("utf-8", errors="replace"), stopped=stopped)


class Operation(Enum):
    CREATE = auto()
    START = auto()
    VERSION = auto()
    AUTH_STATUS = auto()
    NEW_TURN = auto()
    RESUME_TURN = auto()


OPERATION_TIMEOUTS = {Operation.CREATE: 20, Operation.START: 10, Operation.VERSION: 10,
                      Operation.AUTH_STATUS: 10, Operation.NEW_TURN: 90, Operation.RESUME_TURN: 90}


class CanaryRuntime:
    """Namespace-bound operations, with no raw argv/container/flag input API."""
    def __init__(self, provider, image, auth, policy, cancellation):
        validate_provider(provider)
        validate_image(image)
        self._provider, self._image = provider, image
        self._auth = canonical_auth_file(auth)
        validate_mount_source(policy)
        self._policy = Path(policy).resolve(strict=True)
        validate_mount_source(self._policy)
        if not self._policy.is_dir():
            raise ValueError('invalid policy directory')
        self._name = 'gopod-trusted-cli-canary-' + uuid.uuid4().hex
        self._cancellation = cancellation
        self._created, self._started, self._closed = False, False, False

    @property
    def provider(self):
        return self._provider

    @property
    def name(self):
        return self._name

    def _command(self, operation, session=None):
        if not isinstance(operation, Operation):
            raise ValueError('typed canary operation required')
        if operation is Operation.CREATE:
            target = {'claude': '/home/node/.claude/.credentials.json', 'codex': '/home/node/.codex/auth.json'}[self._provider]
            return _container_command(self._provider, self._auth, self._policy, self._name, target, self._image)
        if operation is Operation.START:
            return ['docker', 'start', '--', self._name]
        if operation is Operation.VERSION:
            return ['docker', 'exec', '--', self._name, self._provider, '--version']
        if operation is Operation.AUTH_STATUS:
            return _auth_status_command(self._provider, self._name)
        turn = 'new' if operation is Operation.NEW_TURN else 'resume'
        if self._provider == 'claude' or turn == 'resume':
            session = str(uuid.UUID(session))
        return _turn_command(self._provider, self._name, session, turn)

    def _check_state(self, operation):
        if self._closed or not isinstance(operation, Operation):
            raise ValueError('canary runtime unavailable')
        if operation is Operation.CREATE:
            valid = not self._created
        elif operation is Operation.START:
            valid = self._created and not self._started
        else:
            valid = self._started
        if not valid:
            raise ValueError('canary operation not available in current state')

    def _execute(self, operation, *, session=None, input_text=None):
        self._check_state(operation)
        payload = input_payload(input_text)
        command = self._command(operation, session)
        cancelable = operation is not Operation.CREATE
        if cancelable and self._cancellation.signum is not None:
            return Capture(-1, '', stopped='cancelled')
        proc = subprocess.Popen(command, stdin=subprocess.PIPE if payload else subprocess.DEVNULL,
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, shell=False, bufsize=0)
        result = _collect_process(proc, payload, self._cancellation, OPERATION_TIMEOUTS[operation], cancelable=cancelable)
        if result.returncode == 0 and result.stopped is None:
            self._created = self._created or operation is Operation.CREATE
            self._started = self._started or operation is Operation.START
        return result

    def create(self):
        return self._execute(Operation.CREATE)

    def start(self):
        return self._execute(Operation.START)

    def version(self):
        return self._execute(Operation.VERSION)

    def auth_status(self):
        return self._execute(Operation.AUTH_STATUS)

    def new_turn(self, session, prompt):
        return self._execute(Operation.NEW_TURN, session=session, input_text=prompt)

    def resume_turn(self, session, prompt):
        return self._execute(Operation.RESUME_TURN, session=session, input_text=prompt)

    def close(self):
        self._closed = True
        subprocess.run(['docker', 'rm', '-f', '--', self._name], capture_output=True, timeout=15)


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

    def record_call(self, arguments):
        with self.lock:
            if self.count >= self.MAX_CALLS or self.expected is None:
                self.over_budget = True
                self.executor.cancel_signal = signal.SIGTERM
                self.cancellation.cancel(signal.SIGTERM)
                raise ValueError('tool call budget unavailable')
            self.count += 1
            expected, proof = self.expected, self.proof
            record = {'command_matches_expected': isinstance(arguments, dict) and arguments.get('command') == expected,
                      'exit_code': None, 'stopped': 'unknown',
                      'marker_returned': False, 'proof_matches_after': False}
            record.update(argument_shape(arguments))
            self.records.append(record)
            return record, proof

    def record_result(self, record, result):
        code, stopped, output = result.get('exit_code'), result.get('stopped'), result.get('output')
        with self.lock:
            record['exit_code'] = code if type(code) is int else None
            record['stopped'] = stopped if stopped in (None, 'timeout', 'output_limit', 'cancelled') else 'other'
            record['marker_returned'] = isinstance(output, str) and self.marker in output

    def run(self, arguments):
        record, proof = self.record_call(arguments)
        try:
            result = self.original_run(arguments)
            self.record_result(record, result)
            return result
        except Exception as error:
            with self.lock:
                record['stopped'] = exception_reason(error)
            raise
        finally:
            matched = proof_matches(self.workspace, proof, self.marker)
            with self.lock:
                record['proof_matches_after'] = matched


def exception_reason(error):
    if isinstance(error, ValueError) and str(error) in {
            'only command is accepted', 'invalid command', 'executor is stopping'}:
        return 'invalid_arguments'
    if isinstance(error, subprocess.TimeoutExpired):
        return 'cleanup_timeout'
    if isinstance(error, OSError):
        return 'spawn_unavailable'
    return 'unknown'


def argument_shape(arguments):
    def kind(value):
        if value is None:
            return 'null'
        if isinstance(value, dict):
            return 'object'
        if isinstance(value, str):
            return 'string'
        if isinstance(value, list):
            return 'array'
        return 'other'
    has_command = isinstance(arguments, dict) and 'command' in arguments
    command = arguments.get('command') if isinstance(arguments, dict) else None
    return {'arguments_type': kind(arguments), 'has_command': has_command,
            'extra_keys_count': sum(key != 'command' for key in arguments) if isinstance(arguments, dict) else 0,
            'known_argument_fields': sorted(key for key in {'cmd', 'code', 'script', 'input', 'arguments', 'payload'}
                                           if isinstance(arguments, dict) and key in arguments),
            'command_type': kind(command), 'command_length': len(command) if isinstance(command, str) else None}


def _known_label(value, allowed):
    return value if isinstance(value, str) and value in allowed else 'other'


def _json_events(output):
    for line in output.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if isinstance(event, dict):
            yield event


def _record_item(event, item_types, tool_names):
    known_items = {'command_execution', 'mcp_tool_call', 'agent_message', 'reasoning', 'file_change',
                   'web_search', 'todo_list', 'error'}
    item = event.get('item', {})
    if not isinstance(item, dict) or not item:
        return
    kind = item.get('type')
    item_types[_known_label(kind, known_items)] += 1
    if kind == 'mcp_tool_call':
        tool_names[_known_label(item.get('tool'), {'group_exec', 'mcp__group__group_exec'})] += 1


def _claude_inventory(event, provider, tool_names):
    if provider != 'claude' or event.get('type') != 'system' or event.get('subtype') != 'init':
        return False, True, 0
    tools = event.get('tools')
    if not isinstance(tools, list):
        return True, False, 0
    unexpected = 0
    for tool in tools:
        if isinstance(tool, str) and tool in CLAUDE_ALLOWED_TOOLS:
            tool_names[tool] += 1
        else:
            unexpected += 1
    return True, True, unexpected


def sanitized_events(output, provider):
    event_types, item_types, tool_names = Counter(), Counter(), Counter()
    inventory_seen, inventory_valid, unexpected = False, True, 0
    events = []
    known_events = {'system', 'assistant', 'user', 'result', 'thread.started', 'turn.started', 'turn.completed',
                    'turn.failed', 'item.started', 'item.updated', 'item.completed', 'error'}
    for event in _json_events(output):
        events.append(event)
        event_types[_known_label(event.get('type'), known_events)] += 1
        _record_item(event, item_types, tool_names)
        seen, valid, count = _claude_inventory(event, provider, tool_names)
        inventory_seen = inventory_seen or seen
        inventory_valid = inventory_valid and valid
        unexpected += count
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


def cleanup(runtime, server):
    try:
        runtime.close()
    finally:
        server.close()


def run(options):
    with Cancellation() as cancellation:
        return _run(options, cancellation)


def validated_inputs(options):
    validate_provider(options.provider)
    auth = canonical_auth_file(options.auth_file)
    image = options.cli_image
    validate_image(image)
    validate_image(options.tool_image)
    return auth, image


def validate_image(image):
    if not isinstance(image, str) or re.fullmatch(r'[a-z0-9][a-z0-9._/:-]{0,254}(?:@sha256:[a-f0-9]{64})?', image) is None:
        raise ValueError('invalid trusted image reference')


def canonical_auth_file(source):
    auth = Path(source)
    validate_mount_source(auth)
    if not auth.is_absolute() or auth.is_symlink() or not auth.is_file():
        raise ValueError('operator must supply an absolute native auth file')
    canonical = auth.resolve(strict=True)
    validate_mount_source(canonical)
    return canonical


def validate_mount_source(source):
    if any(character in str(source) for character in (',', '"', '\r', '\n')):
        raise ValueError('invalid mount source syntax')


def validate_provider(provider):
    if provider not in ('claude', 'codex'):
        raise ValueError('unsupported provider')


def write_policy(provider, policy, endpoint):
    validate_provider(provider)
    if provider == "claude":
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
    return target


def _container_command(provider, auth, policy, name, target, image):
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
    if provider == "codex":
        command += ['--mount', f'type=bind,src={policy / "config.toml"},dst=/home/node/.codex/config.toml,readonly']
    command += ['--entrypoint=/bin/sleep', '--', image, 'infinity']
    return command


def _turn_command(provider, name, session, turn):
    validate_provider(provider)
    if turn not in ('new', 'resume'):
        raise ValueError('unsupported turn')
    cli = ['docker', 'exec', '--interactive']
    if provider == "claude":
        for key, value in ENV_OVERRIDES.items():
            cli += ['--env', key + '=' + value]
        cli += ['--', name, 'claude', '--restricted', '--tools', '', '--strict-mcp-config',
                '--mcp-config', '/qualification/mcp.json', '--settings', json.dumps(SETTINGS),
                '--setting-sources', '', '--disable-slash-commands', '--permission-mode', 'dontAsk',
                '--allowedTools', 'mcp__group__group_exec', '--system-prompt-snapshot', 'off',
                '--system-prompt', 'Use only the supplied isolated group MCP tool for the synthetic canary.',
                '--model', 'claude-haiku-4-5', '--max-turns', '3',
                '--session-id' if turn == "new" else '--resume', session,
                '--output-format', 'stream-json', '--verbose', '-p']
    else:
        cli += ['--env', 'CODEX_HOME=/home/node/.codex', '--', name, 'codex', 'exec']
        if turn == "resume":
            cli += ['resume', session]
        cli += ['--strict-config', '--skip-git-repo-check', '--json', '-']
    return cli


def codex_session(events, session):
    for event in events:
        if event.get('type') != 'thread.started':
            continue
        try:
            session = str(uuid.UUID(event.get('thread_id')))
        except (ValueError, TypeError, AttributeError):
            session = None
    return session


def collect_turn(provider, turn, result, work, proof, marker, audit, session):
    metadata, events = sanitized_events(result.stdout, provider)
    if provider == 'codex':
        session = codex_session(events, session)
    external_proof = result.returncode == 0 and proof_matches(work, proof, marker)
    inventory_allowed = provider != 'claude' or metadata['inventory_allowed']
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
    return entry, session


def execute_turn(provider, turn, runtime, session, work, marker, audit):
    proof = f"{provider}-{turn}.txt"
    expected_command = f"printf {marker} > {proof}; cat {proof}"
    audit.configure(expected_command, proof)
    prompt = (f"This is an isolation canary, not a user chat. Use only the group MCP tool group_exec once "
              "with exactly one JSON field cmd containing the shell command "
              f"to run exactly: {expected_command}. "
              "Do not inspect accounts, environment, other files, network or any other tools. "
              f"Then reply exactly {marker}. No planning or extra work.")
    operation = runtime.new_turn if turn == 'new' else runtime.resume_turn
    result = operation(session, prompt)
    if result.stopped:
        return ({'turn': turn, 'passed': False, 'failure': result.stopped,
                 'tool_execution': audit.snapshot()}, session)
    return collect_turn(provider, turn, result, work, proof, marker, audit, session)


def initial_session(provider):
    return str(uuid.uuid4()) if provider == 'claude' else None


def _auth_status_command(provider, name):
    validate_provider(provider)
    if provider == 'claude':
        return ['docker', 'exec', '--', name, 'claude', 'auth', 'status', '--json']
    return ['docker', 'exec', '--env', 'CODEX_HOME=/home/node/.codex', '--', name, 'codex', 'login', 'status']


def auth_evidence(provider, result):
    method, logged_in = 'unknown', False
    if provider == 'claude':
        try:
            status = json.loads(result.stdout)
            allowed = {'claude.ai', 'oauth_token', 'api_key', 'api_key_helper', 'third_party', 'none'}
            if isinstance(status, dict) and status.get('authMethod') in allowed:
                method = status['authMethod']
                logged_in = status.get('loggedIn') is True
        except (ValueError, TypeError):
            pass
    else:
        text = result.stdout.strip().lower()
        if 'api key' in text:
            method = 'api_key'
        elif text == 'logged in using chatgpt':
            method, logged_in = 'chatgpt', True
        elif text == 'not logged in':
            method = 'none'
    verified = (result.returncode == 0 and result.stopped is None and logged_in
                and method in {'claude.ai', 'oauth_token', 'chatgpt'})
    return {'auth_method': method, 'subscription_auth_verified': verified}


def qualify_runtime(runtime):
    version_result = runtime.version()
    version_match = version_result.stdout.strip() == EXPECTED_VERSIONS[runtime.provider]
    report = {'version_match': version_match, 'auth_method': 'unknown', 'subscription_auth_verified': False}
    if version_match and version_result.returncode == 0 and version_result.stopped is None:
        status = runtime.auth_status()
        report.update(auth_evidence(runtime.provider, status))
    return report


def _run(options, cancellation):
    auth, image = validated_inputs(options)
    marker = "gopod_subscription_canary"
    report = {"provider": options.provider, "scope": "synthetic new/resume; existing native subscription auth",
              "auth_mounted_readonly": True, "auth_copied": False, "api_key_fallback": False,
              "auth_method": "unknown", "subscription_auth_verified": False,
              "roundtrip_passed": False, "isolation_qualified": False, "turns": []}
    with tempfile.TemporaryDirectory(prefix="gopod-live-canary-") as root:
        root = Path(root)
        work, policy = root / "group", root / "policy"
        work.mkdir(mode=0o777)
        work.chmod(0o777)
        policy.mkdir()
        runtime = CanaryRuntime(options.provider, image, auth, policy, cancellation)
        server, url = start_bridge(str(work), options.tool_image, bind="0.0.0.0")
        audit = ToolAudit(server.executor, work, marker, cancellation)
        endpoint = url.replace("127.0.0.1", "host.docker.internal", 1)
        try:
            write_policy(options.provider, policy, endpoint)
            # A create request cannot execute a command. Wait for its bounded
            # acknowledgement even on cancellation, then never start if stopped.
            created = runtime.create()
            if created.returncode != 0 or created.stopped or cancellation.signum is not None:
                report['failure'] = 'cancelled' if cancellation.signum is not None else 'create_unavailable'
                return report
            started = runtime.start()
            if started.returncode != 0 or started.stopped:
                report['failure'] = started.stopped or 'start_unavailable'
                return report
            report.update(qualify_runtime(runtime))
            if not report['subscription_auth_verified']:
                report.update(passed=False, failure='subscription_auth_unverified')
                return report
            if getattr(options, 'auth_only', False):
                report.update(passed=True, auth_only=True, scope='native subscription auth metadata only; no inference')
                return report
            session = initial_session(options.provider)
            for turn in ("new", "resume"):
                entry, session = execute_turn(options.provider, turn, runtime, session, work, marker, audit)
                report['turns'].append(entry)
                if not entry['passed'] or not session:
                    break
            report['roundtrip_passed'] = (len(report['turns']) == 2
                                          and all(x.get('roundtrip_passed') for x in report['turns']))
            report['passed'] = len(report['turns']) == 2 and all(x['passed'] for x in report['turns'])
            return report
        finally:
            # A timed-out docker exec may leave a CLI process. Remove the entire
            # disposable CLI container; never target an existing gopod runtime.
            cleanup(runtime, server)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--provider', required=True, choices=['claude', 'codex'])
    parser.add_argument('--auth-file', required=True)
    parser.add_argument('--cli-image', required=True)
    parser.add_argument('--tool-image', required=True)
    parser.add_argument('--auth-only', action='store_true', help='verify native auth metadata without model calls')
    options = parser.parse_args()
    try:
        report = run(options)
    except Exception:
        # Do not expose native CLI streams, endpoint capabilities or credentials.
        report = {'provider': options.provider, 'passed': False, 'roundtrip_passed': False,
                  'auth_method': 'unknown', 'subscription_auth_verified': False,
                  'isolation_qualified': False, 'failure': 'qualification_unavailable_no_raw_output'}
    print(json.dumps(report, indent=2))
    raise SystemExit(0 if report.get('passed') else 1)
