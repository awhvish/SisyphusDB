package kv

import (
	"KV-Store/sstable"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxLevelFiles = 4 //Pyramid width

func (s *Store) getFilesForLevel(level int) []string {
	files, _ := os.ReadDir(s.sstDir)
	var matchedFiles []string

	prefix := fmt.Sprintf("L%d_", level)

	for _, f := range files {
		if strings.HasPrefix(f.Name(), prefix) && strings.HasSuffix(f.Name(), ".sst") {
			matchedFiles = append(matchedFiles, f.Name())
		}
	}

	// Sort Reverse (Newest First) for correctly resolving key conflicts
	sort.Sort(sort.Reverse(sort.StringSlice(matchedFiles)))

	return matchedFiles
}

func (s *Store) CheckAndCompact(level int) error {
	s.mu.Lock()
	levelFiles := s.getFilesForLevel(level)
	//if we have fewer than 4 files, do nothing
	if len(levelFiles) < maxLevelFiles {
		s.mu.Unlock()
		return nil
	}
	filesToCompact := levelFiles
	nextLevel := level + 1
	fmt.Printf("[Compaction] Merging %d files from L%d to L%d...\n", len(filesToCompact), level, nextLevel)
	s.mu.Unlock()
	var iterators []*sstable.SSTableIterator
	for _, filename := range filesToCompact {
		it, err := sstable.NewIterator(filepath.Join(s.sstDir, filename))
		if err != nil {
			// If error, cleanup opened ones
			for _, openIt := range iterators {
				openIt.Close()
			}
			return err
		}
		if it.Valid {
			iterators = append(iterators, it)
		}
	}
	// create output builder (Level + 1)
	newFilename := fmt.Sprintf("%s/L%d_%d.sst", s.sstDir, nextLevel, time.Now().UnixNano())
	builder, err := sstable.NewBuilder(newFilename, 10000)
	if err != nil {
		return err
	}
	// K-Way merge loop - similar to merge K sorted lists in Leetcode (just we do not use heap here)
	for {
		var minKey string
		first := true
		activeCount := 0
		// Find min key
		for _, it := range iterators {
			if it.Valid {
				if first || it.Key < minKey {
					minKey = it.Key
					first = false
				}
				activeCount++
			}
		}
		if activeCount == 0 {
			break
		}
		// Resolve collisions (Pick newest) since we sorted file list by timestamp in getFilesForLevel, we just need to iterate in that order
		var winnerVal []byte
		var winnerTomb bool
		foundWinner := false
		for _, it := range iterators {
			if it.Valid && it.Key == minKey {
				// If this is the FIRST (newest) match, capture its data
				if !foundWinner {
					winnerVal = it.Value
					winnerTomb = it.IsTombstone
					foundWinner = true
				}
				// NOW it is safe to advance
				it.Next()
			}
		}
		if !foundWinner {
			continue
		} // Should never happen
		if winnerTomb {
			continue // Correctly skip the tombstone
		}
		if err := builder.Add([]byte(minKey), winnerVal, false); err != nil {
			return err
		}
	}
	_ = builder.Close()
	for _, it := range iterators {
		it.Close()
	}
	// for cleanup of old sst files
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, f := range filesToCompact {
		_ = os.Remove(filepath.Join(s.sstDir, f))
	}
	s.refreshSSTables()
	go func() {
		_ = s.CheckAndCompact(nextLevel)
	}()
	return nil
}

func (s *Store) refreshSSTables() {
	// Close old readers
	for _, r := range s.ssTables {
		_ = r.Close()
	}

	// Scan all levels
	files, _ := os.ReadDir(s.sstDir)
	var sstFiles []string
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".sst") {
			sstFiles = append(sstFiles, filepath.Join(s.sstDir, f.Name()))
		}
	}

	// Global Sort by Timestamp DESCENDING.
	// Since our naming is L{lvl}_{timestamp}, sorting by file name descending might mix levels
	// but typically newer timestamps (higher numbers) appear first, which is what we want.
	sort.Sort(sort.Reverse(sort.StringSlice(sstFiles)))

	var readers []*sstable.Reader
	for _, f := range sstFiles {
		r, err := sstable.OpenSSTable(f)
		if err == nil {
			readers = append(readers, r)
		}
	}
	s.ssTables = readers
}
