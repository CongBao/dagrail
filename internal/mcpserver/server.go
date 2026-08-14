package mcpserver

import (
	"context"
	"encoding/json"
	"os"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/service"
	"github.com/CongBao/dagrail/internal/version"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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

func Run(ctx context.Context, svc *service.Service) error {
	return New(svc).Run(ctx, &mcp.StdioTransport{})
}

func New(svc *service.Service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "dagrail", Title: "DAGrail", Description: "LLM-led local DAG governance control plane.", Version: version.Version, WebsiteURL: "https://github.com/CongBao/dagrail"}, &mcp.ServerOptions{Instructions: "Read bounded context, choose only a returned allowed action, preserve idempotency keys, reconcile unknown effects, and run dag_pre_wait before yielding.", Capabilities: &mcp.ServerCapabilities{}})
	closed, openWorld := false, false
	readOnly := true
	mcp.AddTool(server, &mcp.Tool{Name: "dag_context", Title: "Get DAG context", Description: "Return a byte-bounded role or node context and allowed actions.", InputSchema: schemaFor[ContextInput](), Annotations: &mcp.ToolAnnotations{Title: "Get DAG context", ReadOnlyHint: true, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(_ context.Context, _ *mcp.CallToolRequest, input ContextInput) (*mcp.CallToolResult, service.ContextEnvelope, error) {
		raw, err := svc.ContextSince(input.View, input.RoleID, input.NodeID, input.BudgetBytes, input.Cursor)
		if err != nil {
			return nil, service.ContextEnvelope{}, err
		}
		var output service.ContextEnvelope
		err = json.Unmarshal(raw, &output)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "dag_inspect", Title: "Inspect DAG object", Description: "Resolve one opaque node, attempt, execution package, reuse decision, artifact, effect, incident, resource, or history ref.", InputSchema: schemaFor[InspectInput](), Annotations: &mcp.ToolAnnotations{Title: "Inspect DAG object", ReadOnlyHint: true, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(_ context.Context, _ *mcp.CallToolRequest, input InspectInput) (*mcp.CallToolResult, InspectOutput, error) {
		value, err := svc.Inspect(input.Ref)
		return nil, InspectOutput{Value: value}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "dag_apply", Title: "Apply allowed DAG action", Description: "Apply one version-bound allowed action with a stable idempotency key.", InputSchema: schemaFor[ApplyInput](), Annotations: &mcp.ToolAnnotations{Title: "Apply allowed DAG action", ReadOnlyHint: false, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(_ context.Context, _ *mcp.CallToolRequest, input ApplyInput) (*mcp.CallToolResult, service.ActionResult, error) {
		raw, _ := json.Marshal(input.Input)
		output, err := svc.ApplyAction(input.ActionRef, raw, input.IdempotencyKey)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "dag_graph_change", Title: "Preview or apply graph change", Description: "Preview a typed GraphPatch or apply it with the returned impact token.", InputSchema: graphChangeSchema(), Annotations: &mcp.ToolAnnotations{Title: "Preview or apply graph change", ReadOnlyHint: false, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(_ context.Context, _ *mcp.CallToolRequest, input GraphChangeInput) (*mcp.CallToolResult, service.GraphImpact, error) {
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
		output, err := svc.ApplyGraphChange(path, input.Token, input.IdempotencyKey, input.ActorRole)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "dag_reconcile", Title: "Reconcile effect", Description: "Classify one prepared or unknown external effect using adapter evidence.", InputSchema: schemaFor[ReconcileInput](), Annotations: &mcp.ToolAnnotations{Title: "Reconcile effect", ReadOnlyHint: false, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(_ context.Context, _ *mcp.CallToolRequest, input ReconcileInput) (*mcp.CallToolResult, domain.EffectAction, error) {
		raw, _ := json.Marshal(input.Receipt)
		output, err := svc.ReconcileEffect(input.ActionID, raw, input.IdempotencyKey)
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
	return schema
}
