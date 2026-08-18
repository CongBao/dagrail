package mcpserver

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/service"
	"github.com/CongBao/dagrail/internal/version"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/gowebpki/jcs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	MaxMCPMessageBytes   = 1 << 20
	MaxMCPToolInputBytes = 64 * 1024
)

type ContextInput struct {
	View        string `json:"view" jsonschema:"orchestrator, worker, or reviewer"`
	RoleID      string `json:"role_id,omitempty"`
	RoleRef     string `json:"role_ref,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	NodeRef     string `json:"node_ref,omitempty"`
	Cursor      uint64 `json:"cursor,omitempty"`
	BudgetBytes int    `json:"budget_bytes,omitempty"`
}

type InspectInput struct {
	Ref string `json:"ref"`
}
type InspectOutput struct {
	Value any `json:"value"`
}
type ApplyInput struct {
	ActionRef      string         `json:"action_ref"`
	Input          map[string]any `json:"input,omitempty"`
	IdempotencyKey string         `json:"idempotency_key"`
}
type GraphChangeInput struct {
	Mode           string         `json:"mode"`
	Patch          map[string]any `json:"patch"`
	Token          string         `json:"token,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	ActorRole      string         `json:"actor_role,omitempty"`
	ActorRoleRef   string         `json:"actor_role_ref,omitempty"`
}
type ReconcileInput struct {
	ActionID       string         `json:"action_id,omitempty"`
	EffectRef      string         `json:"effect_ref,omitempty"`
	Receipt        map[string]any `json:"receipt,omitempty"`
	IdempotencyKey string         `json:"idempotency_key"`
}
type PreWaitInput struct{}

type ToolContract struct {
	Name              string `json:"name"`
	InputSchemaSHA256 string `json:"inputSchemaSha256"`
	ReadOnly          bool   `json:"readOnly"`
}

func ToolContracts() []ToolContract {
	return []ToolContract{
		toolContract("dag_context", contextSchema(), true),
		toolContract("dag_inspect", inspectSchema(), true),
		toolContract("dag_apply", applySchema(), false),
		toolContract("dag_graph_change", graphChangeSchema(), false),
		toolContract("dag_reconcile", reconcileSchema(), false),
		toolContract("dag_pre_wait", schemaFor[PreWaitInput](), true),
	}
}

func Run(ctx context.Context, svc *service.Service) error {
	transport := &mcp.IOTransport{
		Reader: &readCloser{Reader: newBoundedNDJSONReader(os.Stdin, MaxMCPMessageBytes)},
		Writer: nopWriteCloser{Writer: os.Stdout},
	}
	return New(svc).Run(ctx, transport)
}

