package kv

import (
	"KV-Store/pkg/arena"
	"KV-Store/pkg/wal"
	"KV-Store/sstable"
	"fmt"
	"os"
	"sort"
	"time"
)

const mapLimit = 10 * 1024 * 1024 // 10MB per map

type Commands byte

const (
	CmdPut    Commands = 1
	CmdDelete Commands = 2
)

type raftCmd struct {
	Op    Commands
	Key   string
	Value string
}
type OpResult struct {
	Value string
	Err   error
}

func NewMemTable(size int, newWal *wal.WAL) *MemTable {
	return &MemTable{
		Index: make(map[string]int),
		Arena: arena.NewArena(size),
		Wal:   newWal,
	}
}

func (s *Store) RotateTable() {
	if s.frozenMap != nil {
		fmt.Println("[RotateTable] Waiting for previous frush to finish")
		for s.frozenMap != nil {
			// conditional sync, when FlushWorker calls Signal() - this re-locks s.mu and continues
			s.cond.Wait()
		}
	}
	s.frozenMap = s.ActiveMap
	s.walSeq++
	newWal, err := wal.OpenWAL(s.WalDir, s.walSeq)
	if err != nil {
		panic(fmt.Sprintf("OpenWAL failed in rotateTable(): %v", err))
	}
	s.ActiveMap = NewMemTable(mapLimit, newWal)

	select {
	case s.FlushChan <- struct{}{}:
	default:
	}
}

func checkTable(table *MemTable, key string) (string, bool, bool) {
	if table == nil {
		return "", false, false
	}
	offset, ok := table.Index[key]
	if !ok {
		return "", false, false
	}
	valBytes, isTombstone, err := table.Arena.Get(offset)
	if err != nil {
		return "", false, false
	}
	if isTombstone {
		return "", true, true
	}
	return string(valBytes), false, true
}

func CreateSSTable(frozenMem *MemTable, sstDir string, level int) error { // Added walDir arg for cleaner path handling
	// sort
	keys := make([]string, 0, len(frozenMem.Index))
	for k := range frozenMem.Index {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	filename := fmt.Sprintf("%s/L%d_%d.sst", sstDir, level, time.Now().UnixNano()) // Use walDir path
	builder, err := sstable.NewBuilder(filename, len(keys))
	if err != nil {
		return fmt.Errorf("failed to create sstable file: %w", err)
	}

	// add to builder
	for _, k := range keys {
		offset := frozenMem.Index[k]
		val, isTombstone, _ := frozenMem.Arena.Get(offset)

		// Convert string to []byte for the Builder
		err := builder.Add([]byte(k), val, isTombstone)
		if err != nil {
			// If write fails, we should probably close and delete the corrupt file
			_ = builder.File.Close()
			_ = os.Remove(filename)
			return fmt.Errorf("failed to add key to sstable: %w", err)
		}
	}

	if err := builder.Close(); err != nil {
		return fmt.Errorf("failed to close sstable: %w", err)
	}

	// delete wal
	if frozenMem.Wal != nil {
		if err := frozenMem.Wal.Remove(); err != nil {
			fmt.Printf("Warning: failed to delete old WAL: %v\n", err)
		}
	}

	return nil
}

func (s *Store) FlushWorker() {
	// Loop forever
	for {
		// Wait for signal
		<-s.FlushChan
		// Keep working until frozenMap is gone
		// Use an inner loop to process back-to-back flushes
		for {
			s.mu.Lock()
			frozenMem := s.frozenMap
			s.mu.Unlock()

			if frozenMem == nil {
				break // Go back to sleep
			}

			//  Do the heavy lifting
			err := CreateSSTable(frozenMem, s.SstDir, 0)

			s.mu.Lock()
			if err != nil {
				// LOG ERROR but DO NOT DEADLOCK.
				// We clear the map anyway to allow new writes.
				fmt.Printf("CRITICAL FLUSH ERROR: %v\n", err)
			}

			//  CLEAR THE MAP (Crucial!)
			s.frozenMap = nil
			s.cond.Signal()
			s.mu.Unlock()

			s.refreshSSTables()

			// Trigger compaction asynchronously
			go func() { _ = s.CheckAndCompact(0) }()
		}
	}
}
