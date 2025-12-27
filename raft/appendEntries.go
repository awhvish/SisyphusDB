package raft

import (
	pb "KV-Store/proto"
	"context"
	"fmt"
	"time"
)

type AppendEntriesArgs struct {
	Term         int //leader's term
	LeaderId     int
	PrevLogIndex int        //index of log entry preceding new ones
	PrevLogTerm  int        // term of prev log index
	Entries      []LogEntry //log entries to store, empty for heartbeats
	LeaderCommit int
}

type AppendEntriesReply struct {
	Term    int
	Success bool
}

func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if len(args.Entries) > 0 || args.LeaderCommit > rf.commitIndex {
		fmt.Printf("[Follower %d] Recv HB. LeaderCommit: %d, MyCommit: %d, Entries: %d\n",
			rf.me, args.LeaderCommit, rf.commitIndex, len(args.Entries))
	}
	//reject older leaders
	if args.Term < rf.currentTerm {
		reply.Term = rf.currentTerm
		reply.Success = false
		return
	}

	// We heard from a valid leader
	rf.currentTerm = args.Term
	rf.state = Follower
	rf.votedFor = -1
	rf.leaderId = args.LeaderId
	rf.lastResetTime = time.Now()
	rf.persist()
	reply.Term = rf.currentTerm

	LastLogIndex := len(rf.log) - 1
	if args.PrevLogIndex > LastLogIndex || rf.log[args.PrevLogIndex].Term != args.PrevLogTerm {
		reply.Success = false
		return
	}

	// conflict resolution & append for entries
	insertIndex := args.PrevLogIndex + 1
	for i, entry := range args.Entries {
		index := insertIndex + i
		if index < len(rf.log) {
			if rf.log[index].Term != args.Term {
				rf.log = rf.log[:index]
				rf.log = append(rf.log, entry)
				rf.persist()
			}
		} else {
			rf.log = append(rf.log, entry)
			rf.persist()
		}
	}
	//update commit index taking the minimum of leader's commit index and the actual committed data in server
	if args.LeaderCommit > rf.commitIndex {
		lastNewIndex := args.PrevLogIndex + len(args.Entries)
		oldCommit := rf.commitIndex
		if args.LeaderCommit < lastNewIndex {
			rf.commitIndex = args.LeaderCommit
		} else {
			rf.commitIndex = lastNewIndex
		}
		if rf.commitIndex > oldCommit {
			fmt.Printf("[Follower %d] Updated CommitIndex: %d -> %d\n", rf.me, oldCommit, rf.commitIndex)
		}
	}

	reply.Success = true
}

func (rf *Raft) sendHeartBeats() {
	rf.mu.Lock()
	if rf.state != Leader {
		rf.mu.Unlock()
		return
	}
	term := rf.currentTerm
	rf.mu.Unlock()

	for i := range rf.peers {
		if i == rf.me {
			continue
		}

		go func(server int) {
			rf.mu.Lock()

			prevLogIndex := rf.nextIndex[server] - 1
			if prevLogIndex < 0 {
				prevLogIndex = 0
			}

			entries := make([]LogEntry, 0)

			if len(rf.log)-1 >= rf.nextIndex[server] {
				entries = append(entries, rf.log[rf.nextIndex[server]:]...)
			}

			args := AppendEntriesArgs{
				Term:         term,
				LeaderId:     rf.me,
				PrevLogIndex: prevLogIndex,
				PrevLogTerm:  rf.log[prevLogIndex].Term,
				Entries:      entries,
				LeaderCommit: rf.commitIndex,
			}
			rf.mu.Unlock()

			var reply AppendEntriesReply
			if rf.sendAppendEntries(server, &args, &reply) {
				rf.mu.Lock()
				defer rf.mu.Unlock()

				if reply.Term > rf.currentTerm {
					rf.currentTerm = reply.Term
					rf.state = Follower
					rf.votedFor = -1
					rf.leaderId = -1
					return
				}
				if reply.Success {
					// Update tracking state
					newMatchIndex := args.PrevLogIndex + len(args.Entries)
					if newMatchIndex > rf.matchIndex[server] {
						rf.matchIndex[server] = newMatchIndex
						rf.nextIndex[server] = rf.matchIndex[server] + 1
					}
					//update commit index
					for N := len(rf.log) - 1; N > rf.commitIndex; N-- {
						count := 1 // Count self
						for i := range rf.peers {
							if i != rf.me && rf.matchIndex[i] >= N {
								count++
							}
						}
						if count > len(rf.peers)/2 && rf.log[N].Term == rf.currentTerm {
							rf.commitIndex = N
							fmt.Printf("[Commit] Leader %d committed Index %d\n", rf.me, N)
							break // Found the highest committed index
						}
					}
				} else {
					// Failure: Follower's log is inconsistent.
					// Backtrack: Decrement nextIndex and retry later
					rf.nextIndex[server]--
				}
			}
		}(i)
	}
}

func (rf *Raft) sendAppendEntries(server int, args *AppendEntriesArgs, reply *AppendEntriesReply) bool {
	// 1. Convert to Proto
	pbEntries := make([]*pb.LogEntry, len(args.Entries))
	for i, v := range args.Entries {
		pbEntries[i] = &pb.LogEntry{
			Index:   int32(v.Index),
			Term:    int32(v.Term),
			Command: v.Command,
		}
	}

	pbArgs := &pb.AppendEntriesRequest{
		Term:         int32(args.Term),
		LeaderId:     int32(args.LeaderId),
		PrevLogIndex: int32(args.PrevLogIndex),
		PrevLogTerm:  int32(args.PrevLogTerm),
		Entries:      pbEntries,
		LeaderCommit: int32(args.LeaderCommit),
	}

	// 2. Call gRPC
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*100)
	defer cancel()

	pbReply, err := rf.peers[server].AppendEntries(ctx, pbArgs)
	if err != nil {
		return false
	}

	// 3. Unpack Response
	reply.Term = int(pbReply.Term)
	reply.Success = pbReply.Success
	return true
}
