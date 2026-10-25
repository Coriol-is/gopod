"""Offline lifecycle, evidence and hostile file tests; no Docker/model calls."""
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import threading
import uuid
from types import SimpleNamespace
import unittest
from unittest import mock

import live_probe


class ProofTests(unittest.TestCase):
    def test_only_exact_regular_nonlinked_bounded_proof(self):
        with tempfile.TemporaryDirectory() as root:
            work = Path(root)
            (work / 'proof').write_text('canary')
            self.assertTrue(live_probe.proof_matches(work, 'proof', 'canary'))
            self.assertFalse(live_probe.proof_matches(work, '../proof', 'canary'))
            self.assertFalse(live_probe.proof_matches(work, 'proof', 'can'))
            (work / 'escape').symlink_to(work / 'proof')
            self.assertFalse(live_probe.proof_matches(work, 'escape', 'canary'))
            os.mkfifo(work / 'fifo')
            self.assertFalse(live_probe.proof_matches(work, 'fifo', 'canary'))
            os.link(work / 'proof', work / 'hardlink')
            self.assertFalse(live_probe.proof_matches(work, 'proof', 'canary'))
            self.assertFalse(live_probe.proof_matches(work, 'hardlink', 'canary'))
            self.assertFalse(live_probe.proof_matches(work / 'escape', 'proof', 'canary'))


class EventTests(unittest.TestCase):
    def test_auth_evidence_accepts_only_native_subscription_metadata(self):
        for method in ('claude.ai', 'oauth_token'):
            value = live_probe.Capture(0, json.dumps({'loggedIn': True, 'authMethod': method,
                                                     'email': 'private-account-value'}))
            report = live_probe.auth_evidence('claude', value)
            self.assertTrue(report['subscription_auth_verified'])
            self.assertNotIn('private-account-value', json.dumps(report))
        for method in ('api_key', 'api_key_helper', 'third_party', 'none', 'private-value'):
            value = live_probe.Capture(0, json.dumps({'loggedIn': True, 'authMethod': method}))
            self.assertFalse(live_probe.auth_evidence('claude', value)['subscription_auth_verified'])
        self.assertTrue(live_probe.auth_evidence('codex', live_probe.Capture(0, 'Logged in using ChatGPT\n'))['subscription_auth_verified'])
        for value in (live_probe.Capture(0, 'Logged in using API key - private-value'),
                      live_probe.Capture(1, 'Logged in using ChatGPT'),
                      live_probe.Capture(0, 'Logged in using ChatGPT', stopped='cancelled')):
            report = live_probe.auth_evidence('codex', value)
            self.assertFalse(report['subscription_auth_verified'])
            self.assertNotIn('private-value', json.dumps(report))

    def test_claude_inventory_is_required_and_unexpected_strings_never_reported(self):
        output = json.dumps({'type': 'system', 'subtype': 'init',
                             'tools': ['mcp__group__group_exec', 'EndConversation']})
        report, _ = live_probe.sanitized_events(output, 'claude')
        self.assertTrue(report['inventory_allowed'])
        output += '\n' + json.dumps({'type': 'system', 'subtype': 'init', 'tools': ['private-secret-value']})
        report, _ = live_probe.sanitized_events(output, 'claude')
        self.assertFalse(report['inventory_allowed'])
        self.assertEqual(report['unexpected_tool_count'], 1)
        self.assertNotIn('private-secret-value', json.dumps(report))
        self.assertFalse(live_probe.sanitized_events('{}', 'claude')[0]['inventory_allowed'])

    def test_codex_diagnostics_emit_only_known_labels(self):
        output = '\n'.join(json.dumps(x) for x in [
            {'type': 'item.completed', 'item': {'type': 'mcp_tool_call', 'tool': 'group_exec', 'arguments': 'private-secret-value'}},
            {'type': 'private-secret-value', 'item': {'type': 'private-secret-value', 'tool': 'private-secret-value'}},
            {'type': 'error', 'message': 'MCP unsupported requires approval private-secret-value'},
        ])
        report, _ = live_probe.sanitized_events(output, 'codex')
        self.assertEqual(report['event_types']['other'], 1)
        self.assertTrue(report['diagnostic_flags']['mcp'])
        self.assertTrue(report['diagnostic_flags']['approval'])
        self.assertTrue(report['diagnostic_flags']['unsupported'])
        self.assertNotIn('private-secret-value', json.dumps(report))


