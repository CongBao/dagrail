package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/gowebpki/jcs"
)

type legacyUnsignedSegment struct {
	SchemaVersion int             `json:"schemaVersion"`
	Sequence      uint64          `json:"sequence"`
	ProjectID     string          `json:"projectId"`
	PreviousHash  string          `json:"previousHash"`
	Command       journal.Command `json:"command"`
	Events        []journal.Event `json:"events"`
	CommittedAt   string          `json:"committedAt"`
}

type legacyStoredSegment struct {
	SchemaVersion int             `json:"schemaVersion"`
	Sequence      uint64          `json:"sequence"`
	ProjectID     string          `json:"projectId"`
	PreviousHash  string          `json:"previousHash"`
	Command       journal.Command `json:"command"`
	Events        []journal.Event `json:"events"`
	CommittedAt   string          `json:"committedAt"`
	SegmentHash   string          `json:"segmentHash"`
}

func TestServiceReplaysLegacyGraphEventIntoCurrentProjection(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, ".data"))
	service, err := Init(root, "legacy-replay")
	if err != nil {
		t.Fatal(err)
	}
	graph := domain.GraphDefinition{
		APIVersion: "dagrail.io/v1alpha1",
		Kind:       "Graph",
		Metadata:   domain.GraphMetadata{Name: "legacy"},
		Spec: domain.GraphSpec{
			Roles: []domain.RoleDefinition{{ID: "developer", Capabilities: []string{"node.run"}}},
			Nodes: []domain.NodeDefinition{{
				ID:       "A",
				Kind:     "task",
				Role:     "developer",
				Title:    "legacy task",
				Outcomes: []domain.Outcome{{ID: "success", Class: "success"}},
			}},
		},
	}
	revision, err := graphRevision(graph)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(struct {
		Graph    domain.GraphDefinition `json:"graph"`
		Revision string                 `json:"revision"`
	}{Graph: graph, Revision: revision})
	if err != nil {
		t.Fatal(err)
	}
	unsigned := legacyUnsignedSegment{
		SchemaVersion: journal.LegacySegmentSchemaVersion,
		Sequence:      1,
		ProjectID:     service.Project.Config.ProjectID,
		Command: journal.Command{
			ID:             "22222222-2222-4222-8222-222222222222",
			Kind:           "graph.import",
			IdempotencyKey: "legacy-import",
		},
		Events:      []journal.Event{{Type: "graph.imported", Payload: payload}},
		CommittedAt: "2026-01-01T00:00:00Z",
	}
	unsignedRaw, err := json.Marshal(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	canonicalUnsigned, err := jcs.Transform(unsignedRaw)
	if err != nil {
		t.Fatal(err)
	}
	hasher := sha256.New()
	_, _ = hasher.Write([]byte("dagrail-journal-v1\x00"))
	_, _ = hasher.Write(canonicalUnsigned)
	hash := hex.EncodeToString(hasher.Sum(nil))
	stored := legacyStoredSegment{
		SchemaVersion: unsigned.SchemaVersion,
		Sequence:      unsigned.Sequence,
		ProjectID:     unsigned.ProjectID,
		PreviousHash:  unsigned.PreviousHash,
		Command:       unsigned.Command,
		Events:        unsigned.Events,
		CommittedAt:   unsigned.CommittedAt,
		SegmentHash:   hash,
	}
	storedRaw, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	canonicalStored, err := jcs.Transform(storedRaw)
	if err != nil {
		t.Fatal(err)
	}
	segmentPath := filepath.Join(service.Project.DataDir, "journal", fmt.Sprintf("%012d-%s.json", stored.Sequence, hash))
	if err := os.WriteFile(segmentPath, canonicalStored, 0o600); err != nil {
		t.Fatal(err)
	}

	state, err := service.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.GraphRevision != revision || state.Nodes["A"].Status != "planned" {
		t.Fatalf("legacy event was not reduced: revision=%q node=%#v", state.GraphRevision, state.Nodes["A"])
	}
	if err := service.RebuildProjection(); err != nil {
		t.Fatal(err)
	}
	compatibility, err := service.Compatibility()
	if err != nil {
		t.Fatal(err)
	}
	if compatibility.Journal.UpcastedEventCount != 1 || compatibility.ProjectionSchemaVersion != 3 {
		t.Fatalf("unexpected compatibility after replay: %#v", compatibility)
	}
}
