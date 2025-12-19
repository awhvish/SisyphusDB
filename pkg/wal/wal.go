package wal

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type WAL struct {
	file       *os.File
	path       string
	mu         sync.Mutex
	currentLSN uint64
}
type Command byte

const (
	CmdPut    Command = 1
	CmdDelete Command = 2
)

const headerSize = 29

// Entry CRC(4) + LSN(8) + TimeStamp(8) + Cmd(1) + Key(4) + Val(4)
type Entry struct {
	CRC       uint32
	LSN       uint64
	TimeStamp uint64
	Cmd       Command
	Key       []byte
	Value     []byte
}

func FindActiveFile(dirPath string) (string, int64, error) {
	files, err := os.ReadDir(dirPath)
	if err != nil {
		return "", 0, err
	}
	var walFiles []string
	for _, file := range files {
		fileName := file.Name()
		if strings.HasPrefix(fileName, "wal-") && strings.HasSuffix(fileName, ".log") {
			walFiles = append(walFiles, fileName)
		}
	}
	sort.Slice(walFiles, func(i, j int) bool {
		return walFiles[i] < walFiles[j]
	})
	var activeFileName string
	var activeSegmentID int64

	if len(walFiles) == 0 {
		activeSegmentID = 0
		activeFileName = filepath.Join(dirPath, fmt.Sprintf("wal-%05d.log", activeSegmentID))

	} else {
		lastFile := walFiles[len(walFiles)-1]
		activeFileName = filepath.Join(dirPath, lastFile)
		_, err = fmt.Sscanf(lastFile, "wal-%05d.log", &activeSegmentID)
		if err != nil {
			return "", 0, err
		}
	}
	return activeFileName, activeSegmentID, nil
}

func OpenWAL(dir string, id int64) (*WAL, error) {
	filename := fmt.Sprintf("wal-%05d.log", id)
	path := filepath.Join(dir, filename)

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, err
	}
	stat, _ := f.Stat()
	return &WAL{
		file:       f,
		path:       path,
		currentLSN: uint64(stat.Size()),
	}, nil
}
func (w *WAL) Close() error {
	return w.file.Close()
}
func (w *WAL) Remove() error {
	// Attempt to close just in case, ignore error if already closed
	_ = w.file.Close()
	return os.Remove(w.path)
}

func (w *WAL) Write(key string, val string, cmd Command) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	keySize := len(key)
	valSize := len(val)

	totalSize := int64(headerSize + keySize + valSize)

	buf := make([]byte, totalSize)
	timestamp := uint64(time.Now().UnixNano())

	binary.LittleEndian.PutUint64(buf[4:12], w.currentLSN)
	binary.LittleEndian.PutUint64(buf[12:20], timestamp)
	buf[20] = byte(cmd)
	binary.LittleEndian.PutUint32(buf[21:25], uint32(keySize))
	binary.LittleEndian.PutUint32(buf[25:29], uint32(valSize))

	copy(buf[29:], []byte(key))
	copy(buf[29+keySize:], []byte(val))

	crc := crc32.ChecksumIEEE(buf[4:])
	binary.LittleEndian.PutUint32(buf[0:4], crc)

	if _, err := w.file.Write(buf); err != nil {
		return err
	}
	// Do not sync for every entry for larger applications
	if err := w.file.Sync(); err != nil {
		return err
	}

	nextLSN := w.currentLSN + uint64(totalSize)
	w.currentLSN = nextLSN

	return nil
}

func (w *WAL) Recover() ([]Entry, error) {
	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	var entries []Entry

	header := make([]byte, headerSize)

	for {
		_, err := io.ReadFull(w.file, header)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		crc := binary.LittleEndian.Uint32(header[0:4])
		lsn := binary.LittleEndian.Uint64(header[4:12])
		timestamp := binary.LittleEndian.Uint64(header[12:20])
		cmd := Command(header[20])
		keySize := int(binary.LittleEndian.Uint32(header[21:25]))
		valSize := int(binary.LittleEndian.Uint32(header[25:29]))

		data := make([]byte, valSize+keySize)

		if _, err := io.ReadFull(w.file, data); err != nil {
			return nil, io.ErrUnexpectedEOF
		}
		digest := crc32.NewIEEE()

		_, err2 := digest.Write(header[4:])
		if err2 != nil {
			return nil, err2
		}
		_, err3 := digest.Write(data)
		if err3 != nil {
			return nil, err3
		}

		if digest.Sum32() != crc {
			return nil, fmt.Errorf("checksum mismatch for key/val at LSN: %d", lsn)
		}

		entries = append(entries, Entry{
			CRC:       crc,
			LSN:       lsn,
			TimeStamp: timestamp,
			Cmd:       cmd,
			Key:       data[:keySize],
			Value:     data[keySize:],
		})

		w.currentLSN = lsn + headerSize + uint64(keySize) + uint64(valSize)
	}
	return entries, nil
}
