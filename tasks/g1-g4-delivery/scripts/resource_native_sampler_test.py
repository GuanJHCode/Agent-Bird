import asyncio
import json
from pathlib import Path
import tempfile
import unittest

import native_resource_sampler as sampler


def identity(pid: int, birth: str = "birth") -> dict[str, object]:
    return {
        "pid": pid,
        "uid": 501,
        "birth": birth,
        "executable": f"/private/tmp/tool-{pid}",
        "executable_sha256": f"{pid:064x}",
    }


class NativeResourceSamplerTest(unittest.TestCase):
    def test_group_sample_uses_one_ps_and_adds_report_rusage(self) -> None:
        members = [
            {"role": "g0-service", "identity": identity(11), "evidence": "service-start"},
            {"role": "g1-coordinator", "identity": identity(12), "evidence": "coordinator.pid"},
            {"role": "native-backend-new", "identity": identity(13), "evidence": "excluded"},
        ]
        probes = {pid: 0 for pid in (11, 12, 13)}
        ps_calls: list[tuple[int, ...]] = []

        def probe(pid: int) -> dict[str, object]:
            probes[pid] += 1
            return identity(pid)

        def group_ps(pids: tuple[int, ...]) -> str:
            ps_calls.append(pids)
            return "11 1 100 1.5\n12 1 200 2.5\n"

        row = sampler.sample_set(
            members,
            identity_probe=probe,
            ps_runner=group_ps,
            report_rusage=[{"producer_id": "worker-1", "cpu_seconds": 0.25, "max_rss_kib": 50}],
            clock=lambda: 10.0,
        )
        self.assertEqual(ps_calls, [(11, 12)])
        self.assertEqual(probes, {11: 2, 12: 2, 13: 0})
        self.assertEqual(row["sampled_rss_kib"], 300)
        self.assertEqual(row["product_rss_kib_upper_bound"], 350)
        self.assertEqual(row["excluded_roles"], ["native-backend-new"])

    def test_identity_drift_after_group_ps_is_rejected(self) -> None:
        calls = 0

        def probe(pid: int) -> dict[str, object]:
            nonlocal calls
            calls += 1
            return identity(pid, "birth" if calls == 1 else "reused")

        with self.assertRaises(sampler.ResourceIdentityError):
            sampler.sample_set(
                [{"role": "g0-service", "identity": identity(11)}],
                identity_probe=probe,
                ps_runner=lambda _: "11 1 100 1.0\n",
            )

    def test_lifecycle_writes_private_one_second_samples(self) -> None:
        class FakeClock:
            value = 0.0

            def now(self) -> float:
                return self.value

            async def sleep(self, seconds: float) -> None:
                self.value += seconds

        clock = FakeClock()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "samples.jsonl"
            rows = asyncio.run(
                sampler.sample_lifecycle(
                    path,
                    lambda: [{"role": "g0-service", "identity": identity(11)}],
                    identity_probe=lambda pid: identity(pid),
                    duration_seconds=2,
                    ps_runner=lambda _: "11 1 100 1.0\n",
                    clock=clock.now,
                    sleep=clock.sleep,
                )
            )
            persisted = [json.loads(line) for line in path.read_text().splitlines()]
            self.assertEqual(len(rows), 2)
            self.assertEqual(persisted, rows)
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)


if __name__ == "__main__":
    unittest.main()
