package grouping

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/gowebpki/jcs"
)

type Projection struct {
	ProjectID        string          `json:"projectId"`
	GraphRevision    string          `json:"graphRevision"`
	JournalHead      string          `json:"journalHead"`
	MembershipDigest string          `json:"membershipDigest"`
	ProjectionDigest string          `json:"projectionDigest"`
	Groups           []GroupSummary  `json:"groups"`
	VisibleGroupIDs  []string        `json:"visibleGroupIds"`
	VisibleNodes     []string        `json:"visibleNodes"`
	Lanes            []Lane          `json:"lanes"`
	AggregateEdges   []AggregateEdge `json:"aggregateEdges"`
}

type Lane struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	GroupRefs []string `json:"groupRefs"`
	NodeRefs  []string `json:"nodeRefs"`
}

type GroupSummary struct {
	ID                    string   `json:"id"`
	Title                 string   `json:"title"`
	Kind                  string   `json:"kind"`
	LaneID                string   `json:"laneId"`
	ParentGroupID         string   `json:"parentGroupId,omitempty"`
	SummaryNodeID         string   `json:"summaryNodeId,omitempty"`
	CollapsedByDefault    bool     `json:"collapsedByDefault"`
	Visible               bool     `json:"visible"`
	Expanded              bool     `json:"expanded"`
	Breadcrumbs           []string `json:"breadcrumbs"`
	ChildGroupIDs         []string `json:"childGroupIds"`
	LifecycleStatus       string   `json:"lifecycleStatus"`
	HealthStatus          string   `json:"healthStatus"`
	DisplayStatus         string   `json:"displayStatus"`
	SummaryOutcome        string   `json:"summaryOutcome,omitempty"`
	SummaryOutcomeClass   string   `json:"summaryOutcomeClass,omitempty"`
	NodeCount             int      `json:"nodeCount"`
	DirectNodeCount       int      `json:"directNodeCount"`
	ReadyNodeCount        int      `json:"readyNodeCount"`
	ActiveNodeCount       int      `json:"activeNodeCount"`
	AttemptCount          int      `json:"attemptCount"`
	ActiveAttemptCount    int      `json:"activeAttemptCount"`
	CurrentCycle          int      `json:"currentCycle"`
	OpenIncidentCount     int      `json:"openIncidentCount"`
	UncertainEffectCount  int      `json:"uncertainEffectCount"`
	UnclosedResourceCount int      `json:"unclosedResourceCount"`
	MembershipDigest      string   `json:"membershipDigest"`
}

type AggregateEdge struct {
	FromRef       string   `json:"fromRef"`
	ToRef         string   `json:"toRef"`
	PredicateKind string   `json:"predicateKind"`
	OutcomeClass  string   `json:"outcomeClass,omitempty"`
	Count         int      `json:"count"`
	EdgeIDs       []string `json:"edgeIds"`
	EdgeDigest    string   `json:"edgeDigest"`
	InspectRef    string   `json:"inspectRef"`
}

type groupIndex struct {
	definitions map[string]domain.GroupDefinition
	parents     map[string]string
	children    map[string][]string
	members     map[string][]string
	direct      map[string][]string
	expanded    map[string]bool
	visible     map[string]bool
}

