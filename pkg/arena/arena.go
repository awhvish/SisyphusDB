package arena

import (
	"encoding/binary"
	"errors"
)

const (
	typeVal       = 0x00
	typeTombStone = 0x01
)

type Arena struct {
	data   []byte
	offset int //current write position
}

func NewArena(size int) *Arena {
	return &Arena{
		data:   make([]byte, 0, size),
		offset: 0,
	}
}

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
