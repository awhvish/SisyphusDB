# SisyphusDB

<p align="center">
  <b>A distributed key-value store built from scratch with Raft consensus</b>
</p>

<p align="center">
  <img src="docs/graphana_dashboard.png" width="80%" alt="Grafana Dashboard">
</p>

<p align="center">
  <a href="#features">Features</a> •
  <a href="#quick-start">Quick Start</a> •
  <a href="#architecture">Architecture</a> •
  <a href="#benchmarks">Benchmarks</a> •
  <a href="#chaos-testing">Chaos Testing</a>
</p>

---

## Features

- **Raft Consensus** — Leader election, log replication, and automatic failover with <550ms recovery
- **LSM-Tree Storage** — LevelDB-style tiered compaction with Bloom filters for 95% fewer disk lookups
- **3,000+ Write RPS** — Achieved through batched RPCs, arena-based memory pooling, and async persistence
- **Kubernetes Native** — StatefulSet deployment with persistent volumes and Prometheus/Grafana monitoring

---

## Quick Start

### Local (Docker Compose)
```bash
docker-compose up
# Access: http://localhost:8001/put?key=hello&val=world
# Grafana: http://localhost:3000 (admin/admin)
```

### Kubernetes
```bash
kubectl apply -f deploy/k8s/
```

See [INSTALL.md](INSTALL.md) for detailed setup and [EKS-INSTALL.md](EKS-INSTALL.md) for AWS deployment.

---

## Architecture

<p align="center">
  <img src="docs/high_level_architecture.png" width="70%" alt="Architecture">
</p>

The system consists of three layers:

| Layer | Components |
|-------|------------|
| **Consensus** | Raft leader election, log replication via gRPC, term-based conflict resolution |
| **Storage** | Write-ahead log, MemTable with arena allocator, SSTable compaction |
| **API** | HTTP interface with automatic leader forwarding |

See [docs/ARCHITECHTURE.md](docs/ARCHITECHTURE.md) for detailed documentation.

---

## Benchmarks

### Write Performance

| Metric | Value |
|--------|-------|
| Peak Throughput | **2,960 RPS** |
| Mean Latency | 53.64ms |
| P99 Latency | 90.32ms |
| Success Rate | 100% |

### Memory Optimization

Custom arena allocator reduced write latency by **71%** (82ns → 23ns) by eliminating GC pressure.

| Implementation | Latency | Allocations/Op |
|----------------|---------|----------------|
| Standard Map | 82.21 ns | 0 B |
| Arena Allocator | **23.17 ns** | **64 B** |

**Benchmarks:** [docs/benchmarks/](docs/benchmarks/) | **Arena profiling:** [docs/benchmarks/arena/](docs/benchmarks/arena/)

---

## Chaos Testing

Chaos tests validate Raft's correctness guarantees under failure conditions.

### Test 1: Leader Failover

Kills the leader mid-write and verifies no acknowledged data is lost.

```
[Step 1] Start 3-node cluster
[Step 2] Write 50 keys
[Step 3] SIGKILL the leader
[Step 4] Wait for new leader election (<550ms)
[Step 5] Write 50 more keys
[Step 6] Verify all 100 keys readable
```

**Result:** Zero data loss. All acknowledged writes survive leader failures.

### Test 2: Split-Brain Prevention

Sends writes during election and verifies only one node accepts them.

```bash
# Run chaos tests
cd tests/chaos
go test -v -timeout 120s
```

See [tests/chaos/README.md](tests/chaos/README.md) for details.

---

## Observability

Built-in Prometheus metrics and Grafana dashboards:

- Write throughput (RPS per node)
- P99 latency
- Raft leader state
- Replication lag

```bash
# Access dashboards
kubectl port-forward svc/grafana 3000:3000
# Open http://localhost:3000 (admin/admin)
```

---

## Project Structure

```
├── cmd/server/       # Entry point
├── raft/             # Consensus implementation
├── kv/               # Storage engine (LSM-tree)
├── pkg/
│   ├── arena/        # Zero-allocation memory pool
│   ├── wal/          # Write-ahead log
│   └── bloom/        # Bloom filter
├── sstable/          # Sorted string tables
├── tests/chaos/      # Chaos tests
├── deploy/
│   ├── k8s/          # Kubernetes manifests
│   ├── prometheus/   # Monitoring config
│   └── grafana/      # Dashboards
└── docs/             # Architecture & benchmarks
```

---

## License

MIT
