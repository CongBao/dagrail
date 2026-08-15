package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/sdk"
)

type fixtureImporter struct{}

func (fixtureImporter) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "fixture.importer", Version: "1.0.0", SchemaHash: "fixture-v1", Stability: sdk.StabilityExperimental}
}

type alternateFixtureImporter struct{ fixtureImporter }

func (alternateFixtureImporter) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "fixture.importer.alternate", Version: "1.0.0", SchemaHash: "fixture-v1", Stability: sdk.StabilityExperimental}
}
func (fixtureImporter) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["name"],"properties":{"name":{"type":"string","minLength":1}}}`)
}
func (fixtureImporter) Import(_ context.Context, input json.RawMessage) (json.RawMessage, error) {
	var value struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(input, &value)
	return json.RawMessage(`{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"` + value.Name + `"},"spec":{"roles":[{"id":"worker","capabilities":["node.run"]}],"nodes":[{"id":"A","kind":"task","role":"worker","title":"A","outcomes":[{"id":"success","class":"success"}]}],"edges":[]}}`), nil
}

type countProjection struct{}

func (countProjection) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "fixture.projection", Version: "1.0.0", SchemaHash: "fixture-v1", Stability: sdk.StabilityExperimental}
}
func (countProjection) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"array","items":{"type":"object","required":["sequence","type","payload"]}}`)
}
func (countProjection) Project(_ context.Context, events []json.RawMessage) (json.RawMessage, error) {
	return json.Marshal(map[string]int{"events": len(events)})
}

func TestImporterAndProjectionProvidersUseBoundedRuntime(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", root+"/.data")
	svc, err := Init(root, "providers")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Providers.RegisterImporter(fixtureImporter{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Providers.RegisterImporter(alternateFixtureImporter{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Providers.RegisterProjection(countProjection{}); err != nil {
		t.Fatal(err)
	}

	result, err := svc.ImportGraphFromProvider(context.Background(), "fixture.importer", json.RawMessage(`{"name":"from-provider"}`), "provider-import", "governor")
	if err != nil {
		t.Fatal(err)
	}
	if result.GraphRevision == "" {
		t.Fatalf("missing graph revision: %+v", result)
	}
	if _, err := svc.ImportGraphFromProvider(context.Background(), "fixture.importer.alternate", json.RawMessage(`{"name":"from-provider"}`), "provider-import", "governor"); err == nil || !strings.Contains(err.Error(), "another command") {
		t.Fatalf("provider ID was not bound into import intent: %v", err)
	}
	segments, err := svc.VerifyJournal()
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 1 || !strings.Contains(string(segments[0].Events[0].Payload), `"kind":"provider"`) || !strings.Contains(string(segments[0].Events[0].Payload), `"inputDigest":"sha256:`) {
		t.Fatalf("import provenance was not journaled: %+v", segments)
	}

	projection, err := svc.RenderProjection(context.Background(), "fixture.projection")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(projection.Output), `"events":1`) {
		t.Fatalf("unexpected projection: %s", projection.Output)
	}
}
