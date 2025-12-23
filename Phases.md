# ChronosDB Implementation: From Simple Map to Distributed Storage Engine

This document details the architectural evolution and implementation of ChronosDB,
a distributed, strongly consistent key-value store built in Go. It chronicles the journey
from a naive in-memory prototype to a production-grade Log-Structured Merge-Tree (LSM)
storage engine, optimized for write-heavy workloads and designed with distributed consensus
as a core objective. Key innovations include a custom arena allocator to mitigate Go garbage
collection overhead, a tiered compaction strategy to control read amplification,
and a modular architecture primed for integration with the Raft consensus protocol.
This work serves as a blueprint for understanding the foundational challenges of building
durable, high-performance storage systems.

---

### Phase 1: The In-Memory Prototype
#### 1.1. Core Structure
**The Starting Point:** Implemented a simple `map[string]string` protected by a `sync.RWMutex`.

        type KVStore struct {
            mu       sync.RWMutex
            memTable map[string]string
        }
#### 1.2. Operations (Get & Put)
        // Get retrieves the value for a given key.
        func (s *KVStore) Get(key string) (string, bool) {
            s.mu.RLock()
            defer s.mu.RUnlock()
            val, found := s.memTable[key]
            return val, found
        }
        
        // Put stores a key-value pair.
        func (s *KVStore) Put(key, value string) error {
            s.mu.Lock()
            defer s.mu.Unlock()
            s.memTable[key] = value
            return nil
        }
#### 1.3. The Critical Limitation

* **Performance**: Excellent. O(1) access time for reads and writes.

* **Durability**: None. A process crash, power loss, or restart results in the complete and irreversible loss of all stored data. This is the fundamental problem Phase 2 must solve.

### Phase 2: Ensuring Durability with a Write-Ahead Log (WAL)
To survive crashes, every state change must be recorded on non-volatile storage (disk) before it is applied to the in-memory state. This is the principle of a Write-Ahead Log.

#### 2.1. Core WAL Structures
        //wal.go  
        package wal
        
        import (
            "encoding/binary"
            "os"
            "sync"
        )
        
        // Command represents the type of operation stored in the log.
        type Command byte
        
        const (
            CmdPut Command = 1
            CmdDelete Command = 2
        )
        
        // Entry is the structured record written to disk for every operation.
        type Entry struct {
            CRC       uint32 // CheckSum
            LSN       uint64 //Log Sequence number, tracked by bytes offset
            TimeStamp uint64
            Cmd       Command
            Key       []byte
            Value     []byte
        }
        
        // WAL manages the append-only log file.
        type WAL struct {
            file       *os.File
            mu         sync.Mutex
            currentLSN uint64 // Tracks the latest assigned sequence number
            filePath   string
        }
#### 2.2. The Updated KVStore Structure
        // kvstore.go
        package kv
        
        import (
            "your_project/wal" // Import the WAL package
            "sync"
        )
        
        // KVStore v2: An in-memory store backed by a durable WAL.
        type KVStore struct {
            mu       sync.RWMutex
            memTable map[string]string
            wal      *wal.WAL // Reference to the Write-Ahead Log
        }
* **The Implementation:** Introduced a **Write-Ahead Log (WAL)**. Before modifying the in-memory map, every command (e.g., `PUT key=val`) was appended to a persistent file on disk.
* **The Recovery Mechanism:** On startup, the system read the file from the beginning and "replayed" every command to reconstruct the memory state.
* **The Bottleneck:** **Infinite Growth & Slow Startup**. The log grew indefinitely. Replaying a multi-gigabyte log took excessive time during restarts.

### Phase 3: Memory Optimization (Arena Allocator)
#### 3.1 The Problem: Go Garbage Collection Overhead
n Phase 2, the MemTable stored data directly as map[string]string. While simple, this approach creates significant pressure on Go's garbage collector. Each key-value pair exists as at least two separate heap-allocated string objects. For 1 million entries, that's over 2 million small objects the GC must track, scan, and potentially collect. During periods of high write throughput or major compaction, this can lead to noticeable stop-the-world GC pauses, increasing latency and reducing throughput.
#### 3.2 Arena Allocation: Core structure
An arena allocator consolidates many small allocations into one large, contiguous block of memory. Instead of storing strings directly in the map, we store only integer offsets pointing to locations within this single byte slice.

        // arena/arena.go
        package arena
        
        type Arena struct {
            data   []byte  // The single, large byte slice serving as our arena
            offset int     // Tracks the next free position for writing
        }
        
        // store/memtable.go
        package store
        
        type MemTable struct {
            Index map[string]int  // Key → Offset in Arena (NOT the value itself)
            Arena *arena.Arena    // The contiguous memory block
            size  uint32          // Tracks current data size
        }
