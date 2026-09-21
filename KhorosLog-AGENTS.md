# AGENTS.md — KhorosLog: Distributed Fault-Tolerant Commit Log & Event Stream Engine

This file gives coding agents (Antigravity and others) the context needed to build, extend, and validate this repository correctly. Read this in full before making changes. When a request conflicts with this file, this file wins unless the user explicitly overrides it in chat.

## 1. What we're building

**KhorosLog**: a distributed, fault-tolerant commit log and event streaming engine implemented **from first principles in Go** — not Kafka/RabbitMQ wrapped in Docker. It mirrors the core engineering internals of Apache Kafka, Apache BookKeeper, and etcd: a Raft-consensus-backed replicated log with zero-copy mmap storage and a custom binary wire protocol.

The point of this project is that the hard parts are hand-built, not delegated to a library:
1. **Raft Consensus** — leader election, `AppendEntries` log replication, and term/safety rules implemented directly, guaranteeing linearizable writes and preventing split-brain across a dynamic cluster (quorum = `N/2 + 1`).
2. **Zero-Copy Storage** — memory-mapped WAL segments read/written through the OS page cache, bypassing the user-kernel copy that a naive `read()`/`write()` implementation would incur.
3. **Sparse Indexing** — O(1) offset-to-byte-position lookups via a binary sparse index file, not a linear scan.
4. **Custom Binary Wire Protocol** — a fixed-layout TCP frame format outperforming JSON/REST serialization, since generic serialization is a bottleneck at the throughput this system targets.

Non-negotiable constraints:
- **Linearizability**: no acknowledged write may be lost or reordered across a leader failover. This is the entire reason Raft exists here — a correctness bug in `raft/consensus.go` is not a style issue, it's a project-failing bug.
- **Durable throughput target: >80,000 msgs/sec** on local NVMe with fsync in the loop.
- **Failover recovery target: <150ms** from leader termination to a new leader accepting writes.
- **No library shortcuts for the consensus or storage core** — using an off-the-shelf Raft library (e.g. `hashicorp/raft`) or a generic KV store for the WAL defeats the purpose of the project. Flag this explicitly if ever tempted.

## 2. Target environment

- OS: Windows 11 native (`.exe` cluster nodes communicating over loopback — `127.0.0.1:8001`, `:8002`, `:8003`)
- CPU: Intel Core i7-12700H, 14 cores / 20 threads — goroutines handle TCP connections, heartbeat timers, and replication concurrently; this is a concurrency-bound design, not a raw-compute-bound one
- RAM: 16GB DDR5 — the entire 3-node cluster should run in ~80–200MB; if memory use grows well past that, something is retaining data it shouldn't (e.g., unbounded in-memory log buffering instead of relying on mmap + page cache)
- Disk: NVMe PCIe Gen4 SSD — sub-millisecond durable fsync is assumed; don't design around spinning-disk latency assumptions
- Language/runtime: Go 1.22+, native Windows 64-bit build (`GOOS=windows`)

Agents must not assume a POSIX-only mmap API (`mmap()`/`msync()`); this project uses Win32 memory-mapped file APIs via `golang.org/x/sys/windows` or `edsrzf/mmap-go`, and must build and run correctly as a native Windows binary, not just under WSL.

## 3. System architecture

```
Producer clients --(custom TCP binary protocol)--> Cluster Leader
Leader --AppendEntries RPC--> Follower (Node 2), Follower (Node 3)
Quorum reached (2/3 ack) --> Commit applied to WAL (mmap zero-copy segment)
WAL --> Consumer/Subscriber groups (via group_coordinator offset tracking)
```

Three-node cluster is the default target for development and the benchmark suite; the design should generalize to N nodes but N=3 is what gets tested and tuned.

## 4. Repository layout

```
/raft
  consensus.go            # Module 1: leader election, AppendEntries, term/safety rules
/storage
  wal_segment.go           # Module 2: mmap WAL segments, sparse index, CRC32 framing
/network
  binary_protocol.go        # Module 3: custom TCP wire format
/consumer
  group_coordinator.go       # Module 4: consumer group rebalancing, offset commits
/dashboard
  cluster_ui/                 # Module 5: React/Next.js cluster visualizer + chaos simulator
/cmd
  node/main.go                 # Node entrypoint (reads node ID, peer list, port from config)
/benchmark
  throughput_bench.go           # sync vs async fsync msgs/sec
  replication_latency_bench.go   # p50/p95/p99 producer-write-to-follower-commit latency
  failover_bench.go               # leader-kill-to-new-leader-accepting-writes timing
/tests
  raft_test.go                    # election correctness, split-vote prevention, term rules
  wal_segment_test.go               # segment rolling, sparse index correctness, CRC32 detection
  binary_protocol_test.go            # frame encode/decode round-trip
  group_coordinator_test.go           # rebalancing, offset persistence across restarts
  chaos_test.go                        # simulated partition/crash scenarios
go.mod
go.sum
AGENTS.md
README.md
```

Keep `raft`, `storage`, `network`, and `consumer` as separate packages with narrow public interfaces between them — the whole value of this project as a portfolio piece is that each subsystem (consensus, storage, wire protocol) can be read, tested, and reasoned about independently, the way the real Kafka/etcd codebases are structured.

## 5. Module contracts

### `raft/consensus.go`
- States: `Follower → Candidate → Leader`, with randomized election timeout in the 150–300ms range specifically to avoid split votes — don't use a fixed timeout.
- `AppendEntries` RPC must carry `term`, `prevLogIndex`, `prevLogTerm`, `entries[]`, `leaderCommit`, matching the standard Raft paper's fields — don't invent a simplified variant that skips consistency checks.
- Must reject stale terms and entries from a leader that no longer holds quorum. A node accepting a write from a de-quorate leader is a linearizability violation.
- Log replication and commit-index advancement must only occur after a strict majority (`N/2 + 1`) has acknowledged.

