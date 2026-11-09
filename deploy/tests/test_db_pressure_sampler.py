import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("sampler", Path(__file__).parents[1] / "db_pressure_sampler.py")
sampler = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sampler)


class SamplerTests(unittest.TestCase):
    def test_size_bound_and_no_overwrite(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "capture.jsonl"
            writer = sampler.BoundedWriter(path, 512)
            try:
                self.assertTrue(writer.write({"kind": "first"}))
                self.assertFalse(writer.write({"large": "x" * 1000}))
                self.assertTrue(writer.full.is_set())
                self.assertTrue(writer.write({"kind": "end", "capacity_reached": True}, final=True))
            finally:
                writer.close()
            self.assertLessEqual(path.stat().st_size, 512)
            with self.assertRaises(FileExistsError):
                sampler.BoundedWriter(path, 100)

    def test_pid_reuse_and_cpu_units(self):
        previous = {1: {"start": 10, "user": 20, "system": 30}}
        current = {1: {"start": 10, "user": 40, "system": 130, "rss_pages": 2, "name": "postgres", "state": "R", "namespace_pid": 7}}
        row = sampler.process_deltas(previous, current, 2, 100, 4096)[0]
        self.assertEqual(row["system_cores"], 0.5)
        self.assertEqual(row["user_cores"], 0.1)
        current[1]["start"] = 11
        self.assertEqual(sampler.process_deltas(previous, current, 2, 100, 4096), [])

    @patch.object(sampler.subprocess, "run")
    def test_readonly_query_and_redacted_failure(self, run):
        run.return_value = subprocess.CompletedProcess([], 1, "", "password=secret")
        result = sampler.database_snapshot("sub2api-postgres")
        self.assertNotIn("secret", json.dumps(result))
        self.assertIn("default_transaction_read_only = on", run.call_args.kwargs["input"])
        self.assertEqual(run.call_args.kwargs["timeout"], 3)
        run.side_effect = subprocess.TimeoutExpired(["psql"], 2)
        self.assertEqual(sampler.database_snapshot()["error"], "database_sample_timeout")

    def test_blocked_process_is_kept_without_cpu_and_namespace_is_preserved(self):
        previous = {1: {"start": 10, "user": 20, "system": 30, "io": {"read_bytes": 40}}}
        current = {1: {"start": 10, "user": 20, "system": 30, "rss_pages": 2, "name": "postgres", "state": "D", "namespace_pid": 7, "pid_namespace": "pid:[123]", "io": {"read_bytes": 140}}}
        row = sampler.process_deltas(previous, current, 1, 100, 4096)[0]
        self.assertEqual(row["pid_namespace"], "pid:[123]")
        self.assertEqual(row["io_delta"], {"read_bytes": 100})
        self.assertEqual(row["user_cores"], 0)

    @patch.object(sampler.subprocess, "run")
    def test_database_visibility_and_container_deadline(self, run):
        run.return_value = subprocess.CompletedProcess([], 0, '{"visibility":"own_role_only"}\n', "")
        self.assertEqual(sampler.database_snapshot("postgres")["visibility"], "own_role_only")
        self.assertIn("timeout -s KILL 2 psql", run.call_args.args[0][-1])
        self.assertEqual(run.call_args.kwargs["env"]["PGCONNECT_TIMEOUT"], "2")

    @patch.object(sampler.subprocess, "run")
    def test_only_allowlisted_database_fields_are_saved(self, run):
        run.return_value = subprocess.CompletedProcess([], 0, '{"pid":1,"wait":"WalSync","query":"secret SQL","password":"secret"}\n', "")
        result = sampler.database_snapshot()
        self.assertEqual(result, {"activity": [{"pid": 1, "wait": "WalSync"}], "visibility": "unknown"})


if __name__ == "__main__":
    unittest.main()
