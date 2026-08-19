package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/gowebpki/jcs"
)

func TestVerifiedSnapshotStoreMeetsLargeProjectHotReadBudgets(t *testing.T) {
	processSnapshotCacheEnabled.Store(false)
	processSnapshotCaches.Range(func(key, _ any) bool { processSnapshotCaches.Delete(key); return true })
	t.Cleanup(func() {
		processSnapshotCacheEnabled.Store(false)
		processSnapshotCaches.Range(func(key, _ any) bool { processSnapshotCaches.Delete(key); return true })
	})

	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "runtime"))
	svc, err := Init(root, "generic scale fixture")
	if err != nil {
		t.Fatal(err)
	}
	graph := domain.GraphDefinition{
		APIVersion: domain.GraphAPIVersion,
		Kind:       domain.GraphKind,
		Metadata:   domain.GraphMetadata{Name: "generic 1000-node graph"},
		Spec:       domain.GraphSpec{Roles: []domain.RoleDefinition{{ID: "worker", Capabilities: []string{"node.run"}}}},
	}
	for index := 0; index < 1000; index++ {
		id := fmt.Sprintf("node-%04d", index)
		graph.Spec.Nodes = append(graph.Spec.Nodes, domain.NodeDefinition{ID: id, Kind: "task", Role: "worker", Title: id, Outcomes: []domain.Outcome{{ID: "done", Class: "success"}}})
	}
	graphRaw, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	graphPath := filepath.Join(root, "graph.json")
	if err := os.WriteFile(graphPath, graphRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportGraph(graphPath, "scale/import", "governor"); err != nil {
		t.Fatal(err)
	}

	// The padding is ignored by the typed reducer but remains authenticated in
	// twelve independently bounded immutable segments. The remaining segments
	// are small. This models a generic 100+ MiB / 1500-segment authority without
	// coupling the gate to any external project's vocabulary or paying the
	// ordinary append path's deliberate O(history) idempotency scan 1498 times.
	appendScaleSegments(t, svc, 1500)
	var authorityBytes int64
	entries, err := os.ReadDir(filepath.Join(svc.Project.DataDir, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		authorityBytes += info.Size()
	}
	if authorityBytes < 100*1024*1024 || len(entries) != 1500 {
		t.Fatalf("scale fixture = %d bytes / %d segments", authorityBytes, len(entries))
	}

	EnableProcessSnapshotCache()
	warmed, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := warmed.State()
	if err != nil || state.HeadSequence != 1500 || len(state.Nodes) != 1000 {
		t.Fatalf("warm state = seq %d nodes %d err %v", state.HeadSequence, len(state.Nodes), err)
	}

	sequential := make([]time.Duration, 20)
	for index := range sequential {
		started := time.Now()
		reader, err := OpenForInspection(root)
		if err == nil {
			_, err = reader.State()
		}
		if err != nil {
			t.Fatal(err)
		}
		sequential[index] = time.Since(started)
	}
	sequentialP95 := durationP95(sequential)
	t.Logf("100+ MiB / 1500-segment / 1000-node hot query p95: %s", sequentialP95)
	if sequentialP95 >= 250*time.Millisecond {
		t.Fatalf("hot query p95 = %s, want <250ms", sequentialP95)
	}

	start := make(chan struct{})
	concurrent := make([]time.Duration, 32)
	errors := make(chan error, len(concurrent))
	var wait sync.WaitGroup
	for index := range concurrent {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			started := time.Now()
			reader, err := OpenForInspection(root)
			if err == nil {
				_, err = reader.State()
			}
			concurrent[index] = time.Since(started)
			errors <- err
		}(index)
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	concurrentP95 := durationP95(concurrent)
	t.Logf("32-reader p95: %s", concurrentP95)
	if concurrentP95 >= 500*time.Millisecond {
		t.Fatalf("32-reader p95 = %s, want <500ms", concurrentP95)
	}
}

type scaleUnsignedSegment struct {
	SchemaVersion int             `json:"schemaVersion"`
	Sequence      uint64          `json:"sequence"`
	ProjectID     string          `json:"projectId"`
	PreviousHash  string          `json:"previousHash"`
	Command       journal.Command `json:"command"`
	Events        []journal.Event `json:"events"`
	CommittedAt   string          `json:"committedAt"`
}

func appendScaleSegments(t *testing.T, svc *Service, target int) {
	t.Helper()
	prefix, err := svc.Journal.ReadAll()
	if err != nil || len(prefix) != 2 {
		t.Fatalf("scale prefix = %d segments, %v", len(prefix), err)
	}
	previous := prefix[len(prefix)-1].SegmentHash
	largeValue := make([]string, 10)
	for index := range largeValue {
		largeValue[index] = strings.Repeat("x", 900*1024)
	}
	journalDir := filepath.Join(svc.Project.DataDir, "journal")
	for sequence := len(prefix) + 1; sequence <= target; sequence++ {
		now := time.Date(2026, 8, 19, 0, 0, sequence, 0, time.UTC).Format(time.RFC3339Nano)
		payloadValue := map[string]any{
			"roleId": "worker", "harness": "fixture", "sessionId": fmt.Sprintf("session-%04d", sequence),
			"boundAt": now, "expiresAt": time.Date(2026, 8, 20, 0, 0, sequence, 0, time.UTC).Format(time.RFC3339Nano), "active": true,
		}
		if sequence <= 14 {
			payloadValue["padding"] = largeValue
		}
		payload, err := json.Marshal(payloadValue)
		if err != nil {
			t.Fatal(err)
		}
		unsigned := scaleUnsignedSegment{
			SchemaVersion: journal.CurrentSegmentSchemaVersion, Sequence: uint64(sequence), ProjectID: svc.Project.Config.ProjectID, PreviousHash: previous,
			Command: journal.Command{ID: fmt.Sprintf("scale-command-%04d", sequence), Kind: "scale.fixture", ActorRole: "fixture", IdempotencyKey: fmt.Sprintf("scale/%04d", sequence)},
			Events:  []journal.Event{{Type: "role.bound", SchemaVersion: journal.CurrentEventSchemaVersion, Payload: payload}}, CommittedAt: now,
		}
		unsignedRaw, err := json.Marshal(unsigned)
		if err != nil {
			t.Fatal(err)
		}
		unsignedCanonical, err := jcs.Transform(unsignedRaw)
		if err != nil {
			t.Fatal(err)
		}
		previousBytes, err := hex.DecodeString(previous)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		_, _ = hash.Write([]byte("dagrail-journal-v1\x00"))
		_, _ = hash.Write(previousBytes)
		_, _ = hash.Write(unsignedCanonical)
		segmentHash := hex.EncodeToString(hash.Sum(nil))
		segment := journal.Segment{
			SchemaVersion: unsigned.SchemaVersion, Sequence: unsigned.Sequence, ProjectID: unsigned.ProjectID, PreviousHash: unsigned.PreviousHash,
			Command: unsigned.Command, Events: unsigned.Events, CommittedAt: unsigned.CommittedAt, SegmentHash: segmentHash,
		}
		raw, err := json.Marshal(segment)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := jcs.Transform(raw)
		if err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("%012d-%s.json", sequence, segmentHash)
		if err := os.WriteFile(filepath.Join(journalDir, name), canonical, 0o600); err != nil {
			t.Fatal(err)
		}
		previous = segmentHash
	}
}

func durationP95(values []time.Duration) time.Duration {
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	return ordered[(len(ordered)*95+99)/100-1]
}
