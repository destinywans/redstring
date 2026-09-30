import contextlib
import io
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from testlab import main


class DoctorExitTests(unittest.TestCase):
    def test_dependency_or_isolation_failure_is_not_success(self):
        for checks in ([], [{"status": "blocked"}], [{"status": "passed"}, {"status": "failed"}]):
            with self.subTest(checks=checks), patch("env.doctor", return_value=checks), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(main(["doctor"]), 2)

    def test_all_checks_pass(self):
        with patch("env.doctor", return_value=[{"status": "passed"}]), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(main(["doctor"]), 0)
