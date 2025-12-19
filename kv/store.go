package kv

import (
	"KV-Store/pkg/arena"
	"KV-Store/pkg/wal"
	"KV-Store/sstable"
	sstable2 "KV-Store/sstable"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const mapLimit = 10 * 1024 // 10KB for now

type MemTable struct {
	Index map[string]int
	Arena *arena.Arena
	size  uint32
	Wal   *wal.WAL
}

func NewMemTable(size int, newWal *wal.WAL) *MemTable {
	return &MemTable{
		Index: make(map[string]int),
		Arena: arena.NewArena(size),
		Wal:   newWal,
	}
}

type Store struct {
	activeMap *MemTable
	frozenMap *MemTable
	ssTables  []*sstable2.Reader
	walDir    string
	sstDir    string
	walSeq    int64
	flushChan chan struct{}
	mu        sync.RWMutex
}

func NewKVStore() (*Store, error) {
	walDir := filepath.Join("wal")
	sstDir := filepath.Join("data")

	// 2. Create them if they don't exist
	if err := os.MkdirAll(walDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create wal dir: %w", err)
	}
	if err := os.MkdirAll(sstDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create sst dir: %w", err)
	}
	_, seqId, _ := wal.FindActiveFile(walDir)

	currentWal, _ := wal.OpenWAL(walDir, seqId)
	entries, _ := currentWal.Recover()
	store := &Store{
		activeMap: NewMemTable(mapLimit, currentWal),
		frozenMap: nil,
		walDir:    walDir,
		sstDir:    sstDir,
		flushChan: make(chan struct{}),
	}
	for _, entry := range entries {
		k := string(entry.Key)
		v := string(entry.Value)

		var offset int
		var err error
		switch entry.Cmd {
		case wal.CmdPut:
			offset, err = store.activeMap.Arena.Put(k, v, false)
			if err != nil {
				return nil, err
			}
		case wal.CmdDelete:
			offset, err = store.activeMap.Arena.Put(k, v, true) //handles tombstone
			if err != nil {
				return nil, err
			}
		}
		store.activeMap.Index[k] = offset
		store.activeMap.size += uint32(len(k) + len(v))
	}
	go store.FlushWorker()
	return store, nil
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

	// 5. delete wal
	if frozenMem.Wal != nil {
		if err := frozenMem.Wal.Remove(); err != nil {
			fmt.Printf("Warning: failed to delete old WAL: %v\n", err)
		}
	}

	return nil
}

func (s *Store) FlushWorker() {

	for range s.flushChan {
		s.mu.Lock()
		frozenMem := s.frozenMap
		s.mu.Unlock()
		if frozenMem == nil || frozenMem.size == 0 {
			continue
		}
		err := CreateSSTable(frozenMem, s.sstDir, 0)
		if err != nil {
			fmt.Printf("Failed to create SSTable %s: %s\n", s.walDir, err)
			continue
		}
		s.mu.Lock()
		s.frozenMap = nil
		if s.activeMap.Wal != nil {
			_ = s.activeMap.Wal.Remove() // Clean up old WAL
		}
		s.mu.Unlock()
		s.refreshSSTables()
		_ = s.CheckAndCompact(0)
	}
}

func (s *Store) RotateTable() {
	s.frozenMap = s.activeMap
	s.walSeq++
	newWal, _ := wal.OpenWAL(s.walDir, s.walSeq)
	s.activeMap = NewMemTable(mapLimit, newWal)

	select {
	case s.flushChan <- struct{}{}:
	default:
	}
}

func (s *Store) Put(key string, val string, isDelete bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	//  size: Header(1) + KeyLen(2) + ValLen(4) + Key + Val
	entrySize := 1 + 2 + 4 + len(key) + len(val)
	if int(s.activeMap.size)+entrySize > mapLimit {
		if s.frozenMap != nil {
			return errors.New("write stall: memTable flushing")
		}
		s.RotateTable()
	}

	// Write in logs
	var er error
	if isDelete {
		er = s.activeMap.Wal.Write(key, val, wal.CmdDelete)
	} else {
		er = s.activeMap.Wal.Write(key, val, wal.CmdPut)

	}
	if er != nil {
		fmt.Println("Error writing log: ", er)
	}
	offset, err := s.activeMap.Arena.Put(key, val, isDelete)
	if err != nil {
		return errors.New("failed to put key " + key + ":" + err.Error())
	}
	s.activeMap.Index[key] = offset
	s.activeMap.size += uint32(entrySize)
	return nil
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

func (s *Store) Get(key string) (string, bool) {
	s.mu.RLock()
	// 1. Check active table
	if val, isTomb, found := checkTable(s.activeMap, key); found {
		s.mu.RUnlock()
		if isTomb {
			return "", false
		}
		return val, true
	}
	// 2. Check frozen table
	if val, isTomb, found := checkTable(s.frozenMap, key); found {
		s.mu.RUnlock()
		if isTomb {
			return "", false
		}
		return val, true
	}
	s.mu.RUnlock() // Unlock BEFORE Disk IO to avoid blocking writes!

	// 3. Check SSTables (Disk)
	files, _ := os.ReadDir(s.sstDir)
	var sstFiles []string
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".sst") {
			sstFiles = append(sstFiles, f.Name())
		}
	}
	// Sort reverse to check newest files first (level0_105.sst before level0_100.sst)
	sort.Sort(sort.Reverse(sort.StringSlice(sstFiles)))

	for _, file := range sstFiles {
		// Open the reader
		fullPath := filepath.Join(s.sstDir, file)
		reader, err := sstable2.OpenSSTable(fullPath)
		if err != nil {
			continue // Skip bad files
		}

		// Search
		val, isTomb, found, err := reader.Get(key)
		_ = reader.Close()

		if err != nil {
			continue
		}

		if found {
			if isTomb {
				return "", false
			}
			return val, true
		}
	}

	return "", false
}
