"""Offline regressions for deployment failure paths; never calls AWS or Docker."""

import gzip
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class OperationsTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.scratch = Path(self.directory.name)
        self.env = dict(os.environ, PATH=f"{self.scratch}:{os.environ['PATH']}")
        for name in ("terraform", "aws", "docker", "mkdir"):
            self.stub(name, "exit 97")

    def stub(self, name, body):
        path = self.scratch / name
        path.write_text(f"#!/bin/sh\n{body}\n")
        path.chmod(0o755)

    def test_missing_data_volume_stops_boot(self):
        source = (ROOT / "deploy/demo/user_data.sh.tftpl").read_text()
        block = source.split('if [ -n "$DATA_DEV" ]; then', 1)[1].split("# Caddy", 1)[0]
        result = subprocess.run(
            ["bash", "-eu", "-c", 'DATA_DEV=""; if [ -n "$DATA_DEV" ]; then' + block],
            env=self.env, capture_output=True, text=True, timeout=5,
        )
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("refusing to start", result.stderr)

    def test_unknown_service_rejected_before_external_commands(self):
        for script in ("ship.sh", "roll.sh"):
            with self.subTest(script=script):
                result = subprocess.run(
                    ["bash", str(ROOT / "scripts" / script), "api; false"],
                    env=self.env, capture_output=True, text=True, timeout=5,
                )
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertIn("unknown service", result.stderr)

    def restore_command(self):
        source = (ROOT / "scripts/migrate-to-data-volume.sh").read_text()
        command = source.split('ssm "set -e\n', 1)[1].split('echo restored"', 1)[0]
        return "set -e\n" + command.replace("/tmp/restore.sql.gz", str(self.scratch / "dump.gz")) + "echo restored"

    def test_restore_fails_on_sql_error(self):
        self.stub("aws", "exit 0")
        # psql normally exits zero after a SQL error unless ON_ERROR_STOP is set.
        self.stub("docker", 'case "$*" in *ON_ERROR_STOP=1*) cat >/dev/null; exit 1;; *) cat >/dev/null; exit 0;; esac')
        with gzip.open(self.scratch / "dump.gz", "wb") as output:
            output.write(b"invalid SQL;\n")
        result = subprocess.run(
            ["bash", "-c", self.restore_command()], env=self.env,
            capture_output=True, text=True, timeout=5,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("restored", result.stdout)

    def test_restore_rejects_corrupt_dump_before_sql(self):
        self.stub("aws", "exit 0")
        marker = self.scratch / "sql-called"
        self.stub("docker", f'touch "{marker}"; cat >/dev/null')
        (self.scratch / "dump.gz").write_bytes(b"not gzip")
        result = subprocess.run(
            ["bash", "-c", self.restore_command()], env=self.env,
            capture_output=True, text=True, timeout=5,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(marker.exists())


if __name__ == "__main__":
    unittest.main()
