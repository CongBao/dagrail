package observe

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/CongBao/dagrail/internal/service"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestObservationSnapshotAndShadowAreSourceReadOnlyAndDriftDetecting(t *testing.T) {
	root, source, graphPath, shadow := observationFixture(t)
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "runtime-data"))
	first, err := Assess(source, graphPath, []string{"governance/policy.json", "requirements.md"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Assess(source, graphPath, []string{"requirements.md", "governance/policy.json"})
	if err != nil || first.SnapshotDigest != second.SnapshotDigest {
		t.Fatalf("assessment is not order-independent: %v %s %s", err, first.SnapshotDigest, second.SnapshotDigest)
	}
	if first.AuthorityFiles[0].Path != "governance/policy.json" || first.NodeCount != 2 || first.EdgeCount != 1 {
		t.Fatalf("unexpected observation snapshot: %#v", first)
	}
	assertSnapshotSchema(t, first)

	before := treeDigest(t, source)
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Join(source, "requirements.md"), 0o444); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(source, "governance", "policy.json"), 0o444); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(source, "governance"), 0o555); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(source, 0o555); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = os.Chmod(source, 0o755)
			_ = os.Chmod(filepath.Join(source, "governance"), 0o755)
			_ = os.Chmod(filepath.Join(source, "requirements.md"), 0o644)
			_ = os.Chmod(filepath.Join(source, "governance", "policy.json"), 0o644)
		}()
	}
	report, err := CreateShadow(source, graphPath, shadow, []string{"governance/policy.json", "requirements.md"})
	if err != nil {
		t.Fatal(err)
	}
	if report.ProjectID == "" || report.HeadSequence != 2 || report.Snapshot.SnapshotDigest != first.SnapshotDigest {
		t.Fatalf("unexpected shadow report: %#v", report)
	}
	if after := treeDigest(t, source); after != before {
		t.Fatalf("source tree changed during shadow creation: %s != %s", after, before)
	}
	if _, err := os.Stat(filepath.Join(source, ".dagrail")); !os.IsNotExist(err) {
		t.Fatalf("source received DAGrail state: %v", err)
	}
	shadowService, err := service.Open(shadow)
	if err != nil {
		t.Fatal(err)
	}
	segments, err := shadowService.VerifyJournal()
	if err != nil {
		t.Fatal(err)
	}
	for _, segment := range segments {
		for _, event := range segment.Events {
			if bytes.Contains(event.Payload, []byte(source)) || bytes.Contains(event.Payload, []byte(graphPath)) {
				t.Fatal("absolute source locator leaked into portable journal authority")
			}
		}
	}
	locatorInfo, err := os.Stat(filepath.Join(shadowService.Project.DataDir, "observation-locator.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && locatorInfo.Mode().Perm() != 0o600 {
		t.Fatalf("observation locator permissions = %o", locatorInfo.Mode().Perm())
	}
	verified, err := VerifyShadow(shadow)
	if err != nil || !verified.Valid || verified.ExpectedSnapshotDigest != verified.ObservedSnapshotDigest {
		t.Fatalf("fresh shadow verification: %#v %v", verified, err)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Join(source, "requirements.md"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "requirements.md"), []byte("changed requirement\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	drifted, err := VerifyShadow(shadow)
	if err != nil || drifted.Valid || drifted.ExpectedSnapshotDigest == drifted.ObservedSnapshotDigest {
		t.Fatalf("source drift was not detected: %#v %v", drifted, err)
	}
}

func assertSnapshotSchema(t *testing.T, snapshot Snapshot) {
	t.Helper()
	schemaRaw, err := os.ReadFile("../../schemas/observation-snapshot-v1beta1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(schemaRaw, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:dagrail:observation-snapshot", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:dagrail:observation-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var instance any
	if err := json.Unmarshal(raw, &instance); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("snapshot does not match published schema: %v", err)
	}
}

func TestObservationRejectsSourceWritesAndEscapingAuthoritySymlinks(t *testing.T) {
	root, source, graphPath, _ := observationFixture(t)
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "runtime-data"))
	inside := filepath.Join(source, "shadow")
	if _, err := CreateShadow(source, graphPath, inside, []string{"requirements.md"}); err == nil {
		t.Fatal("shadow inside source root was accepted")
	}
	if _, err := os.Stat(filepath.Join(source, ".dagrail")); !os.IsNotExist(err) {
		t.Fatalf("rejected migration wrote source state: %v", err)
	}
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(source, "escape.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Assess(source, graphPath, []string{"escape.txt"}); err == nil {
		t.Fatal("authority symlink outside source root was accepted")
	}
}

func observationFixture(t *testing.T) (root, source, graphPath, shadow string) {
	t.Helper()
	root = t.TempDir()
	source = filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "governance"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "requirements.md"), []byte("requirement\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "governance", "policy.json"), []byte(`{"gate":"review"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	graphPath = filepath.Join(root, "converted-graph.json")
	graph := `{"apiVersion":"dagrail.io/v1alpha1","kind":"Graph","metadata":{"name":"observed"},"spec":{"roles":[{"id":"dev","capabilities":["node.run","node.review"]}],"nodes":[{"id":"A","kind":"task","role":"dev","title":"A","outcomes":[{"id":"ok","class":"success"}]},{"id":"B","kind":"review","role":"dev","title":"B","outcomes":[{"id":"approve","class":"success"}]}],"edges":[{"id":"A-B","from":"A","to":"B","when":{"outcome":"ok"}}]}}`
	if err := os.WriteFile(graphPath, []byte(graph), 0o600); err != nil {
		t.Fatal(err)
	}
	shadow = filepath.Join(root, "shadow")
	return root, source, graphPath, shadow
}

func treeDigest(t *testing.T, root string) string {
	t.Helper()
	paths := []string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		relative, _ := filepath.Rel(root, path)
		_, _ = io.WriteString(hash, filepath.ToSlash(relative)+"\x00")
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(hash, file); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		_ = file.Close()
	}
	return hex.EncodeToString(hash.Sum(nil))
}
