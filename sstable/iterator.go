package sstable

import (
	"encoding/binary"
	"io"
	"os"
)

// SSTableIterator reads an SSTable sequentially
type SSTableIterator struct {
	file     *os.File
	fileSize int64

	// Current Entry State (The "Head" of the stream)
	Key         string
	Value       []byte
	IsTombstone bool

	// Internal State
	Valid bool // (false = EOF or Error)
	err   error
}

// NewIterator opens a file and prepares to read the first key
func NewIterator(filename string) (*SSTableIterator, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	stat, _ := f.Stat()

	it := &SSTableIterator{
		file:     f,
		fileSize: stat.Size(),
		Valid:    true,
	}
	// Prime the first key immediately so 'it.Key' is ready to use
	it.Next()
	return it, nil
}

// Next reads the next entry in the file
func (it *SSTableIterator) Next() {
	if !it.Valid {
		return
	}

	// Read Header (1 byte)
	var header [1]byte
	if _, err := it.file.Read(header[:]); err != nil {
		it.Valid = false
		if err != io.EOF {
			it.err = err
		}
		return
	}
	it.IsTombstone = header[0] == 1

	// read lengths (KeyLen=2, ValLen=4)
	var lenBuf [6]byte
	if _, err := it.file.Read(lenBuf[:]); err != nil {
		it.Valid = false
		return
	}
	kLen := binary.LittleEndian.Uint16(lenBuf[0:2])
	vLen := binary.LittleEndian.Uint32(lenBuf[2:6])

	// Read Key
	keyBytes := make([]byte, kLen)
	if _, err := it.file.Read(keyBytes); err != nil {
		it.Valid = false
		return
	}
	it.Key = string(keyBytes)

	// 4. read value
	if it.IsTombstone {
		it.Value = nil
	} else {
		valBytes := make([]byte, vLen)
		if _, err := it.file.Read(valBytes); err != nil {
			it.Valid = false
			return
		}
		it.Value = valBytes
	}
}

func (it *SSTableIterator) Close() {
	_ = it.file.Close()
}