func Build(state domain.State, expandedOverrides map[string]bool) (Projection, error) {
	result := Projection{
		ProjectID: state.ProjectID, GraphRevision: state.GraphRevision, JournalHead: state.HeadHash,
		Groups: []GroupSummary{}, VisibleGroupIDs: []string{}, VisibleNodes: []string{}, Lanes: []Lane{}, AggregateEdges: []AggregateEdge{},
	}
	if state.Graph == nil {
		result.MembershipDigest = digestValue([]string{})
		result.ProjectionDigest = digestProjection(result)
		return result, nil
	}
	index := buildGroupIndex(*state.Graph, expandedOverrides)
	frontier := domain.ComputeFrontier(state)
	ready := stringSet(frontier.Ready)
	blocked := stringSet(frontier.Blocked)
	unreachable := stringSet(frontier.Unreachable)
	resourceBlocked := stringSet(frontier.ResourceBlocked)

	groupIDs := make([]string, 0, len(index.definitions))
	allMembership := make([]string, 0, len(state.Graph.Spec.Nodes))
	for _, node := range state.Graph.Spec.Nodes {
		allMembership = append(allMembership, node.ID+"\x00"+node.GroupID)
	}
	sort.Strings(allMembership)
	result.MembershipDigest = digestValue(allMembership)
	for groupID := range index.definitions {
		groupIDs = append(groupIDs, groupID)
	}
	sort.Strings(groupIDs)
	for _, groupID := range groupIDs {
		summary := summarizeGroup(state, index, groupID, ready, blocked, unreachable, resourceBlocked)
		result.Groups = append(result.Groups, summary)
		if summary.Visible {
			result.VisibleGroupIDs = append(result.VisibleGroupIDs, groupID)
		}
	}

	for _, node := range state.Graph.Spec.Nodes {
		if visibleEndpoint(index, node.GroupID) == "node:" {
			result.VisibleNodes = append(result.VisibleNodes, node.ID)
		}
	}
	sort.Strings(result.VisibleNodes)
	result.Lanes = buildLanes(*state.Graph, index, result.VisibleGroupIDs, result.VisibleNodes)
	result.AggregateEdges = aggregateEdges(*state.Graph, index)
	result.ProjectionDigest = digestProjection(result)
	return result, nil
}

func buildLanes(graph domain.GraphDefinition, index groupIndex, visibleGroupIDs, visibleNodeIDs []string) []Lane {
	type orderedLane struct {
		Lane
		order int
	}
	lanes := []orderedLane{
		{Lane: Lane{ID: "work-units", Title: "Work units", GroupRefs: []string{}, NodeRefs: []string{}}, order: 100},
		{Lane: Lane{ID: "milestones-gates", Title: "Milestones & gates", GroupRefs: []string{}, NodeRefs: []string{}}, order: 200},
		{Lane: Lane{ID: "external-actions", Title: "External actions", GroupRefs: []string{}, NodeRefs: []string{}}, order: 300},
		{Lane: Lane{ID: "global-governance", Title: "Global governance", GroupRefs: []string{}, NodeRefs: []string{}}, order: 400},
		{Lane: Lane{ID: "ungrouped", Title: "Ungrouped", GroupRefs: []string{}, NodeRefs: []string{}}, order: 1000},
	}
	indices := map[string]int{}
	for i := range lanes {
		indices[lanes[i].ID] = i
	}
	for _, declaration := range graph.Spec.Lanes {
		if index, exists := indices[declaration.ID]; exists {
			lanes[index].Title = declaration.Title
			lanes[index].order = declaration.Order
			continue
		}
		lanes = append(lanes, orderedLane{Lane: Lane{ID: declaration.ID, Title: declaration.Title, GroupRefs: []string{}, NodeRefs: []string{}}, order: declaration.Order})
		indices[declaration.ID] = len(lanes) - 1
	}
	byID := map[string]*Lane{}
	for i := range lanes {
		byID[lanes[i].ID] = &lanes[i].Lane
	}
	for _, groupID := range visibleGroupIDs {
		laneID := groupLaneID(index, groupID)
		byID[laneID].GroupRefs = append(byID[laneID].GroupRefs, "group:"+groupID)
	}
	visibleNodes := stringSet(visibleNodeIDs)
	for _, node := range graph.Spec.Nodes {
		if !visibleNodes[node.ID] {
			continue
		}
		laneID := node.LaneID
		if laneID == "" && node.GroupID != "" {
			laneID = groupLaneID(index, node.GroupID)
		}
		if laneID == "" {
			laneID = nodeKindLane(node.Kind)
		}
		byID[laneID].NodeRefs = append(byID[laneID].NodeRefs, "node:"+node.ID)
	}
	sort.SliceStable(lanes, func(i, j int) bool {
		if lanes[i].order == lanes[j].order {
			return lanes[i].ID < lanes[j].ID
		}
		return lanes[i].order < lanes[j].order
	})
	result := make([]Lane, 0, len(lanes))
	for _, ordered := range lanes {
		lane := ordered.Lane
		sort.Strings(lane.GroupRefs)
		sort.Strings(lane.NodeRefs)
		if len(lane.GroupRefs)+len(lane.NodeRefs) > 0 {
			result = append(result, lane)
		}
	}
	return result
}

