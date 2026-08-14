package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/google/uuid"
	"github.com/gowebpki/jcs"
	"gopkg.in/yaml.v3"
)

type GraphPatch struct {
	APIVersion string           `json:"apiVersion" yaml:"apiVersion"`
	Kind       string           `json:"kind" yaml:"kind"`
	Operations []PatchOperation `json:"operations" yaml:"operations"`
}

type PatchOperation struct {
	Op     string                 `json:"op" yaml:"op"`
	NodeID string                 `json:"nodeId,omitempty" yaml:"nodeId,omitempty"`
	EdgeID string                 `json:"edgeId,omitempty" yaml:"edgeId,omitempty"`
	Node   *domain.NodeDefinition `json:"node,omitempty" yaml:"node,omitempty"`
	Edge   *domain.EdgeDefinition `json:"edge,omitempty" yaml:"edge,omitempty"`
}

type GraphImpact struct {
	CurrentRevision  string   `json:"currentRevision"`
	ProposedRevision string   `json:"proposedRevision"`
	AddedNodes       []string `json:"addedNodes,omitempty"`
	UpdatedNodes     []string `json:"updatedNodes,omitempty"`
	RemovedNodes     []string `json:"removedNodes,omitempty"`
	AddedEdges       []string `json:"addedEdges,omitempty"`
	RemovedEdges     []string `json:"removedEdges,omitempty"`
	DependencyCut    []string `json:"dependencyCut,omitempty"`
	Token            string   `json:"token"`
	ExpiresAt        string   `json:"expiresAt"`
}

type graphChangeToken struct {
	ProjectID        string `json:"projectId"`
	HeadHash         string `json:"headHash"`
	CurrentRevision  string `json:"currentRevision"`
	ProposedRevision string `json:"proposedRevision"`
	PatchDigest      string `json:"patchDigest"`
	ProviderSet      string `json:"providerSet"`
	ExpiresAt        string `json:"expiresAt"`
}

func (s *Service) PreviewGraphChange(path string) (GraphImpact, error) {
	state, _, err := s.load()
	if err != nil {
		return GraphImpact{}, err
	}
	if state.Graph == nil {
		return GraphImpact{}, fmt.Errorf("no graph is imported")
	}
	patch, digest, err := decodeGraphPatch(path)
	if err != nil {
		return GraphImpact{}, err
	}
	graph, impact, superseded, err := applyGraphPatch(state, patch)
	_ = superseded
	if err != nil {
		return GraphImpact{}, err
	}
	if err := s.validateGraphProviders(graph); err != nil {
		return GraphImpact{}, err
	}
	revision, err := graphRevision(graph)
	if err != nil {
		return GraphImpact{}, err
	}
	impact.CurrentRevision = state.GraphRevision
	impact.ProposedRevision = revision
	impact.ExpiresAt = s.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339Nano)
	tokenPayload := graphChangeToken{ProjectID: state.ProjectID, HeadHash: state.HeadHash, CurrentRevision: state.GraphRevision, ProposedRevision: revision, PatchDigest: digest, ProviderSet: providerFingerprint(state.Graph), ExpiresAt: impact.ExpiresAt}
	secret, err := s.actionSecret()
	if err != nil {
		return GraphImpact{}, err
	}
	impact.Token, err = signGraphChangeToken(tokenPayload, secret)
	return impact, err
}

