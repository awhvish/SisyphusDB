package sstable

import (
	"KV-Store/pkg/bloom"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
)

type Reader struct {
	file     *os.File
	index    []IndexEntry // Sparse Index
	filter   *bloom.BloomFilter
	filename string
}

func OpenSSTable(filename string) (*Reader, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}

	// Read Footer
	stat, _ := f.Stat()
	if stat.Size() < 16 { // Changed from 8 to 16
		return nil, errors.New("invalid ssTable: too small")
	}

	// Seek to End - 16 bytes
	_, err = f.Seek(-16, io.SeekEnd)
	if err != nil {
		return nil, err
	}

	var footer [16]byte
	if _, err := f.Read(footer[:]); err != nil {
		return nil, err
	}

	indexOffset := int64(binary.LittleEndian.Uint64(footer[:]))
	filterOffset := int64(binary.LittleEndian.Uint64(footer[8:16])) //bloom filter
	// Jump to Index and Read it
	if _, err := f.Seek(indexOffset, io.SeekStart); err != nil {
		return nil, err
	}

	indexSize := filterOffset - indexOffset
	indexBytes := make([]byte, indexSize)
	if _, err := f.Read(indexBytes); err != nil {
		return nil, err
	}

	//  Parse Bytes -> []IndexEntry Slice
	var index []IndexEntry
	buf := bytes.NewReader(indexBytes)

	// IndexEntry format:
	//Size = Header(1) + KeyLen(2) + ValLen(4) + KeyBytes + ValBytes
	for buf.Len() > 0 {
		var kLen uint16
		if err := binary.Read(buf, binary.LittleEndian, &kLen); err != nil {
			break
		}

		keyBytes := make([]byte, kLen)
		if _, err := buf.Read(keyBytes); err != nil {
			break
		}

		// Read Offset (8 bytes)
		var offset int64
		if err := binary.Read(buf, binary.LittleEndian, &offset); err != nil {
			break
		}

		index = append(index, IndexEntry{
			key:    string(keyBytes),
			Offset: offset,
		})
	}
	filterSize := stat.Size() - filterOffset - 16
	filterBytes := make([]byte, filterSize)

	// Seek to Filter Start
	if _, err := f.Seek(filterOffset, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := f.Read(filterBytes); err != nil {
		return nil, err
	}
	// Load Filter
	bf := bloom.Load(filterBytes)

	return &Reader{
		file:     f,
		index:    index,
		filename: filename,
		filter:   bf,
	}, nil
}

func (r *Reader) Get(targetKey string) (string, bool, bool, error) {
	//bloom filter check
	if !r.filter.MaybeContains([]byte(targetKey)) {
		fmt.Printf(" [Bloom Filter] Blocked key '%s' (Saved disk seek!)\n", targetKey)
		return "", false, false, nil
	}
	// BINARY SEARCH IN RAM - []IndexEntries
	idx := sort.Search(len(r.index), func(i int) bool {
		return r.index[i].key > targetKey
	})

	if idx == 0 {
		return "", false, false, nil
	}

	// The target is in the previous block
	blockStart := r.index[idx-1].Offset

	// DISK SEEK & SCAN
	if _, err := r.file.Seek(blockStart, io.SeekStart); err != nil {
		return "", false, false, err
	}

	// LINEAR SCAN OF THE BLOCK
	for {
		var header [1]byte
		if _, err := r.file.Read(header[:]); err != nil {
			if err == io.EOF {
				break
			}
			return "", false, false, err
		}
		isTombstone := header[0] == 1

		// 2. Read Lengths (Key=2, Val=4)
		var lenBuf [6]byte
		if _, err := r.file.Read(lenBuf[:]); err != nil {
			return "", false, false, err
		}

		kLen := binary.LittleEndian.Uint16(lenBuf[0:2])
		vLen := binary.LittleEndian.Uint32(lenBuf[2:6])

		// 3. Read Key
		keyBytes := make([]byte, kLen)
		if _, err := r.file.Read(keyBytes); err != nil {
			return "", false, false, err
		}
		currentKey := string(keyBytes)

		// 4. CHECK MATCH
		if currentKey == targetKey {
			if isTombstone {
				return "", true, true, nil // Found, but deleted
			}
			// Read Value
			valBytes := make([]byte, vLen)
			if _, err := r.file.Read(valBytes); err != nil {
				return "", false, false, err
			}

			return string(valBytes), false, true, nil
		}

		// OPTIMIZATION: Sorted file!
		// Sorted Array: currKey > target means key does not exist in block
		if currentKey > targetKey {
			return "", false, false, nil
		}

		// 5. Skip Value (Jump to next entry)
		// If match failed, skip over the value bytes to get to next header
		if !isTombstone {
			if _, err := r.file.Seek(int64(vLen), io.SeekCurrent); err != nil {
				return "", false, false, err
			}
		}
	}

	return "", false, false, nil // Not found in this block
}

func (r *Reader) Close() error {
	return r.file.Close()
}