func groupLaneID(index groupIndex, groupID string) string {
	root := index.definitions[groupID]
	for current := groupID; current != ""; current = index.parents[current] {
		group := index.definitions[current]
		root = group
		if group.LaneID != "" {
			return group.LaneID
		}
	}
	switch root.Kind {
	case "milestone", "gate":
		return "milestones-gates"
	case "external":
		return "external-actions"
	case "governance":
		return "global-governance"
	default:
		return "work-units"
	}
}

func nodeKindLane(kind string) string {
	switch kind {
	case "milestone", "gate", "join":
		return "milestones-gates"
	case "effect":
		return "external-actions"
	default:
		return "ungrouped"
	}
}

func buildGroupIndex(graph domain.GraphDefinition, overrides map[string]bool) groupIndex {
	index := groupIndex{
		definitions: map[string]domain.GroupDefinition{}, parents: map[string]string{}, children: map[string][]string{},
		members: map[string][]string{}, direct: map[string][]string{}, expanded: map[string]bool{}, visible: map[string]bool{},
	}
	for _, group := range graph.Spec.Groups {
		index.definitions[group.ID] = group
		index.parents[group.ID] = group.ParentGroupID
		index.children[group.ParentGroupID] = append(index.children[group.ParentGroupID], group.ID)
		index.expanded[group.ID] = !group.CollapsedByDefault
		if value, exists := overrides[group.ID]; exists {
			index.expanded[group.ID] = value
		}
	}
	for parent := range index.children {
		sort.Strings(index.children[parent])
	}
	for _, group := range graph.Spec.Groups {
		visible := true
		for parent := group.ParentGroupID; parent != ""; parent = index.parents[parent] {
			if !index.expanded[parent] {
				visible = false
				break
			}
		}
		index.visible[group.ID] = visible
	}
	for _, node := range graph.Spec.Nodes {
		if node.GroupID == "" {
			continue
		}
		index.direct[node.GroupID] = append(index.direct[node.GroupID], node.ID)
		for groupID := node.GroupID; groupID != ""; groupID = index.parents[groupID] {
			index.members[groupID] = append(index.members[groupID], node.ID)
		}
	}
	for groupID := range index.definitions {
		sort.Strings(index.direct[groupID])
		sort.Strings(index.members[groupID])
	}
	return index
}

