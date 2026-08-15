package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"sort"
	"strings"
)

const (
	GraphAPIVersion = "dagrail.io/v1alpha1"
	GraphKind       = "Graph"

	// Authority JSON is persisted or used to derive a durable decision. These
	// limits keep every caller on the same duplicate-key, nesting, and scalar
	// rules instead of relying on decoder-specific behavior.
	MaxAuthorityDepth       = 64
	MaxAuthorityValues      = 1_000_000
	MaxAuthorityStringBytes = 1 << 20
	MaxAuthorityKeyBytes    = 1024
)

type GraphDefinition struct {
	APIVersion string        `json:"apiVersion" yaml:"apiVersion"`
	Kind       string        `json:"kind" yaml:"kind"`
	Metadata   GraphMetadata `json:"metadata" yaml:"metadata"`
	Spec       GraphSpec     `json:"spec" yaml:"spec"`
}

type GraphMetadata struct {
	Name         string            `json:"name" yaml:"name"`
	ExternalRefs []ExternalRef     `json:"externalRefs,omitempty" yaml:"externalRefs,omitempty"`
	Labels       map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
}

type GraphSpec struct {
	Roles              []RoleDefinition   `json:"roles" yaml:"roles"`
	Nodes              []NodeDefinition   `json:"nodes" yaml:"nodes"`
	Edges              []EdgeDefinition   `json:"edges,omitempty" yaml:"edges,omitempty"`
	ResourceCapacities []ResourceCapacity `json:"resourceCapacities,omitempty" yaml:"resourceCapacities,omitempty"`
	Providers          []ProviderRef      `json:"providers,omitempty" yaml:"providers,omitempty"`
}

type ResourceCapacity struct {
	Kind     string `json:"kind" yaml:"kind"`
	Capacity int    `json:"capacity" yaml:"capacity"`
}

type ProviderRef struct {
	ID         string `json:"id" yaml:"id"`
	Version    string `json:"version" yaml:"version"`
	SchemaHash string `json:"schemaHash,omitempty" yaml:"schemaHash,omitempty"`
}

type ExternalRef struct {
	System        string `json:"system" yaml:"system"`
	Type          string `json:"type" yaml:"type"`
	ID            string `json:"id" yaml:"id"`
	URL           string `json:"url,omitempty" yaml:"url,omitempty"`
	Revision      string `json:"revision,omitempty" yaml:"revision,omitempty"`
	ContentDigest string `json:"contentDigest,omitempty" yaml:"contentDigest,omitempty"`
}

type RoleDefinition struct {
	ID           string   `json:"id" yaml:"id"`
	Capabilities []string `json:"capabilities" yaml:"capabilities"`
}

const (
	CapabilityNodeRun         = "node.run"
	CapabilityNodeReview      = "node.review"
	CapabilityNodeDecide      = "node.decide"
	CapabilityNodeGate        = "node.gate"
	CapabilityEffectApply     = "effect.apply"
	CapabilityEffectReconcile = "effect.reconcile"
	CapabilityGraphChange     = "graph.change"
	CapabilityIncidentManage  = "incident.manage"
	CapabilityResourceClose   = "resource.close"
)

