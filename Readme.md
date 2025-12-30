# SisyphusDB: Distributed Key-Value Store

SisyphusDB is a high-performance, distributed key-value store engineered for strong consistency (CP system) and high write throughput. It implements a Log-Structured Merge (LSM) Tree storage engine and utilizes the Raft consensus algorithm to manage replication across a coordinated fleet of nodes.

This project demonstrates the architectural evolution from a simple in-memory map to a fault-tolerant distributed system capable of handling production-grade workloads.

---


##
You can install locally, in dockerized containers or K8s clusters.
[A detailed guide](/INSTALL.md)

## System Architecture

<img src="docs/high_level_architecture.png" alt="Architecture" width="50%>

The architecture is composed of three distinct layers, separating network communication, consensus logic, and physical storage.
A detailed explaination: [HERE](docs/ARCHITECHTURE.md)


## Performance Engineering & Benchmarks

The core objective of SisyphusDB is to minimize write latency and maximize throughput through low-level memory optimizations and asynchronous I/O strategies.

### 1. Memory Optimization: Custom Arena Allocator

To address the garbage collection (GC) overhead inherent in Go's standard map implementation, a custom **Bump-Pointer Arena Allocator** was engineered. By replacing standard hashing and bucket lookups with direct memory offset calculations, the system achieves O(1) storage time with zero allocations per operation in the hot path.

### Benchmark Results
|**Implementation**| **Latency (ns/op)** |**Throughput (Ops/sec)**| **Allocations/Op** |**Memory/Op**|
|---|---------------------|---|-------------------|---|
|**Standard Map (Baseline)**| 82.21 ns            |~12.16 M| 0                 |176 B|
|**Arena Allocator**| **23.17 ns**        |**~24.86 M**| **0**             |**64 B**|
|**Improvement**| **71.1% Faster**    |**104.4% Increase**| **50% Reduction** |**63.6% Reduction**|

### Verification
**Before (Standard Map):** High CPU time spent in `runtime.mallocgc` (GC Pauses). [GC Pressure](docs/benchmarks/arena/graph_baseline.png)

**After (Arena):** GC overhead eliminated; CPU spends time only on ingestion. [Arena Optimization](docs/benchmarks/arena/graph_arena.png)

*Reproducible via `go test -bench=. docs/benchmarks/arena/benchmark_test.go`*

_Results verified on Intel Core i5-12450H._

### 2. Throughput Scaling: The Journey to 3,000 RPS
Through iterative engineering and bottleneck analysis, SisyphusDB achieved a **30x increase in write throughput**, scaling from a baseline of 100 RPS to a stable **2,960 RPS**.

### 📉 Optimization Phases

The system evolved through three distinct architectural phases to overcome physical hardware and OS limitations:

* **Phase 1: Baseline (100 RPS)**
    * **Bottleneck:** Synchronous Disk I/O.
    * **Context:** The initial implementation used "Safety First" persistence, calling `fsync()` immediately on every Raft log entry. Throughput was physically capped by the disk's rotational latency.

* **Phase 2: Asynchronous Persistence**
    * **Bottleneck:** Ephemeral Port Exhaustion.
    * **Context:** Persistence was moved to background workers. While this removed the disk bottleneck, the naive network implementation opened a new connection for every replication request, causing `dial tcp: address already in use` errors.

* **Phase 3: Final Optimization (3,000 RPS)**
    * **Solution:** Implemented **Adaptive Micro-Batching** (aggregating writes into 50ms windows) and **TCP Connection Pooling** (Keep-Alive).
    * **Result:** Reduced network packet count by 95% and eliminated TCP handshake overhead, stabilizing the system at extreme loads.

---

#### 📊 Load Test Results

Benchmarks were conducted using **Vegeta** running inside the Kubernetes cluster to bypass ingress bottlenecks.

**1. Peak Performance (Stress Test)**
Pushing the system to its limits with a target of **3,000 Write RPS**:

| Target Rate | Actual Throughput | Success Rate | Mean Latency | P99 Latency |
| :--- | :--- | :--- | :--- | :--- |
| **2,500 RPS** | **2,481 RPS** | 100.00% | 29.94ms | 51.43ms |
| **3,000 RPS** | **2,960 RPS** | 100.00% | 53.64ms | 90.32ms |

> **Analysis:** Even at ~3,000 writes/second, the system maintains sub-100ms tail latency (P99), proving the efficacy of the non-blocking WAL architecture.

**2. Latency Breakdown (Leader vs. Follower)**
At a sustained load of 2,000 RPS, we analyzed the cost of internal request forwarding. Writes sent to **Followers** incur additional latency as they must be proxied to the Leader.

| Metric | Leader Node (Direct Write) | Follower Node (Proxy Overhead) |
| :--- | :--- | :--- |
| **Throughput** | **1,996 RPS** | 1,915 RPS |
| **Mean Latency** | **29.49ms** | 82.07ms |
| **P99 Latency** | **55.55ms** | 328.04ms |

> **Note:** The higher P99 latency on Follower nodes validates the internal "Smart Routing" mechanism. It proves that followers correctly buffered and forwarded traffic to the leader under pressure rather than dropping requests.

**To Reproduce:**
```bash
# Run from inside the cluster
echo "GET [http://kv-0.kv-raft:8001/put?key=load&val=test](http://kv-0.kv-raft:8001/put?key=load&val=test)" | vegeta attack -duration=5s -rate=3000 | vegeta report
````
---


## Reliability & Chaos Engineering

Fault tolerance was validated via Chaos Testing on a 3-node Kubernetes cluster to verify **sub-second leader election** and **Linearizability** under failure conditions.

### Experiment: Leader Failure during Write Load

Scenario: A client sends continuous Write (PUT) requests while the Leader node (kv-0) is forcibly deleted.

Constraint: No split-brain writes allowed; the system must failover automatically.

**Log Analysis Results:**

Plaintext

```
1767014613776,UP    System Healthy
1767014613925,DOWN  Leader Killed (Election Starts)
1767014614062,DOWN  Writes Rejected (Proxy Failed)
1767014614474,UP    New Leader Elected (Write Accepted)
```
[Recovery Benchmark Results](docs/benchmarks/recovery_log.csv)

To Reproduce the benchmarks, refer to [INSTALL.md](INSTALL.md)

**Recovery Metrics:**

- **Total Recovery Time:** 549ms

- **Consistency:** Zero data loss. Follower nodes correctly rejected writes during the election window, preserving strong consistency.


---

## Feature Implementation Status

The feature set targets distributed systems complexity comparable to senior-level engineering requirements.

|**Feature**|**Technical Justification**|**Status**|
|---|---|---|
|**LSM Tree Storage**|High-throughput write engine (vs. B-Trees).|✅ Done|
|**Arena Allocator**|Zero-allocation memory management.|✅ Done|
|**WAL & Crash Recovery**|Durability via `fsync` and replay logic.|✅ Done|
|**SSTables + Sparse Index**|Optimized disk I/O and binary search.|✅ Done|
|**Bloom Filters**|Probabilistic structures to minimize disk reads.|✅ Done|
|**Leveled Compaction**|Mitigation of Write/Read Amplification.|✅ Done|
|**Raft Consensus**|Distributed consistency (CAP Theorem compliance).|✅ Done|
|**gRPC & Protobuf**|Schema-strict internal communication.|✅ Done|
|**Prometheus Metrics**|System observability and telemetry.|✅ Done|
