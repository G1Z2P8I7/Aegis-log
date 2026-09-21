# PRODUCT.md — Aegis Product Context

## 1. Product Identity & Purpose
* **Product Name**: Aegis
* **Tagline**: The Ultra-Low Latency Distributed Commit Log for Windows.
* **Core Purpose**: A high-performance, fault-tolerant, append-only commit log and event stream engine engineered entirely from first principles in Go for Windows 11. It provides an enterprise alternative to heavy Java/Scala streaming stacks (like Apache Kafka) for resource-constrained edge machines, industrial SCADA setups, and low-latency trading systems.

## 2. Target Audience
* **Systems & Infrastructure Engineers**: Building distributed systems, storage engines, and internal message buses.
* **FinTech & Trading Developers**: Requiring deterministic sub-6ms quorum commits without garbage collection pauses.
* **Industrial IoT & Edge Developers**: Deploying on ruggedized Windows 10/11 IoT devices that cannot afford the 2GB+ memory overhead of Kafka/JVM.

## 3. Operating Context & Constraints
* **Platform**: Native Windows 11 / Windows Server (x86_64).
* **Storage Layer**: Direct Win32 memory mapping via `CreateFileMapping`, `MapViewOfFile`, and `FlushViewOfFile` over 64MB rolling `.log` segments and sparse `.index` files.
* **Network Framing**: Custom 17-byte raw binary protocol `[Magic(1B) | Offset(8B) | Length(4B) | CRC32(4B)]` with IEEE polynomial checksum validation and optional TLS 1.3 mutual authentication (mTLS).
* **Consensus**: Linearizable Raft state machine with quorum commits ($\lfloor N/2 \rfloor + 1$), Pre-Vote protocol (§9.6), Raft §7 log snapshotting and compaction (`TakeSnapshot` / `InstallSnapshot`), 80ms heartbeats, and 350–700ms randomized election timers.
* **Runtime Footprint**: Zero external dependencies, single standalone `.exe` (< 25MB).

## 4. Voice & Principles
* **Authoritative & Technical**: Focus on concrete metrics (8.29M msgs/sec, 1.18ms p50 latency, <97ms failover) rather than vague marketing hype.
* **First-Principles Craft**: Celebrate the absence of middleware, JVM bloat, and third-party frameworks.
* **Deterministic Reliability**: Emphasize crash recovery, bitrot protection, bounded memory via snapshotting, and zero data races under chaos.
