## Performance Engineering

To maximize write throughput, I engineered a custom **Bump-Pointer Arena Allocator**. By replacing standard Go Map hashing with direct memory offset calculations, I achieved a **3.5x speedup** in raw write operations.

### 📊 Benchmark Results

|**Implementation**|**Latency (ns/op)**|**Throughput (Ops/sec)**|**Allocations/Op**|
|---|---|---|---|
|**Standard Map (Baseline)**|82.21 ns|~12.1 M|0|
|**Arena Allocator (Optimized)**|**23.17 ns**|**~43.1 M**|**0**|
|**Improvement**|**71.8% Faster**|**3.5x Boost**|**-**|

### Key Takeaways

1. Hashing vs. Pointer Arithmetic:

Even with zero allocations, the Standard Map implementation is limited by the CPU cost of the Murmur3/AES hash functions and bucket lookup logic (82ns). The Arena allocator bypasses this entirely, using simple pointer addition to store data in O(1) time (23ns).

**2. Latency Reduction:**

The optimizations reduced the average write operation latency by **~59ns** per op. In a high-frequency trading or telemetry context, this nanosecond-level optimization compounds to support millions of additional requests per second.

> Reproducibility:
>
> Results verified on Intel Core i5-12450H.
>
> Bash
>
> ```
> go test -bench=. -benchmem ./docs/benchmarks/arena
> ```
>
> _Full pprof data available in [docs/benchmarks/arena](docs/benchmarks/arena)._