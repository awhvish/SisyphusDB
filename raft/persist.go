package raft

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"os"
)

func (rf *Raft) persist() {
	rf.reportMetrics()
	w := new(bytes.Buffer)
	e := gob.NewEncoder(w)

	_ = e.Encode(rf.currentTerm)
	_ = e.Encode(rf.votedFor)
	_ = e.Encode(rf.log)

	data := w.Bytes()

	filename := fmt.Sprintf("raft_state_%d.gob", rf.me)

	err := os.WriteFile(filename, data, 0644)
	if err != nil {
		fmt.Printf("raft persist err node %d: %s", rf.me, err)
	}
}

func (rf *Raft) readPersist() {
	filename := fmt.Sprintf("raft_state_%d.gob", rf.me)

	data, err := os.ReadFile(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		fmt.Printf("raft readPersist err node %d: %s", rf.me, err)
		return
	}
	r := bytes.NewBuffer(data)
	d := gob.NewDecoder(r)

	var currentTerm, votedFor int
	var logs []LogEntry

	if d.Decode(&currentTerm) != nil ||
		d.Decode(&votedFor) != nil ||
		d.Decode(&logs) != nil {
		fmt.Printf("Error decoding state for node %d\n", rf.me)
	} else {
		rf.currentTerm = currentTerm
		rf.votedFor = votedFor
		rf.log = logs
	}
}