func summarizeGroup(state domain.State, index groupIndex, groupID string, ready, blocked, unreachable, resourceBlocked map[string]bool) GroupSummary {
	group := index.definitions[groupID]
	members := index.members[groupID]
	summary := GroupSummary{
		ID: group.ID, Title: group.Title, Kind: group.Kind, LaneID: groupLaneID(index, groupID), ParentGroupID: group.ParentGroupID,
		SummaryNodeID: group.SummaryNodeID, CollapsedByDefault: group.CollapsedByDefault,
		Visible: index.visible[groupID], Expanded: index.visible[groupID] && index.expanded[groupID],
		Breadcrumbs: breadcrumbs(index, groupID), ChildGroupIDs: append([]string{}, index.children[groupID]...),
		NodeCount: len(members), DirectNodeCount: len(index.direct[groupID]), MembershipDigest: digestValue(members),
	}
	memberSet := stringSet(members)
	hasPlanned, hasFailure, hasBlocked, allClosed := false, false, false, len(members) > 0
	for _, nodeID := range members {
		runtime := state.Nodes[nodeID]
		summary.ReadyNodeCount += boolInt(ready[nodeID])
		summary.ActiveNodeCount += boolInt(runtime.Status == "active")
		hasPlanned = hasPlanned || runtime.Status == "planned"
		hasFailure = hasFailure || (runtime.Status == "terminal" && (runtime.OutcomeClass == "failure" || runtime.OutcomeClass == "cancelled"))
		hasBlocked = hasBlocked || blocked[nodeID] || resourceBlocked[nodeID] || unreachable[nodeID]
		allClosed = allClosed && (runtime.Status == "terminal" || runtime.Status == "superseded" || runtime.Status == "skipped")
	}
	for _, attempt := range state.Attempts {
		if !memberSet[attempt.NodeID] {
			continue
		}
		summary.AttemptCount++
		if attempt.Number > summary.CurrentCycle {
			summary.CurrentCycle = attempt.Number
		}
		if attempt.Status != "terminal" {
			summary.ActiveAttemptCount++
		}
	}
	for _, incident := range state.Incidents {
		if memberSet[incident.NodeID] && incident.Status != "resolved" {
			summary.OpenIncidentCount++
		}
	}
	for _, effect := range state.Effects {
		if memberSet[effect.NodeID] && (effect.Status == "unknown" || effect.Status == "failed") {
			summary.UncertainEffectCount++
		}
	}
	for _, resource := range state.Resources {
		if memberSet[resource.NodeID] && (resource.Status != "released" || resource.ClosureStatus != "confirmed") {
			summary.UnclosedResourceCount++
		}
	}

	if group.SummaryNodeID != "" {
		runtime := state.Nodes[group.SummaryNodeID]
		summary.SummaryOutcome, summary.SummaryOutcomeClass = runtime.Outcome, runtime.OutcomeClass
		summary.LifecycleStatus = nodeLifecycle(runtime, group.SummaryNodeID, ready, blocked, unreachable, resourceBlocked)
	} else {
		switch {
		case summary.ActiveNodeCount > 0:
			summary.LifecycleStatus = "active"
		case summary.ReadyNodeCount > 0:
			summary.LifecycleStatus = "ready"
		case hasFailure, hasBlocked:
			summary.LifecycleStatus = "blocked"
		case allClosed:
			summary.LifecycleStatus = "completed"
		case hasPlanned:
			summary.LifecycleStatus = "planned"
		default:
			summary.LifecycleStatus = "empty"
		}
	}
	switch {
	case summary.OpenIncidentCount > 0 || hasFailure:
		summary.HealthStatus = "blocked"
	case summary.UncertainEffectCount > 0 || summary.UnclosedResourceCount > 0:
		summary.HealthStatus = "attention"
	default:
		summary.HealthStatus = "clean"
	}
	summary.DisplayStatus = summary.LifecycleStatus
	if summary.HealthStatus != "clean" {
		summary.DisplayStatus = summary.HealthStatus
	}
	return summary
}

func nodeLifecycle(runtime domain.NodeRuntime, nodeID string, ready, blocked, unreachable, resourceBlocked map[string]bool) string {
	switch runtime.Status {
	case "terminal":
		if runtime.Outcome != "" {
			return runtime.Outcome
		}
		return "terminal"
	case "active", "superseded", "skipped":
		return runtime.Status
	case "planned":
		switch {
		case ready[nodeID]:
			return "ready"
		case unreachable[nodeID]:
			return "unreachable"
		case resourceBlocked[nodeID], blocked[nodeID]:
			return "blocked"
		default:
			return "planned"
		}
	default:
		return "planned"
	}
}

