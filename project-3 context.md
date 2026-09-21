# Project 3: KhorosLog — Distributed Fault-Tolerant Commit Log & Event Stream Engine with Raft Consensus

## 1. Project Overview & Objective
An enterprise-grade, high-throughput distributed commit log and event streaming engine implemented from first principles in **Go (Golang)** on Windows 11. 

Most software engineering candidates showcase basic message brokers by simply installing Kafka or RabbitMQ as a Docker black-box. **KhorosLog builds the distributed consensus and storage internals from scratch**, mirroring the core engineering architecture behind **Apache Kafka**, **Apache BookKeeper**, and **etcd**.

### The Real-World Engineering Problems Solved:
1. **Linearizable Consistency & Split-Brain Prevention**: In distributed event streaming, network partitions and node failures cause message loss and divergent states. KhorosLog implements the **Raft Consensus Algorithm** to guarantee linearizable writes across dynamic clusters with automated quorum elections ($N/2 + 1$).
2. **Disk I/O Bottlenecks & User-Space Copy Overhead**: Standard disk logging incurs severe CPU cache and memory copy overhead. KhorosLog implements **Memory-Mapped Files (`mmap`)** and **Zero-Copy Direct Disk I/O**, writing and reading binary log segments directly through the OS page cache without intermediate user-space buffer allocations.
3. **Partitioned Segment Management & Retention**: Implements immutable write-ahead log (WAL) segment rolling, sparse index lookups, and time/size-based log compaction.

---

## 2. Hardware Resource Budget (ASUS TUF F15 - Windows 11)
* **Execution Environment**: Native Windows 64-bit (`.exe` multi-node cluster communicating over local loopback ports `127.0.0.1:8001`, `:8002`, `:8003`).
* **CPU (Intel Core i7-12700H - 14 Cores / 20 Threads)**:
  * Highly parallel goroutines handle incoming TCP client connections, heartbeat timers, and peer-to-peer log replication concurrently with near-zero CPU overhead.
* **RAM (16 GB DDR5)**:
  * Minimal memory footprint: **~80 MB to 200 MB RAM** for the entire 3-node cluster, leaving your GPU and system free.
* **Disk I/O**:
  * Leverages high-speed NVMe PCIe Gen4 SSD for sub-millisecond durable WAL fsync operations.

---

## 3. End-to-End System Architecture

```
                 [ Producer / Publisher Clients ]
                                │
                                ▼ Custom TCP Binary Protocol
                 ┌─────────────────────────────┐
                 │     Cluster Leader (Node 1) │
                 └──────────────┬──────────────┘
                                │
        ┌───────────────────────┴───────────────────────┐
        ▼ AppendEntries RPC                             ▼ AppendEntries RPC
 ┌──────────────┐                                ┌──────────────┐
 │ Node 2       │                                │ Node 3       │
 │ (Follower)   │                                │ (Follower)   │
 └──────┬───────┘                                └──────┬───────┘
        │                                               │
        └───────────────────────┬───────────────────────┘
                                │
                                ▼ (Quorum Reached: 2/3 Nodes Ack)
                 ┌─────────────────────────────┐
                 │   Commit Log Applied to WAL │
                 │  (mmap Zero-Copy Segment)   │
                 └──────────────┬──────────────┘
                                │
                                ▼ High-Throughput Stream
                 [ Consumer / Subscriber Groups ]
```

### Core Architectural Modules:

#### 1. `raft/consensus.go` (Raft Protocol Implementation)
* **Leader Election**: Randomized election timers ($150\text{ms} - 300\text{ms}$) preventing split-vote scenarios. Transitions through `Follower` $\to$ `Candidate` $\to$ `Leader` states.
* **Log Replication**: `AppendEntries` RPCs ensuring all follower logs mirror the leader's commit index.
* **Safety & Term Management**: Strictly rejects stale terms and uncommitted entries from un-quorate leader partitions.

#### 2. `storage/wal_segment.go` (Zero-Copy Append-Only Log)
* Log data is organized into fixed-size **Segments** (e.g., 64MB binary chunks).
* Employs **Windows Memory-Mapped Files (`mmap`)** to bypass standard `read()`/`write()` user-kernel copy boundaries.
* Maintains a binary **Sparse Index File (`.index`)** mapping relative message offsets to physical byte positions, enabling **$O(1)$ constant-time message lookups**.
* Enforces binary message framing with **CRC32 checksums** to detect disk corruption.

#### 3. `network/binary_protocol.go` (Custom TCP Wire Format)
* Custom, low-overhead binary framing protocol:
  `[Magic Byte (1B)][Message Length (4B)][Offset (8B)][CRC32 (4B)][Payload (NB)]`
* Outperforms heavy JSON/REST serialization by 8x–12x in throughput and network serialization latency.

#### 4. `consumer/group_coordinator.go` (Consumer Group Offset Tracking)
* Consumer group partition rebalancing.
* Persistent offset commits allowing clients to resume consumption seamlessly after crashes or restarts.

#### 5. `dashboard/cluster_ui/` (Interactive Cluster Visualizer)
* Visual interactive React / Web-based cluster dashboard connecting via WebSockets.
* Displays live node states (`LEADER`, `FOLLOWER`, `CANDIDATE`), current term, commit indices, and replication lag.
* **Built-in Chaos Engineering Simulator**: Includes buttons to simulate node crashes ("Kill Leader"), network partitions, and latency spikes with real-time election animations.

---

## 4. Benchmark & Metrics Suite
1. **Durable Ingestion Throughput**:
   * Measures messages per second under synchronous vs. asynchronous `fsync` flushing (Target: **$>80,000\text{ msgs/sec}$** on local NVMe SSD).
2. **End-to-End Replication Latency**:
   * Evaluates round-trip latency from Producer write to Follower disk commit ($p50, p95, p99$).
3. **Partition Failover Recovery Time**:
   * Measures time taken from leader process termination to successful election of a new leader and resumption of client writes (Target: **$<150\text{ ms}$**).

---

## 5. Technology Stack
* **Language & Runtime**: Go (Golang 1.22+ on native Windows 64-bit)
* **Concurrency Primitives**: Goroutines, Channels, `sync.RWMutex`, Atomic pointers
* **Storage & OS Primitives**: Win32 Memory-Mapped Files (`golang.org/x/sys/windows`, `edsrzf/mmap-go`)
* **Networking**: Native Go `net.TCP`, Protobuf / Custom Binary Protocol, WebSockets
* **Monitoring & UI**: React / Next.js, Tailwind CSS, Chart.js / D3.js for cluster state graph visualization
