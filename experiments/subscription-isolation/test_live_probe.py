"""Offline lifecycle, evidence and hostile file tests; no Docker/model calls."""
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import threading
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


class LifecycleTests(unittest.TestCase):
    def test_capture_deadline_output_and_sigterm_are_bounded(self):
        cancellation = live_probe.Cancellation()
        value = live_probe.capture([sys.executable, '-c', 'import time;time.sleep(5)'], cancellation, timeout=0.1)
        self.assertEqual(value.stopped, 'deadline')
        value = live_probe.capture([sys.executable, '-c', 'print("x"*300000)'], cancellation, timeout=3)
        self.assertEqual(value.stopped, 'output_limit')
        self.assertEqual(len(value.stdout), live_probe.CAPTURE_LIMIT)
        with cancellation:
            timer = threading.Timer(0.1, lambda: os.kill(os.getpid(), signal.SIGTERM))
            timer.start()
            try:
                value = live_probe.capture([sys.executable, '-c', 'import time;time.sleep(5)'], cancellation, timeout=3)
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


if __name__ == '__main__':
    unittest.main()
