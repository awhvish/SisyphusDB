package sstable

import (
	"KV-Store/pkg/bloom"
	"encoding/binary"
	"os"
)

const blockSize = 4 * 1024 // 4KB

// table of contents
type IndexEntry struct {
	key    string
	Offset int64
}

type Builder struct {
	File          *os.File
	index         []IndexEntry
	filter        *bloom.BloomFilter
	currentOffset int64
	blockStart    int64
}

func NewBuilder(filename string, keyCount int) (*Builder, error) {
	f, err := os.Create(filename)
	if err != nil {
		return nil, err
	}
	bf := bloom.New(keyCount)

	return &Builder{
		File:          f,
		filter:        bf,
		currentOffset: 0,
		blockStart:    0,
	}, nil
}

func (b *Builder) Add(key []byte, val []byte, isTombstone bool) error {
	//Sparse Index: logic
	b.filter.Add(key)
	if b.currentOffset == 0 || (b.currentOffset-b.blockStart) > blockSize {
		b.index = append(b.index, IndexEntry{
			key:    string(key),
			Offset: b.currentOffset,
		})
		b.blockStart = b.currentOffset
	}
	header := byte(0)
	if isTombstone {
		header = 1
	}
	if _, err := b.File.Write([]byte{header}); err != nil {
		return err
	}
	var lenBuf [6]byte
	binary.LittleEndian.PutUint16(lenBuf[0:2], uint16(len(key)))
	valLen := len(val)
	if isTombstone {
		valLen = 0 // Tombstones have no val
	}
	binary.LittleEndian.PutUint32(lenBuf[2:6], uint32(valLen))
	if _, err := b.File.Write(lenBuf[:]); err != nil {
		return err
	}
	if _, err := b.File.Write(key); err != nil {
		return err
	}
	if !isTombstone {
		if _, err := b.File.Write(val); err != nil {
			return err
		}
	}
	// Size = Header(1) + KeyLen(2) + ValLen(4) + KeyBytes + ValBytes
	entrySize := int64(1 + 2 + 4 + len(key) + valLen)
	b.currentOffset += entrySize

	return nil
}

func (b *Builder) Close() error {
	indexStartOffset := b.currentOffset // end of the last block is the start of the index entries
	// Format for each entry: [KeyLen(2)][KeyBytes][Offset(8)]
	var buf [10]byte // Reusable buffer for lengths and offset
	for _, entry := range b.index {
		binary.LittleEndian.PutUint16(buf[0:2], uint16(len(entry.key)))
		if _, err := b.File.Write(buf[0:2]); err != nil {
			return err
		}

		if _, err := b.File.WriteString(entry.key); err != nil {
			return err
		}

		binary.LittleEndian.PutUint64(buf[2:10], uint64(entry.Offset))
		if _, err := b.File.Write(buf[2:10]); err != nil {
			return err
		}
		entrySize := int64(2 + len(entry.key) + 8)
		b.currentOffset += entrySize
	}
	filterStartOffset := b.currentOffset // This is where the filter begins
	filterBytes := b.filter.Bytes()
	if _, err := b.File.Write(filterBytes); err != nil {
		return err
	}
	b.currentOffset += int64(len(filterBytes))

	//Write the footer
	// [Index Offset (8 bytes)] + [Filter Offset (8 bytes)]
	var footer [16]byte
	binary.LittleEndian.PutUint64(footer[0:8], uint64(indexStartOffset))
	binary.LittleEndian.PutUint64(footer[8:16], uint64(filterStartOffset))

	if _, err := b.File.Write(footer[:]); err != nil {
		return err
	}

	if err := b.File.Sync(); err != nil {
		return err
	}
	return b.File.Close()
}
