package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/commandcatalog"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestTypedErrorsAreBoundedAndStable(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		code      string
		exitCode  int
		retryable bool
	}{
		{name: "usage", err: usagef("bad command"), code: "usage", exitCode: commandcatalog.ExitUsage},
		{name: "operation", err: errors.New("operation broke"), code: "operation_failed", exitCode: commandcatalog.ExitOperationFailed},
		{name: "diagnostic", err: diagnosticError(errors.New("unhealthy")), code: "diagnostic_failed", exitCode: commandcatalog.ExitDiagnostic},
		{name: "interrupt", err: context.Canceled, code: "interrupted", exitCode: commandcatalog.ExitInterrupted, retryable: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			report := DescribeError(test.err)
			if report.Code != test.code || report.ExitCode != test.exitCode || report.Retryable != test.retryable {
				t.Fatalf("unexpected report: %#v", report)
			}
			var output bytes.Buffer
			if err := WriteErrorJSON(&output, test.err); err != nil {
				t.Fatal(err)
			}
			if output.Len() > 4096 || !json.Valid(output.Bytes()) {
				t.Fatalf("error envelope is invalid or unbounded: %d", output.Len())
			}
			validateErrorSchema(t, output.Bytes())
		})
	}
	long := errors.New(strings.Repeat("界", 4096))
	if len([]byte(DescribeError(long).Message)) > 2048 {
		t.Fatal("error message exceeded its byte budget")
	}
}

func validateErrorSchema(t *testing.T, raw []byte) {
	t.Helper()
	schemaRaw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "cli-error-v1alpha1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document, instance any
	if err := json.Unmarshal(schemaRaw, &document); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &instance); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:dagrail:cli-error", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:dagrail:cli-error")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("error does not match schema: %v", err)
	}
}

func TestRunContextClassifiesUsageAndCancellation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := RunContext(context.Background(), []string{"--errors=json", "unknown"}, strings.NewReader(""), &stdout, &stderr)
	if report := DescribeError(err); report.Code != "usage" || report.ExitCode != 2 {
		t.Fatalf("unknown command was not classified as usage: %#v", report)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = RunContext(ctx, []string{"version"}, strings.NewReader(""), &stdout, &stderr)
	if report := DescribeError(err); report.Code != "interrupted" || report.ExitCode != 130 {
		t.Fatalf("cancellation was not preserved: %#v", report)
	}
}

func TestGlobalJSONErrorSelectionIsExplicit(t *testing.T) {
	t.Setenv("DAGRAIL_ERROR_FORMAT", "")
	if !WantsJSONErrors([]string{"--errors=json", "version"}) || WantsJSONErrors([]string{"version"}) {
		t.Fatal("global error format selection drifted")
	}
	t.Setenv("DAGRAIL_ERROR_FORMAT", "json")
	if !WantsJSONErrors([]string{"version"}) {
		t.Fatal("environment opt-in was ignored")
	}
}

func TestJSONErrorModeSuppressesFlagPackageNoise(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := RunContext(context.Background(), []string{"--errors=json", "frontier", "--unknown"}, strings.NewReader(""), &stdout, &stderr)
	if err == nil || DescribeError(err).Code != "usage" {
		t.Fatalf("unknown flag was not a usage error: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("JSON error mode leaked non-JSON flag output: %q", stderr.String())
	}
}
