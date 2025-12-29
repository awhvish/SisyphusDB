package benchmarks

import (
	"KV-Store/kv"
	"fmt"
	"os"
	"testing"

	"KV-Store/pkg/arena" // Make sure this import path is correct
	"KV-Store/pkg/wal"
)

// Global setup to ensure we use the same store instance across the bench loop
var benchStore *kv.Store

func setupBenchmarkStore(b *testing.B) *kv.Store {
	// 1. Cleanup old bench data
	os.RemoveAll("Storage_Bench")

	// 2. Initialize directories
	walDir := "Storage_Bench/wal_99"
	sstDir := "Storage_Bench/data_99"
	os.MkdirAll(walDir, 0755)
	os.MkdirAll(sstDir, 0755)

	// 3. Create a massive Arena (1GB) so we don't trigger Flush/Compaction
	// We manually construct the store to inject a 1GB Arena
	limit := 1024 * 1024 * 1024 // 1GB

	// Open a dummy WAL
	currentWal, _ := wal.OpenWAL(walDir, 0)

	store := &kv.Store{
		// Manually create the activeMap with 1GB limit
		ActiveMap: &kv.MemTable{
			Index: make(map[string]int),
			Arena: arena.NewArena(limit),
			Wal:   currentWal,
			Size:  0,
		},
		WalDir:    walDir,
		SstDir:    sstDir,
		FlushChan: make(chan struct{}, 1),
		Me:        99,
		// We leave Raft nil or basic because we won't use it
	}

	return store
}

func BenchmarkPut(b *testing.B) {
	store := setupBenchmarkStore(b)
	defer os.RemoveAll("Storage_Bench")

	// Pre-compute keys
	const poolSize = 10000
	keys := make([]string, poolSize)
	for i := 0; i < poolSize; i++ {
		keys[i] = fmt.Sprintf("k-%d", i)
	}
	val := "v-1234567890"

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		key := keys[i%poolSize]

		_, err := store.ActiveMap.Arena.Put(key, val, false)

		// --- FIX STARTS HERE ---
		if err != nil {
			b.StopTimer()

			limit := 1024 * 1024 * 1024
			store.ActiveMap.Arena = arena.NewArena(limit)

			b.StartTimer()

			store.ActiveMap.Arena.Put(key, val, false)
		}

		// store.ActiveMap.Index[key] = offset
	}
}