func New(svc *service.Service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "dagrail", Title: "DAGrail", Description: "LLM-led local DAG governance control plane.", Version: version.Version, WebsiteURL: "https://github.com/CongBao/dagrail"}, &mcp.ServerOptions{Instructions: "Treat DAGrail as runtime authority. Read bounded context, use returned opaque refs instead of copying oversized identifiers, follow its authorization and remediation plan, apply only a current returned action ref with one stable idempotency key, use NodeKind-specific outcomes, close or reconcile resources, inspect Effect continuity before confusing an unrelated head advance with a causal change, and call dag_pre_wait before yielding.", Capabilities: &mcp.ServerCapabilities{}})
	closed, openWorld := false, false
	readOnly := true
	mcp.AddTool(server, &mcp.Tool{Name: "dag_context", Title: "Get DAG context", Description: "Return byte-bounded role or node context with authorization, typed remediations, and signed allowed actions; use role_ref/node_ref for returned opaque identities.", InputSchema: contextSchema(), Annotations: &mcp.ToolAnnotations{Title: "Get DAG context", ReadOnlyHint: true, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input ContextInput) (*mcp.CallToolResult, service.ContextEnvelope, error) {
		if err := validateToolInput(input); err != nil {
			return nil, service.ContextEnvelope{}, err
		}
		roleID, nodeID := input.RoleID, input.NodeID
		if input.RoleRef != "" {
			if roleID != "" {
				return nil, service.ContextEnvelope{}, fmt.Errorf("provide role_id or role_ref, not both")
			}
			resolved, resolveErr := svc.ResolveEntityRef("role", input.RoleRef)
			if resolveErr != nil {
				return nil, service.ContextEnvelope{}, resolveErr
			}
			roleID = resolved
		}
		if input.NodeRef != "" {
			if nodeID != "" {
				return nil, service.ContextEnvelope{}, fmt.Errorf("provide node_id or node_ref, not both")
			}
			resolved, resolveErr := svc.ResolveEntityRef("node", input.NodeRef)
			if resolveErr != nil {
				return nil, service.ContextEnvelope{}, resolveErr
			}
			nodeID = resolved
		}
		raw, err := svc.ContextSinceContext(ctx, input.View, roleID, nodeID, input.BudgetBytes, input.Cursor)
		if err != nil {
			return nil, service.ContextEnvelope{}, err
		}
		var output service.ContextEnvelope
		err = json.Unmarshal(raw, &output)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "dag_inspect", Title: "Inspect DAG object", Description: "Resolve one opaque object or snapshot-bound page; oversized identifiers and action detail are returned as digest-bound chunks.", InputSchema: inspectSchema(), Annotations: &mcp.ToolAnnotations{Title: "Inspect DAG object", ReadOnlyHint: true, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input InspectInput) (*mcp.CallToolResult, InspectOutput, error) {
		if err := validateToolInput(input); err != nil {
			return nil, InspectOutput{}, err
		}
		value, err := svc.InspectContext(ctx, input.Ref)
		return nil, InspectOutput{Value: value}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "dag_apply", Title: "Apply allowed DAG action", Description: "Apply exactly one controller-issued action ref; its Role, Node, Attempt, resource, provider set, revision, head, and expiry are already bound.", InputSchema: applySchema(), Annotations: &mcp.ToolAnnotations{Title: "Apply allowed DAG action", ReadOnlyHint: false, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input ApplyInput) (*mcp.CallToolResult, service.ActionResult, error) {
		if err := validateToolInput(input); err != nil {
			return nil, service.ActionResult{}, err
		}
		raw, _ := json.Marshal(input.Input)
		output, err := svc.ApplyActionContext(ctx, input.ActionRef, raw, input.IdempotencyKey)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "dag_graph_change", Title: "Preview or apply graph change", Description: "Preview a typed GraphPatch, then apply that exact token as a leased Role with graph.change; re-preview after any head change.", InputSchema: graphChangeSchema(), Annotations: &mcp.ToolAnnotations{Title: "Preview or apply graph change", ReadOnlyHint: false, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(_ context.Context, _ *mcp.CallToolRequest, input GraphChangeInput) (*mcp.CallToolResult, service.GraphImpact, error) {
		if err := validateToolInput(input); err != nil {
			return nil, service.GraphImpact{}, err
		}
		raw, _ := json.Marshal(input.Patch)
		file, err := os.CreateTemp(svc.Project.DataDir, "mcp-graph-patch-*.json")
		if err != nil {
			return nil, service.GraphImpact{}, err
		}
		path := file.Name()
		defer os.Remove(path)
		if _, err = file.Write(raw); err != nil {
			_ = file.Close()
			return nil, service.GraphImpact{}, err
		}
		if err = file.Close(); err != nil {
			return nil, service.GraphImpact{}, err
		}
		if input.Mode == "preview" {
			output, err := svc.PreviewGraphChange(path)
			return nil, service.BoundedGraphImpact(output), err
		}
		if input.Mode != "apply" {
			return nil, service.GraphImpact{}, fmt.Errorf("graph change mode must be preview or apply")
		}
		actorRole := input.ActorRole
		if input.ActorRoleRef != "" {
			if actorRole != "" {
				return nil, service.GraphImpact{}, fmt.Errorf("provide actor_role or actor_role_ref, not both")
			}
			actorRole, err = svc.ResolveEntityRef("role", input.ActorRoleRef)
			if err != nil {
				return nil, service.GraphImpact{}, err
			}
		}
		output, err := svc.ApplyGraphChange(path, input.Token, input.IdempotencyKey, actorRole)
		return nil, service.BoundedGraphImpact(output), err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "dag_reconcile", Title: "Reconcile effect", Description: "Reconcile one prepared or unknown effect through native observation or typed adapter evidence; use effect_ref for an opaque identity.", InputSchema: reconcileSchema(), Annotations: &mcp.ToolAnnotations{Title: "Reconcile effect", ReadOnlyHint: false, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input ReconcileInput) (*mcp.CallToolResult, service.EffectActionResult, error) {
		if err := validateToolInput(input); err != nil {
			return nil, service.EffectActionResult{}, err
		}
		actionID := input.ActionID
		if input.EffectRef != "" {
			if actionID != "" {
				return nil, service.EffectActionResult{}, fmt.Errorf("provide action_id or effect_ref, not both")
			}
			resolved, resolveErr := svc.ResolveEntityRef("effect", input.EffectRef)
			if resolveErr != nil {
				return nil, service.EffectActionResult{}, resolveErr
			}
			actionID = resolved
		}
		if actionID == "" {
			return nil, service.EffectActionResult{}, fmt.Errorf("action_id or effect_ref is required")
		}
		raw, _ := json.Marshal(input.Receipt)
		output, err := svc.ReconcileEffectContext(ctx, actionID, raw, input.IdempotencyKey)
		if err != nil {
			return nil, service.EffectActionResult{}, err
		}
		bounded, err := svc.BoundedEffectAction(output)
		return nil, bounded, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "dag_pre_wait", Title: "Audit DAG liveness", Description: "Reject passive waiting while work remains and return a bounded count/preview plus paginated inspect refs for deterministic remediation.", InputSchema: schemaFor[PreWaitInput](), Annotations: &mcp.ToolAnnotations{Title: "Audit DAG liveness", ReadOnlyHint: readOnly, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, _ PreWaitInput) (*mcp.CallToolResult, service.PreWaitAudit, error) {
		output, err := svc.PreWaitContext(ctx)
		return nil, output, err
	})
	return server
}

func schemaFor[T any]() *jsonschema.Schema {
	schema, err := jsonschema.For[T](nil)
	if err != nil {
		panic(err)
	}
	return schema
}

func graphChangeSchema() *jsonschema.Schema {
	schema := schemaFor[GraphChangeInput]()
	schema.Properties["mode"].Enum = []any{"preview", "apply"}
	setMaxLength(schema, "mode", 16)
	setMaxLength(schema, "token", 16*1024)
	setMaxLength(schema, "idempotency_key", 256)
	setMaxLength(schema, "actor_role", 256)
	setMaxLength(schema, "actor_role_ref", 128)
	return schema
}

func contextSchema() *jsonschema.Schema {
	schema := schemaFor[ContextInput]()
	setMaxLength(schema, "view", 32)
	schema.Properties["view"].Enum = []any{"orchestrator", "worker", "reviewer"}
	setMaxLength(schema, "role_id", 256)
	setMaxLength(schema, "role_ref", 128)
	setMaxLength(schema, "node_id", 256)
	setMaxLength(schema, "node_ref", 128)
	minimum, maximum := float64(512), float64(12288)
	schema.Properties["budget_bytes"].Minimum = &minimum
	schema.Properties["budget_bytes"].Maximum = &maximum
	return schema
}

func inspectSchema() *jsonschema.Schema {
	schema := schemaFor[InspectInput]()
	setMaxLength(schema, "ref", 2048)
	return schema
}

func applySchema() *jsonschema.Schema {
	schema := schemaFor[ApplyInput]()
	setMaxLength(schema, "action_ref", 16*1024)
	setMaxLength(schema, "idempotency_key", 256)
	return schema
}

func reconcileSchema() *jsonschema.Schema {
	schema := schemaFor[ReconcileInput]()
	setMaxLength(schema, "action_id", 256)
	setMaxLength(schema, "effect_ref", 128)
	setMaxLength(schema, "idempotency_key", 256)
	return schema
}

func setMaxLength(schema *jsonschema.Schema, property string, value int) {
	if candidate := schema.Properties[property]; candidate != nil {
		candidate.MaxLength = &value
	}
}

func validateToolInput(input any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode MCP tool input: %w", err)
	}
	if len(raw) > MaxMCPToolInputBytes {
		return fmt.Errorf("MCP tool input exceeds %d bytes", MaxMCPToolInputBytes)
	}
	if err := domain.ValidateAuthorityJSON(raw); err != nil {
		return fmt.Errorf("MCP tool input: %w", err)
	}
	return nil
}

type boundedNDJSONReader struct {
	reader  *bufio.Reader
	limit   int
	pending []byte
}

func newBoundedNDJSONReader(reader io.Reader, limit int) *boundedNDJSONReader {
	return &boundedNDJSONReader{reader: bufio.NewReaderSize(reader, 64*1024), limit: limit}
}

func (r *boundedNDJSONReader) Read(target []byte) (int, error) {
	if len(r.pending) == 0 {
		line, err := r.readLine()
		if err != nil {
			return 0, err
		}
		r.pending = line
	}
	n := copy(target, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func (r *boundedNDJSONReader) readLine() ([]byte, error) {
	line := make([]byte, 0, 4096)
	for {
		chunk, err := r.reader.ReadSlice('\n')
		if len(line)+len(chunk) > r.limit {
			for err == bufio.ErrBufferFull {
				_, err = r.reader.ReadSlice('\n')
			}
			return nil, fmt.Errorf("MCP message exceeds %d bytes", r.limit)
		}
		line = append(line, chunk...)
		switch err {
		case nil:
			return line, nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			if len(line) == 0 {
				return nil, io.EOF
			}
			return line, nil
		default:
			return nil, err
		}
	}
}

type readCloser struct{ io.Reader }

func (*readCloser) Close() error { return nil }

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func toolContract(name string, schema *jsonschema.Schema, readOnly bool) ToolContract {
	raw, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(append([]byte("dagrail-mcp-input-schema-v1\x00"), canonical...))
	return ToolContract{Name: name, InputSchemaSHA256: "sha256:" + hex.EncodeToString(digest[:]), ReadOnly: readOnly}
}
