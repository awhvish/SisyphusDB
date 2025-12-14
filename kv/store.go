package kv

import (
	"KV-Store/arena"
	"KV-Store/wal"
	"errors"
	"sync"
)

const mapLimit = 10 * 1024 // 10KB for now

type MemTable struct {
	Index map[string]int
	Arena *arena.Arena
	size  uint32
}

func NewMemTable(size int) *MemTable {
	return &MemTable{
		Index: make(map[string]int),
		Arena: arena.NewArena(size),
	}
}

type Store struct {
	activeMap *MemTable
	frozenMap *MemTable
	flushChan chan struct{}
	mu        sync.RWMutex
	Wal       *wal.WAL
}

func NewKVStore(filename string) (*Store, error) {

	walLog, entries, err := wal.OpenWAL(filename)
	if err != nil {
		return nil, err
	}

	store := &Store{
		activeMap: NewMemTable(mapLimit),
		frozenMap: nil,
		Wal:       walLog,
		flushChan: make(chan struct{}, 1),
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

func (s *Store) FlushWorker() {

}

func (s *Store) RotateTable() {
	s.frozenMap = s.activeMap
	s.activeMap = NewMemTable(mapLimit)

	select {
	case s.flushChan <- struct{}{}:
	}
}

func (s *Store) Put(key string, val string, isDelete bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	//  size: Header(1) + KeyLen(2) + ValLen(4) + Key + Val
	entrySize := 1 + 2 + 4 + len(key) + len(val)
	if int(s.activeMap.size)+entrySize > mapLimit {
		s.RotateTable()
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
	defer s.mu.RUnlock()

	if val, isTomb, found := checkTable(s.activeMap, key); found {
		if isTomb {
			return "", false
		}
		return val, true
	}
	// check frozen table
	if val, isTomb, found := checkTable(s.frozenMap, key); found {
		if isTomb {
			return "", false
		}
		return val, true
	}

	// TODO: Check in SSTable
	return "", false
}