type NodeDefinition struct {
	ID           string            `json:"id" yaml:"id"`
	Kind         string            `json:"kind" yaml:"kind"`
	Role         string            `json:"role,omitempty" yaml:"role,omitempty"`
	Title        string            `json:"title" yaml:"title"`
	Objective    string            `json:"objective,omitempty" yaml:"objective,omitempty"`
	Parent       string            `json:"parent,omitempty" yaml:"parent,omitempty"`
	Supersedes   string            `json:"supersedes,omitempty" yaml:"supersedes,omitempty"`
	Inputs       json.RawMessage   `json:"inputs,omitempty" yaml:"-"`
	Outcomes     []Outcome         `json:"outcomes" yaml:"outcomes"`
	RetryBudget  int               `json:"retryBudget,omitempty" yaml:"retryBudget,omitempty"`
	Resources    []ResourceRequest `json:"resources,omitempty" yaml:"resources,omitempty"`
	Decision     *DecisionContract `json:"decision,omitempty" yaml:"decision,omitempty"`
	ExternalRefs []ExternalRef     `json:"externalRefs,omitempty" yaml:"externalRefs,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// DecisionContract binds a semantic choice to a closed key and source. Provider
// decisions name the compiled policy provider and policy entry point whose output
// becomes journal authority.
type DecisionContract struct {
	Key        string `json:"key" yaml:"key"`
	Source     string `json:"source" yaml:"source"`
	ProviderID string `json:"providerId,omitempty" yaml:"providerId,omitempty"`
	PolicyID   string `json:"policyId,omitempty" yaml:"policyId,omitempty"`
}

type Outcome struct {
	ID    string `json:"id" yaml:"id"`
	Class string `json:"class" yaml:"class"`
}

type ResourceRequest struct {
	Kind     string `json:"kind" yaml:"kind"`
	Quantity int    `json:"quantity" yaml:"quantity"`
}

type EdgeDefinition struct {
	ID   string    `json:"id" yaml:"id"`
	From string    `json:"from" yaml:"from"`
	To   string    `json:"to" yaml:"to"`
	When Predicate `json:"when" yaml:"when"`
}

type Predicate struct {
	Outcome  string      `json:"outcome,omitempty" yaml:"outcome,omitempty"`
	Decision *ValueMatch `json:"decision,omitempty" yaml:"decision,omitempty"`
	Evidence *ValueMatch `json:"evidence,omitempty" yaml:"evidence,omitempty"`
	Policy   *ValueMatch `json:"policy,omitempty" yaml:"policy,omitempty"`
	All      []Predicate `json:"all,omitempty" yaml:"all,omitempty"`
	Any      []Predicate `json:"any,omitempty" yaml:"any,omitempty"`
}

type ValueMatch struct {
	Key   string `json:"key" yaml:"key"`
	Value string `json:"value" yaml:"value"`
}

var builtinKinds = map[string]bool{
	"task": true, "review": true, "decision": true, "gate": true,
	"effect": true, "join": true, "milestone": true,
}

func IsBuiltinNodeKind(kind string) bool { return builtinKinds[kind] }

func ValidateGraph(g GraphDefinition) error {
	raw, err := json.Marshal(g)
	if err != nil {
		return fmt.Errorf("encode graph authority: %w", err)
	}
	if err := RejectSensitiveFields(raw); err != nil {
		return fmt.Errorf("graph authority: %w", err)
	}
	if g.APIVersion != GraphAPIVersion || g.Kind != GraphKind {
		return fmt.Errorf("graph must be %s %s", GraphAPIVersion, GraphKind)
	}
	if strings.TrimSpace(g.Metadata.Name) == "" {
		return fmt.Errorf("graph metadata.name is required")
	}
	roles := map[string]bool{}
	roleCapabilities := map[string]map[string]bool{}
	for _, role := range g.Spec.Roles {
		if role.ID == "" || roles[role.ID] {
			return fmt.Errorf("role IDs must be non-empty and unique: %q", role.ID)
		}
		roles[role.ID] = true
		roleCapabilities[role.ID] = map[string]bool{}
		for _, capability := range role.Capabilities {
			if strings.TrimSpace(capability) == "" || roleCapabilities[role.ID][capability] {
				return fmt.Errorf("role %s capabilities must be non-empty and unique", role.ID)
			}
			roleCapabilities[role.ID][capability] = true
		}
	}
	capacities := map[string]int{}
	for _, resource := range g.Spec.ResourceCapacities {
		if resource.Kind == "" || resource.Capacity <= 0 || capacities[resource.Kind] != 0 {
			return fmt.Errorf("resource capacities require a unique kind and positive capacity")
		}
		capacities[resource.Kind] = resource.Capacity
	}
	providerIDs := map[string]bool{}
	for _, provider := range g.Spec.Providers {
		if provider.ID == "" || provider.Version == "" || provider.SchemaHash == "" || providerIDs[provider.ID] {
			return fmt.Errorf("providers require a unique ID, version and schema hash")
		}
		providerIDs[provider.ID] = true
	}
	nodes := map[string]NodeDefinition{}
	for _, node := range g.Spec.Nodes {
		if node.ID == "" || nodes[node.ID].ID != "" {
			return fmt.Errorf("node IDs must be non-empty and unique: %q", node.ID)
		}
		if !builtinKinds[node.Kind] && !strings.Contains(node.Kind, ".") {
			return fmt.Errorf("node %s has unknown kind %q", node.ID, node.Kind)
		}
		if node.Role != "" && !roles[node.Role] {
			return fmt.Errorf("node %s references unknown role %s", node.ID, node.Role)
		}
		if (node.Kind == "task" || node.Kind == "review" || node.Kind == "decision" || node.Kind == "gate" || node.Kind == "effect") && node.Role == "" {
			return fmt.Errorf("node %s kind %s requires a role", node.ID, node.Kind)
		}
		if node.Decision != nil {
			if node.Decision.Key == "" {
				return fmt.Errorf("node %s decision key is required", node.ID)
			}
			switch node.Decision.Source {
			case "human", "llm":
				if node.Decision.ProviderID != "" || node.Decision.PolicyID != "" {
					return fmt.Errorf("node %s non-provider decision cannot declare provider fields", node.ID)
				}
			case "provider":
				if node.Decision.ProviderID == "" || node.Decision.PolicyID == "" {
					return fmt.Errorf("node %s provider decision requires providerId and policyId", node.ID)
				}
			default:
				return fmt.Errorf("node %s decision source must be human, llm or provider", node.ID)
			}
		}
		if node.Kind == "decision" && node.Decision != nil && node.Decision.Source == "provider" {
			return fmt.Errorf("decision node %s cannot use a provider decision contract", node.ID)
		}
		if node.Kind == "gate" && node.Decision != nil && node.Decision.Source != "provider" {
			return fmt.Errorf("gate node %s decision contract must use a provider", node.ID)
		}
		if len(node.Outcomes) == 0 {
			return fmt.Errorf("node %s must declare outcomes", node.ID)
		}
		if node.RetryBudget < 0 {
			return fmt.Errorf("node %s retry budget cannot be negative", node.ID)
		}
		if len(node.Inputs) > 0 {
			if err := ValidateAuthorityJSON(node.Inputs); err != nil {
				return fmt.Errorf("node %s inputs: %w", node.ID, err)
			}
		}
		if node.Kind == "join" || node.Kind == "milestone" {
			if len(node.Resources) > 0 {
				return fmt.Errorf("deterministic node %s cannot request executor resources", node.ID)
			}
			successes := 0
			for _, outcome := range node.Outcomes {
				if outcome.Class == "success" {
					successes++
				}
			}
			if successes != 1 {
				return fmt.Errorf("deterministic node %s must declare exactly one success outcome", node.ID)
			}
		}
		seenOutcomes := map[string]bool{}
		for _, outcome := range node.Outcomes {
			if outcome.ID == "" || seenOutcomes[outcome.ID] {
				return fmt.Errorf("node %s outcomes must have unique IDs", node.ID)
			}
			switch outcome.Class {
			case "success", "retryable", "failure", "cancelled":
			default:
				return fmt.Errorf("node %s outcome %s has invalid class %s", node.ID, outcome.ID, outcome.Class)
			}
			seenOutcomes[outcome.ID] = true
		}
		seenResources := map[string]bool{}
		for _, resource := range node.Resources {
			if resource.Kind == "" || resource.Quantity <= 0 || seenResources[resource.Kind] {
				return fmt.Errorf("node %s resource requests require a unique kind and positive quantity", node.ID)
			}
			if capacity := capacities[resource.Kind]; capacity == 0 {
				return fmt.Errorf("node %s requests undeclared resource %s", node.ID, resource.Kind)
			} else if resource.Quantity > capacity {
				return fmt.Errorf("node %s requests %d %s but capacity is %d", node.ID, resource.Quantity, resource.Kind, capacity)
			}
			seenResources[resource.Kind] = true
		}
		nodes[node.ID] = node
	}
	parents := map[string]string{}
	superseded := map[string]string{}
	for _, node := range g.Spec.Nodes {
		if node.Parent != "" {
			if nodes[node.Parent].ID == "" || node.Parent == node.ID {
				return fmt.Errorf("node %s has invalid hierarchy parent %s", node.ID, node.Parent)
			}
			parents[node.ID] = node.Parent
		}
		if node.Supersedes != "" {
			if nodes[node.Supersedes].ID == "" || node.Supersedes == node.ID {
				return fmt.Errorf("node %s has invalid supersedes reference %s", node.ID, node.Supersedes)
			}
			if replacement := superseded[node.Supersedes]; replacement != "" {
				return fmt.Errorf("node %s is superseded by both %s and %s", node.Supersedes, replacement, node.ID)
			}
			superseded[node.Supersedes] = node.ID
		}
	}
	for nodeID := range parents {
		seen := map[string]bool{}
		for current := nodeID; current != ""; current = parents[current] {
			if seen[current] {
				return fmt.Errorf("node hierarchy contains a cycle at %s", current)
			}
			seen[current] = true
		}
	}
	seenEdges := map[string]bool{}
	adj := map[string][]string{}
	indegree := map[string]int{}
	for id := range nodes {
		indegree[id] = 0
	}
	for _, edge := range g.Spec.Edges {
		if edge.ID == "" || seenEdges[edge.ID] {
			return fmt.Errorf("edge IDs must be non-empty and unique: %q", edge.ID)
		}
		seenEdges[edge.ID] = true
		if nodes[edge.From].ID == "" || nodes[edge.To].ID == "" {
			return fmt.Errorf("edge %s references an unknown node", edge.ID)
		}
		if edge.From == edge.To {
			return fmt.Errorf("edge %s is a self-cycle", edge.ID)
		}
		if err := validatePredicate(edge.When, nodes[edge.From]); err != nil {
			return fmt.Errorf("edge %s: %w", edge.ID, err)
		}
		adj[edge.From] = append(adj[edge.From], edge.To)
		indegree[edge.To]++
	}
	queue := make([]string, 0)
	for id, degree := range indegree {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, child := range adj[id] {
			indegree[child]--
			if indegree[child] == 0 {
				queue = append(queue, child)
			}
		}
	}
	for _, degree := range indegree {
		if degree != 0 {
			return fmt.Errorf("graph contains a cycle")
		}
	}
	return nil
}

func ValidateAuthorityJSON(raw json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	limit := new(big.Int).SetInt64(9_007_199_254_740_991)
	values := 0
	var walk func(int) error
	walk = func(depth int) error {
		if depth > MaxAuthorityDepth {
			return fmt.Errorf("authority JSON nesting exceeds %d levels", MaxAuthorityDepth)
		}
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("invalid JSON: %w", err)
		}
		values++
		if values > MaxAuthorityValues {
			return fmt.Errorf("authority JSON exceeds %d values", MaxAuthorityValues)
		}
		switch typed := token.(type) {
		case json.Delim:
			switch typed {
			case '{':
				seen := map[string]struct{}{}
				for decoder.More() {
					keyToken, err := decoder.Token()
					if err != nil {
						return fmt.Errorf("invalid JSON object: %w", err)
					}
					key, ok := keyToken.(string)
					if !ok {
						return fmt.Errorf("invalid JSON object key")
					}
					if len([]byte(key)) > MaxAuthorityKeyBytes {
						return fmt.Errorf("authority JSON key exceeds %d bytes", MaxAuthorityKeyBytes)
					}
					if _, exists := seen[key]; exists {
						return fmt.Errorf("authority JSON contains duplicate key %q", key)
					}
					seen[key] = struct{}{}
					if err := walk(depth + 1); err != nil {
						return err
					}
				}
				closing, err := decoder.Token()
				if err != nil || closing != json.Delim('}') {
					return fmt.Errorf("invalid JSON object terminator")
				}
			case '[':
				for decoder.More() {
					if err := walk(depth + 1); err != nil {
						return err
					}
				}
				closing, err := decoder.Token()
				if err != nil || closing != json.Delim(']') {
					return fmt.Errorf("invalid JSON array terminator")
				}
			default:
				return fmt.Errorf("unexpected JSON delimiter %q", typed)
			}
		case json.Number:
			text := typed.String()
			if strings.ContainsAny(text, ".eE") {
				return fmt.Errorf("floating authority number %s must be an integer or string", text)
			}
			integer, ok := new(big.Int).SetString(text, 10)
			if !ok || new(big.Int).Abs(integer).Cmp(limit) > 0 {
				return fmt.Errorf("authority integer %s exceeds the RFC 8785 safe range; encode it as a string", text)
			}
		case string:
			if len([]byte(typed)) > MaxAuthorityStringBytes {
				return fmt.Errorf("authority JSON string exceeds %d bytes", MaxAuthorityStringBytes)
			}
		}
		return nil
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("invalid trailing JSON content")
	}
	return nil
}

func RejectSensitiveFields(raw json.RawMessage) error {
	if err := ValidateAuthorityJSON(raw); err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	var walk func(any) error
	walk = func(current any) error {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
				for _, forbidden := range []string{"password", "secret", "token", "apikey", "authorization", "privatekey", "credential", "accesskey", "cookie"} {
					if strings.Contains(normalized, forbidden) {
						return fmt.Errorf("field %s may contain a secret; store an external reference instead", key)
					}
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range typed {
				if err := walk(child); err != nil {
					return err
				}
			}
		case string:
			if err := rejectSensitiveString(typed); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(value)
}

func rejectSensitiveString(value string) error {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "-----begin") && strings.Contains(lower, "private key-----") {
		return fmt.Errorf("value contains private key material; store an external reference instead")
	}
	for _, prefix := range []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "xoxb-", "xoxp-", "xoxa-", "xoxr-"} {
		if strings.HasPrefix(lower, prefix) && len(trimmed) >= len(prefix)+12 {
			return fmt.Errorf("value resembles a secret token; store an external reference instead")
		}
	}
	if strings.HasPrefix(lower, "bearer ") && len(trimmed) >= 20 {
		return fmt.Errorf("value contains a bearer credential; store an external reference instead")
	}
	if strings.HasPrefix(trimmed, "AKIA") && len(trimmed) >= 16 {
		return fmt.Errorf("value resembles an AWS access key; store an external reference instead")
	}
	if strings.HasPrefix(lower, "sk-") && len(trimmed) >= 20 {
		return fmt.Errorf("value resembles an API secret; store an external reference instead")
	}
	if !strings.Contains(trimmed, "://") {
		return nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil
	}
	if parsed.User != nil {
		return fmt.Errorf("URI userinfo may contain credentials; store a credential-free external reference instead")
	}
	for key := range parsed.Query() {
		normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
		if normalized == "sig" {
			return fmt.Errorf("URI query field %s may contain a credential; store a credential-free external reference instead", key)
		}
		for _, forbidden := range []string{"password", "secret", "token", "apikey", "authorization", "credential", "accesskey", "signature"} {
			if strings.Contains(normalized, forbidden) {
				return fmt.Errorf("URI query field %s may contain a credential; store a credential-free external reference instead", key)
			}
		}
	}
	return nil
}

func validatePredicate(p Predicate, source NodeDefinition) error {
	return validatePredicateAtDepth(p, source, 0)
}

func RequiredNodeCapability(kind string) string {
	switch kind {
	case "task":
		return CapabilityNodeRun
	case "review":
		return CapabilityNodeReview
	case "decision":
		return CapabilityNodeDecide
	case "gate":
		return CapabilityNodeGate
	case "effect":
		return CapabilityEffectApply
	default:
		if strings.Contains(kind, ".") {
			return CapabilityNodeRun
		}
		return ""
	}
}

func RoleHasCapability(graph *GraphDefinition, roleID, capability string) bool {
	if graph == nil || roleID == "" || capability == "" {
		return false
	}
	for _, role := range graph.Spec.Roles {
		if role.ID != roleID {
			continue
		}
		for _, candidate := range role.Capabilities {
			if candidate == capability {
				return true
			}
		}
	}
	return false
}

func validatePredicateAtDepth(p Predicate, source NodeDefinition, depth int) error {
	if depth > 64 {
		return fmt.Errorf("predicate nesting exceeds 64 levels")
	}
	branches := 0
	if p.Outcome != "" {
		branches++
		found := false
		for _, outcome := range source.Outcomes {
			found = found || outcome.ID == p.Outcome
		}
		if !found {
			return fmt.Errorf("outcome %s is not declared by source %s", p.Outcome, source.ID)
		}
	}
	if p.Decision != nil {
		branches++
		if p.Decision.Key == "" || p.Decision.Value == "" {
			return fmt.Errorf("decision predicate requires non-empty key and value")
		}
	}
	if p.Evidence != nil {
		branches++
		if p.Evidence.Key == "" || p.Evidence.Value == "" {
			return fmt.Errorf("evidence predicate requires non-empty key and value")
		}
	}
	if p.Policy != nil {
		branches++
		if p.Policy.Key == "" || p.Policy.Value == "" {
			return fmt.Errorf("policy predicate requires non-empty key and value")
		}
	}
	if len(p.All) > 0 {
		branches++
	}
	if len(p.Any) > 0 {
		branches++
	}
	if branches != 1 {
		return fmt.Errorf("predicate must contain exactly one closed operator")
	}
	for _, child := range append(append([]Predicate{}, p.All...), p.Any...) {
		if err := validatePredicateAtDepth(child, source, depth+1); err != nil {
			return err
		}
	}
	return nil
}

type NodeRuntime struct {
	Status       string         `json:"status"`
	Outcome      string         `json:"outcome,omitempty"`
	OutcomeClass string         `json:"outcomeClass,omitempty"`
	Facts        PredicateFacts `json:"facts,omitempty"`
}

type PredicateFacts struct {
	Decision map[string]string `json:"decision,omitempty"`
	Evidence map[string]string `json:"evidence,omitempty"`
	Policy   map[string]string `json:"policy,omitempty"`
}

type Attempt struct {
	ID           string `json:"id"`
	NodeID       string `json:"nodeId"`
	RoleID       string `json:"roleId"`
	Number       int    `json:"number"`
	Status       string `json:"status"`
	Outcome      string `json:"outcome,omitempty"`
	CheckpointID string `json:"checkpointId,omitempty"`
	StartedAt    string `json:"startedAt"`
	UpdatedAt    string `json:"updatedAt"`
}

type RoleLease struct {
	RoleID    string `json:"roleId"`
	Harness   string `json:"harness"`
	SessionID string `json:"sessionId"`
	BoundAt   string `json:"boundAt"`
	ExpiresAt string `json:"expiresAt"`
	Active    bool   `json:"active"`
}

type EvidenceRef struct {
	Digest string `json:"digest"`
	Type   string `json:"type"`
	Size   int64  `json:"size"`
	URI    string `json:"uri,omitempty"`
}

type Checkpoint struct {
	ID           string        `json:"id"`
	AttemptID    string        `json:"attemptId"`
	Summary      string        `json:"summary"`
	EvidenceRefs []EvidenceRef `json:"evidenceRefs,omitempty"`
	CreatedAt    string        `json:"createdAt"`
}

type DecisionProviderBinding struct {
	ID         string `json:"id"`
	Version    string `json:"version"`
	SchemaHash string `json:"schemaHash"`
}

type DecisionRecord struct {
	ID            string                   `json:"id"`
	ProjectID     string                   `json:"projectId"`
	GraphRevision string                   `json:"graphRevision"`
	NodeID        string                   `json:"nodeId"`
	AttemptID     string                   `json:"attemptId"`
	RoleID        string                   `json:"roleId"`
	Key           string                   `json:"key"`
	Source        string                   `json:"source"`
	Outcome       string                   `json:"outcome"`
	Facts         PredicateFacts           `json:"facts,omitempty"`
	EvidenceRefs  []EvidenceRef            `json:"evidenceRefs,omitempty"`
	InputDigest   string                   `json:"inputDigest"`
	Provider      *DecisionProviderBinding `json:"provider,omitempty"`
	CreatedAt     string                   `json:"createdAt"`
	Sequence      uint64                   `json:"sequence,omitempty"`
}

type ActionRecord struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	NodeID    string          `json:"nodeId,omitempty"`
	AttemptID string          `json:"attemptId,omitempty"`
	Status    string          `json:"status"`
	Input     json.RawMessage `json:"input,omitempty"`
	Sequence  uint64          `json:"sequence"`
}

type EffectAction struct {
	ID             string          `json:"id"`
	NodeID         string          `json:"nodeId"`
	AttemptID      string          `json:"attemptId"`
	AdapterID      string          `json:"adapterId"`
	OwnerRole      string          `json:"ownerRole,omitempty"`
	Status         string          `json:"status"`
	Request        json.RawMessage `json:"request"`
	Prepared       json.RawMessage `json:"prepared"`
	Receipt        json.RawMessage `json:"receipt,omitempty"`
	IdempotencyKey string          `json:"idempotencyKey"`
	PreparedAt     string          `json:"preparedAt"`
	UpdatedAt      string          `json:"updatedAt"`
	Sequence       uint64          `json:"sequence"`
}

type ResourceLease struct {
	ID               string          `json:"id"`
	Kind             string          `json:"kind"`
	Quantity         int             `json:"quantity"`
	NodeID           string          `json:"nodeId"`
	AttemptID        string          `json:"attemptId"`
	RoleID           string          `json:"roleId,omitempty"`
	Status           string          `json:"status"`
	ClosureStatus    string          `json:"closureStatus,omitempty"`
	ClosureReceipt   json.RawMessage `json:"closureReceipt,omitempty"`
	ClosureUpdatedAt string          `json:"closureUpdatedAt,omitempty"`
	LeasedAt         string          `json:"leasedAt"`
	ReleasedAt       string          `json:"releasedAt,omitempty"`
}

type Incident struct {
	ID                 string   `json:"id"`
	SourceType         string   `json:"sourceType"`
	SourceID           string   `json:"sourceId"`
	NodeID             string   `json:"nodeId,omitempty"`
	OwnerRole          string   `json:"ownerRole,omitempty"`
	Status             string   `json:"status"`
	Classification     string   `json:"classification"`
	Deadline           string   `json:"deadline,omitempty"`
	AttemptBudget      int      `json:"attemptBudget"`
	Attempts           int      `json:"attempts"`
	NoProgressAttempts int      `json:"noProgressAttempts,omitempty"`
	ProgressMetric     string   `json:"progressMetric,omitempty"`
	LastProgress       string   `json:"lastProgress,omitempty"`
	LastProgressAt     string   `json:"lastProgressAt,omitempty"`
	CircuitReason      string   `json:"circuitReason,omitempty"`
	Resolution         string   `json:"resolution,omitempty"`
	Disposition        string   `json:"disposition,omitempty"`
	DispositionBy      string   `json:"dispositionBy,omitempty"`
	DispositionAt      string   `json:"dispositionAt,omitempty"`
	DependencyCut      []string `json:"dependencyCut,omitempty"`
	OpenedAt           string   `json:"openedAt"`
	UpdatedAt          string   `json:"updatedAt"`
}

// LifecycleMigrationReceipt binds one atomic historical import to the
// authenticated external authority prefix it represents. Source event bodies
// are not duplicated here; their canonical mapping digest is journal-bound.
type LifecycleMigrationReceipt struct {
	ID                  string `json:"id"`
	SourceSystem        string `json:"sourceSystem"`
	SourceProject       string `json:"sourceProject"`
	SourceAuthorityHash string `json:"sourceAuthorityHash"`
	SourceHeadSequence  uint64 `json:"sourceHeadSequence"`
	SourceHeadEventID   string `json:"sourceHeadEventId"`
	SourceHeadEventHash string `json:"sourceHeadEventHash"`
	RecordsDigest       string `json:"recordsDigest"`
	RecordCount         int    `json:"recordCount"`
	NativeEventCount    int    `json:"nativeEventCount"`
	GraphRevision       string `json:"graphRevision"`
	TargetSequence      uint64 `json:"targetSequence"`
	ImportedAt          string `json:"importedAt"`
}

var incidentClassifications = map[string]bool{
	"work-product": true, "policy": true, "fixture": true,
	"infrastructure": true, "evidence": true, "external-effect": true,
	"unknown": true,
}

var incidentDispositions = map[string]bool{
	"retry": true, "rollback": true, "lkg": true, "quarantine": true,
	"off-critical-path": true, "escalate": true,
}

func ValidIncidentClassification(value string) bool { return incidentClassifications[value] }

func ValidIncidentDisposition(value string) bool { return incidentDispositions[value] }

type State struct {
	ProjectID           string                               `json:"projectId"`
	Graph               *GraphDefinition                     `json:"graph,omitempty"`
	GraphRevision       string                               `json:"graphRevision,omitempty"`
	HeadSequence        uint64                               `json:"headSequence"`
	HeadHash            string                               `json:"headHash,omitempty"`
	Nodes               map[string]NodeRuntime               `json:"nodes"`
	Attempts            map[string]Attempt                   `json:"attempts"`
	NodeAttempts        map[string][]string                  `json:"nodeAttempts"`
	Leases              map[string]RoleLease                 `json:"leases"`
	Checkpoints         map[string]Checkpoint                `json:"checkpoints"`
	Decisions           map[string]DecisionRecord            `json:"decisions"`
	AttemptDecisions    map[string][]string                  `json:"attemptDecisions"`
	EvidencePackages    map[string]ExecutionPackage          `json:"evidencePackages"`
	AttemptPackages     map[string][]string                  `json:"attemptPackages"`
	ReuseDecisions      map[string]ReuseDecision             `json:"reuseDecisions"`
	PackageDecisions    map[string][]string                  `json:"packageDecisions"`
	Actions             map[string]ActionRecord              `json:"actions"`
	Effects             map[string]EffectAction              `json:"effects"`
	Resources           map[string]ResourceLease             `json:"resources"`
	Incidents           map[string]Incident                  `json:"incidents"`
	LifecycleMigrations map[string]LifecycleMigrationReceipt `json:"lifecycleMigrations"`
	Commands            map[string]CommandResult             `json:"commands"`
}

type CommandResult struct {
	Kind          string `json:"kind"`
	ActorRole     string `json:"actorRole,omitempty"`
	ObjectRef     string `json:"objectRef,omitempty"`
	RequestDigest string `json:"requestDigest,omitempty"`
	GraphRevision string `json:"graphRevision,omitempty"`
	Sequence      uint64 `json:"sequence"`
}

func NewState(projectID string) State {
	return State{
		ProjectID: projectID, Nodes: map[string]NodeRuntime{}, Attempts: map[string]Attempt{},
		NodeAttempts: map[string][]string{}, Leases: map[string]RoleLease{}, Checkpoints: map[string]Checkpoint{}, Decisions: map[string]DecisionRecord{}, AttemptDecisions: map[string][]string{},
		EvidencePackages: map[string]ExecutionPackage{}, AttemptPackages: map[string][]string{}, ReuseDecisions: map[string]ReuseDecision{}, PackageDecisions: map[string][]string{},
		Actions: map[string]ActionRecord{}, Effects: map[string]EffectAction{}, Resources: map[string]ResourceLease{}, Incidents: map[string]Incident{}, LifecycleMigrations: map[string]LifecycleMigrationReceipt{}, Commands: map[string]CommandResult{},
	}
}

func (s State) LatestExecutionPackage(attemptID string) (ExecutionPackage, bool) {
	ids := s.AttemptPackages[attemptID]
	if len(ids) == 0 {
		return ExecutionPackage{}, false
	}
	value, ok := s.EvidencePackages[ids[len(ids)-1]]
	return value, ok
}

func (s State) EffectForAttempt(attemptID string) (EffectAction, bool) {
	for _, effect := range s.Effects {
		if effect.AttemptID == attemptID {
			return effect, true
		}
	}
	return EffectAction{}, false
}

func (s State) LatestAttempt(nodeID string) (Attempt, bool) {
	ids := s.NodeAttempts[nodeID]
	if len(ids) == 0 {
		return Attempt{}, false
	}
	attempt, ok := s.Attempts[ids[len(ids)-1]]
	return attempt, ok
}

func (s State) NodeDefinition(nodeID string) (NodeDefinition, bool) {
	if s.Graph == nil {
		return NodeDefinition{}, false
	}
	for _, node := range s.Graph.Spec.Nodes {
		if node.ID == nodeID {
			return node, true
		}
	}
	return NodeDefinition{}, false
}

type Frontier struct {
	GraphRevision   string                 `json:"graphRevision"`
	Ready           []string               `json:"ready"`
	Blocked         []string               `json:"blocked,omitempty"`
	Unreachable     []string               `json:"unreachable,omitempty"`
	ResourceBlocked []string               `json:"resourceBlocked,omitempty"`
	DependencyCuts  map[string][]string    `json:"dependencyCuts,omitempty"`
	Explanations    []ReadinessExplanation `json:"explanations,omitempty"`
}

type ReadinessExplanation struct {
	NodeID  string            `json:"nodeId"`
	State   string            `json:"state"`
	Reasons []ReadinessReason `json:"reasons,omitempty"`
}

type ReadinessReason struct {
	Code          string `json:"code"`
	EdgeID        string `json:"edgeId,omitempty"`
	SourceNodeID  string `json:"sourceNodeId,omitempty"`
	PredicatePath string `json:"predicatePath,omitempty"`
	Expected      string `json:"expected,omitempty"`
	Actual        string `json:"actual,omitempty"`
	ResourceKind  string `json:"resourceKind,omitempty"`
	Required      int    `json:"required,omitempty"`
	Available     int    `json:"available,omitempty"`
}

func ComputeFrontier(state State) Frontier {
	result := Frontier{GraphRevision: state.GraphRevision, Ready: []string{}}
	if state.Graph == nil {
		return result
	}
	incoming := map[string][]EdgeDefinition{}
	for _, edge := range state.Graph.Spec.Edges {
		incoming[edge.To] = append(incoming[edge.To], edge)
	}
	for _, node := range state.Graph.Spec.Nodes {
		runtime := state.Nodes[node.ID]
		if runtime.Status != "planned" {
			continue
		}
		reasons := []ReadinessReason{}
		unreachable := false
		for _, edge := range incoming[node.ID] {
			source := state.Nodes[edge.From]
			if source.Status != "terminal" && source.Status != "skipped" {
				reasons = append(reasons, ReadinessReason{Code: "source_not_terminal", EdgeID: edge.ID, SourceNodeID: edge.From, Expected: "terminal", Actual: source.Status})
				continue
			}
			if ok, detail := explainPredicate(edge.When, source, "$"); !ok {
				detail.EdgeID, detail.SourceNodeID = edge.ID, edge.From
				reasons = append(reasons, detail)
				unreachable = true
			}
		}
		if unreachable {
			result.Unreachable = append(result.Unreachable, node.ID)
			result.Explanations = append(result.Explanations, ReadinessExplanation{NodeID: node.ID, State: "unreachable", Reasons: reasons})
		} else if len(reasons) == 0 {
			resourceReasons := resourceReadinessReasons(state, node)
			if len(resourceReasons) == 0 {
				result.Ready = append(result.Ready, node.ID)
				result.Explanations = append(result.Explanations, ReadinessExplanation{NodeID: node.ID, State: "ready"})
			} else {
				result.Blocked = append(result.Blocked, node.ID)
				result.ResourceBlocked = append(result.ResourceBlocked, node.ID)
				result.Explanations = append(result.Explanations, ReadinessExplanation{NodeID: node.ID, State: "blocked", Reasons: resourceReasons})
			}
		} else {
			result.Blocked = append(result.Blocked, node.ID)
			result.Explanations = append(result.Explanations, ReadinessExplanation{NodeID: node.ID, State: "blocked", Reasons: reasons})
		}
	}
	sort.Strings(result.Ready)
	sort.Strings(result.Blocked)
	sort.Strings(result.Unreachable)
	sort.Strings(result.ResourceBlocked)
	sort.Slice(result.Explanations, func(i, j int) bool { return result.Explanations[i].NodeID < result.Explanations[j].NodeID })
	for nodeID, runtime := range state.Nodes {
		if runtime.Status != "terminal" || (runtime.OutcomeClass != "failure" && runtime.OutcomeClass != "cancelled") {
			continue
		}
		cut := DependencyCut(state, nodeID)
		if len(cut) > 0 {
			if result.DependencyCuts == nil {
				result.DependencyCuts = map[string][]string{}
			}
			result.DependencyCuts[nodeID] = cut
		}
	}
	return result
}

func DependencyCut(state State, rootNodeID string) []string {
	if state.Graph == nil {
		return nil
	}
	outgoing := map[string][]EdgeDefinition{}
	for _, edge := range state.Graph.Spec.Edges {
		outgoing[edge.From] = append(outgoing[edge.From], edge)
	}
	seen := map[string]bool{}
	queue := []string{rootNodeID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, edge := range outgoing[current] {
			if current == rootNodeID && predicateSatisfied(edge.When, state.Nodes[current]) {
				continue
			}
			runtime := state.Nodes[edge.To]
			if runtime.Status == "terminal" || runtime.Status == "superseded" || runtime.Status == "skipped" || seen[edge.To] {
				continue
			}
			seen[edge.To] = true
			queue = append(queue, edge.To)
		}
	}
	result := make([]string, 0, len(seen))
	for nodeID := range seen {
		result = append(result, nodeID)
	}
	sort.Strings(result)
	return result
}

func resourceReadinessReasons(state State, node NodeDefinition) []ReadinessReason {
	if len(node.Resources) == 0 {
		return nil
	}
	capacity := map[string]int{}
	for _, declaration := range state.Graph.Spec.ResourceCapacities {
		capacity[declaration.Kind] = declaration.Capacity
	}
	for _, lease := range state.Resources {
		if lease.Status == "active" {
			capacity[lease.Kind] -= lease.Quantity
		}
	}
	reasons := []ReadinessReason{}
	for _, request := range node.Resources {
		if capacity[request.Kind] < request.Quantity {
			reasons = append(reasons, ReadinessReason{Code: "resource_capacity_exhausted", ResourceKind: request.Kind, Required: request.Quantity, Available: capacity[request.Kind]})
		}
	}
	return reasons
}

func predicateSatisfied(p Predicate, source NodeRuntime) bool {
	ok, _ := explainPredicate(p, source, "$")
	return ok
}

func explainPredicate(p Predicate, source NodeRuntime, path string) (bool, ReadinessReason) {
	if p.Outcome != "" {
		ok := source.Outcome == p.Outcome
		return ok, ReadinessReason{Code: "predicate_unsatisfied", PredicatePath: path + ".outcome", Expected: p.Outcome, Actual: source.Outcome}
	}
	if p.Decision != nil {
		actual := source.Facts.Decision[p.Decision.Key]
		return actual == p.Decision.Value, ReadinessReason{Code: "predicate_unsatisfied", PredicatePath: path + ".decision." + p.Decision.Key, Expected: p.Decision.Value, Actual: actual}
	}
	if p.Evidence != nil {
		actual := source.Facts.Evidence[p.Evidence.Key]
		return actual == p.Evidence.Value, ReadinessReason{Code: "predicate_unsatisfied", PredicatePath: path + ".evidence." + p.Evidence.Key, Expected: p.Evidence.Value, Actual: actual}
	}
	if p.Policy != nil {
		actual := source.Facts.Policy[p.Policy.Key]
		return actual == p.Policy.Value, ReadinessReason{Code: "predicate_unsatisfied", PredicatePath: path + ".policy." + p.Policy.Key, Expected: p.Policy.Value, Actual: actual}
	}
	if len(p.All) > 0 {
		for index, child := range p.All {
			if ok, detail := explainPredicate(child, source, fmt.Sprintf("%s.all[%d]", path, index)); !ok {
				return false, detail
			}
		}
		return true, ReadinessReason{}
	}
	if len(p.Any) > 0 {
		for index, child := range p.Any {
			if ok, _ := explainPredicate(child, source, fmt.Sprintf("%s.any[%d]", path, index)); ok {
				return true, ReadinessReason{}
			}
		}
		return false, ReadinessReason{Code: "predicate_any_unsatisfied", PredicatePath: path + ".any", Expected: "at least one branch true", Actual: "all branches false"}
	}
	return false, ReadinessReason{Code: "predicate_invalid", PredicatePath: path}
}
