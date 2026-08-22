package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/CongBao/dagrail/internal/controller"
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
	Root        string `json:"root,omitempty"`
	View        string `json:"view" jsonschema:"orchestrator, worker, or reviewer"`
	RoleID      string `json:"role_id,omitempty"`
	RoleRef     string `json:"role_ref,omitempty"`
	NodeID      string `json:"node_id,omitempty"`
	NodeRef     string `json:"node_ref,omitempty"`
	Cursor      uint64 `json:"cursor,omitempty"`
	BudgetBytes int    `json:"budget_bytes,omitempty"`
}

type InspectInput struct {
	Root string `json:"root,omitempty"`
	Ref  string `json:"ref"`
}
type InspectOutput struct {
	Value any `json:"value"`
}
type ApplyInput struct {
	Root           string         `json:"root,omitempty"`
	ActionRef      string         `json:"action_ref"`
	Input          map[string]any `json:"input"`
	IdempotencyKey string         `json:"idempotency_key"`
}
type GraphChangeInput struct {
	Root           string         `json:"root,omitempty"`
	Mode           string         `json:"mode"`
	Patch          map[string]any `json:"patch"`
	Token          string         `json:"token,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	ActorRole      string         `json:"actor_role,omitempty"`
	ActorRoleRef   string         `json:"actor_role_ref,omitempty"`
}
type ReconcileInput struct {
	Root           string         `json:"root,omitempty"`
	ActionID       string         `json:"action_id,omitempty"`
	EffectRef      string         `json:"effect_ref,omitempty"`
	Receipt        map[string]any `json:"receipt,omitempty"`
	IdempotencyKey string         `json:"idempotency_key"`
}
type PreWaitInput struct {
	Root string `json:"root,omitempty"`
}

type CommandExecutor interface {
	Execute(context.Context, []string, []byte) ([]byte, []byte, error)
}

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
	return runServer(ctx, New(svc))
}

func RunRemote(ctx context.Context, executor CommandExecutor, defaultRoot string) error {
	return runServer(ctx, NewRemote(executor, defaultRoot))
}

func runServer(ctx context.Context, server *mcp.Server) error {
	transport := &mcp.IOTransport{
		Reader: &readCloser{Reader: newBoundedNDJSONReader(os.Stdin, MaxMCPMessageBytes)},
		Writer: nopWriteCloser{Writer: os.Stdout},
	}
	return server.Run(ctx, transport)
}

func New(svc *service.Service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "dagrail", Title: "DAGrail", Description: "LLM-led local DAG governance control plane.", Version: version.Version, WebsiteURL: "https://github.com/CongBao/dagrail"}, &mcp.ServerOptions{Instructions: "Treat DAGrail as runtime authority. Read bounded context, use returned opaque refs instead of copying oversized identifiers, follow its authorization and remediation plan, apply only a current returned action ref with one stable idempotency key, use NodeKind-specific outcomes, close or reconcile resources, and call dag_pre_wait before yielding. A returned incident.control-resolve action closes a terminal failed/cancelled Attempt under truthful controller authority; never impersonate its original owner or substitute it for incident.supersede or observation closure. A returned role.control-transfer action replaces one exact unexpired session under a distinct truthful controller; ordinary takeover remains expiry-only.", Capabilities: &mcp.ServerCapabilities{}})
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
	mcp.AddTool(server, &mcp.Tool{Name: "dag_apply", Title: "Apply allowed DAG action", Description: "Apply exactly one controller-issued action ref with an explicit input JSON object (use {} for a zero-field action); its Role, Node, Attempt, resource, provider set, revision, head, and expiry are already bound.", InputSchema: applySchema(), Annotations: &mcp.ToolAnnotations{Title: "Apply allowed DAG action", ReadOnlyHint: false, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input ApplyInput) (*mcp.CallToolResult, service.ActionResult, error) {
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

func NewRemote(executor CommandExecutor, defaultRoot string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "dagrail", Title: "DAGrail", Description: "LLM-led local DAG governance control plane.", Version: version.Version, WebsiteURL: "https://github.com/CongBao/dagrail"}, &mcp.ServerOptions{Instructions: "DAGrail initializes without a project. Pass root when project discovery is ambiguous. For dag_context use role_id/node_id for stable IDs or role_ref/node_ref for controller-issued opaque selectors. Treat returned context and refs as runtime authority, reuse one stable idempotency key for retries, distinguish Effect dispatch from confirmation, and call dag_pre_wait before yielding. Use incident.control-resolve only when returned to a truthful controller Role for a terminal failed/cancelled Attempt; never impersonate the original owner. Use role.control-transfer only when returned to a distinct truthful controller for the exact active prior session; takeover remains expiry-only.", Capabilities: &mcp.ServerCapabilities{}})
	closed, openWorld := false, false
	mcp.AddTool(server, &mcp.Tool{Name: "dag_context", Title: "Get DAG context", Description: "Return byte-bounded context; select stable identities with role_id/node_id or opaque identities with role_ref/node_ref.", InputSchema: contextSchema(), Annotations: &mcp.ToolAnnotations{Title: "Get DAG context", ReadOnlyHint: true, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input ContextInput) (*mcp.CallToolResult, service.ContextEnvelope, error) {
		if err := validateToolInput(input); err != nil {
			return nil, service.ContextEnvelope{}, err
		}
		args := []string{"context", "--root", selectedRoot(input.Root, defaultRoot), "--view", input.View}
		args = appendOptional(args, "--role", input.RoleID, "--role-ref", input.RoleRef, "--node", input.NodeID, "--node-ref", input.NodeRef)
		if input.Cursor != 0 {
			args = append(args, "--cursor", strconv.FormatUint(input.Cursor, 10))
		}
		if input.BudgetBytes != 0 {
			args = append(args, "--budget-bytes", strconv.Itoa(input.BudgetBytes))
		}
		output, err := remoteJSON[service.ContextEnvelope](ctx, executor, args, nil)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "dag_inspect", Title: "Inspect DAG object", Description: "Resolve one opaque object or snapshot-bound page.", InputSchema: inspectSchema(), Annotations: &mcp.ToolAnnotations{Title: "Inspect DAG object", ReadOnlyHint: true, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input InspectInput) (*mcp.CallToolResult, InspectOutput, error) {
		if err := validateToolInput(input); err != nil {
			return nil, InspectOutput{}, err
		}
		value, err := remoteAny(ctx, executor, []string{"inspect", "--root", selectedRoot(input.Root, defaultRoot), "--ref", input.Ref})
		return nil, InspectOutput{Value: value}, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "dag_apply", Title: "Apply allowed DAG action", Description: "Apply exactly one controller-issued action ref with an explicit input JSON object; use {} for a zero-field action.", InputSchema: applySchema(), Annotations: &mcp.ToolAnnotations{Title: "Apply allowed DAG action", ReadOnlyHint: false, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input ApplyInput) (*mcp.CallToolResult, service.ActionResult, error) {
		if err := validateToolInput(input); err != nil {
			return nil, service.ActionResult{}, err
		}
		raw, _ := json.Marshal(input.Input)
		args := []string{"action", "apply", "--root", selectedRoot(input.Root, defaultRoot), "--ref", input.ActionRef, "--input", string(raw), "--idempotency-key", input.IdempotencyKey}
		output, err := remoteJSON[service.ActionResult](ctx, executor, args, nil)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "dag_graph_change", Title: "Preview or apply graph change", Description: "Preview a typed GraphPatch, then apply its exact impact token.", InputSchema: graphChangeSchema(), Annotations: &mcp.ToolAnnotations{Title: "Preview or apply graph change", ReadOnlyHint: false, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input GraphChangeInput) (*mcp.CallToolResult, service.GraphImpact, error) {
		if err := validateToolInput(input); err != nil {
			return nil, service.GraphImpact{}, err
		}
		raw, _ := json.Marshal(input.Patch)
		file, err := os.CreateTemp("", "dagrail-mcp-graph-patch-*.json")
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
		command := "preview-change"
		if input.Mode == "apply" {
			command = "apply-change"
		} else if input.Mode != "preview" {
			return nil, service.GraphImpact{}, fmt.Errorf("graph change mode must be preview or apply")
		}
		args := []string{"graph", command, "--root", selectedRoot(input.Root, defaultRoot), "--file", path}
		if input.Mode == "apply" {
			args = appendOptional(args, "--token", input.Token, "--idempotency-key", input.IdempotencyKey, "--actor-role", input.ActorRole, "--actor-role-ref", input.ActorRoleRef)
		}
		output, err := remoteJSON[service.GraphImpact](ctx, executor, args, nil)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "dag_reconcile", Title: "Reconcile effect", Description: "Reconcile one prepared or unknown Effect.", InputSchema: reconcileSchema(), Annotations: &mcp.ToolAnnotations{Title: "Reconcile effect", ReadOnlyHint: false, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input ReconcileInput) (*mcp.CallToolResult, service.EffectActionResult, error) {
		if err := validateToolInput(input); err != nil {
			return nil, service.EffectActionResult{}, err
		}
		raw, _ := json.Marshal(input.Receipt)
		args := []string{"reconcile", "--root", selectedRoot(input.Root, defaultRoot), "--receipt", string(raw), "--idempotency-key", input.IdempotencyKey}
		args = appendOptional(args, "--action", input.ActionID, "--effect-ref", input.EffectRef)
		output, err := remoteJSON[service.EffectActionResult](ctx, executor, args, nil)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "dag_pre_wait", Title: "Audit DAG liveness", Description: "Reject passive waiting while work remains.", InputSchema: schemaFor[PreWaitInput](), Annotations: &mcp.ToolAnnotations{Title: "Audit DAG liveness", ReadOnlyHint: true, DestructiveHint: &closed, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input PreWaitInput) (*mcp.CallToolResult, service.PreWaitAudit, error) {
		if err := validateToolInput(input); err != nil {
			return nil, service.PreWaitAudit{}, err
		}
		output, err := remoteJSON[service.PreWaitAudit](ctx, executor, []string{"pre-wait", "--root", selectedRoot(input.Root, defaultRoot)}, nil)
		return nil, output, err
	})
	return server
}

func selectedRoot(root, fallback string) string {
	if root != "" {
		return root
	}
	if fallback != "" {
		return fallback
	}
	return "."
}

func appendOptional(args []string, values ...string) []string {
	for index := 0; index+1 < len(values); index += 2 {
		if values[index+1] != "" {
			args = append(args, values[index], values[index+1])
		}
	}
	return args
}

func remoteJSON[T any](ctx context.Context, executor CommandExecutor, args []string, input []byte) (T, error) {
	var result T
	if executor == nil {
		return result, fmt.Errorf("DAGrail controller is unavailable")
	}
	stdout, stderr, err := executor.Execute(ctx, args, input)
	if err != nil {
		if structured := structuredRemoteError(err); structured != nil {
			return result, structured
		}
		if len(stderr) != 0 {
			return result, fmt.Errorf("%w: %s", err, string(stderr))
		}
		return result, err
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("decode DAGrail controller response: %w", err)
	}
	return result, nil
}

func remoteAny(ctx context.Context, executor CommandExecutor, args []string) (any, error) {
	stdout, stderr, err := executor.Execute(ctx, args, nil)
	if err != nil {
		if structured := structuredRemoteError(err); structured != nil {
			return nil, structured
		}
		if len(stderr) != 0 {
			return nil, fmt.Errorf("%w: %s", err, string(stderr))
		}
		return nil, err
	}
	var result any
	decoder := json.NewDecoder(bytes.NewReader(stdout))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

type remoteToolError struct{ payload []byte }

func (err *remoteToolError) Error() string { return string(err.payload) }

func structuredRemoteError(err error) error {
	var remote *controller.RPCError
	if !errors.As(err, &remote) {
		return nil
	}
	payload, marshalErr := json.Marshal(map[string]any{
		"apiVersion": "dagrail.io/mcp-error/v1alpha1",
		"kind":       "MCPToolError",
		"code":       remote.Code,
		"message":    remote.Message,
		"hint":       remote.Hint,
		"retryable":  remote.Retryable,
	})
	if marshalErr != nil {
		return remote
	}
	return &remoteToolError{payload: payload}
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
	schema.Properties["token"].Description = "Apply-only impact token returned by preview. Ignored in preview mode."
	schema.Properties["idempotency_key"].Description = "Apply-only stable command key. Ignored in preview mode."
	schema.Properties["actor_role"].Description = "Apply-only stable Role ID. Ignored in preview mode; use actor_role_ref for an opaque selector."
	schema.Properties["actor_role_ref"].Description = "Apply-only controller-issued opaque Role selector. Ignored in preview mode."
	setMaxLength(schema, "mode", 16)
	setMaxLength(schema, "token", 16*1024)
	setMaxLength(schema, "idempotency_key", 256)
	setMaxLength(schema, "actor_role", 256)
	setMaxLength(schema, "actor_role_ref", 128)
	setMaxLength(schema, "root", 4096)
	return schema
}

func contextSchema() *jsonschema.Schema {
	schema := schemaFor[ContextInput]()
	setMaxLength(schema, "view", 32)
	limits := service.ContextBudgetLimits()
	views := make([]any, 0, len(limits))
	maximumBudget := 0
	for _, limit := range limits {
		views = append(views, limit.View)
		if limit.Bytes > maximumBudget {
			maximumBudget = limit.Bytes
		}
		view := any(limit.View)
		maximum := float64(limit.Bytes)
		schema.AllOf = append(schema.AllOf, &jsonschema.Schema{
			If: &jsonschema.Schema{
				Required:   []string{"view"},
				Properties: map[string]*jsonschema.Schema{"view": {Const: &view}},
			},
			Then: &jsonschema.Schema{
				Properties: map[string]*jsonschema.Schema{"budget_bytes": {Maximum: &maximum}},
			},
		})
	}
	schema.Properties["view"].Enum = views
	setMaxLength(schema, "role_id", 256)
	setMaxLength(schema, "role_ref", 128)
	setMaxLength(schema, "node_id", 256)
	setMaxLength(schema, "node_ref", 128)
	setMaxLength(schema, "root", 4096)
	minimum, maximum := float64(service.MinimumContextBudgetBytes), float64(maximumBudget)
	schema.Properties["budget_bytes"].Minimum = &minimum
	schema.Properties["budget_bytes"].Maximum = &maximum
	return schema
}

func inspectSchema() *jsonschema.Schema {
	schema := schemaFor[InspectInput]()
	setMaxLength(schema, "ref", 2048)
	setMaxLength(schema, "root", 4096)
	return schema
}

func applySchema() *jsonschema.Schema {
	schema := schemaFor[ApplyInput]()
	setMaxLength(schema, "action_ref", 16*1024)
	setMaxLength(schema, "idempotency_key", 256)
	setMaxLength(schema, "root", 4096)
	return schema
}

func reconcileSchema() *jsonschema.Schema {
	schema := schemaFor[ReconcileInput]()
	setMaxLength(schema, "action_id", 256)
	setMaxLength(schema, "effect_ref", 128)
	setMaxLength(schema, "idempotency_key", 256)
	setMaxLength(schema, "root", 4096)
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
