package bloom

import "github.com/spaolacci/murmur3"

type BloomFilter struct {
	bitset []byte
	k      uint //no of hash
	m      uint // size of bitset array
}

func New(n int) *BloomFilter {
	if n < 1 {
		n = 1
	}
	// 1. Calculate needed bits (10 bits per key)
	neededBits := uint(n * 10)

	// 2. Round up to nearest byte (8 bits)
	// This ensures m is always a multiple of 8
	bytesNeeded := (neededBits + 7) / 8
	m := bytesNeeded * 8

	k := uint(7)

	return &BloomFilter{
		bitset: make([]byte, bytesNeeded),
		k:      k,
		m:      m,
	}
}

func (bf *BloomFilter) Add(key []byte) {
	h64 := murmur3.Sum64(key)
	h1 := uint32(h64)
	h2 := uint32(h64 >> 32)
	for i := uint(0); i < bf.k; i++ {
		// Formula: (h1 + i*h2) % m
		combinedHash := h1 + (uint32(i) * h2)
		bitPos := uint(combinedHash) % bf.m

		// Set the bit at bitPos to 1
		byteIndex := bitPos / 8
		bitIndex := bitPos % 8
		bf.bitset[byteIndex] |= 1 << bitIndex
	}
}

func (bf *BloomFilter) MaybeContains(key []byte) bool {
	h64 := murmur3.Sum64(key)
	h1 := uint32(h64)
	h2 := uint32(h64 >> 32)

	for i := uint(0); i < bf.k; i++ {
		combinedHash := h1 + (uint32(i) * h2)
		bitPos := uint(combinedHash) % bf.m

		byteIndex := bitPos / 8
		bitIndex := bitPos % 8

		// If ANY bit is 0, the key is definitely NOT here
		if (bf.bitset[byteIndex] & (1 << bitIndex)) == 0 {
			return false
		}
	}
	return true
}

func (bf *BloomFilter) Bytes() []byte {
	return bf.bitset
}

func Load(data []byte) *BloomFilter {
	return &BloomFilter{
		bitset: data,
		k:      7, // Must match the writer's k
		m:      uint(len(data) * 8),
	}
}
