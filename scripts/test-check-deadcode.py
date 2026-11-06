#!/usr/bin/env python3
"""Integration checks for staged-only deadcode enforcement; no user repo writes."""
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

CHECKER = Path(__file__).with_name("check-deadcode.py").resolve()


class StagedDeadcodeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="gopod-deadcode-test-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.git("init", "-q")
        (self.root / "scripts").mkdir()
        shutil.copyfile(CHECKER, self.root / "scripts/check-deadcode.py")
        self.baseline = self.root / "scripts/deadcode-baseline.json"
        self.baseline.write_text("[]\n")
        (self.root / "go.mod").write_text("module example.org/deadcodefixture\n\ngo 1.27.0\n")
        self.source = self.root / "main.go"

    def git(self, *args):
        subprocess.run(["git", *args], cwd=self.root, check=True, capture_output=True)

    def check(self):
        return subprocess.run([sys.executable, str(CHECKER), "--staged"],
                              cwd=self.root, text=True, capture_output=True)

    def test_unstaged_caller_cannot_hide_staged_dead_code(self):
        self.source.write_text("package main\nfunc main() {}\nfunc abandoned() {}\n")
        self.git("add", ".")
        self.source.write_text("package main\nfunc main() { abandoned() }\nfunc abandoned() {}\n")
        result = self.check()
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("NEW unreachable: example.org/deadcodefixture.abandoned", result.stderr)

    def test_unstaged_deletion_does_not_change_staged_reachability(self):
        self.source.write_text("package main\nfunc main() { used() }\nfunc used() {}\n")
        self.git("add", ".")
        self.source.write_text("package main\nfunc main() {}\nfunc used() {}\n")
        result = self.check()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_stale_baseline_is_rejected(self):
        self.source.write_text("package main\nfunc main() {}\n")
        self.baseline.write_text(json.dumps(["example.org/deadcodefixture.removed"]))
        self.git("add", ".")
        result = self.check()
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("STALE baseline", result.stderr)

    def test_go_to_non_go_rename_cannot_skip_analysis(self):
        self.source.write_text("package main\nfunc main() {}\nfunc helper() {}\n")
        (self.root / "calls.go").write_text("package main\nfunc init() { helper() }\n")
        self.git("add", ".")
        self.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                 "-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture")
        self.git("mv", "calls.go", "calls.txt")
        result = self.check()
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("NEW unreachable: example.org/deadcodefixture.helper", result.stderr)

    def test_docs_only_commit_skips_analysis(self):
        (self.root / "README.md").write_text("Documentation only.\n")
        self.git("add", "README.md")
        result = self.check()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("skipped", result.stdout)


if __name__ == "__main__":
    unittest.main()
