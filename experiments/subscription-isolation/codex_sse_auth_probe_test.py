"""Image argument admission tests; no Docker daemon or account is used."""
import types
import unittest
from unittest.mock import patch
from codex_sse_auth_probe import preloaded_image_id


class ImageAdmissionTest(unittest.TestCase):
    def setUp(self):
        patcher = patch('codex_sse_auth_probe.subprocess.run')
        self.run = patcher.start()
        self.addCleanup(patcher.stop)
        self.run.return_value = types.SimpleNamespace(stdout='sha256:' + 'a' * 64 + '\n')
        self.resolve = preloaded_image_id

    def test_option_and_control_inputs_rejected_before_docker(self):
        for reference in ['--privileged', '-v/etc:/etc', 'node\n--privileged',
                          'node;touch /tmp/injected', 'node,readonly', '', 'x' * 256]:
            with self.subTest(reference=reference), self.assertRaises(SystemExit):
                self.resolve(reference)
        self.run.assert_not_called()

    def test_preloaded_reference_becomes_immutable_id(self):
        self.assertEqual(self.resolve('registry.example/group:qualified'),
                         'sha256:' + 'a' * 64)
        self.assertEqual(self.run.call_args.args[0], [
            'docker', 'image', 'inspect', '--format', '{{.Id}}', '--',
            'registry.example/group:qualified'])

    def test_nonimmutable_or_option_like_inspect_output_rejected(self):
        for output in ['--privileged', 'node:latest', 'sha256:' + 'a' * 63,
                       'sha256:' + 'a' * 64 + '\n--privileged']:
            self.run.return_value.stdout = output
            with self.subTest(output=output), self.assertRaises(SystemExit):
                self.resolve('node:22-slim')


if __name__ == '__main__':
    unittest.main()
