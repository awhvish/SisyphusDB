package raft

import (
	"KV-Store/pkg/metrics"
	pb "KV-Store/proto"
	"context"
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
	Index   int
	Term    int
	Command []byte
}

type Raft struct {
	mu        sync.Mutex
	peers     []pb.RaftServiceClient // RPC clients to talk to other nodes
	me        int
	leaderId  int
	applyCh   chan LogEntry
	triggerCh chan struct{}

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

func (rf *Raft) GetLeader() int {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	return rf.leaderId
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

	fmt.Printf("[Start] Leader %d received command at Index %d, Term %d\n", rf.me, index, term)

	//trigger replication
	select {
	case rf.triggerCh <- struct{}{}:
	default:
	}
	return index, term, true
}

// goroutine that pushes data into the KV Store
func (rf *Raft) applier() {
	for {
		time.Sleep(10 * time.Millisecond)

		rf.mu.Lock()
		if rf.commitIndex <= rf.lastApplied {
			rf.mu.Unlock()
			continue
		}

		// Snapshot all ready entries into a local slice
		var entriesToApply []LogEntry
		for rf.lastApplied < rf.commitIndex {
			rf.lastApplied++
			entriesToApply = append(entriesToApply, rf.log[rf.lastApplied])
		}
		rf.mu.Unlock()

		for _, entry := range entriesToApply {
			msg := LogEntry{
				Command: entry.Command,
				Index:   entry.Index,
				Term:    entry.Term,
			}
			// send to kv store
			rf.applyCh <- msg
		}
	}
}

func Make(peers []pb.RaftServiceClient, me int, applyCh chan LogEntry) *Raft {
	rf := &Raft{}
	rf.peers = peers
	rf.me = me
	rf.applyCh = applyCh
	rf.triggerCh = make(chan struct{}, 1)
	rf.state = Follower
	rf.currentTerm = 0
	rf.votedFor = -1
	rf.leaderId = -1
	rf.log = make([]LogEntry, 0)

	rf.log = append(rf.log, LogEntry{Term: 0})

	rf.commitIndex = 0
	rf.lastApplied = 0

	rf.nextIndex = make(map[int]int)
	rf.matchIndex = make(map[int]int)

	rf.readPersist()
	rf.lastResetTime = time.Now()

	go rf.ticker()
	go rf.applier()
	go rf.replicator()
	return rf
}
func (rf *Raft) replicator() {
	for {
		select {
		case <-rf.triggerCh:
			// Instead of sending immediately, we sleep for 10ms.
			// This allows multiple 'Start()' calls to pile up entries in rf.log.
			time.Sleep(30 * time.Millisecond)

			rf.mu.Lock()
			rf.persist()
			rf.mu.Unlock()
			// Now we send ONE RPC containing ALL the new entries
			rf.sendHeartBeats()
		}
	}
}

func (rf *Raft) ticker() {
	for {
		//sleep for a short duration of time
		time.Sleep(100 * time.Millisecond)
		rf.reportMetrics()

		rf.mu.Lock()
		currentState := rf.state
		lastReset := rf.lastResetTime
		rf.mu.Unlock()

		if currentState == Leader {
			rf.sendHeartBeats()
		} else {
			// a randomized timeout after which election starts
			electionTimeout := time.Duration(800+rand.Intn(200)) * time.Millisecond
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
	rf.persist() // Save the new term/vote immediately!

	term := rf.currentTerm
	lastLogIndex := len(rf.log) - 1
	lastLogTerm := rf.log[lastLogIndex].Term
	rf.mu.Unlock()

	votesReceived := 1 // Vote for self
	votesRequired := len(rf.peers)/2 + 1

	fmt.Printf("Node %d starting election for term %d\n", rf.me, term)

	for i := range rf.peers {
		go func(peerIndex int) {
			args := RequestVoteArgs{
				Term:         term,
				CandidateId:  rf.me,
				LastLogTerm:  lastLogTerm,
				LastLogIndex: lastLogIndex,
			}
			reply := RequestVoteReply{}

			if rf.sendRequestVote(peerIndex, &args, &reply) {
				rf.mu.Lock()
				defer rf.mu.Unlock()

				// Discard old replies
				if rf.state != Candidate || rf.currentTerm != term {
					return
				}

				if reply.Term > rf.currentTerm {
					rf.currentTerm = reply.Term
					rf.state = Follower
					rf.votedFor = -1
					rf.leaderId = -1
					rf.persist()
					return
				}

				if reply.VoteGranted {
					votesReceived++
					if votesReceived == votesRequired {
						fmt.Printf("Node %d won election and will be leader for the term: %d\n", rf.me, reply.Term)
						rf.state = Leader
						rf.leaderId = rf.me
						for p := range rf.peers {
							rf.nextIndex[p] = len(rf.log)
							rf.matchIndex[p] = 0
						}
						go rf.sendHeartBeats()
					}
				}
			}
		}(i)
	}
}

func (rf *Raft) sendRequestVote(server int, args *RequestVoteArgs, reply *RequestVoteReply) bool {
	if rf.peers[server] == nil {
		return false
	}
	pbArgs := &pb.RequestVoteRequest{
		Term:         int32(args.Term),
		CandidateId:  int32(args.CandidateId),
		LastLogIndex: int32(args.LastLogIndex),
		LastLogTerm:  int32(args.LastLogTerm),
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*100)
	defer cancel()

	pbReply, err := rf.peers[server].RequestVote(ctx, pbArgs)
	if err != nil {
		fmt.Printf(" [ERROR] Node %d failed to request vote from Node %d: %v\n", rf.me, server, err)
		return false
	}
	reply.Term = int(pbReply.Term)
	reply.VoteGranted = pbReply.VoteGranted
	if !reply.VoteGranted {
		fmt.Printf(" [DENIED] Node %d refused vote to Node %d. (My Term: %d, Peer Term: %d)\n", server, rf.me, args.Term, reply.Term)
	} else {
		fmt.Printf(" [GRANTED] Node %d voted for Node %d!\n", server, rf.me)
	}
	return true
}

func (rf *Raft) reportMetrics() {
	id := fmt.Sprintf("%d", rf.me)

	metrics.TermGauge.WithLabelValues(id).Set(float64(rf.currentTerm))
	metrics.CommitIndexGauge.WithLabelValues(id).Set(float64(rf.commitIndex))
	metrics.StateGauge.WithLabelValues(id).Set(float64(rf.state))
	metrics.LeaderGauge.WithLabelValues(id).Set(float64(rf.leaderId))
}
