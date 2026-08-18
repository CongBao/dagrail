package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/gitartifact"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestArtifactCLIClosesConsumerGitObjectsAndReportsScope(t *testing.T) {
	repository := t.TempDir()
	gitCLI(t, repository, "init")
	gitCLI(t, repository, "config", "user.name", "DAGrail Test")
	gitCLI(t, repository, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "file.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCLI(t, repository, "add", "file.txt")
	gitCLI(t, repository, "commit", "-m", "candidate")
	gitCLI(t, repository, "tag", "handoff")
	commit := gitCLI(t, repository, "rev-parse", "HEAD")
	tree := gitCLI(t, repository, "show", "-s", "--format=%T", commit)
	manifest := gitartifact.ClosureManifest{
		APIVersion: gitartifact.ClosureAPIVersion,
		Kind:       gitartifact.ClosureKind,
		Refs:       []gitartifact.RefExpectation{{Name: "refs/tags/handoff", OID: commit}},
		Objects: []gitartifact.ObjectExpectation{
			{Name: "candidate", OID: commit, Type: "commit", Tree: tree, RetainedBy: []string{"refs/tags/handoff"}},
			{Name: "tree", OID: tree, Type: "tree", RetainedBy: []string{"refs/tags/handoff"}},
		},
	}
	raw, _ := json.Marshal(manifest)
	manifestPath := filepath.Join(t.TempDir(), "closure.json")
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"artifact", "verify-git-closure", "--repo", repository, "--file", manifestPath}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("artifact closure CLI failed: %v stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"valid":true`) || !strings.Contains(stdout.String(), `"manifestDigest":"sha256:`) {
		t.Fatalf("unexpected closure output: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run([]string{"artifact", "inspect-scope", "--repo", repository, "--base", commit, "--candidate", commit, "--target", commit, "--prospective", commit}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"closed":true`) {
		t.Fatalf("unexpected scope output: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"entries":[]`) {
		t.Fatalf("zero-delta scope did not encode an empty array: %s", stdout.String())
	}
	validateArtifactScopeOutput(t, stdout.Bytes())
	if err := os.WriteFile(filepath.Join(repository, "unexpected.txt"), []byte("unexpected\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCLI(t, repository, "add", "unexpected.txt")
	gitCLI(t, repository, "commit", "-m", "unexplained")
	prospective := gitCLI(t, repository, "rev-parse", "HEAD")
	stdout.Reset()
	if err := Run([]string{"artifact", "inspect-scope", "--repo", repository, "--base", commit, "--candidate", commit, "--target", commit, "--prospective", prospective}, strings.NewReader(""), &stdout, &stderr); err == nil || DescribeError(err).Code != "diagnostic_failed" {
		t.Fatalf("unexplained scope did not fail its diagnostic gate: %v", err)
	}
	if !strings.Contains(stdout.String(), `"closed":false`) || !strings.Contains(stdout.String(), `"unexplained_prospective_delta":1`) {
		t.Fatalf("unexpected failed scope output: %s", stdout.String())
	}
}

func validateArtifactScopeOutput(t *testing.T, raw []byte) {
	t.Helper()
	schemaRaw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "git-integration-scope-v1alpha1.schema.json"))
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
	if err := compiler.AddResource("urn:dagrail:git-integration-scope-cli", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:dagrail:git-integration-scope-cli")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("CLI scope report violates its published schema: %v", err)
	}
}

func TestArtifactScopeCLIFailsWhenCandidateAndTargetShareOneChangeButProspectiveInventsAnother(t *testing.T) {
	repository := t.TempDir()
	gitCLI(t, repository, "init")
	gitCLI(t, repository, "config", "user.name", "DAGrail Test")
	gitCLI(t, repository, "config", "user.email", "test@example.invalid")
	path := filepath.Join(repository, "file.txt")
	if err := os.WriteFile(path, []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCLI(t, repository, "add", "file.txt")
	gitCLI(t, repository, "commit", "-m", "base")
	base := gitCLI(t, repository, "rev-parse", "HEAD")
	if err := os.WriteFile(path, []byte("shared\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCLI(t, repository, "commit", "-am", "shared candidate and target")
	shared := gitCLI(t, repository, "rev-parse", "HEAD")
	if err := os.WriteFile(path, []byte("unexplained third value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCLI(t, repository, "commit", "-am", "unexplained prospective")
	prospective := gitCLI(t, repository, "rev-parse", "HEAD")

	var stdout, stderr bytes.Buffer
	err := Run([]string{"artifact", "inspect-scope", "--repo", repository, "--base", base, "--candidate", shared, "--target", shared, "--prospective", prospective}, strings.NewReader(""), &stdout, &stderr)
	if err == nil || DescribeError(err).Code != "diagnostic_failed" {
		t.Fatalf("shared candidate/target with third prospective value passed: %v stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"closed":false`) || !strings.Contains(stdout.String(), `"unexplained_prospective_delta":1`) {
		t.Fatalf("unexpected fail-closed scope report: %s", stdout.String())
	}
}

func gitCLI(t *testing.T, repository string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repository}, args...)...)
	command.Env = append(os.Environ(), "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
