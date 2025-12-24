package raft

import (
	"fmt"
	"math/rand"
	"sync"
	"time"
)

type State int

const (
	Follower State = iota
	Candidate
	Leader
)

type LogEntry struct {
	index   int
	term    int
	Command []byte
}

type Raft struct {
	mu      sync.Mutex
	peers   []interface{} // RPC clients to talk to other nodes
	me      int           // this peer's index into peers[]
	applyCh chan LogEntry // Channel to send committed data to the KV Store

	//persistent states
	currentTerm int
	votedFor    int
	log         []LogEntry

	//volatile state on all servers
	commitIndex int // index of highest log entry known to be committed
	lastApplied int // index of highest log entry applied to state machine

	//volatile state on leaders
	nextIndex  map[int]int
	matchIndex map[int]int

	state         State
	lastResetTime time.Time //last time we heard from a leader
}

func (rf *Raft) getState() (int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	return rf.currentTerm, rf.state == Leader
}

func (rf *Raft) Start(command interface{}) (int, int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	if rf.state != Leader {
		return -1, rf.currentTerm, false
	}

	//create log entry
	index := len(rf.log)
	term := rf.currentTerm
	// TODO: Use type assertion or serialization later here
	cmdBytes, ok := command.([]byte)
	if !ok {
		return -1, rf.currentTerm, false
	}
	rf.log = append(rf.log, LogEntry{index, term, cmdBytes})

	// TODO: Persistance
	// rf.persist() - saves to disk

	//trigger replication
	//TODO: Optimization
	go rf.sendHeartBeats()
	return index, term, true
}

// goroutine that pushes data into the KV Store
func (rf *Raft) applier() {
	for {
		time.Sleep(10 * time.Millisecond)
		rf.mu.Lock()

		if rf.commitIndex > rf.lastApplied {
			rf.lastApplied++
			entry := rf.log[rf.lastApplied]

			msg := LogEntry{
				Command: entry.Command,
				index:   entry.index,
				term:    entry.term,
			}
			rf.mu.Unlock()
			// send to kv store
			rf.applyCh <- msg
		} else {
			rf.mu.Unlock()
		}
	}
}

func Make(peers []interface{}, me int, applyCh chan LogEntry) *Raft {
	rf := &Raft{}
	rf.peers = peers
	rf.me = me
	rf.applyCh = applyCh

	rf.state = Follower
	rf.currentTerm = 0
	rf.votedFor = -1
	rf.log = make([]LogEntry, 0)

	rf.log = append(rf.log, LogEntry{term: 0})

	rf.commitIndex = 0
	rf.lastApplied = 0

	rf.nextIndex = make(map[int]int)
	rf.matchIndex = make(map[int]int)

	rf.lastResetTime = time.Now()

	go rf.ticker()
	return rf
}

func (rf *Raft) ticker() {
	for {
		//sleep for a short duration of time
		time.Sleep(10 * time.Millisecond)

		rf.mu.Lock()
		currentState := rf.state
		lastReset := rf.lastResetTime
		rf.mu.Unlock()

		if currentState == Leader {
			rf.sendHeartBeats()
		} else {
			// a randomized timeout after which election starts
			electionTimeout := time.Duration(300+rand.Intn(300)) * time.Millisecond
			if time.Since(lastReset) > electionTimeout {
				rf.startElection()
			}
		}
	}
}
func (rf *Raft) startElection() {
	rf.mu.Lock()
	rf.state = Candidate
	rf.currentTerm++
	rf.votedFor = rf.me
	rf.lastResetTime = time.Now()

	term := rf.currentTerm
	lastLogIndex := len(rf.log) - 1
	lastLogTerm := rf.log[lastLogIndex].term
	rf.mu.Unlock()

	votesReceived := 1 //self
	votesRequired := len(rf.peers)/2 + 1

	fmt.Printf("Node %d starting election timer for term %d", rf.me, term)
	for i := range rf.peers {
		if i == rf.me {
			continue
		}
		for i := range rf.peers {
			go func(peerIndex int) {
				args := RequestVoteArgs{
					Term:         term,
					CandidateId:  rf.me,
					LastLogTerm:  lastLogTerm,
					LastLogIndex: lastLogIndex,
				}
				reply := RequestVoteReply{}
				if rf.state != Candidate || rf.currentTerm != term {
					return
				}
				// current term is outdated
				if reply.Term > rf.currentTerm {
					rf.currentTerm = reply.Term
					rf.state = Follower
					rf.votedFor = -1
					return
				}
				if rf.sendRequestVote(peerIndex, &args, &reply) {
					rf.mu.Lock()
					defer rf.mu.Unlock()
					if reply.VoteGranted {
						votesReceived++
						if votesReceived >= votesRequired {
							fmt.Printf("Node %d won election and will be leader for the term: %d", rf.me, reply.Term)
							rf.state = Leader
							for p := range rf.peers {
								rf.nextIndex[p] = len(rf.log) // Optimistically assume they match
								rf.matchIndex[p] = 0
							}
							go rf.sendHeartBeats()
						}
					}
				}
			}(i)
		}

	}
}

func (rf *Raft) sendRequestVote(server int, args *RequestVoteArgs, reply *RequestVoteReply) bool {
	return false
}
