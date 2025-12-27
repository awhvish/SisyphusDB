package kv

import (
	"KV-Store/pkg/arena"
	"KV-Store/pkg/wal"
	pb "KV-Store/proto"
	"KV-Store/raft"
	"KV-Store/sstable"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type MemTable struct {
	Index map[string]int
	Arena *arena.Arena
	size  uint32
	Wal   *wal.WAL
}

type Store struct {
	activeMap *MemTable
	frozenMap *MemTable
	ssTables  []*sstable.Reader
	walDir    string
	sstDir    string
	walSeq    int64
	flushChan chan struct{} // FrozenMem -> Active Mem
	me        int           // same as raft.me, for prometheus metrics
	// Raft Channels
	Raft        *raft.Raft
	notifyChans map[int]chan OpResult // return client -> success
	applyCh     chan raft.LogEntry    // applied cmds -> internal storage
	mu          sync.RWMutex
}

func NewKVStore(peers []pb.RaftServiceClient, me int) (*Store, error) {
	walDir := fmt.Sprintf("Storage/wal/wal_%d", me)
	sstDir := fmt.Sprintf("Storage/data/data_%d", me)

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
	applyCh := make(chan raft.LogEntry)
	store := &Store{
		activeMap: NewMemTable(mapLimit, currentWal),
		frozenMap: nil,
		walDir:    walDir,
		sstDir:    sstDir,
		flushChan: make(chan struct{}, 1),
		applyCh:   applyCh,
		me:        me,
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
	store.refreshSSTables()
	store.Raft = raft.Make(peers, me, applyCh)
	go store.readAppliedLogs()
	go store.FlushWorker()
	return store, nil
}

// Loop that pulls data from Raft and writes to Store
func (s *Store) readAppliedLogs() {
	for msg := range s.applyCh {
		// Deserialize
		fmt.Printf("[Apply] Store applying Index %d. Waiting clients: %d\n", msg.Index, len(s.notifyChans))
		var cmd raftCmd
		if err := json.Unmarshal(msg.Command, &cmd); err != nil {
			fmt.Printf("Error unmarshalling log: %v\n", err)
			continue
		}

		var err error
		if cmd.Op == CmdPut {
			err = s.applyInternal(cmd.Key, cmd.Value, false)
		} else if cmd.Op == CmdDelete {
			err = s.applyInternal(cmd.Key, "", true)
		}

		s.mu.Lock()
		// We check if any client is waiting for this specific log index
		if ch, ok := s.notifyChans[msg.Index]; ok {
			fmt.Printf("[Notify] Notifying client for Index %d\n", msg.Index)
			ch <- OpResult{
				Value: cmd.Value,
				Err:   err,
			}
			delete(s.notifyChans, msg.Index)
		}
		s.mu.Unlock()
	}
}

// Put in storage
func (s *Store) applyInternal(key string, val string, isDelete bool) error {
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

func (s *Store) Put(key string, val string, isDelete bool) error {
	op := CmdPut
	if isDelete {
		op = CmdDelete
	}
	cmd := raftCmd{Op: op, Key: key, Value: val}
	cmdBytes, _ := json.Marshal(cmd)

	index, _, isLeader := s.Raft.Start(cmdBytes)
	if !isLeader {
		return fmt.Errorf("not leader")
	}
	//create notify channel
	s.mu.Lock()
	if s.notifyChans == nil {
		s.notifyChans = make(map[int]chan OpResult)
	}
	ch := make(chan OpResult, 1)
	s.notifyChans[index] = ch
	s.mu.Unlock()

	// wait for consensus to replicate
	select {
	case res := <-ch:
		return res.Err
	case <-time.After(2 * time.Second):
		s.mu.Lock()
		delete(s.notifyChans, index)
		s.mu.Unlock()
		return fmt.Errorf("timeout waiting for consensus")
	}
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
		reader, err := sstable.OpenSSTable(fullPath)
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