class ToolAuditTests(unittest.TestCase):
    def test_execution_status_is_sanitized_without_arguments_or_output(self):
        with tempfile.TemporaryDirectory() as root:
            workspace = Path(root)
            marker = 'synthetic_marker'
            executor = SimpleNamespace(run=mock.Mock(return_value={'exit_code': 7, 'stopped': 'private-stop-value',
                                                                   'output': marker + ' private-output-value'}))
            audit = live_probe.ToolAudit(executor, workspace, marker, live_probe.Cancellation())
            audit.configure('expected-command', 'proof')
            (workspace / 'proof').write_text(marker)
            executor.run({'command': 'private-argument-value'})
            report = audit.snapshot()
            self.assertEqual(report['calls'], [{'command_matches_expected': False, 'exit_code': 7,
                                               'stopped': 'other', 'marker_returned': True,
                                               'proof_matches_after': True, 'arguments_type': 'object',
                                               'has_command': True, 'extra_keys_count': 0, 'known_argument_fields': [], 'command_type': 'string',
                                               'command_length': len('private-argument-value')}])
            self.assertNotIn('private-', json.dumps(report))

    def test_call_budget_revokes_executor_without_more_execution(self):
        with tempfile.TemporaryDirectory() as root:
            executor = SimpleNamespace(run=mock.Mock(return_value={'exit_code': 0, 'stopped': None, 'output': ''}))
            cancellation = live_probe.Cancellation()
            audit = live_probe.ToolAudit(executor, Path(root), 'marker', cancellation)
            audit.configure('expected-command', 'proof')
            for _ in range(audit.MAX_CALLS):
                executor.run({'command': 'expected-command'})
            with self.assertRaises(ValueError):
                executor.run({'command': 'must-not-execute'})
            report = audit.snapshot()
            self.assertEqual(audit.original_run.call_count, audit.MAX_CALLS)
            self.assertEqual(len(report['calls']), audit.MAX_CALLS)
            self.assertTrue(report['call_limit_exceeded'])
            self.assertEqual(executor.cancel_signal, signal.SIGTERM)
            self.assertEqual(cancellation.signum, signal.SIGTERM)

    def test_execution_exception_does_not_retain_error_text(self):
        with tempfile.TemporaryDirectory() as root:
            executor = SimpleNamespace(run=mock.Mock(side_effect=ValueError('private-error-value')))
            audit = live_probe.ToolAudit(executor, Path(root), 'marker', live_probe.Cancellation())
            audit.configure('expected-command', 'proof')
            with self.assertRaises(ValueError):
                executor.run({'command': 'expected-command'})
            self.assertEqual(audit.snapshot()['calls'][0]['stopped'], 'unknown')
            self.assertNotIn('private-error-value', json.dumps(audit.snapshot()))

    def test_argument_shape_and_exception_classification_are_fixed_labels(self):
        self.assertEqual(live_probe.argument_shape({'command': ['private-value'], 'private-key': 'private-value'}),
                         {'arguments_type': 'object', 'has_command': True, 'extra_keys_count': 1,
                          'known_argument_fields': [], 'command_type': 'array', 'command_length': None})
        self.assertEqual(live_probe.argument_shape({'cmd': 'private-value', 'private-key': 'private-value'})['known_argument_fields'], ['cmd'])
        for arguments, expected_type in [('private-value', 'string'), ([], 'array'), (None, 'null'), (7, 'other')]:
            self.assertEqual(live_probe.argument_shape(arguments)['arguments_type'], expected_type)
        for exception, expected in [(ValueError('only command is accepted'), 'invalid_arguments'),
                                    (OSError('private-error-value'), 'spawn_unavailable'),
                                    (subprocess.TimeoutExpired('private-command', 5), 'cleanup_timeout')]:
            with tempfile.TemporaryDirectory() as root:
                executor = SimpleNamespace(run=mock.Mock(side_effect=exception))
                audit = live_probe.ToolAudit(executor, Path(root), 'marker', live_probe.Cancellation())
                audit.configure('expected', 'proof')
                with self.assertRaises(type(exception)):
                    executor.run({'private-key': 'private-value'})
                report = audit.snapshot()
                self.assertEqual(report['calls'][0]['stopped'], expected)
                self.assertNotIn('private-', json.dumps(report))