### `storage/wal_segment.go`
- Fixed-size segments (default 64MB); rolling to a new segment on size threshold, not on a time-only basis unless explicitly requested.
- Memory-mapped via Win32 APIs — writes go through the mapped region, not a buffered `os.File.Write` call, or the zero-copy claim is false.
- Sparse index file maps relative offsets to physical byte positions for O(1) lookup; don't fall back to scanning the segment on lookup miss without updating the index.
- Every message frame includes a CRC32 checksum; reads must validate it and surface corruption rather than silently returning bad data.

### `network/binary_protocol.go`
- Frame layout: `[Magic Byte (1B)][Message Length (4B)][Offset (8B)][CRC32 (4B)][Payload (NB)]` — don't substitute a generic serialization format (JSON, Protobuf) for the client-facing hot path; that's the exact overhead this module exists to avoid. Protobuf is acceptable only for internal control-plane messages if explicitly scoped that way, not the data path.
- Must handle partial reads/writes correctly over TCP (framing, not assuming one `Read()` call returns one full frame).

### `consumer/group_coordinator.go`
- Persists offsets so a crashed/restarted consumer resumes from its last committed offset, not from the beginning or from an in-memory-only position.
- Rebalancing must handle a consumer joining or leaving a group without losing or duplicating partition assignments beyond what at-least-once semantics allow.

### `dashboard/cluster_ui/`
- Connects via WebSocket to the cluster for live node state (`LEADER`/`FOLLOWER`/`CANDIDATE`), current term, commit indices, and replication lag.
- **Chaos engineering controls** ("Kill Leader", simulate partition, simulate latency spike) must map to real actions against the running cluster (killing a real process/connection, real induced delay) — not a cosmetic animation with no backing effect. The point of the simulator is to visibly trigger and observe a real election, not to fake one.

## 6. Dynamic UI (cluster visualizer) requirements

Build `/dashboard/cluster_ui` as a **React/Next.js + Tailwind CSS** app with **Chart.js or D3.js** for the cluster state graph:

- Live topology view: nodes as graph elements, colored/labeled by current Raft state, with term number and commit index shown per node.
- Replication lag indicator per follower (how far behind the leader's commit index).
- Election events should animate in real time when they actually happen (triggered by the WebSocket feed), not on a fixed interval or as a canned demo sequence.
- Chaos controls panel: buttons wired to real cluster-affecting RPCs/signals (kill leader process, drop a peer connection, inject latency) — surface the actual consequence (a real election, a real recovery time) in the same UI, not a separate mocked timeline.
- No fabricated node states or fake replication numbers in the shipped build — if scaffolded with mock data during development, replace before calling the feature done and say so explicitly.

## 7. Setup, build, and test commands

```bash
# Build
go build ./...

# Run a 3-node local cluster (separate terminals or a script)
go run ./cmd/node --id=1 --port=8001 --peers=127.0.0.1:8002,127.0.0.1:8003
go run ./cmd/node --id=2 --port=8002 --peers=127.0.0.1:8001,127.0.0.1:8003
go run ./cmd/node --id=3 --port=8003 --peers=127.0.0.1:8001,127.0.0.1:8002

# Tests
go test ./... -race -v            # -race is mandatory given heavy goroutine/channel use

# Dashboard
cd dashboard/cluster_ui
npm install
npm run dev
npm run build                      # must succeed before merge
npm run lint

# Benchmarks (slow — run before claiming a perf win, not on every commit)
go run ./benchmark/throughput_bench.go
go run ./benchmark/replication_latency_bench.go
go run ./benchmark/failover_bench.go
```

`-race` is not optional for this codebase — Raft state transitions, log replication, and the mmap-backed WAL all involve concurrent access, and a data race here is a correctness bug that won't show up in a quick manual test.

## 8. Correctness bar before claiming a task done

1. **Linearizability under failure**: `tests/chaos_test.go` and `raft_test.go` must demonstrate no acknowledged write is lost across a simulated leader crash, a network partition, or a stale-leader scenario. This is the primary bar — throughput numbers don't matter if this fails.
2. **Throughput**: `benchmark/throughput_bench.go` shows >80,000 msgs/sec durable ingestion on the target NVMe hardware. Report both sync and async fsync modes, don't blend them into one number.
3. **Failover time**: `benchmark/failover_bench.go` shows <150ms from leader termination to a new leader accepting writes. If a change increases this (e.g., a longer election timeout "for stability"), justify it explicitly against this target.
4. **Data integrity**: CRC32 validation actually catches injected corruption in a test, not just in theory.
5. **Dashboard reflects reality**: every node state, term number, and chaos action in the UI traces to a real cluster event over the WebSocket feed, never a placeholder or canned animation.

## 9. Style and process notes for agents

- Don't reach for an existing Raft or storage library to "save time" — the entire value of this project is the from-scratch implementation. If a request would effectively delegate consensus or WAL storage to a library, flag it before proceeding.
- Prefer explicit state machines (typed enums/consts for `Follower`/`Candidate`/`Leader`) over stringly-typed state — this code is meant to be read as a reference implementation of Raft, so clarity here matters more than brevity.
- Concurrency primitives should match what's already declared in the stack (goroutines, channels, `sync.RWMutex`, atomics) — don't introduce a third-party actor framework or job queue library without asking.
- Any change to the wire protocol's frame layout is a breaking change across all three modules (`network`, `storage` framing assumptions, and the dashboard's WebSocket parsing if it touches raw frames) — call this out explicitly rather than changing it locally.
- If a requested change would compromise linearizability, the >80k msgs/sec target, or the <150ms failover target, say so explicitly before implementing rather than after.
