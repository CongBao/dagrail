package ui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/grouping"
	"github.com/CongBao/dagrail/internal/service"
)

const (
	explorerAPIVersion   = "dagrail.io/ui/v1beta2"
	maxAPIResponse       = 2 * 1024 * 1024
	maxNodePage          = 200
	maxTopologyNodes     = 500
	maxHistoryPage       = 100
	maxOperationsPage    = 200
	maxNodeDetailItems   = 100
	maxGroupEdgePage     = 100
	maxAggregateEdgePage = 100
)

type ExplorerOverview struct {
	APIVersion    string              `json:"apiVersion"`
	ReadOnly      bool                `json:"readOnly"`
	Project       map[string]string   `json:"project"`
	GraphRevision string              `json:"graphRevision,omitempty"`
	HeadSequence  uint64              `json:"headSequence"`
	Counts        map[string]int      `json:"counts"`
	Frontier      FrontierSummary     `json:"frontier"`
	Facets        map[string][]string `json:"facets"`
}

type FrontierSummary struct {
	Ready           []string `json:"ready"`
	ReadyCount      int      `json:"readyCount"`
	ReadyTruncated  bool     `json:"readyTruncated"`
	BlockedCount    int      `json:"blockedCount"`
	ResourceBlocked int      `json:"resourceBlockedCount"`
	DependencyCuts  int      `json:"dependencyCutCount"`
}

type PageInfo struct {
	Cursor     int  `json:"cursor"`
	Limit      int  `json:"limit"`
	Total      int  `json:"total"`
	NextCursor *int `json:"nextCursor,omitempty"`
	Truncated  bool `json:"truncated"`
}

type NodePage struct {
	APIVersion    string     `json:"apiVersion"`
	GraphRevision string     `json:"graphRevision"`
	HeadSequence  uint64     `json:"headSequence"`
	Page          PageInfo   `json:"page"`
	Nodes         []NodeView `json:"nodes"`
}

type TopologyPage struct {
	APIVersion            string                  `json:"apiVersion"`
	GraphRevision         string                  `json:"graphRevision"`
	HeadSequence          uint64                  `json:"headSequence"`
	Mode                  string                  `json:"mode"`
	Focus                 string                  `json:"focus,omitempty"`
	Depth                 int                     `json:"depth"`
	Page                  PageInfo                `json:"page"`
	Nodes                 []NodeView              `json:"nodes"`
	Edges                 []domain.EdgeDefinition `json:"edges"`
	Groups                []GroupView             `json:"groups,omitempty"`
	Lanes                 []grouping.Lane         `json:"lanes,omitempty"`
	AggregateEdges        []AggregateEdgeView     `json:"aggregateEdges,omitempty"`
	AggregateEdgePage     *PageInfo               `json:"aggregateEdgePage,omitempty"`
	AggregateEdgeIndexRef string                  `json:"aggregateEdgeIndexRef,omitempty"`
	ExpandedGroupIDs      []string                `json:"expandedGroupIds,omitempty"`
	MembershipDigest      string                  `json:"membershipDigest,omitempty"`
	ProjectionDigest      string                  `json:"projectionDigest,omitempty"`
}

type GroupView struct {
	grouping.GroupSummary
	InternalMatchCount int `json:"internalMatchCount,omitempty"`
}

type AggregateEdgeView struct {
	FromRef       string `json:"fromRef"`
	ToRef         string `json:"toRef"`
	PredicateKind string `json:"predicateKind"`
	OutcomeClass  string `json:"outcomeClass,omitempty"`
	Count         int    `json:"count"`
	EdgeDigest    string `json:"edgeDigest"`
	InspectRef    string `json:"inspectRef"`
}

type GroupEdgePage struct {
	APIVersion    string   `json:"apiVersion"`
	GraphRevision string   `json:"graphRevision"`
	HeadSequence  uint64   `json:"headSequence"`
	Ref           string   `json:"ref"`
	Page          PageInfo `json:"page"`
	EdgeIDs       []string `json:"edgeIds"`
}

type AggregateEdgePage struct {
	APIVersion     string              `json:"apiVersion"`
	GraphRevision  string              `json:"graphRevision"`
	HeadSequence   uint64              `json:"headSequence"`
	Ref            string              `json:"ref"`
	Page           PageInfo            `json:"page"`
	AggregateEdges []AggregateEdgeView `json:"aggregateEdges"`
}

type NodeContractView struct {
	ID          string                   `json:"id"`
	Title       string                   `json:"title"`
	Kind        string                   `json:"kind"`
	Role        string                   `json:"role,omitempty"`
	Objective   string                   `json:"objective,omitempty"`
	Parent      string                   `json:"parent,omitempty"`
	GroupID     string                   `json:"groupId,omitempty"`
	Supersedes  string                   `json:"supersedes,omitempty"`
	Outcomes    []domain.Outcome         `json:"outcomes"`
	RetryBudget int                      `json:"retryBudget"`
	Resources   []domain.ResourceRequest `json:"resources"`
	InputBytes  int                      `json:"inputBytes"`
	InputSHA256 string                   `json:"inputSha256,omitempty"`
}

type EvidenceIndexView struct {
	ID         string `json:"id"`
	AttemptID  string `json:"attemptId"`
	CoreDigest string `json:"coreDigest"`
	CreatedAt  string `json:"createdAt"`
}

type DecisionView struct {
	ID              string `json:"id"`
	NodeID          string `json:"nodeId"`
	AttemptID       string `json:"attemptId"`
	RoleID          string `json:"roleId"`
	Key             string `json:"key"`
	Source          string `json:"source"`
	Outcome         string `json:"outcome"`
	ProviderID      string `json:"providerId,omitempty"`
	ProviderVersion string `json:"providerVersion,omitempty"`
	CreatedAt       string `json:"createdAt"`
}

type EffectView struct {
	ID                string `json:"id"`
	NodeID            string `json:"nodeId"`
	AttemptID         string `json:"attemptId"`
	AdapterID         string `json:"adapterId"`
	AdapterVersion    string `json:"adapterVersion,omitempty"`
	AdapterSchemaHash string `json:"adapterSchemaHash,omitempty"`
	OwnerRole         string `json:"ownerRole,omitempty"`
	Status            string `json:"status"`
	TransportStatus   string `json:"transportStatus,omitempty"`
	SessionStatus     string `json:"sessionStatus,omitempty"`
	DeliveryStatus    string `json:"deliveryStatus,omitempty"`
	AcceptanceStatus  string `json:"acceptanceStatus,omitempty"`
	CompletionStatus  string `json:"completionStatus,omitempty"`
	PreparedAt        string `json:"preparedAt"`
	UpdatedAt         string `json:"updatedAt"`
}