func aggregateEdges(graph domain.GraphDefinition, index groupIndex) []AggregateEdge {
	type key struct{ from, to, predicate, outcomeClass string }
	values := map[key][]string{}
	nodeOutcomes := map[string]map[string]string{}
	for _, node := range graph.Spec.Nodes {
		nodeOutcomes[node.ID] = map[string]string{}
		for _, outcome := range node.Outcomes {
			nodeOutcomes[node.ID][outcome.ID] = outcome.Class
		}
	}
	nodeGroups := map[string]string{}
	for _, node := range graph.Spec.Nodes {
		nodeGroups[node.ID] = node.GroupID
	}
	for _, edge := range graph.Spec.Edges {
		from := visibleEndpoint(index, nodeGroups[edge.From])
		if strings.HasPrefix(from, "node:") {
			from = "node:" + edge.From
		}
		to := visibleEndpoint(index, nodeGroups[edge.To])
		if strings.HasPrefix(to, "node:") {
			to = "node:" + edge.To
		}
		if from == to {
			continue
		}
		predicate, classes := predicateDescriptor(edge.When, nodeOutcomes[edge.From])
		item := key{from: from, to: to, predicate: predicate, outcomeClass: strings.Join(classes, "+")}
		values[item] = append(values[item], edge.ID)
	}
	keys := make([]key, 0, len(values))
	for item := range values {
		keys = append(keys, item)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		return a.from+"\x00"+a.to+"\x00"+a.predicate+"\x00"+a.outcomeClass < b.from+"\x00"+b.to+"\x00"+b.predicate+"\x00"+b.outcomeClass
	})
	result := make([]AggregateEdge, 0, len(keys))
	for _, item := range keys {
		ids := values[item]
		sort.Strings(ids)
		digest := digestValue(ids)
		result = append(result, AggregateEdge{FromRef: item.from, ToRef: item.to, PredicateKind: item.predicate, OutcomeClass: item.outcomeClass, Count: len(ids), EdgeIDs: ids, EdgeDigest: digest, InspectRef: "group-edges:" + strings.TrimPrefix(digest, "sha256:")})
	}
	return result
}

func visibleEndpoint(index groupIndex, groupID string) string {
	if groupID == "" {
		return "node:"
	}
	for current := groupID; current != ""; current = index.parents[current] {
		if !index.expanded[current] {
			return "group:" + current
		}
	}
	return "node:"
}

func predicateDescriptor(predicate domain.Predicate, outcomes map[string]string) (string, []string) {
	kind := "unknown"
	classes := []string{}
	switch {
	case predicate.Outcome != "":
		kind = "outcome"
		if class := outcomes[predicate.Outcome]; class != "" {
			classes = append(classes, class)
		}
	case predicate.Decision != nil:
		kind = "decision"
	case predicate.Evidence != nil:
		kind = "evidence"
	case predicate.Policy != nil:
		kind = "policy"
	case len(predicate.All) > 0:
		kind = "all"
		for _, child := range predicate.All {
			_, childClasses := predicateDescriptor(child, outcomes)
			classes = append(classes, childClasses...)
		}
	case len(predicate.Any) > 0:
		kind = "any"
		for _, child := range predicate.Any {
			_, childClasses := predicateDescriptor(child, outcomes)
			classes = append(classes, childClasses...)
		}
	}
	sort.Strings(classes)
	classes = unique(classes)
	return kind, classes
}

func breadcrumbs(index groupIndex, groupID string) []string {
	result := []string{}
	for current := groupID; current != ""; current = index.parents[current] {
		result = append(result, current)
	}
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func unique(values []string) []string {
	if len(values) == 0 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func digestProjection(value Projection) string {
	value.ProjectionDigest = ""
	return digestValue(value)
}

func digestValue(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("encode deterministic group projection: %v", err))
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		panic(fmt.Sprintf("canonicalize deterministic group projection: %v", err))
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:])
}