#### 3.3 How It Works: The Put Operation
##### When inserting a key-value pair:
* Calculate Entry Size: Determine total bytes needed: header (1 byte) + key length (2 bytes) + value length (4 bytes) + actual key + actual value.

* Check Capacity: Ensure the arena has enough space.

##### Write to Arena:

* Serialize metadata (header, lengths) using binary encoding

* Append key bytes, then value bytes to Arena.data

* Advance the offset pointer

* Update Index: Store only the starting offset (an integer) in the map[string]int, not the actual data.

* **The Critical Change**: The map now stores int offsets (e.g., "user123" → 142), not string pointers. All actual data lives in one contiguous []byte.

                func (a *Arena) Put(key string, val string, isDelete bool) (int, error) {
                    // Header(1) + KeyLen(2) + ValLen(4) + Key + Val
                    entrySize := 1 + 2 + 4 + len(key) + len(val)
                    header := byte(typeVal)
                    if isDelete {
                        entrySize = 1 + 2 + 4 + len(key) //not storing value for deletes
                        header = byte(typeTombStone)
                    }
                    if (a.offset + entrySize) > cap(a.data) {
                        return 0, errors.New("arena is full")
                    }
                    startOffset := a.offset
        
                    var lenBuff [6]byte
                    binary.LittleEndian.PutUint16(lenBuff[0:2], uint16(len(key)))
                    binary.LittleEndian.PutUint32(lenBuff[2:6], uint32(len(val)))
        
                    a.data = append(a.data, header)
                    a.data = append(a.data, lenBuff[:]...)
                    a.data = append(a.data, key...)
        
                    if !isDelete {
                        a.data = append(a.data, val...) // append only when not delete
                    }
                        a.offset += entrySize
                        return startOffset, nil
                }

#### 3.4 How It Works: The Get Operation
##### When retrieving a value by key:

* Lookup Offset: Find the integer offset in the Index map.

##### Navigate Arena:

* Jump to that position in Arena.data

* Read header and length prefixes

* Skip past the key bytes to locate the value bytes