type ResourceView struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	Quantity         int    `json:"quantity"`
	NodeID           string `json:"nodeId"`
	AttemptID        string `json:"attemptId"`
	RoleID           string `json:"roleId,omitempty"`
	Status           string `json:"status"`
	ClosureStatus    string `json:"closureStatus,omitempty"`
	ClosureUpdatedAt string `json:"closureUpdatedAt,omitempty"`
	LeasedAt         string `json:"leasedAt"`
	ReleasedAt       string `json:"releasedAt,omitempty"`
}

type NodeDetail struct {
	APIVersion    string                       `json:"apiVersion"`
	GraphRevision string                       `json:"graphRevision"`
	HeadSequence  uint64                       `json:"headSequence"`
	Counts        map[string]int               `json:"counts"`
	Truncated     map[string]bool              `json:"truncated"`
	Contract      NodeContractView             `json:"contract"`
	Runtime       domain.NodeRuntime           `json:"runtime"`
	Readiness     *domain.ReadinessExplanation `json:"readiness,omitempty"`
	Incoming      []domain.EdgeDefinition      `json:"incoming"`
	Outgoing      []domain.EdgeDefinition      `json:"outgoing"`
	Attempts      []domain.Attempt             `json:"attempts"`
	Checkpoints   []domain.Checkpoint          `json:"checkpoints"`
	Evidence      []EvidenceIndexView          `json:"evidence"`
	Decisions     []DecisionView               `json:"decisions"`
	Incidents     []domain.Incident            `json:"incidents"`
	Resources     []ResourceView               `json:"resources"`
	Effects       []EffectView                 `json:"effects"`
}

type HistoryResponse struct {
	APIVersion    string              `json:"apiVersion"`
	GraphRevision string              `json:"graphRevision"`
	HeadSequence  uint64              `json:"headSequence"`
	Before        uint64              `json:"before"`
	OlderBefore   *uint64             `json:"olderBefore,omitempty"`
	NewerBefore   *uint64             `json:"newerBefore,omitempty"`
	Page          service.HistoryPage `json:"page"`
}

type OperationsResponse struct {
	APIVersion    string             `json:"apiVersion"`
	GraphRevision string             `json:"graphRevision"`
	HeadSequence  uint64             `json:"headSequence"`
	Limit         int                `json:"limit"`
	Counts        map[string]int     `json:"counts"`
	Truncated     map[string]bool    `json:"truncated"`
	Attempts      []domain.Attempt   `json:"attempts"`
	Leases        []domain.RoleLease `json:"leases"`
	Incidents     []domain.Incident  `json:"incidents"`
	Resources     []ResourceView     `json:"resources"`
	Effects       []EffectView       `json:"effects"`
}

type nodeFilter struct {
	Query  string
	Status string
	Kind   string
	Role   string
}

func registerExplorerAPI(mux *http.ServeMux, svc *service.Service) {
	mux.HandleFunc("/api/v1/overview", apiEndpoint(func(w http.ResponseWriter, r *http.Request) error {
		if err := requireQuery(r, nil); err != nil {
			return err
		}
		value, err := buildOverview(svc)
		return writeAPIResult(w, value, err)
	}))
	mux.HandleFunc("/api/v1/nodes", apiEndpoint(func(w http.ResponseWriter, r *http.Request) error {
		filter, err := parseNodeFilter(r, map[string]bool{"q": true, "status": true, "kind": true, "role": true, "cursor": true, "limit": true})
		if err != nil {
			return err
		}
		cursor, err := queryInt(r, "cursor", 0, 0, 1_000_000_000)
		if err != nil {
			return err
		}
		limit, err := queryInt(r, "limit", 100, 1, maxNodePage)
		if err != nil {
			return err
		}
		value, err := buildNodePage(svc, filter, cursor, limit)
		return writeAPIResult(w, value, err)
	}))
	mux.HandleFunc("/api/v1/topology", apiEndpoint(func(w http.ResponseWriter, r *http.Request) error {
		filter, err := parseNodeFilter(r, map[string]bool{"q": true, "status": true, "kind": true, "role": true, "focus": true, "depth": true, "limit": true, "mode": true, "groupState": true, "expanded": true, "collapsed": true})
		if err != nil {
			return err
		}
		focus, err := queryString(r, "focus", 4096)
		if err != nil {
			return err
		}
		depth, err := queryInt(r, "depth", 2, 0, 4)
		if err != nil {
			return err
		}
		limit, err := queryInt(r, "limit", 200, 1, maxTopologyNodes)
		if err != nil {
			return err
		}
		mode, err := queryString(r, "mode", 16)
		if err != nil || (mode != "" && mode != "summary" && mode != "detail") {
			return clientError(http.StatusBadRequest, "mode must be summary or detail")
		}
		groupState, err := queryGroupState(r)
		if err != nil {
			return err
		}
		expanded, err := queryStrings(r, "expanded", 256, 4096, 16*1024)
		if err != nil {
			return err
		}
		collapsed, err := queryStrings(r, "collapsed", 256, 4096, 16*1024)
		if err != nil {
			return err
		}
		value, err := buildTopology(svc, filter, focus, depth, limit, mode, groupState, expanded, collapsed)
		return writeAPIResult(w, value, err)
	}))
	mux.HandleFunc("/api/v1/node", apiEndpoint(func(w http.ResponseWriter, r *http.Request) error {
		if err := requireQuery(r, map[string]bool{"id": true}); err != nil {
			return err
		}
		nodeID, err := queryString(r, "id", 4096)
		if err != nil || nodeID == "" {
			return clientError(http.StatusBadRequest, "node id is required")
		}
		value, err := buildNodeDetail(svc, nodeID)
		return writeAPIResult(w, value, err)
	}))
	mux.HandleFunc("/api/v1/group-edges", apiEndpoint(func(w http.ResponseWriter, r *http.Request) error {
		if err := requireQuery(r, map[string]bool{"ref": true, "cursor": true, "limit": true, "groupState": true, "expanded": true, "collapsed": true}); err != nil {
			return err
		}
		ref, err := queryString(r, "ref", 128)
		if err != nil || !strings.HasPrefix(ref, "group-edges:") {
			return clientError(http.StatusBadRequest, "valid group edge ref is required")
		}
		cursor, err := queryInt(r, "cursor", 0, 0, 1_000_000_000)
		if err != nil {
			return err
		}
		groupState, err := queryGroupState(r)
		if err != nil {
			return err
		}
		limit, err := queryInt(r, "limit", 100, 1, maxGroupEdgePage)
		if err != nil {
			return err
		}
		expanded, err := queryStrings(r, "expanded", 256, 4096, 16*1024)
		if err != nil {
			return err
		}
		collapsed, err := queryStrings(r, "collapsed", 256, 4096, 16*1024)
		if err != nil {
			return err
		}
		value, err := buildGroupEdgePage(svc, ref, cursor, limit, groupState, expanded, collapsed)
		return writeAPIResult(w, value, err)
	}))
	mux.HandleFunc("/api/v1/aggregate-edges", apiEndpoint(func(w http.ResponseWriter, r *http.Request) error {
		if err := requireQuery(r, map[string]bool{"ref": true, "cursor": true, "limit": true, "groupState": true, "expanded": true, "collapsed": true}); err != nil {
			return err
		}
		ref, err := queryString(r, "ref", 128)
		if err != nil || !strings.HasPrefix(ref, "aggregate-edges:") {
			return clientError(http.StatusBadRequest, "valid aggregate edge index ref is required")
		}
		cursor, err := queryInt(r, "cursor", 0, 0, 1_000_000_000)
		if err != nil {
			return err
		}
		limit, err := queryInt(r, "limit", maxAggregateEdgePage, 1, maxAggregateEdgePage)
		if err != nil {
			return err
		}
		groupState, err := queryGroupState(r)
		if err != nil {
			return err
		}
		expanded, err := queryStrings(r, "expanded", 256, 4096, 16*1024)
		if err != nil {
			return err
		}
		collapsed, err := queryStrings(r, "collapsed", 256, 4096, 16*1024)
		if err != nil {
			return err
		}
		value, err := buildAggregateEdgePage(svc, ref, cursor, limit, groupState, expanded, collapsed)
		return writeAPIResult(w, value, err)
	}))
	mux.HandleFunc("/api/v1/history", apiEndpoint(func(w http.ResponseWriter, r *http.Request) error {
		if err := requireQuery(r, map[string]bool{"before": true, "limit": true}); err != nil {
			return err
		}
		limit, err := queryInt(r, "limit", 50, 1, maxHistoryPage)
		if err != nil {
			return err
		}
		before, present, err := queryUint64(r, "before")
		if err != nil {
			return err
		}
		value, err := buildHistory(svc, before, present, limit)
		return writeAPIResult(w, value, err)
	}))
	mux.HandleFunc("/api/v1/operations", apiEndpoint(func(w http.ResponseWriter, r *http.Request) error {
		if err := requireQuery(r, map[string]bool{"limit": true}); err != nil {
			return err
		}
		limit, err := queryInt(r, "limit", 100, 1, maxOperationsPage)
		if err != nil {
			return err
		}
		value, err := buildOperations(svc, limit)
		return writeAPIResult(w, value, err)
	}))
}

