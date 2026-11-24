"""Exercise the actual workflow's input validator without executing a release."""
import os
from pathlib import Path
import subprocess
import sys
import textwrap
import unittest

WORKFLOW = Path(__file__).resolve().parents[1] / '.github/workflows/recover-release.yml'
SOURCE = WORKFLOW.read_text().split("python3 - <<'PY'\n", 1)[1].split('\n          PY', 1)[0]
VALIDATOR = textwrap.dedent(SOURCE)


class RecoveryCoordinatesTests(unittest.TestCase):
    def validate(self, tag, commit):
        env = {**os.environ, 'RELEASE_TAG': tag, 'EXPECTED_SHA': commit}
        return subprocess.run([sys.executable, '-c', VALIDATOR], env=env,
                              text=True, capture_output=True)

    def test_stable_root_and_full_commit_are_accepted(self):
        self.assertEqual(self.validate('v1.3.5', 'a' * 40).returncode, 0)

    def test_noncanonical_tags_cannot_select_checkout(self):
        for tag in ['main', 'primitives/v1.3.5', 'v01.3.5', 'v1.3.05',
                    'v1.3.5-rc.1', 'v1.3.5\n', 'v1.3.5;false', '$(false)']:
            with self.subTest(tag=tag):
                self.assertNotEqual(self.validate(tag, 'a' * 40).returncode, 0)

    def test_only_exact_lowercase_full_sha_is_accepted(self):
        for commit in ['a' * 39, 'a' * 41, 'A' * 40, 'g' * 40, 'main',
                       'a' * 40 + '\n', '$(false)']:
            with self.subTest(commit=commit):
                self.assertNotEqual(self.validate('v1.3.5', commit).returncode, 0)


if __name__ == '__main__':
    unittest.main()
