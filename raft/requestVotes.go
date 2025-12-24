package raft

import "time"

type RequestVoteArgs struct {
	Term         int
	CandidateId  int
	LastLogIndex int
	LastLogTerm  int
}

type RequestVoteReply struct {
	Term        int
	VoteGranted bool
}

func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// if candidate term smaller than current term reject immediately
	if args.Term < rf.currentTerm {
		reply.Term = rf.currentTerm
		reply.VoteGranted = false
		return
	}
	//We can become a follower
	if args.Term > rf.currentTerm {
		rf.currentTerm = args.Term
		rf.state = Follower
		rf.votedFor = -1
	}
	lastLogIndex := len(rf.log) - 1
	LastLogTerm := rf.log[lastLogIndex].term

	logOk := false
	//if log of candidate is greater or upto date with current node accept
	if args.LastLogTerm > LastLogTerm {
		logOk = true
	} else if args.LastLogTerm == LastLogTerm && args.LastLogIndex >= lastLogIndex {
		logOk = true
	}

	if (rf.votedFor == -1 || rf.votedFor == args.CandidateId) && logOk {
		rf.votedFor = args.CandidateId
		rf.state = Follower
		rf.lastResetTime = time.Now()
		reply.VoteGranted = true
	} else {
		reply.VoteGranted = false
	}
}
