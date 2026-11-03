# Raspberry Pi 4 resource qualification snapshot

Read-only live observation 2026-11-01 12:14 UTC; no restart, deploy, configuration,
schedule or inference mutation. Production source remained 8a3ce75.

| Observation | Result |
|---|---|
| Architecture / CPUs | aarch64 / 4 |
| RAM | 3796 MiB total; 1754 MiB MemAvailable with current services already running |
| Swap | 6143 MiB total; 2063 MiB used |
| Disk | 36 GiB free of 117 GiB; 68% used |
| Load | 1.62 / 1.40 / 1.31 |
| Short vmstat interval | 72-80% CPU idle; almost no swap IO; not a peak-load qualification |
| Temperature/throttling | 51.6 C / 0x0 |
| Agent runtime | gopod-owner idle, one process; 2 CPU and 1024 PID limits |
| Controller | about 122 MiB RSS, about 77% of one CPU in snapshot; no CPU/PID cap |
| Docker/cgroups | v2/systemd; CPU and PID controllers available; memory/swap controllers absent |
| PSI | /proc/pressure unavailable |

MemAvailable already accounts for all running services, including gopod. Do not
subtract existing controller/agent RSS again. docker stats reported 0B because
memory accounting is unavailable, not because containers consume no memory.
An idle agent process does not establish peak native CLI/tool consumption.

## Opt-in profile staged for complete-feature deployment

`deploy/compose.rpi4.yml`: maximum two agent processes, strict minimum available
1536 MiB, additional conservative unmeasured growth headroom 1024 MiB per already
admitted agent, Claude V8 heap 512 MiB, worker CPU 1.5 and PID limit 256, requested
worker memory cap 1024 MiB; controller CPU 1.0 and PID limit 128.

The additional growth reserve is deliberately conservative: it is not a second
subtraction of measured resident usage and is not a measured per-provider RSS
forecast. At the observed 1754 MiB, one start fits; a second waits until available
memory is at least 2560 MiB. With >=2560 MiB, two can be admitted, including during
startup before RSS grows. This preserves parallel capability without claiming the
current live sample can safely run two full native workloads. A future measured
remaining-growth model may reclaim this conservative reserve.

Strict admission never bypasses low/unreadable memory after 60 seconds. It affects
starts, not killing running work. Setting a Docker memory cap is only a requested
limit until the host memory controller is enabled and verified. V8 heap does not
bound Codex or tool/process-tree RSS; without hard enforcement an unexpected peak
can still cause host OOM. Auto scaling remains gated, not silently enabled.

Validate shared-host peak workloads, both pinned CLI providers and tools before
final deployment; CPU/PID/heap settings are initial bounded qualification values,
not demonstrated provider acceptance. Do not count swap as extra runnable RAM.
Cgroup boot changes/reboot need their own planned maintenance scope. No such
change was made in this task.

## Verification strategy

Unit admission tests cover a second startup on the same memory snapshot waiting,
then admission when headroom rises. Synthetic Docker tests qualify simultaneous
execution/independent Stop/drain without real credentials. They do not establish
native CLI resource envelopes. Per-thread parallel queues are later CP-037 slices;
P0 admission already supports different runtime folders but does not enable
same-chat thread parallelism. Full-feature deployment only, per owner instruction.
