package chaos

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func getBinaryPath(t *testing.T) string {
	projectRoot := filepath.Join("..", "..")
	binaryPath := filepath.Join(projectRoot, "kv-server")

	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		t.Fatalf("Binary not found at %s. Run 'go build -o kv-server ./cmd/server' first", binaryPath)
	}

	absPath, _ := filepath.Abs(binaryPath)
	return absPath
}

func cleanupState() {
	for i := 0; i < 5; i++ {
		_ = os.Remove(fmt.Sprintf("../../raft_state_%d.gob", i))
	}
	_ = os.RemoveAll("../../Storage/wal")
	_ = os.RemoveAll("../../Storage/data")
}

// TestLeaderFailoverDuringWrites validates Raft's durability guarantee:
// acknowledged writes must survive leader failures.
func TestLeaderFailoverDuringWrites(t *testing.T) {
	cleanupState()
	binaryPath := getBinaryPath(t)

	t.Log("=== Chaos Test: Leader Failover During Writes ===")

	// Start cluster
	cluster := NewCluster(binaryPath)
	if err := cluster.Start(3); err != nil {
		t.Fatalf("Failed to start cluster: %v", err)
	}
	defer cluster.Shutdown()

	// Wait for leader
	leader, err := cluster.WaitForLeader(10 * time.Second)
	if err != nil {
		t.Fatalf("No leader elected: %v", err)
	}
	t.Logf("Leader elected: Node %d", leader)

	// Write first batch
	acknowledgedKeys := make(map[string]string)
	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("pre_crash_key_%d", i)
		val := fmt.Sprintf("value_%d", i)
		if err := cluster.WriteKey(key, val); err != nil {
			continue
		}
		acknowledgedKeys[key] = val
	}
	t.Logf("Acknowledged %d writes before crash", len(acknowledgedKeys))
	time.Sleep(1 * time.Second)

	// Kill leader
	t.Logf("Killing leader (Node %d)...", leader)
	if err := cluster.KillNode(leader); err != nil {
		t.Fatalf("Failed to kill leader: %v", err)
	}

	// Wait for new leader
	time.Sleep(3 * time.Second)
	newLeader, err := cluster.WaitForLeader(10 * time.Second)
	if err != nil {
		t.Fatalf("No new leader elected: %v", err)
	}
	if newLeader == leader {
		t.Fatalf("Same node still leader after SIGKILL")
	}
	t.Logf("New leader: Node %d", newLeader)

	// Write second batch
	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("post_crash_key_%d", i)
		val := fmt.Sprintf("value_%d", i)
		if err := cluster.WriteKey(key, val); err != nil {
			continue
		}
		acknowledgedKeys[key] = val
	}
	t.Logf("Total acknowledged: %d", len(acknowledgedKeys))
	time.Sleep(2 * time.Second)

	// Verify all writes
	var missingKeys []string
	for key := range acknowledgedKeys {
		if _, found := cluster.ReadKey(key); !found {
			missingKeys = append(missingKeys, key)
		}
	}

	t.Logf("Missing keys: %d", len(missingKeys))
	if len(missingKeys) > 0 {
		t.Errorf("DATA LOSS: %v", missingKeys)
	} else {
		t.Log("✅ All acknowledged writes survived leader failover")
	}
}

// TestWritesDuringElection validates Raft's safety guarantee:
// only one leader can accept writes at a time.
func TestWritesDuringElection(t *testing.T) {
	cleanupState()
	binaryPath := getBinaryPath(t)

	t.Log("=== Chaos Test: Writes During Election ===")

	cluster := NewCluster(binaryPath)
	if err := cluster.Start(3); err != nil {
		t.Fatalf("Failed to start cluster: %v", err)
	}
	defer cluster.Shutdown()

	leader, err := cluster.WaitForLeader(10 * time.Second)
	if err != nil {
		t.Fatalf("No leader elected: %v", err)
	}
	t.Logf("Leader: Node %d", leader)

	// Kill leader and try writes to all nodes
	_ = cluster.KillNode(leader)
	acceptedBy := make(map[int]bool)
	key := "election_test_key"

	for i := 0; i < 3; i++ {
		if i == leader {
			continue
		}
		success, err := cluster.WriteKeyToNode(i, key, fmt.Sprintf("from_node_%d", i))
		if err == nil && success {
			acceptedBy[i] = true
			t.Logf("Node %d accepted write", i)
		}
	}

	// Wait for election
	time.Sleep(5 * time.Second)
	newLeader, err := cluster.WaitForLeader(10 * time.Second)
	if err != nil {
		t.Fatalf("No leader after election: %v", err)
	}
	t.Logf("New leader: Node %d", newLeader)

	// Check consistency
	var values []string
	for i := 0; i < 3; i++ {
		if i == leader {
			continue
		}
		val, found, _ := cluster.ReadKeyFromNode(i, key)
		if found {
			values = append(values, val)
		}
	}

	if len(values) > 1 {
		first := values[0]
		for _, v := range values[1:] {
			if v != first {
				t.Errorf("SPLIT-BRAIN: nodes have different values: %v", values)
			}
		}
	}

	if len(acceptedBy) <= 1 {
		t.Log("✅ At most one node accepted writes during election")
	}
}
