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
	NodeID      string `json:"node_id,omitempty"`
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
}
type ReconcileInput struct {
	ActionID       string         `json:"action_id"`
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
	server := mcp.NewServer(&mcp.Implementation{Name: "dagrail", Title: "DAGrail", Description: "LLM-led local DAG governance control plane.", Version: version.Version, WebsiteURL: "https://github.com/CongBao/dagrail"}, &mcp.ServerOptions{Instructions: "Treat DAGrail as runtime authority. Read bounded context, apply only a current returned action ref with one stable idempotency key, use NodeKind-specific outcomes, close or reconcile resources, reconcile unknown effects, and call dag_pre_wait before yielding.", Capabilities: &mcp.ServerCapabilities{}})
	closed, openWorld := false, false
	readOnly := true
	mcp.AddTool(server, &mcp.Tool{Name: "dag_context", Title: "Get DAG context", Description: "Return a byte-bounded role or node context and allowed actions.", InputSchema: contextSchema(), Annotations: &mcp.ToolAnnotations{Title: "Get DAG context", ReadOnlyHint: true, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(_ context.Context, _ *mcp.CallToolRequest, input ContextInput) (*mcp.CallToolResult, service.ContextEnvelope, error) {
		if err := validateToolInput(input); err != nil {
			return nil, service.ContextEnvelope{}, err
		}
		raw, err := svc.ContextSince(input.View, input.RoleID, input.NodeID, input.BudgetBytes, input.Cursor)
		if err != nil {
			return nil, service.ContextEnvelope{}, err
		}
		var output service.ContextEnvelope
		err = json.Unmarshal(raw, &output)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "dag_inspect", Title: "Inspect DAG object", Description: "Resolve one opaque node, attempt, decision, execution package, reuse decision, artifact, effect, incident, resource, or bounded history ref.", InputSchema: inspectSchema(), Annotations: &mcp.ToolAnnotations{Title: "Inspect DAG object", ReadOnlyHint: true, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(_ context.Context, _ *mcp.CallToolRequest, input InspectInput) (*mcp.CallToolResult, InspectOutput, error) {
		if err := validateToolInput(input); err != nil {
			return nil, InspectOutput{}, err
		}
		value, err := svc.Inspect(input.Ref)
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
			return nil, output, err
		}
		if input.Mode != "apply" {
			return nil, service.GraphImpact{}, fmt.Errorf("graph change mode must be preview or apply")
		}
		output, err := svc.ApplyGraphChange(path, input.Token, input.IdempotencyKey, input.ActorRole)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "dag_reconcile", Title: "Reconcile effect", Description: "Reconcile one prepared or unknown effect through native observation or typed adapter evidence.", InputSchema: reconcileSchema(), Annotations: &mcp.ToolAnnotations{Title: "Reconcile effect", ReadOnlyHint: false, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input ReconcileInput) (*mcp.CallToolResult, domain.EffectAction, error) {
		if err := validateToolInput(input); err != nil {
			return nil, domain.EffectAction{}, err
		}
		raw, _ := json.Marshal(input.Receipt)
		output, err := svc.ReconcileEffectContext(ctx, input.ActionID, raw, input.IdempotencyKey)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "dag_pre_wait", Title: "Audit DAG liveness", Description: "Reject passive waiting while ready, submitted, expired, or unreconciled work exists.", InputSchema: schemaFor[PreWaitInput](), Annotations: &mcp.ToolAnnotations{Title: "Audit DAG liveness", ReadOnlyHint: readOnly, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(_ context.Context, _ *mcp.CallToolRequest, _ PreWaitInput) (*mcp.CallToolResult, service.PreWaitAudit, error) {
		output, err := svc.PreWait()
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
	return schema
}

func contextSchema() *jsonschema.Schema {
	schema := schemaFor[ContextInput]()
	setMaxLength(schema, "view", 32)
	setMaxLength(schema, "role_id", 256)
	setMaxLength(schema, "node_id", 256)
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