type endpointFunc func(http.ResponseWriter, *http.Request) error

func apiEndpoint(handler endpointFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method == http.MethodHead {
			w = headResponseWriter{ResponseWriter: w}
		}
		if err := handler(w, r); err != nil {
			var apiErr *explorerError
			if ok := asExplorerError(err, &apiErr); ok {
				writeAPIError(w, apiErr.Status, apiErr.Message)
				return
			}
			writeAPIError(w, http.StatusInternalServerError, "explorer data unavailable")
		}
	}
}

type headResponseWriter struct {
	http.ResponseWriter
}

func (headResponseWriter) Write(payload []byte) (int, error) {
	return len(payload), nil
}

type explorerError struct {
	Status  int
	Message string
}

func (e *explorerError) Error() string { return e.Message }

func clientError(status int, message string) error {
	return &explorerError{Status: status, Message: message}
}

func asExplorerError(err error, target **explorerError) bool {
	value, ok := err.(*explorerError)
	if ok {
		*target = value
	}
	return ok
}

func writeAPIResult(w http.ResponseWriter, value any, err error) error {
	if err != nil {
		return err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if buffer.Len() > maxAPIResponse {
		return clientError(http.StatusRequestEntityTooLarge, "explorer response exceeds the bounded API limit")
	}
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(buffer.Bytes())
	return err
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": explorerAPIVersion, "error": message, "status": status})
}

func buildOverview(svc *service.Service) (ExplorerOverview, error) {
	state, err := svc.State()
	if err != nil {
		return ExplorerOverview{}, err
	}
	frontier := domain.ComputeFrontier(state)
	ready := append([]string{}, frontier.Ready...)
	if len(ready) > 100 {
		ready = ready[:100]
	}
	counts := map[string]int{"nodes": len(state.Nodes), "attempts": len(state.Attempts), "leases": len(state.Leases), "incidents": len(state.Incidents), "resources": len(state.Resources), "effects": len(state.Effects)}
	for status, count := range countNodeRuntime(state) {
		counts["nodes."+status] = count
	}
	for status, count := range countIncidentRuntime(state) {
		counts["incidents."+status] = count
	}
	kinds, roles := []string{}, []string{}
	if state.Graph != nil {
		kindSet, roleSet := map[string]bool{}, map[string]bool{}
		for _, node := range state.Graph.Spec.Nodes {
			kindSet[node.Kind], roleSet[node.Role] = true, node.Role != ""
		}
		for value := range kindSet {
			kinds = append(kinds, value)
		}
		for value, include := range roleSet {
			if include {
				roles = append(roles, value)
			}
		}
	}
	sort.Strings(kinds)
	sort.Strings(roles)
	return ExplorerOverview{
		APIVersion: explorerAPIVersion, ReadOnly: true,
		Project:       map[string]string{"id": svc.Project.Config.ProjectID, "name": svc.Project.Config.Name},
		GraphRevision: state.GraphRevision, HeadSequence: state.HeadSequence, Counts: counts,
		Frontier: FrontierSummary{Ready: ready, ReadyCount: len(frontier.Ready), ReadyTruncated: len(ready) < len(frontier.Ready), BlockedCount: len(frontier.Blocked), ResourceBlocked: len(frontier.ResourceBlocked), DependencyCuts: len(frontier.DependencyCuts)},
		Facets:   map[string][]string{"statuses": {"ready", "blocked", "attention", "accepted", "returned", "completed", "unreachable", "resource-blocked", "planned", "active", "terminal", "superseded", "skipped"}, "kinds": kinds, "roles": roles},
	}, nil
}

func buildNodePage(svc *service.Service, filter nodeFilter, cursor, limit int) (NodePage, error) {
	state, err := svc.State()
	if err != nil {
		return NodePage{}, err
	}
	views := filteredNodeViews(state, filter)
	if cursor > len(views) {
		return NodePage{}, clientError(http.StatusBadRequest, "node cursor exceeds filtered result")
	}
	end := cursor + limit
	if end > len(views) {
		end = len(views)
	}
	page := PageInfo{Cursor: cursor, Limit: limit, Total: len(views), Truncated: end < len(views)}
	if end < len(views) {
		next := end
		page.NextCursor = &next
	}
	return NodePage{APIVersion: explorerAPIVersion, GraphRevision: state.GraphRevision, HeadSequence: state.HeadSequence, Page: page, Nodes: views[cursor:end]}, nil
}

func buildTopology(svc *service.Service, filter nodeFilter, focus string, depth, limit int, mode, groupState string, expandedIDs, collapsedIDs []string) (TopologyPage, error) {
	state, err := svc.State()
	if err != nil {
		return TopologyPage{}, err
	}
	if mode == "" {
		mode = "detail"
		if state.Graph != nil && len(state.Graph.Spec.Groups) > 0 {
			mode = "summary"
		}
	}
	if mode == "summary" {
		return buildSummaryTopology(state, filter, focus, depth, limit, groupState, expandedIDs, collapsedIDs)
	}
	views := filteredNodeViews(state, filter)
	if focus != "" {
		if _, ok := state.NodeDefinition(focus); !ok {
			return TopologyPage{}, clientError(http.StatusNotFound, "unknown focus node")
		}
		ids := focusedNodeIDs(state, focus, depth)
		views = viewsForOrderedIDs(state, ids)
	}
	total := len(views)
	if len(views) > limit {
		views = views[:limit]
	}
	included := map[string]bool{}
	for _, node := range views {
		included[node.ID] = true
	}
	edges := []domain.EdgeDefinition{}
	if state.Graph != nil {
		for _, edge := range state.Graph.Spec.Edges {
			if included[edge.From] && included[edge.To] {
				edges = append(edges, edge)
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	return TopologyPage{APIVersion: explorerAPIVersion, GraphRevision: state.GraphRevision, HeadSequence: state.HeadSequence, Mode: "detail", Focus: focus, Depth: depth, Page: PageInfo{Cursor: 0, Limit: limit, Total: total, Truncated: len(views) < total}, Nodes: views, Edges: edges}, nil
}

func buildSummaryTopology(state domain.State, filter nodeFilter, focus string, depth, limit int, groupState string, expandedIDs, collapsedIDs []string) (TopologyPage, error) {
	if state.Graph == nil {
		return TopologyPage{APIVersion: explorerAPIVersion, Mode: "summary", Depth: depth, Nodes: []NodeView{}, Edges: []domain.EdgeDefinition{}, Groups: []GroupView{}, AggregateEdges: []AggregateEdgeView{}}, nil
	}
	groups := map[string]domain.GroupDefinition{}
	for _, group := range state.Graph.Spec.Groups {
		groups[group.ID] = group
	}
	overrides, err := groupOverrides(state.Graph, groupState, expandedIDs, collapsedIDs)
	if err != nil {
		return TopologyPage{}, err
	}
	if focus != "" {
		if group, ok := groups[focus]; ok {
			for current := group.ParentGroupID; current != ""; current = groups[current].ParentGroupID {
				overrides[current] = true
			}
		} else if node, ok := state.NodeDefinition(focus); ok {
			for current := node.GroupID; current != ""; current = groups[current].ParentGroupID {
				overrides[current] = true
			}
		} else {
			return TopologyPage{}, clientError(http.StatusNotFound, "unknown focus group or node")
		}
	}
	projection, err := grouping.Build(state, overrides)
	if err != nil {
		return TopologyPage{}, err
	}
	matchedViews := filteredNodeViews(state, filter)
	matchedNodes := map[string]bool{}
	matchCounts := map[string]int{}
	for _, view := range matchedViews {
		matchedNodes[view.ID] = true
		node, _ := state.NodeDefinition(view.ID)
		for current := node.GroupID; current != ""; current = groups[current].ParentGroupID {
			matchCounts[current]++
		}
	}
	filtering := filter.Query != "" || filter.Status != "" || filter.Kind != "" || filter.Role != ""
	includedGroups := map[string]bool{}
	if filtering {
		query := strings.ToLower(filter.Query)
		for _, summary := range projection.Groups {
			if matchCounts[summary.ID] > 0 || (query != "" && strings.Contains(strings.ToLower(summary.ID+"\x00"+summary.Title+"\x00"+summary.Kind), query)) || (filter.Status != "" && (summary.DisplayStatus == filter.Status || summary.LifecycleStatus == filter.Status)) {
				for current := summary.ID; current != ""; current = groups[current].ParentGroupID {
					includedGroups[current] = true
				}
			}
		}
	}
	groupViews := []GroupView{}
	for _, summary := range projection.Groups {
		if filtering && !includedGroups[summary.ID] {
			continue
		}
		groupViews = append(groupViews, GroupView{GroupSummary: summary, InternalMatchCount: matchCounts[summary.ID]})
	}
	visibleNodeIDs := []string{}
	for _, nodeID := range projection.VisibleNodes {
		if !filtering || matchedNodes[nodeID] {
			visibleNodeIDs = append(visibleNodeIDs, nodeID)
		}
	}
	total := len(visibleNodeIDs)
	if len(visibleNodeIDs) > limit {
		visibleNodeIDs = visibleNodeIDs[:limit]
	}
	views := viewsForOrderedIDs(state, visibleNodeIDs)
	expanded := []string{}
	for _, summary := range projection.Groups {
		if summary.Expanded {
			expanded = append(expanded, summary.ID)
		}
	}
	sort.Strings(expanded)
	aggregateEdges, aggregatePage, aggregateRef := aggregateEdgeIndex(projection, 0, maxAggregateEdgePage)
	return TopologyPage{
		APIVersion: explorerAPIVersion, GraphRevision: state.GraphRevision, HeadSequence: state.HeadSequence,
		Mode: "summary", Focus: focus, Depth: depth, Page: PageInfo{Cursor: 0, Limit: limit, Total: total, Truncated: len(views) < total},
		Nodes: views, Edges: []domain.EdgeDefinition{}, Groups: groupViews, Lanes: filterLanes(projection.Lanes, groupViews, views), AggregateEdges: aggregateEdges,
		AggregateEdgePage: &aggregatePage, AggregateEdgeIndexRef: aggregateRef,
		ExpandedGroupIDs: expanded, MembershipDigest: projection.MembershipDigest, ProjectionDigest: projection.ProjectionDigest,
	}, nil
}

func filterLanes(lanes []grouping.Lane, groups []GroupView, nodes []NodeView) []grouping.Lane {
	visibleGroups := map[string]bool{}
	for _, group := range groups {
		visibleGroups["group:"+group.ID] = true
	}
	visibleNodes := map[string]bool{}
	for _, node := range nodes {
		visibleNodes["node:"+node.ID] = true
	}
	result := make([]grouping.Lane, 0, len(lanes))
	for _, lane := range lanes {
		filtered := grouping.Lane{ID: lane.ID, Title: lane.Title, GroupRefs: []string{}, NodeRefs: []string{}}
		for _, ref := range lane.GroupRefs {
			if visibleGroups[ref] {
				filtered.GroupRefs = append(filtered.GroupRefs, ref)
			}
		}
		for _, ref := range lane.NodeRefs {
			if visibleNodes[ref] {
				filtered.NodeRefs = append(filtered.NodeRefs, ref)
			}
		}
		if len(filtered.GroupRefs)+len(filtered.NodeRefs) > 0 {
			result = append(result, filtered)
		}
	}
	return result
}

func buildGroupEdgePage(svc *service.Service, ref string, cursor, limit int, groupState string, expandedIDs, collapsedIDs []string) (GroupEdgePage, error) {
	state, err := svc.State()
	if err != nil {
		return GroupEdgePage{}, err
	}
	if state.Graph == nil {
		return GroupEdgePage{}, clientError(http.StatusNotFound, "group edge ref is unavailable")
	}
	overrides, err := groupOverrides(state.Graph, groupState, expandedIDs, collapsedIDs)
	if err != nil {
		return GroupEdgePage{}, err
	}
	projection, err := grouping.Build(state, overrides)
	if err != nil {
		return GroupEdgePage{}, err
	}
	for _, edge := range projection.AggregateEdges {
		if edge.InspectRef != ref {
			continue
		}
		if cursor > len(edge.EdgeIDs) {
			return GroupEdgePage{}, clientError(http.StatusBadRequest, "group edge cursor exceeds result")
		}
		end := cursor + limit
		if end > len(edge.EdgeIDs) {
			end = len(edge.EdgeIDs)
		}
		page := PageInfo{Cursor: cursor, Limit: limit, Total: len(edge.EdgeIDs), Truncated: end < len(edge.EdgeIDs)}
		if end < len(edge.EdgeIDs) {
			next := end
			page.NextCursor = &next
		}
		return GroupEdgePage{APIVersion: explorerAPIVersion, GraphRevision: state.GraphRevision, HeadSequence: state.HeadSequence, Ref: ref, Page: page, EdgeIDs: append([]string{}, edge.EdgeIDs[cursor:end]...)}, nil
	}
	return GroupEdgePage{}, clientError(http.StatusNotFound, "group edge ref is stale or unknown")
}

func buildAggregateEdgePage(svc *service.Service, ref string, cursor, limit int, groupState string, expandedIDs, collapsedIDs []string) (AggregateEdgePage, error) {
	state, err := svc.State()
	if err != nil {
		return AggregateEdgePage{}, err
	}
	if state.Graph == nil {
		return AggregateEdgePage{}, clientError(http.StatusNotFound, "aggregate edge index ref is unavailable")
	}
	overrides, err := groupOverrides(state.Graph, groupState, expandedIDs, collapsedIDs)
	if err != nil {
		return AggregateEdgePage{}, err
	}
	projection, err := grouping.Build(state, overrides)
	if err != nil {
		return AggregateEdgePage{}, err
	}
	expectedRef := aggregateEdgeIndexRef(projection)
	if ref != expectedRef {
		return AggregateEdgePage{}, clientError(http.StatusNotFound, "aggregate edge index ref is stale or unknown")
	}
	if cursor > len(projection.AggregateEdges) {
		return AggregateEdgePage{}, clientError(http.StatusBadRequest, "aggregate edge cursor exceeds result")
	}
	edges, page, _ := aggregateEdgeIndex(projection, cursor, limit)
	return AggregateEdgePage{APIVersion: explorerAPIVersion, GraphRevision: state.GraphRevision, HeadSequence: state.HeadSequence, Ref: ref, Page: page, AggregateEdges: edges}, nil
}

func groupOverrides(graph *domain.GraphDefinition, groupState string, expandedIDs, collapsedIDs []string) (map[string]bool, error) {
	groups := map[string]bool{}
	for _, group := range graph.Spec.Groups {
		groups[group.ID] = true
	}
	overrides := map[string]bool{}
	if groupState != "" {
		expanded := groupState == "expanded"
		for groupID := range groups {
			overrides[groupID] = expanded
		}
	}
	explicitExpanded := map[string]bool{}
	for _, groupID := range expandedIDs {
		if !groups[groupID] {
			return nil, clientError(http.StatusBadRequest, "unknown expanded group")
		}
		explicitExpanded[groupID] = true
		overrides[groupID] = true
	}
	for _, groupID := range collapsedIDs {
		if !groups[groupID] {
			return nil, clientError(http.StatusBadRequest, "unknown collapsed group")
		}
		if explicitExpanded[groupID] {
			return nil, clientError(http.StatusBadRequest, "group cannot be both expanded and collapsed")
		}
		overrides[groupID] = false
	}
	return overrides, nil
}

func aggregateEdgeIndex(projection grouping.Projection, cursor, limit int) ([]AggregateEdgeView, PageInfo, string) {
	total := len(projection.AggregateEdges)
	if cursor > total {
		cursor = total
	}
	end := cursor + limit
	if end > total {
		end = total
	}
	page := PageInfo{Cursor: cursor, Limit: limit, Total: total, Truncated: end < total}
	if end < total {
		next := end
		page.NextCursor = &next
	}
	edges := make([]AggregateEdgeView, 0, end-cursor)
	for _, edge := range projection.AggregateEdges[cursor:end] {
		edges = append(edges, AggregateEdgeView{FromRef: edge.FromRef, ToRef: edge.ToRef, PredicateKind: edge.PredicateKind, OutcomeClass: edge.OutcomeClass, Count: edge.Count, EdgeDigest: edge.EdgeDigest, InspectRef: edge.InspectRef})
	}
	ref := ""
	if total > 0 {
		ref = aggregateEdgeIndexRef(projection)
	}
	return edges, page, ref
}

func aggregateEdgeIndexRef(projection grouping.Projection) string {
	digest := sha256.Sum256([]byte("dagrail-ui-aggregate-edge-index-v1\x00" + projection.ProjectionDigest))
	return "aggregate-edges:" + hex.EncodeToString(digest[:])
}

func buildNodeDetail(svc *service.Service, nodeID string) (NodeDetail, error) {
	state, err := svc.State()
	if err != nil {
		return NodeDetail{}, err
	}
	node, ok := state.NodeDefinition(nodeID)
	if !ok {
		return NodeDetail{}, clientError(http.StatusNotFound, "unknown node")
	}
	contract := NodeContractView{ID: node.ID, Title: node.Title, Kind: node.Kind, Role: node.Role, Objective: node.Objective, Parent: node.Parent, GroupID: node.GroupID, Supersedes: node.Supersedes, Outcomes: append([]domain.Outcome{}, node.Outcomes...), RetryBudget: node.RetryBudget, Resources: append([]domain.ResourceRequest{}, node.Resources...), InputBytes: len(node.Inputs)}
	if len(node.Inputs) > 0 {
		digest := sha256.Sum256(node.Inputs)
		contract.InputSHA256 = "sha256:" + hex.EncodeToString(digest[:])
	}
	detail := NodeDetail{APIVersion: explorerAPIVersion, GraphRevision: state.GraphRevision, HeadSequence: state.HeadSequence, Counts: map[string]int{}, Truncated: map[string]bool{}, Contract: contract, Runtime: state.Nodes[nodeID], Incoming: []domain.EdgeDefinition{}, Outgoing: []domain.EdgeDefinition{}, Attempts: []domain.Attempt{}, Checkpoints: []domain.Checkpoint{}, Evidence: []EvidenceIndexView{}, Decisions: []DecisionView{}, Incidents: []domain.Incident{}, Resources: []ResourceView{}, Effects: []EffectView{}}
	frontier := domain.ComputeFrontier(state)
	for _, explanation := range frontier.Explanations {
		if explanation.NodeID == nodeID {
			value := explanation
			detail.Readiness = &value
			break
		}
	}
	if state.Graph != nil {
		for _, edge := range state.Graph.Spec.Edges {
			if edge.To == nodeID {
				detail.Incoming = append(detail.Incoming, edge)
			}
			if edge.From == nodeID {
				detail.Outgoing = append(detail.Outgoing, edge)
			}
		}
	}
	for _, attempt := range state.Attempts {
		if attempt.NodeID == nodeID {
			detail.Attempts = append(detail.Attempts, attempt)
			if checkpoint, exists := state.Checkpoints[attempt.CheckpointID]; exists {
				detail.Checkpoints = append(detail.Checkpoints, checkpoint)
			}
		}
	}
	for _, pack := range state.EvidencePackages {
		if pack.NodeID == nodeID {
			detail.Evidence = append(detail.Evidence, EvidenceIndexView{ID: pack.ID, AttemptID: pack.AttemptID, CoreDigest: pack.CoreDigest, CreatedAt: pack.CreatedAt})
		}
	}
	for _, decision := range state.Decisions {
		if decision.NodeID != nodeID {
			continue
		}
		view := DecisionView{ID: decision.ID, NodeID: decision.NodeID, AttemptID: decision.AttemptID, RoleID: decision.RoleID, Key: decision.Key, Source: decision.Source, Outcome: decision.Outcome, CreatedAt: decision.CreatedAt}
		if decision.Provider != nil {
			view.ProviderID = decision.Provider.ID
			view.ProviderVersion = decision.Provider.Version
		}
		detail.Decisions = append(detail.Decisions, view)
	}
	for _, incident := range state.Incidents {
		if incident.NodeID == nodeID {
			detail.Incidents = append(detail.Incidents, incident)
		}
	}
	for _, resource := range state.Resources {
		if resource.NodeID == nodeID {
			detail.Resources = append(detail.Resources, resourceView(resource))
		}
	}
	for _, effect := range state.Effects {
		if effect.NodeID == nodeID {
			detail.Effects = append(detail.Effects, effectView(effect))
		}
	}
	sort.Slice(detail.Incoming, func(i, j int) bool { return detail.Incoming[i].ID < detail.Incoming[j].ID })
	sort.Slice(detail.Outgoing, func(i, j int) bool { return detail.Outgoing[i].ID < detail.Outgoing[j].ID })
	sort.Slice(detail.Attempts, func(i, j int) bool { return detail.Attempts[i].Number < detail.Attempts[j].Number })
	sort.Slice(detail.Checkpoints, func(i, j int) bool { return detail.Checkpoints[i].CreatedAt < detail.Checkpoints[j].CreatedAt })
	sort.Slice(detail.Evidence, func(i, j int) bool { return detail.Evidence[i].ID < detail.Evidence[j].ID })
	sort.Slice(detail.Decisions, func(i, j int) bool {
		if detail.Decisions[i].CreatedAt == detail.Decisions[j].CreatedAt {
			return detail.Decisions[i].ID < detail.Decisions[j].ID
		}
		return detail.Decisions[i].CreatedAt < detail.Decisions[j].CreatedAt
	})
	sort.Slice(detail.Incidents, func(i, j int) bool { return detail.Incidents[i].ID < detail.Incidents[j].ID })
	sort.Slice(detail.Resources, func(i, j int) bool { return detail.Resources[i].ID < detail.Resources[j].ID })
	sort.Slice(detail.Effects, func(i, j int) bool { return detail.Effects[i].ID < detail.Effects[j].ID })
	detail.Counts = map[string]int{
		"incoming": len(detail.Incoming), "outgoing": len(detail.Outgoing), "attempts": len(detail.Attempts),
		"checkpoints": len(detail.Checkpoints), "evidence": len(detail.Evidence), "decisions": len(detail.Decisions),
		"incidents": len(detail.Incidents), "resources": len(detail.Resources), "effects": len(detail.Effects),
	}
	detail.Incoming = capFirst(detail.Incoming, maxNodeDetailItems, detail.Truncated, "incoming")
	detail.Outgoing = capFirst(detail.Outgoing, maxNodeDetailItems, detail.Truncated, "outgoing")
	detail.Attempts = capLast(detail.Attempts, maxNodeDetailItems, detail.Truncated, "attempts")
	detail.Checkpoints = capLast(detail.Checkpoints, maxNodeDetailItems, detail.Truncated, "checkpoints")
	detail.Evidence = capLast(detail.Evidence, maxNodeDetailItems, detail.Truncated, "evidence")
	detail.Decisions = capLast(detail.Decisions, maxNodeDetailItems, detail.Truncated, "decisions")
	detail.Incidents = capLast(detail.Incidents, maxNodeDetailItems, detail.Truncated, "incidents")
	detail.Resources = capLast(detail.Resources, maxNodeDetailItems, detail.Truncated, "resources")
	detail.Effects = capLast(detail.Effects, maxNodeDetailItems, detail.Truncated, "effects")
	return detail, nil
}

func capFirst[T any](items []T, limit int, truncated map[string]bool, key string) []T {
	if len(items) <= limit {
		return items
	}
	truncated[key] = true
	return items[:limit]
}

func capLast[T any](items []T, limit int, truncated map[string]bool, key string) []T {
	if len(items) <= limit {
		return items
	}
	truncated[key] = true
	return items[len(items)-limit:]
}

func buildHistory(svc *service.Service, before uint64, present bool, limit int) (HistoryResponse, error) {
	state, err := svc.State()
	if err != nil {
		return HistoryResponse{}, err
	}
	if !present {
		before = state.HeadSequence + 1
	}
	if before == 0 || before > state.HeadSequence+1 {
		return HistoryResponse{}, clientError(http.StatusBadRequest, "history cursor exceeds journal head")
	}
	end := before - 1
	start := uint64(0)
	if end > uint64(limit) {
		start = end - uint64(limit)
	}
	page := service.HistoryPage{After: start, NextCursor: start, Entries: []service.HistoryEntry{}}
	if end > start {
		page, err = svc.History(start, int(end-start))
		if err != nil {
			return HistoryResponse{}, err
		}
	}
	response := HistoryResponse{APIVersion: explorerAPIVersion, GraphRevision: state.GraphRevision, HeadSequence: state.HeadSequence, Before: before, Page: page}
	if start > 0 {
		older := start + 1
		response.OlderBefore = &older
	}
	if end < state.HeadSequence {
		newer := before + uint64(limit)
		if newer > state.HeadSequence+1 {
			newer = state.HeadSequence + 1
		}
		response.NewerBefore = &newer
	}
	return response, nil
}

func buildOperations(svc *service.Service, limit int) (OperationsResponse, error) {
	state, err := svc.State()
	if err != nil {
		return OperationsResponse{}, err
	}
	result := OperationsResponse{APIVersion: explorerAPIVersion, GraphRevision: state.GraphRevision, HeadSequence: state.HeadSequence, Limit: limit, Counts: map[string]int{"attempts": len(state.Attempts), "leases": len(state.Leases), "incidents": len(state.Incidents), "resources": len(state.Resources), "effects": len(state.Effects)}, Truncated: map[string]bool{}, Attempts: []domain.Attempt{}, Leases: []domain.RoleLease{}, Incidents: []domain.Incident{}, Resources: []ResourceView{}, Effects: []EffectView{}}
	for _, value := range state.Attempts {
		result.Attempts = append(result.Attempts, value)
	}
	for _, value := range state.Leases {
		result.Leases = append(result.Leases, value)
	}
	for _, value := range state.Incidents {
		result.Incidents = append(result.Incidents, value)
	}
	for _, value := range state.Resources {
		result.Resources = append(result.Resources, resourceView(value))
	}
	for _, value := range state.Effects {
		result.Effects = append(result.Effects, effectView(value))
	}
	sort.Slice(result.Attempts, func(i, j int) bool {
		return newer(result.Attempts[i].UpdatedAt, result.Attempts[i].ID, result.Attempts[j].UpdatedAt, result.Attempts[j].ID)
	})
	sort.Slice(result.Leases, func(i, j int) bool { return result.Leases[i].RoleID < result.Leases[j].RoleID })
	sort.Slice(result.Incidents, func(i, j int) bool {
		return newer(result.Incidents[i].UpdatedAt, result.Incidents[i].ID, result.Incidents[j].UpdatedAt, result.Incidents[j].ID)
	})
	sort.Slice(result.Resources, func(i, j int) bool {
		return newer(result.Resources[i].LeasedAt, result.Resources[i].ID, result.Resources[j].LeasedAt, result.Resources[j].ID)
	})
	sort.Slice(result.Effects, func(i, j int) bool {
		return newer(result.Effects[i].UpdatedAt, result.Effects[i].ID, result.Effects[j].UpdatedAt, result.Effects[j].ID)
	})
	result.Attempts, result.Truncated["attempts"] = limitSlice(result.Attempts, limit)
	result.Leases, result.Truncated["leases"] = limitSlice(result.Leases, limit)
	result.Incidents, result.Truncated["incidents"] = limitSlice(result.Incidents, limit)
	result.Resources, result.Truncated["resources"] = limitSlice(result.Resources, limit)
	result.Effects, result.Truncated["effects"] = limitSlice(result.Effects, limit)
	return result, nil
}

func filteredNodeViews(state domain.State, filter nodeFilter) []NodeView {
	views := allNodeViews(state)
	result := make([]NodeView, 0, len(views))
	query := strings.ToLower(filter.Query)
	for _, view := range views {
		if query != "" && !strings.Contains(strings.ToLower(strings.Join([]string{view.ID, view.Title, view.Kind, view.Role, view.Parent}, "\x00")), query) {
			continue
		}
		if filter.Kind != "" && view.Kind != filter.Kind {
			continue
		}
		if filter.Role != "" && view.Role != filter.Role {
			continue
		}
		if filter.Status != "" && view.Status != filter.Status && view.Readiness != filter.Status {
			continue
		}
		result = append(result, view)
	}
	return result
}

func allNodeViews(state domain.State) []NodeView {
	result := []NodeView{}
	readiness := map[string]string{}
	for _, explanation := range domain.ComputeFrontier(state).Explanations {
		readiness[explanation.NodeID] = explanation.State
	}
	if state.Graph != nil {
		for _, node := range state.Graph.Spec.Nodes {
			runtime := state.Nodes[node.ID]
			result = append(result, NodeView{ID: node.ID, Title: node.Title, Kind: node.Kind, Role: node.Role, Parent: node.Parent, GroupID: node.GroupID, Status: runtime.Status, Readiness: readiness[node.ID], Outcome: runtime.Outcome})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func viewsForOrderedIDs(state domain.State, ids []string) []NodeView {
	byID := map[string]NodeView{}
	for _, view := range allNodeViews(state) {
		byID[view.ID] = view
	}
	result := make([]NodeView, 0, len(ids))
	for _, id := range ids {
		if view, ok := byID[id]; ok {
			result = append(result, view)
		}
	}
	return result
}

func focusedNodeIDs(state domain.State, focus string, depth int) []string {
	adjacent := map[string][]string{}
	if state.Graph != nil {
		for _, edge := range state.Graph.Spec.Edges {
			adjacent[edge.From] = append(adjacent[edge.From], edge.To)
			adjacent[edge.To] = append(adjacent[edge.To], edge.From)
		}
	}
	for id := range adjacent {
		sort.Strings(adjacent[id])
	}
	seen := map[string]bool{focus: true}
	frontier := []string{focus}
	result := []string{focus}
	for range depth {
		nextSet := map[string]bool{}
		for _, id := range frontier {
			for _, neighbor := range adjacent[id] {
				if !seen[neighbor] {
					nextSet[neighbor] = true
				}
			}
		}
		next := make([]string, 0, len(nextSet))
		for id := range nextSet {
			next = append(next, id)
		}
		sort.Strings(next)
		for _, id := range next {
			seen[id] = true
		}
		result = append(result, next...)
		frontier = next
	}
	return result
}

func effectView(effect domain.EffectAction) EffectView {
	view := EffectView{ID: effect.ID, NodeID: effect.NodeID, AttemptID: effect.AttemptID, AdapterID: effect.AdapterID, AdapterVersion: effect.AdapterVersion, AdapterSchemaHash: effect.AdapterSchemaHash, OwnerRole: effect.OwnerRole, Status: effect.Status, PreparedAt: effect.PreparedAt, UpdatedAt: effect.UpdatedAt}
	var receipt struct {
		TransportStatus  string `json:"transportStatus"`
		SessionStatus    string `json:"sessionStatus"`
		DeliveryStatus   string `json:"deliveryStatus"`
		AcceptanceStatus string `json:"acceptanceStatus"`
		CompletionStatus string `json:"completionStatus"`
	}
	if len(effect.Receipt) > 0 && json.Unmarshal(effect.Receipt, &receipt) == nil {
		view.TransportStatus, view.SessionStatus, view.DeliveryStatus = receipt.TransportStatus, receipt.SessionStatus, receipt.DeliveryStatus
		view.AcceptanceStatus, view.CompletionStatus = receipt.AcceptanceStatus, receipt.CompletionStatus
	}
	return view
}

func resourceView(resource domain.ResourceLease) ResourceView {
	return ResourceView{
		ID: resource.ID, Kind: resource.Kind, Quantity: resource.Quantity, NodeID: resource.NodeID,
		AttemptID: resource.AttemptID, RoleID: resource.RoleID, Status: resource.Status,
		ClosureStatus: resource.ClosureStatus, ClosureUpdatedAt: resource.ClosureUpdatedAt,
		LeasedAt: resource.LeasedAt, ReleasedAt: resource.ReleasedAt,
	}
}

func countNodeRuntime(state domain.State) map[string]int {
	result := map[string]int{}
	for _, value := range state.Nodes {
		result[value.Status]++
	}
	return result
}

func countIncidentRuntime(state domain.State) map[string]int {
	result := map[string]int{}
	for _, value := range state.Incidents {
		result[value.Status]++
	}
	return result
}

func parseNodeFilter(r *http.Request, allowed map[string]bool) (nodeFilter, error) {
	if err := requireQuery(r, allowed); err != nil {
		return nodeFilter{}, err
	}
	query, err := queryString(r, "q", 256)
	if err != nil {
		return nodeFilter{}, err
	}
	status, err := queryString(r, "status", 64)
	if err != nil {
		return nodeFilter{}, err
	}
	kind, err := queryString(r, "kind", 4096)
	if err != nil {
		return nodeFilter{}, err
	}
	role, err := queryString(r, "role", 4096)
	if err != nil {
		return nodeFilter{}, err
	}
	if status != "" {
		allowed := map[string]bool{"ready": true, "blocked": true, "attention": true, "accepted": true, "returned": true, "completed": true, "unreachable": true, "resource-blocked": true, "planned": true, "active": true, "terminal": true, "superseded": true, "skipped": true}
		if !allowed[status] {
			return nodeFilter{}, clientError(http.StatusBadRequest, "invalid node status filter")
		}
	}
	return nodeFilter{Query: query, Status: status, Kind: kind, Role: role}, nil
}

func requireQuery(r *http.Request, allowed map[string]bool) error {
	for key, values := range r.URL.Query() {
		if !allowed[key] {
			return clientError(http.StatusBadRequest, "unknown query parameter: "+key)
		}
		if len(values) != 1 && key != "expanded" && key != "collapsed" {
			return clientError(http.StatusBadRequest, "query parameter must appear once: "+key)
		}
	}
	return nil
}

func queryStrings(r *http.Request, name string, maxItems, itemLimit, totalLimit int) ([]string, error) {
	values := r.URL.Query()[name]
	if len(values) > maxItems {
		return nil, clientError(http.StatusBadRequest, name+" has too many values")
	}
	total := 0
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		length := len([]byte(value))
		total += length
		if length == 0 || length > itemLimit || total > totalLimit {
			return nil, clientError(http.StatusBadRequest, name+" exceeds its byte limit")
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result, nil
}

func queryGroupState(r *http.Request) (string, error) {
	value, err := queryString(r, "groupState", 16)
	if err != nil {
		return "", err
	}
	if value != "" && value != "expanded" && value != "collapsed" {
		return "", clientError(http.StatusBadRequest, "groupState must be expanded or collapsed")
	}
	return value, nil
}

func queryString(r *http.Request, name string, limit int) (string, error) {
	value := r.URL.Query().Get(name)
	if len([]byte(value)) > limit {
		return "", clientError(http.StatusBadRequest, name+" exceeds its byte limit")
	}
	return value, nil
}

func queryInt(r *http.Request, name string, fallback, minimum, maximum int) (int, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, clientError(http.StatusBadRequest, fmt.Sprintf("%s must be %d..%d", name, minimum, maximum))
	}
	return parsed, nil
}

func queryUint64(r *http.Request, name string) (uint64, bool, error) {
	value, exists := r.URL.Query()[name]
	if !exists {
		return 0, false, nil
	}
	parsed, err := strconv.ParseUint(value[0], 10, 64)
	if err != nil {
		return 0, true, clientError(http.StatusBadRequest, name+" must be an unsigned integer")
	}
	return parsed, true, nil
}

func newer(leftTime, leftID, rightTime, rightID string) bool {
	if leftTime == rightTime {
		return leftID < rightID
	}
	return leftTime > rightTime
}

func limitSlice[T any](values []T, limit int) ([]T, bool) {
	if len(values) <= limit {
		return values, false
	}
	return values[:limit], true
}