func (s *Service) ApplyGraphChange(path, token, idempotencyKey, actorRole string) (GraphImpact, error) {
	if idempotencyKey == "" {
		return GraphImpact{}, fmt.Errorf("idempotency key is required")
	}
	secret, err := s.actionSecret()
	if err != nil {
		return GraphImpact{}, err
	}
	tokenPayload, err := verifyGraphChangeToken(token, secret)
	if err != nil {
		return GraphImpact{}, err
	}
	state, segments, err := s.load()
	if err != nil {
		return GraphImpact{}, err
	}
	if command, ok := state.Commands[idempotencyKey]; ok {
		return graphImpactForSequence(segments, command.Sequence)
	}
	if tokenPayload.ProjectID != state.ProjectID || tokenPayload.HeadHash != state.HeadHash || tokenPayload.CurrentRevision != state.GraphRevision || tokenPayload.ProviderSet != providerFingerprint(state.Graph) {
		return GraphImpact{}, fmt.Errorf("graph change token is stale")
	}
	expires, err := time.Parse(time.RFC3339Nano, tokenPayload.ExpiresAt)
	if err != nil || !s.Now().UTC().Before(expires) {
		return GraphImpact{}, fmt.Errorf("graph change token is expired")
	}
	patch, digest, err := decodeGraphPatch(path)
	if err != nil {
		return GraphImpact{}, err
	}
	if digest != tokenPayload.PatchDigest {
		return GraphImpact{}, fmt.Errorf("graph patch does not match impact token")
	}
	graph, impact, superseded, err := applyGraphPatch(state, patch)
	if err != nil {
		return GraphImpact{}, err
	}
	if err := s.validateGraphProviders(graph); err != nil {
		return GraphImpact{}, err
	}
	revision, err := graphRevision(graph)
	if err != nil {
		return GraphImpact{}, err
	}
	if revision != tokenPayload.ProposedRevision {
		return GraphImpact{}, fmt.Errorf("proposed graph revision does not match impact token")
	}
	impact.CurrentRevision, impact.ProposedRevision, impact.ExpiresAt = state.GraphRevision, revision, tokenPayload.ExpiresAt
	payload, _ := json.Marshal(struct {
		Graph            domain.GraphDefinition `json:"graph"`
		Revision         string                 `json:"revision"`
		PreviousRevision string                 `json:"previousRevision"`
		Superseded       []string               `json:"superseded,omitempty"`
		Impact           GraphImpact            `json:"impact"`
	}{graph, revision, state.GraphRevision, superseded, impact})
	expectedHead := tokenPayload.HeadHash
	segment, _, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "graph.change", ActorRole: actorRole, IdempotencyKey: idempotencyKey}, []journal.Event{{Type: "graph.revised", Payload: payload}}, s.Now(), &expectedHead)
	if err != nil {
		return GraphImpact{}, err
	}
	if err := s.settleAutomatic(); err != nil {
		return GraphImpact{}, err
	}
	state, segments, err = s.load()
	if err != nil {
		return GraphImpact{}, err
	}
	if err := s.Projection.Sync(state, segments); err != nil {
		return GraphImpact{}, err
	}
	return graphImpactForSequence(segments, segment.Sequence)
}