* **Return Copy**: Extract the value bytes and return a copy (to prevent callers from modifying the arena's internal data).

          func (a *Arena) Get(offset int) ([]byte, bool, error) {
          if offset >= len(a.data) {
          return nil, false, errors.New("offset out of range")
          }
          header := a.data[offset]
          isTombStone := (header & typeTombStone) != 0
          if isTombStone {
          return nil, true, nil
          }
          cursor := offset + 1
        
              keyLen := binary.LittleEndian.Uint16(a.data[cursor : cursor+2])
              valLen := binary.LittleEndian.Uint32(a.data[cursor+2 : cursor+6])
        
              cursor += 6           //skip lens
              cursor += int(keyLen) // skip key
        
              val := a.data[cursor : cursor+int(valLen)]
        
              valCopy := make([]byte, valLen)
              copy(valCopy, val) // return a copy to prevent arena modification
        
              return valCopy, false, nil
          }

* **The Implementation:** Replaced direct `string` storage in the Go map with an **Arena Allocator**.
    * **Old Way:** Map stores pointers to millions of small string objects.
    * **New Way:** Map stores `int` offsets pointing to one massive, pre-allocated byte slice (The Arena).
* **The "Why" (GC Overhead):** Go’s Garbage Collector (GC) has to scan every individual object in the heap.
    * **Before:** 1 million keys = 1 million objects to scan (High CPU usage/Latency spikes).
    * **After:** 1 million keys = **1 object** to scan (The Arena slice). This resulted in near-zero GC overhead.

### Phase 4: The LSM Tree Architecture (Per-MemTable WAL)

The critical architectural shift here is moving ownership of the Write-Ahead Log **from the Store (Global) to the MemTable (Local)**. Instead of one giant `database.log` that grows forever, every memory segment ("MemTable") has its own disposable WAL file.

#### 4.1. The Restructure Logic

We replace the single `map` with two distinct pointers: `activeMap` and `frozenMap`.

1.  **Active MemTable:** All new `PUT/DELETE` requests go here. It writes to a specific WAL file (e.g., `wal-100.log`).
2.  **Frozen MemTable:** When the Active table reaches a size threshold (e.g., 64MB), it is moved here. It becomes **Read-Only**.
3.  **The Flush:** A background process takes the Frozen table and saves it to a permanent **SSTable** file.
4.  **The Prune:** **Crucially**, once the SSTable is safe on disk, we **delete `wal-100.log`**.

#### 4.2. How This Solves the Bottlenecks

##### Problem A: Infinite Log Growth
* **Old Way:** One file (`database.log`) appended forever.
* **Phase 4 Solution:** **Log Segmentation**. Because every MemTable creates a *new* WAL file (e.g., `wal-101.log`) upon rotation, the logs are naturally segmented into chunks (e.g., 64MB). We never have a single 100GB file. Once the data is flushed to an SSTable, the corresponding WAL file is deleted, keeping total disk usage efficient.

##### Problem B: Slow Startup (Recovery Time)
* **Old Way:** Replaying millions of lines from a massive log file took minutes or hours.
* **Phase 4 Solution:** **Fast Recovery**. On restart, the system does not replay history from the beginning of time.
    1.  It loads the **SSTables** (which are already durable checkpoints).
    2.  It replays **ONLY** the WAL files that correspond to MemTables that hadn't finished flushing before the crash (usually just the last 1 or 2 files).
    * **Result:** Recovery takes seconds, regardless of database size.

##### 4.3. Implementation Code

```go
package kv

type MemTable struct {
	Index map[string]int
	Arena *arena.Arena
	size uint32
	Wal *wal.WAL
}

type Store struct {
	activeMap *MemTable
	frozenMap *MemTable
	walSeq int64
	flushChan chan struct{}
	mu sync.RWMutex
}
```

* **The Lifecycle:**
    1.  **Active MemTable:** Handles new incoming writes. Has its own dedicated `activeMap.wal`.
    2.  **Frozen MemTable:** When the Active table reaches a size threshold (e.g., 64MB), it becomes "Frozen" (Immutable). It retains its WAL temporarily until flushed.
* **The Benefit:** Decoupled writing from flushing. The system can safely truncate/delete the WAL for specific MemTables once they are persisted to disk, without blocking new writes.

### Phase 5: Persistence (SSTables & Auto-Deletion)

This phase transitions the system from a "RAM-limited" store to a "Disk-unlimited" store. By introducing **Immutable SSTables (Sorted String Tables)**, we decouple durability from the WAL. The WAL becomes a temporary buffer that is discarded (deleted) as soon as data is safely converted into an optimized, read-efficient disk format.

#### 5.1 The Builder (Constructing the SSTable)
The `Builder` is responsible for serializing the in-memory `MemTable` into a structured binary file on disk. It operates in a single pass to ensure high write throughput.

**The Struct:**
```go
type Builder struct {
    File          *os.File           // The SSTable file being written
    index         []IndexEntry       // In-memory buffer for the Sparse Index
    filter        *bloom.BloomFilter // In-memory Bloom Filter accumulator
    currentOffset int64              // Tracks current file write position
    blockStart    int64              // Offset of the start of the current 4KB block
}
```

**Key Functions:**
* **`NewBuilder`**: Creates the file and initializes the Bloom Filter.
* **`Add(key, val, isTombstone)`**: Appends the KV pair to the file. It updates the `BloomFilter` with the key. If enough bytes have been written since the last index entry (e.g., 4KB), it appends a new entry to the `index` slice.
* **`Close()`**: The finalization step. It writes the **Sparse Index** block, the **Bloom Filter** block, and finally a fixed-size **Footer** (16 bytes) containing the offsets of those blocks. This ensures the metadata is durable and locatable.

#### 5.2 The Iterator (Sequential Reader)
The `SSTableIterator` provides a memory-efficient way to read these potentially massive files from start to finish. It is primarily used during **Compaction** (merging old tables) to stream data without loading the entire file into RAM.

**The Struct:**
```go
type SSTableIterator struct {
    file     *os.File
    fileSize int64

    // Current Entry State (The "Head" of the stream)
    Key         string
    Value       []byte
    IsTombstone bool

    // Internal State
    Valid bool  // False if EOF or Error is encountered
    err   error // Stores read errors
}
```

**Key Functions:**
* **`Next()`**: Advances the cursor one entry forward. It decodes the binary format (`Header` -> `Lengths` -> `Key` -> `Value`) byte-by-byte from the disk. If it encounters a deleted key (header byte `1`), it marks `IsTombstone = true`.

#### 5.3 The Reader (Random Access Lookup)
The `Reader` is optimized for finding a specific key quickly. Unlike the Iterator, it keeps lightweight metadata in RAM to minimize disk seeking.

**The Struct:**
```go
type Reader struct {
    file     *os.File           // Open file handle for seeking
    index    []IndexEntry       // The Sparse Index loaded into RAM
    filter   *bloom.BloomFilter // The Bloom Filter loaded into RAM
    filename string
}
```

**Key Functions:**
* **`OpenSSTable`**: The startup routine. It reads the last 16 bytes (Footer) to find the `IndexOffset` and `FilterOffset`. It then jumps to those locations to load the **Sparse Index** and **Bloom Filter** into memory.
* **`Get(key)`**: The retrieval logic.
    1.  **Bloom Filter Check:** Returns immediately if the filter says the key is missing (saves a disk IO).
    2.  **Binary Search:** Uses the in-memory `index` to find the specific 4KB block that *might* contain the key.
    3.  **Disk Seek:** Jumps to the start of that block.
    4.  **Linear Scan:** Reads entries in that block one by one until the key is found or the read key becomes larger than the target (guaranteeing the target doesn't exist).

#### 5.4 The Lifecycle Architecture
The interaction between these components creates the durability loop:
1.  **Freeze:** The Active `MemTable` fills up and becomes immutable.
2.  **Flush:** A background worker creates a `Builder`. It iterates the Frozen MemTable and calls `builder.Add()` for every key.
3.  **Finalize:** `builder.Close()` is called, writing the Index/Filter/Footer and syncing to disk.
4.  **Prune:** The system detects the flush is complete and **deletes the WAL file** associated with that MemTable.
### Phase 6: Read Optimization (Level-Based Compaction)
* **The Bottleneck:** Flushing created hundreds of small files (e.g., `L0_1.sst`, `L0_2.sst`). Reading a key required checking **all** of them (High Read Amplification).
* **The Solution:** Implemented **Level-Based (Tiered) Compaction** (Pyramid Structure).
* **The Algorithm:**
    * **Trigger:** When **Level 0** accumulates **> 4 files**.
    * **Action:** A background process performs a **K-Way Merge** on these 4 files.
    * **Result:** The 4 small files are merged into **one larger file** at **Level 1**, discarding overwritten keys and tombstones.
    * **Benefit:** Keeps the file count low, ensuring read performance remains stable as data grows.

---

### Phase 7: Current Architecture (The Code)

The system has evolved into a robust Storage Engine defined by the following structures:

```go
// The Orchestrator
type Store struct {
    activeMap *MemTable          // Phase 4: Mutable write buffer
    frozenMap *MemTable          // Phase 4: Immutable flush buffer (ready for disk)
    ssTables  []*sstable.Reader  // Phase 5: Persistent storage (SSTables)
    walDir    string             
    sstDir    string             
    walSeq    int64
    flushChan chan struct{}      // Phase 5: Trigger for the flush worker
    mu        sync.RWMutex
}

// The Component
type MemTable struct {
    Index map[string]int         // Phase 3: Sparse Index (Offsets only, no raw data)
    Arena *arena.Arena           // Phase 3: Zero-GC contiguous memory storage
    size  uint32
    Wal   *wal.WAL               // Phase 4: Dedicated recovery log for this specific table
}