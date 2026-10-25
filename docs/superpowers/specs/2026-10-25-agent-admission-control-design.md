# Agent admission control (protect the host)

Status: approved direction by the owner (2026-10-25); details are the
controller's calls unless marked.

## Intent

gopod runs on a Raspberry Pi 4B (4 cores, 3.8 GB RAM, other services
share it). Measured on the deploy host: one agent turn that runs a tool
peaks at ~0.5-0.6 GB RSS; an idle agent container costs almost nothing.
RAM is spent by *running agents*, not by containers. The memory limit in
code (4 GiB per container) is not enforced on that host because the memory
cgroup is disabled, so a runaway agent can take down the host. Threads and
background work (memory extraction and compaction also spawn `claude`
processes through `RunFresh`) make several simultaneous agents likely.

## Owner decisions

- Implement parts 1-3 below. Enabling the memory cgroup on the host (needs
  a reboot) is **backlog, not implemented**.
- Containers stay one per chat; limit *running agents*, not containers.

## Parts

### 1. Global agent gate

A gate in `internal/runner` that every agent process start goes through:
`Run`, `RunStream` and `RunFresh` (including extraction, compaction,
task parsing). `Acquire(ctx, class, onWait) (release func(), err)`;
`release` is idempotent. At most `GOPOD_MAX_AGENTS` (default 2) agents run
at once across all chats and threads. Two classes: *interactive* (user
turns) and *background* (RunFresh); interactive waiters are served before
background ones, FIFO inside a class; background never preempts a running
agent. `onWait` is called once when the caller has to wait. A cancelled
ctx (Stop, shutdown) leaves the wait immediately and frees its place. The
queue's own concurrency (`DefaultMaxConcurrent`) stays, but the gate is the
authority; there must be no deadlock between a queue slot, the per-chat
agent slot and the gate (acquire order documented and tested).

### 2. Memory-aware admission

Before a new agent starts, the gate also requires host `MemAvailable`
(from `/proc/meminfo`, visible inside the gopod container) at least
`GOPOD_MIN_AVAILABLE_MB` (default 900). If it is lower the caller waits
(poll about every 2 s) like for a slot. If the value cannot be read
(non-Linux, tests) treat memory as sufficient. The first agent of an idle
system is never blocked by this check alone for more than a bounded time
(for example 60 s), after which it proceeds with a Warn log, so a
mis-sized threshold cannot make the bot silent forever.

User-visible: when an interactive turn has to wait, the Telegram draft (or
the placeholder on the edit path) shows `⏳ waiting for a free agent slot`
(or `... for memory`) instead of a bare cursor.

### 3. Soft memory cap per agent

`NODE_OPTIONS=--max-old-space-size=<MB>` in the environment of Claude
agent execs, `GOPOD_AGENT_NODE_HEAP_MB` default 768, 0 disables. Not for
Codex. It is not a secret, so it is set by the runner (the D012 allowlist
rules for forwarding env from the host do not apply). Existing
`NODE_OPTIONS` must be preserved/merged, not overwritten.

## Non-goals

Memory cgroup / kernel cmdline / reboot (backlog), CPU limits, parallel
execution of threads (its own research item), container-per-thread,
remote Docker hosts.

## Anti-goals

Deadlocks between the queue, the per-chat agent slot, the gate and Stop;
a Stop that leaves a waiter holding a place; background work starving
interactive turns; silent forever-waiting; changing behaviour when there
is plenty of capacity (single user, one turn at a time must feel exactly
as today).

## Done means

Unit tests for the gate (limit, priority, FIFO, cancel, idempotent
release, memory reader injected, bounded wait), runner tests with the fake
exec (three simultaneous turns with `GOPOD_MAX_AGENTS=2`: the third
waits; RunFresh does not starve a turn; Stop while waiting), config tests,
`NODE_OPTIONS` merge test; build, vet, tests green; ADR and HANDOFF note.