func graphImpactForSequence(segments []journal.Segment, sequence uint64) (GraphImpact, error) {
	if sequence == 0 || sequence > uint64(len(segments)) {
		return GraphImpact{}, fmt.Errorf("graph change result sequence %d is unavailable", sequence)
	}
	for _, event := range segments[sequence-1].Events {
		if event.Type != "graph.revised" {
			continue
		}
		var payload struct {
			Impact GraphImpact `json:"impact"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return GraphImpact{}, err
		}
		payload.Impact.Token = ""
		return payload.Impact, nil
	}
	return GraphImpact{}, fmt.Errorf("journal command at sequence %d has no graph change result", sequence)
}

func (s *Service) ExportGraph(format string) ([]byte, error) {
	state, _, err := s.load()
	if err != nil {
		return nil, err
	}
	if state.Graph == nil {
		return nil, fmt.Errorf("no graph is imported")
	}
	if format == "yaml" {
		return yaml.Marshal(state.Graph)
	}
	return json.Marshal(state.Graph)
}

func ValidateGraphFile(path string) (domain.GraphDefinition, error) {
	graph, err := decodeGraph(path)
	if err != nil {
		return graph, err
	}
	return graph, domain.ValidateGraph(graph)
}

func decodeGraphPatch(path string) (GraphPatch, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return GraphPatch{}, "", err
	}
	var patch GraphPatch
	if err := json.Unmarshal(data, &patch); err != nil || patch.APIVersion == "" {
		var value any
		if yamlErr := yaml.Unmarshal(data, &value); yamlErr != nil {
			return patch, "", yamlErr
		}
		normalized, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return patch, "", marshalErr
		}
		if err := json.Unmarshal(normalized, &patch); err != nil {
			return patch, "", err
		}
	}
	if patch.APIVersion != domain.GraphAPIVersion || patch.Kind != "GraphPatch" || len(patch.Operations) == 0 {
		return patch, "", fmt.Errorf("graph patch must be a non-empty %s GraphPatch", domain.GraphAPIVersion)
	}
	raw, _ := json.Marshal(patch)
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return patch, "", err
	}
	sum := sha256.Sum256(append([]byte("dagrail-graph-patch-v1\x00"), canonical...))
	return patch, hex.EncodeToString(sum[:]), nil
}

func applyGraphPatch(state domain.State, patch GraphPatch) (domain.GraphDefinition, GraphImpact, []string, error) {
	raw, _ := json.Marshal(state.Graph)
	var graph domain.GraphDefinition
	if err := json.Unmarshal(raw, &graph); err != nil {
		return graph, GraphImpact{}, nil, err
	}
	impact := GraphImpact{}
	superseded := []string{}
	changedRoots := map[string]bool{}
	for _, operation := range patch.Operations {
		switch operation.Op {
		case "addNode":
			if operation.Node == nil || operation.Node.ID == "" {
				return graph, impact, nil, fmt.Errorf("addNode requires node")
			}
			if _, ok := findNode(graph, operation.Node.ID); ok {
				return graph, impact, nil, fmt.Errorf("node %s already exists", operation.Node.ID)
			}
			graph.Spec.Nodes = append(graph.Spec.Nodes, *operation.Node)
			impact.AddedNodes = append(impact.AddedNodes, operation.Node.ID)
			changedRoots[operation.Node.ID] = true
		case "updateNode":
			if operation.Node == nil {
				return graph, impact, nil, fmt.Errorf("updateNode requires node")
			}
			index, ok := findNode(graph, operation.Node.ID)
			if !ok {
				return graph, impact, nil, fmt.Errorf("unknown node %s", operation.Node.ID)
			}
			if state.Nodes[operation.Node.ID].Status != "planned" {
				return graph, impact, nil, fmt.Errorf("node %s contract is frozen in status %s", operation.Node.ID, state.Nodes[operation.Node.ID].Status)
			}
			graph.Spec.Nodes[index] = *operation.Node
			impact.UpdatedNodes = append(impact.UpdatedNodes, operation.Node.ID)
			changedRoots[operation.Node.ID] = true
		case "removeNode":
			index, ok := findNode(graph, operation.NodeID)
			if !ok {
				return graph, impact, nil, fmt.Errorf("unknown node %s", operation.NodeID)
			}
			if state.Nodes[operation.NodeID].Status != "planned" {
				return graph, impact, nil, fmt.Errorf("node %s cannot be removed in status %s", operation.NodeID, state.Nodes[operation.NodeID].Status)
			}
			graph.Spec.Nodes = append(graph.Spec.Nodes[:index], graph.Spec.Nodes[index+1:]...)
			for i := len(graph.Spec.Edges) - 1; i >= 0; i-- {
				if graph.Spec.Edges[i].From == operation.NodeID || graph.Spec.Edges[i].To == operation.NodeID {
					impact.RemovedEdges = append(impact.RemovedEdges, graph.Spec.Edges[i].ID)
					graph.Spec.Edges = append(graph.Spec.Edges[:i], graph.Spec.Edges[i+1:]...)
				}
			}
			impact.RemovedNodes = append(impact.RemovedNodes, operation.NodeID)
			changedRoots[operation.NodeID] = true
		case "addEdge":
			if operation.Edge == nil || operation.Edge.ID == "" {
				return graph, impact, nil, fmt.Errorf("addEdge requires edge")
			}
			if _, ok := findEdge(graph, operation.Edge.ID); ok {
				return graph, impact, nil, fmt.Errorf("edge %s already exists", operation.Edge.ID)
			}
			if runtime, exists := state.Nodes[operation.Edge.To]; exists && runtime.Status != "planned" {
				return graph, impact, nil, fmt.Errorf("cannot change incoming contract of node %s in status %s", operation.Edge.To, runtime.Status)
			}
			graph.Spec.Edges = append(graph.Spec.Edges, *operation.Edge)
			impact.AddedEdges = append(impact.AddedEdges, operation.Edge.ID)
			changedRoots[operation.Edge.From] = true
			changedRoots[operation.Edge.To] = true
		case "removeEdge":
			index, ok := findEdge(graph, operation.EdgeID)
			if !ok {
				return graph, impact, nil, fmt.Errorf("unknown edge %s", operation.EdgeID)
			}
			targetID := graph.Spec.Edges[index].To
			if runtime := state.Nodes[targetID]; runtime.Status != "planned" {
				return graph, impact, nil, fmt.Errorf("cannot change incoming contract of node %s in status %s", targetID, runtime.Status)
			}
			changedRoots[graph.Spec.Edges[index].From] = true
			changedRoots[graph.Spec.Edges[index].To] = true
			graph.Spec.Edges = append(graph.Spec.Edges[:index], graph.Spec.Edges[index+1:]...)
			impact.RemovedEdges = append(impact.RemovedEdges, operation.EdgeID)
		case "supersedeNode":
			if operation.Node == nil || operation.NodeID == "" || operation.Node.ID == "" || operation.Node.ID == operation.NodeID {
				return graph, impact, nil, fmt.Errorf("supersedeNode requires distinct old and new node IDs")
			}
			if _, ok := findNode(graph, operation.NodeID); !ok {
				return graph, impact, nil, fmt.Errorf("unknown node %s", operation.NodeID)
			}
			if _, ok := findNode(graph, operation.Node.ID); ok {
				return graph, impact, nil, fmt.Errorf("replacement node %s already exists", operation.Node.ID)
			}
			replacement := *operation.Node
			replacement.Supersedes = operation.NodeID
			graph.Spec.Nodes = append(graph.Spec.Nodes, replacement)
			superseded = append(superseded, operation.NodeID)
			impact.AddedNodes = append(impact.AddedNodes, replacement.ID)
			impact.UpdatedNodes = append(impact.UpdatedNodes, operation.NodeID)
			changedRoots[operation.NodeID] = true
			changedRoots[replacement.ID] = true
		default:
			return graph, impact, nil, fmt.Errorf("unsupported graph patch operation %s", operation.Op)
		}
	}
	if err := domain.ValidateGraph(graph); err != nil {
		return graph, impact, nil, err
	}
	impact.DependencyCut = dependencyCut(graph, changedRoots)
	for _, values := range [][]string{impact.AddedNodes, impact.UpdatedNodes, impact.RemovedNodes, impact.AddedEdges, impact.RemovedEdges, impact.DependencyCut} {
		sort.Strings(values)
	}
	return graph, impact, superseded, nil
}

func findNode(graph domain.GraphDefinition, id string) (int, bool) {
	for index, node := range graph.Spec.Nodes {
		if node.ID == id {
			return index, true
		}
	}
	return 0, false
}
func findEdge(graph domain.GraphDefinition, id string) (int, bool) {
	for index, edge := range graph.Spec.Edges {
		if edge.ID == id {
			return index, true
		}
	}
	return 0, false
}

func dependencyCut(graph domain.GraphDefinition, roots map[string]bool) []string {
	adj := map[string][]string{}
	for _, edge := range graph.Spec.Edges {
		adj[edge.From] = append(adj[edge.From], edge.To)
	}
	seen := map[string]bool{}
	queue := []string{}
	for root := range roots {
		queue = append(queue, root)
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if seen[current] {
			continue
		}
		seen[current] = true
		queue = append(queue, adj[current]...)
	}
	result := make([]string, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func signGraphChangeToken(payload graphChangeToken, secret []byte) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func verifyGraphChangeToken(token string, secret []byte) (graphChangeToken, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return graphChangeToken{}, fmt.Errorf("invalid graph change token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return graphChangeToken{}, fmt.Errorf("invalid graph change token")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return graphChangeToken{}, fmt.Errorf("invalid graph change token")
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(raw)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return graphChangeToken{}, fmt.Errorf("invalid graph change token signature")
	}
	var payload graphChangeToken
	if err := json.Unmarshal(raw, &payload); err != nil {
		return payload, err
	}
	return payload, nil
}