class LifecycleTests(unittest.TestCase):
    @unittest.skipUnless(os.environ.get('GOPOD_ISOLATION_DOCKER_TEST') == '1', 'opt-in Docker stdin transport')
    def test_fixed_docker_separators_and_stdin_transport(self):
        name = 'gopod-stdin-canary-' + uuid.uuid4().hex
        cancellation = live_probe.Cancellation()
        image = os.environ.get('GOPOD_ISOLATION_TEST_IMAGE', 'node:22-slim')
        prompt = '--model=bad; $(printf injection)\ncanary-only'
        try:
            created = live_probe.capture(['docker', 'create', '--rm', '--pull=never', '--name', name,
                                          '--network=none', '--read-only', '--cap-drop=ALL',
                                          '--security-opt=no-new-privileges', '--memory=128m', '--pids-limit=64',
                                          '--user=1000:1000', '--entrypoint=/bin/sleep', '--', image, 'infinity'],
                                         cancellation, timeout=10, cancelable=False)
            self.assertEqual(created.returncode, 0)
            self.assertEqual(live_probe.capture(['docker', 'start', '--', name], cancellation, timeout=5).returncode, 0)
            result = live_probe.capture(['docker', 'exec', '--interactive', '--', name, 'node', '-e',
                                        'process.stdin.pipe(process.stdout)'], cancellation, timeout=5, input_text=prompt)
            self.assertEqual(result.returncode, 0)
            self.assertEqual(result.stdout, prompt)
            self.assertIsNone(result.stopped)
        finally:
            subprocess.run(['docker', 'rm', '-f', '--', name], stdout=subprocess.DEVNULL,
                           stderr=subprocess.DEVNULL, timeout=5)

    def fixture_capture(self, script, cancellation, timeout, input_text=None):
        original = subprocess.Popen
        def fixture(argv, **kwargs):
            self.assertEqual(argv, ['docker', 'exec', '--interactive', '--', 'synthetic-only', 'offline-fixture'])
            self.assertIs(kwargs['shell'], False)
            return original([sys.executable, '-c', script], **kwargs)
        with mock.patch.object(live_probe.subprocess, 'Popen', side_effect=fixture):
            return live_probe.capture(['docker', 'exec', '--interactive', '--', 'synthetic-only', 'offline-fixture'],
                                      cancellation, timeout, input_text=input_text)

    def test_capture_rejects_arbitrary_binaries_and_docker_operations(self):
        with mock.patch.object(live_probe.subprocess, 'Popen') as spawn:
            for argv in ([sys.executable, '-c', 'private-command'], ['docker', 'run', 'image'],
                         ['docker', 'exec'], ['docker', 'start', 'invalid\0name']):
                with self.assertRaises(ValueError):
                    live_probe.capture(argv, live_probe.Cancellation(), timeout=1)
            spawn.assert_not_called()

    def test_prompt_is_bounded_stdin_data_and_never_an_argument(self):
        prompt = '--model=bad; $(printf injection)\nprivate-prompt-value'
        for provider in ('claude', 'codex'):
            argv = live_probe.turn_command(provider, 'synthetic-container', '00000000-0000-0000-0000-000000000001', 'new')
            self.assertNotIn(prompt, argv)
            self.assertIn('--interactive', argv)
            self.assertEqual(argv[argv.index('--') + 1], 'synthetic-container')
            self.assertEqual(argv[-1], '-p' if provider == 'claude' else '-')
        with mock.patch.object(live_probe.subprocess, 'Popen') as spawn:
            for value in ('x' * (live_probe.INPUT_LIMIT + 1), '\u044f' * live_probe.INPUT_LIMIT, b'invalid-type'):
                with self.assertRaises(ValueError):
                    live_probe.capture(['docker', 'exec', '--', 'synthetic'], live_probe.Cancellation(), timeout=1, input_text=value)
            spawn.assert_not_called()
        result = self.fixture_capture('import sys;sys.stdout.write(sys.stdin.read())', live_probe.Cancellation(), 3, prompt)
        self.assertEqual(result.stdout, prompt)
        self.assertIsNone(result.stopped)
        result = self.fixture_capture('import time;time.sleep(5)', live_probe.Cancellation(), 0.1, 'x' * live_probe.INPUT_LIMIT)
        self.assertEqual(result.stopped, 'deadline')
        with self.assertRaises(ValueError):
            live_probe.turn_command('arbitrary-program', 'synthetic', None, 'new')

    def test_capture_deadline_output_and_sigterm_are_bounded(self):
        cancellation = live_probe.Cancellation()
        value = self.fixture_capture('import time;time.sleep(5)', cancellation, timeout=0.1)
        self.assertEqual(value.stopped, 'deadline')
        value = self.fixture_capture('print("x"*300000)', cancellation, timeout=3)
        self.assertEqual(value.stopped, 'output_limit')
        self.assertEqual(len(value.stdout), live_probe.CAPTURE_LIMIT)
        with cancellation:
            timer = threading.Timer(0.1, lambda: os.kill(os.getpid(), signal.SIGTERM))
            timer.start()
            try:
                value = self.fixture_capture('import time;time.sleep(5)', cancellation, timeout=3)
            finally:
                timer.join()
        self.assertEqual(value.stopped, 'cancelled')

    def test_bridge_closes_even_when_cli_cleanup_times_out(self):
        server = mock.Mock()
        with mock.patch.object(live_probe.subprocess, 'run', side_effect=subprocess.TimeoutExpired('docker', 15)):
            with self.assertRaises(subprocess.TimeoutExpired):
                live_probe.cleanup('synthetic-test-only', server)
        server.close.assert_called_once_with()

    def test_cancelled_create_is_acknowledged_but_never_started(self):
        with tempfile.TemporaryDirectory() as root:
            auth = Path(root) / 'synthetic-auth'
            auth.write_text('synthetic-never-read')
            options = SimpleNamespace(auth_file=str(auth.resolve()), cli_image='unused:offline',
                                      tool_image='unused:offline', provider='claude')
            cancellation = live_probe.Cancellation()
            server = mock.Mock()
            def create(argv, _cancellation, timeout, cancelable=True):
                self.assertEqual(argv[:2], ['docker', 'create'])
                self.assertFalse(cancelable)
                cancellation.cancel(signal.SIGTERM)
                return live_probe.Capture(0, 'synthetic-container-id')
            with mock.patch.object(live_probe, 'start_bridge', return_value=(server, 'http://127.0.0.1:12345/mcp/synthetic')):
                with mock.patch.object(live_probe, 'capture', side_effect=create) as commands:
                    with mock.patch.object(live_probe.subprocess, 'run') as removal:
                        report = live_probe._run(options, cancellation)
            self.assertEqual(commands.call_count, 1)
            self.assertEqual(removal.call_args.args[0][:3], ['docker', 'rm', '-f'])
            server.close.assert_called_once_with()
            self.assertEqual(report['failure'], 'cancelled')
            self.assertFalse(report['roundtrip_passed'])
            self.assertFalse(report['isolation_qualified'])

    def test_new_resume_builders_preserve_both_provider_flows_offline(self):
        for provider in ('claude', 'codex'):
            with self.subTest(provider=provider), tempfile.TemporaryDirectory() as root:
                auth = Path(root) / 'synthetic-auth'
                auth.write_text('synthetic-never-read')
                options = SimpleNamespace(auth_file=str(auth.resolve()), cli_image='unused:offline',
                                          tool_image='unused:offline', provider=provider)
                holder, commands = {}, []
                marker = 'gopod_subscription_canary'
                def execute(arguments):
                    turn = 'new' if len(holder['turns']) == 1 else 'resume'
                    proof = f'{provider}-{turn}.txt'
                    self.assertEqual(arguments, {'command': f'printf {marker} > {proof}; cat {proof}'})
                    (holder['work'] / proof).write_text(marker)
                    return {'exit_code': 0, 'stopped': None, 'output': marker}
                server = SimpleNamespace(executor=SimpleNamespace(run=execute), close=mock.Mock())
                def bridge(workspace, *_args, **_kwargs):
                    holder.update(work=Path(workspace), turns=[])
                    return server, 'http://127.0.0.1:12345/mcp/synthetic'
                def fake_capture(argv, _cancellation, timeout, cancelable=True, input_text=None):
                    commands.append(live_probe.docker_command(argv))
                    if argv[1] != 'exec':
                        return live_probe.Capture(0, 'synthetic-container-id')
                    if argv[-1] == '--version':
                        return live_probe.Capture(0, live_probe.EXPECTED_VERSIONS[provider])
                    if 'auth' in argv and 'status' in argv:
                        return live_probe.Capture(0, json.dumps({'loggedIn': True, 'authMethod': 'claude.ai'}))
                    if 'login' in argv and 'status' in argv:
                        return live_probe.Capture(0, 'Logged in using ChatGPT')
                    self.assertIn(marker, input_text)
                    self.assertNotIn(input_text, argv)
                    holder['turns'].append(argv)
                    turn = 'new' if len(holder['turns']) == 1 else 'resume'
                    proof = f'{provider}-{turn}.txt'
                    server.executor.run({'command': f'printf {marker} > {proof}; cat {proof}'})
                    event = ({'type': 'system', 'subtype': 'init', 'tools': ['mcp__group__group_exec']}
                             if provider == 'claude' else
                             {'type': 'thread.started', 'thread_id': '00000000-0000-0000-0000-000000000001'})
                    return live_probe.Capture(0, json.dumps(event))
                with mock.patch.object(live_probe, 'start_bridge', side_effect=bridge):
                    with mock.patch.object(live_probe, 'capture', side_effect=fake_capture):
                        with mock.patch.object(live_probe.subprocess, 'run'):
                            report = live_probe._run(options, live_probe.Cancellation())
                self.assertTrue(report['passed'])
                self.assertTrue(report['roundtrip_passed'])
                self.assertFalse(report['isolation_qualified'])
                self.assertTrue(report['subscription_auth_verified'])
                self.assertEqual([command[1] for command in commands], ['create', 'start', 'exec', 'exec', 'exec', 'exec'])
                resume_flag = '--resume' if provider == 'claude' else 'resume'
                self.assertIn(resume_flag, holder['turns'][1])
                server.close.assert_called_once_with()

    def test_auth_only_never_executes_inference_turn(self):
        with tempfile.TemporaryDirectory() as root:
            auth = Path(root) / 'synthetic-auth'
            auth.write_text('synthetic-never-read')
            options = SimpleNamespace(auth_file=str(auth.resolve()), cli_image='unused:offline',
                                      tool_image='unused:offline', provider='claude', auth_only=True)
            server = mock.Mock()
            values = [live_probe.Capture(0, 'created'), live_probe.Capture(0, 'started'),
                      live_probe.Capture(0, live_probe.EXPECTED_VERSIONS['claude']),
                      live_probe.Capture(0, '{"loggedIn":true,"authMethod":"claude.ai","email":"private-account-value"}')]
            with mock.patch.object(live_probe, 'start_bridge', return_value=(server, 'http://127.0.0.1:12345/mcp/synthetic')):
                with mock.patch.object(live_probe, 'capture', side_effect=values):
                    with mock.patch.object(live_probe.subprocess, 'run'):
                        with mock.patch.object(live_probe, 'execute_turn') as inference:
                            report = live_probe._run(options, live_probe.Cancellation())
            inference.assert_not_called()
            self.assertTrue(report['passed'])
            self.assertTrue(report['auth_only'])
            self.assertTrue(report['subscription_auth_verified'])
            self.assertFalse(report['roundtrip_passed'])
            self.assertEqual(report['turns'], [])
            self.assertNotIn('private-account-value', json.dumps(report))
            server.close.assert_called_once_with()


if __name__ == '__main__':
    unittest.main()
