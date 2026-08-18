package gitartifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestVerifyClosesCommitTreeTagAndOrderedParents(t *testing.T) {
	repository := fixtureRepository(t)
	prospective := gitTest(t, repository, "rev-parse", "HEAD")
	tree := gitTest(t, repository, "show", "-s", "--format=%T", prospective)
	parents := strings.Fields(gitTest(t, repository, "show", "-s", "--format=%P", prospective))
	rawTag := gitTest(t, repository, "rev-parse", "refs/tags/handoff")
	manifest := ClosureManifest{
		APIVersion: ClosureAPIVersion,
		Kind:       ClosureKind,
		Refs:       []RefExpectation{{Name: "refs/tags/handoff", OID: rawTag, Peeled: prospective}},
		Objects: []ObjectExpectation{
			{Name: "prospective", OID: prospective, Type: "commit", Tree: tree, Parents: parents, RetainedBy: []string{"refs/tags/handoff"}},
			{Name: "prospective-tree", OID: tree, Type: "tree", RetainedBy: []string{"refs/tags/handoff"}},
			{Name: "handoff-tag", OID: rawTag, Type: "tag", RetainedBy: []string{"refs/tags/handoff"}},
		},
	}
	report, err := Verify(repository, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Valid || report.ManifestDigest == "" || len(report.Objects) != 3 || len(report.Refs) != 1 {
		t.Fatalf("unexpected closure report: %#v", report)
	}
	validateSchema(t, "git-artifact-closure-v1alpha1.schema.json", report)
	manifest.Refs[0].Peeled = strings.Repeat("0", 40)
	report, err = Verify(repository, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if report.Valid || report.Refs[0].Valid {
		t.Fatalf("mismatched peeled target passed closure: %#v", report)
	}
}

func TestInspectScopeSeparatesProducerTargetAndUnexplainedDeltas(t *testing.T) {
	repository := fixtureRepository(t)
	base := gitTest(t, repository, "rev-parse", "--verify", "refs/tags/base")
	candidate := gitTest(t, repository, "rev-parse", "--verify", "refs/tags/candidate")
	target := gitTest(t, repository, "rev-parse", "--verify", "refs/tags/target")
	prospective := gitTest(t, repository, "rev-parse", "HEAD")
	report, err := InspectScope(repository, base, candidate, target, prospective)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts["candidate_only"] != 1 || report.Counts["target_history_only"] != 1 || !report.Closed {
		t.Fatalf("unexpected clean scope report: %#v", report)
	}
	validateSchema(t, "git-integration-scope-v1alpha1.schema.json", report)
	if err := os.WriteFile(filepath.Join(repository, "unexplained.txt"), []byte("unexpected\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "add", "unexplained.txt")
	gitTest(t, repository, "commit", "-m", "unexplained prospective delta")
	prospective = gitTest(t, repository, "rev-parse", "HEAD")
	report, err = InspectScope(repository, base, candidate, target, prospective)
	if err != nil {
		t.Fatal(err)
	}
	if report.Closed || report.Counts["unexplained_prospective_delta"] != 1 {
		t.Fatalf("unexplained prospective delta was not isolated: %#v", report)
	}
	rawTag := gitTest(t, repository, "rev-parse", "refs/tags/handoff")
	if _, err := InspectScope(repository, base, rawTag, target, prospective); err == nil {
		t.Fatal("annotated tag object was accepted as an exact commit identity")
	}
}

func TestInspectScopeZeroDeltaReportMatchesPublishedSchema(t *testing.T) {
	repository := fixtureRepository(t)
	commit := gitTest(t, repository, "rev-parse", "HEAD")
	report, err := InspectScope(repository, commit, commit, commit, commit)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Closed || report.Entries == nil || len(report.Entries) != 0 {
		t.Fatalf("zero-delta scope is not a closed empty array: %#v", report)
	}
	validateSchema(t, "git-integration-scope-v1alpha1.schema.json", report)
}

func TestDecodeManifestRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "closure.json")
	raw, _ := json.Marshal(map[string]any{"apiVersion": ClosureAPIVersion, "kind": ClosureKind, "objects": []any{}, "refs": []any{}, "unexpected": true})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeManifest(path); err == nil {
		t.Fatal("unknown manifest field was accepted")
	}
}

func TestDecodeManifestUsesBoundedRegularFileAndContext(t *testing.T) {
	directory := t.TempDir()
	oversized := filepath.Join(directory, "oversized.json")
	if err := os.WriteFile(oversized, bytes.Repeat([]byte("x"), maxManifestBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeManifest(oversized); err == nil || !strings.Contains(err.Error(), "1..1048576 bytes") {
		t.Fatalf("oversized manifest was not rejected before decode: %v", err)
	}

	ordinary := filepath.Join(directory, "ordinary.json")
	if err := os.WriteFile(ordinary, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "manifest-link.json")
	if err := os.Symlink(ordinary, link); err == nil {
		if _, err := DecodeManifest(link); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("manifest symlink was accepted: %v", err)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DecodeManifestContext(cancelled, ordinary); err != context.Canceled {
		t.Fatalf("manifest decode ignored caller cancellation: %v", err)
	}
}

func TestGitEvidenceIgnoresReplaceGraftsAndRepositoryEnvironment(t *testing.T) {
	t.Run("replace", func(t *testing.T) {
		repository := fixtureRepository(t)
		original := gitTest(t, repository, "rev-parse", "HEAD")
		originalTree := gitTest(t, repository, "show", "-s", "--format=%T", original)
		originalParents := strings.Fields(gitTest(t, repository, "show", "-s", "--format=%P", original))
		replacement := gitTest(t, repository, "rev-parse", "refs/tags/candidate")
		replacementTree := gitTest(t, repository, "show", "-s", "--format=%T", replacement)
		replacementParents := strings.Fields(gitTest(t, repository, "show", "-s", "--format=%P", replacement))
		rawTag := gitTest(t, repository, "rev-parse", "refs/tags/handoff")
		gitTest(t, repository, "replace", original, replacement)

		manifest := ClosureManifest{
			APIVersion: ClosureAPIVersion,
			Kind:       ClosureKind,
			Refs:       []RefExpectation{{Name: "refs/tags/handoff", OID: rawTag, Peeled: original}},
			Objects:    []ObjectExpectation{{Name: "original", OID: original, Type: "commit", Tree: replacementTree, Parents: replacementParents, RetainedBy: []string{"refs/tags/handoff"}}},
		}
		report, err := Verify(repository, manifest)
		if err != nil {
			t.Fatal(err)
		}
		if report.Valid || report.Objects[0].Valid {
			t.Fatalf("replacement commit bytes were accepted for the original OID: %#v", report)
		}
		manifest.Objects[0].Tree = originalTree
		manifest.Objects[0].Parents = originalParents
		report, err = Verify(repository, manifest)
		if err != nil || !report.Valid {
			t.Fatalf("raw original commit did not verify with replace ref present: report=%#v err=%v", report, err)
		}
	})

	t.Run("graft", func(t *testing.T) {
		repository := fixtureRepository(t)
		commit := gitTest(t, repository, "rev-parse", "HEAD")
		tree := gitTest(t, repository, "show", "-s", "--format=%T", commit)
		realParents := strings.Fields(gitTest(t, repository, "show", "-s", "--format=%P", commit))
		fakeParent := gitTest(t, repository, "rev-parse", "refs/tags/base")
		if err := os.MkdirAll(filepath.Join(repository, ".git", "info"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repository, ".git", "info", "grafts"), []byte(commit+" "+fakeParent+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		rawTag := gitTest(t, repository, "rev-parse", "refs/tags/handoff")
		manifest := ClosureManifest{
			APIVersion: ClosureAPIVersion,
			Kind:       ClosureKind,
			Refs:       []RefExpectation{{Name: "refs/tags/handoff", OID: rawTag, Peeled: commit}},
			Objects:    []ObjectExpectation{{Name: "commit", OID: commit, Type: "commit", Tree: tree, Parents: []string{fakeParent}, RetainedBy: []string{"refs/tags/handoff"}}},
		}
		report, err := Verify(repository, manifest)
		if err != nil {
			t.Fatal(err)
		}
		if report.Valid || report.Objects[0].Valid {
			t.Fatalf("grafted parent graph was accepted as raw history: %#v", report)
		}
		manifest.Objects[0].Parents = realParents
		report, err = Verify(repository, manifest)
		if err != nil || !report.Valid {
			t.Fatalf("raw parent graph did not verify with graft present: report=%#v err=%v", report, err)
		}
	})

	t.Run("environment", func(t *testing.T) {
		repositoryA := fixtureRepository(t)
		repositoryB := t.TempDir()
		gitTest(t, repositoryB, "init")
		gitTest(t, repositoryB, "config", "user.name", "DAGrail Test")
		gitTest(t, repositoryB, "config", "user.email", "test@example.invalid")
		if err := os.WriteFile(filepath.Join(repositoryB, "unique.txt"), []byte("repository-b\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitTest(t, repositoryB, "add", "unique.txt")
		gitTest(t, repositoryB, "commit", "-m", "repository B")
		commit := gitTest(t, repositoryB, "rev-parse", "HEAD")
		tree := gitTest(t, repositoryB, "show", "-s", "--format=%T", commit)
		gitTest(t, repositoryB, "update-ref", "refs/heads/proof", commit)
		manifest := ClosureManifest{
			APIVersion: ClosureAPIVersion,
			Kind:       ClosureKind,
			Refs:       []RefExpectation{{Name: "refs/heads/proof", OID: commit}},
			Objects:    []ObjectExpectation{{Name: "repository-b", OID: commit, Type: "commit", Tree: tree, RetainedBy: []string{"refs/heads/proof"}}},
		}
		t.Setenv("GIT_DIR", filepath.Join(repositoryB, ".git"))
		t.Setenv("GIT_WORK_TREE", repositoryB)
		report, err := Verify(repositoryA, manifest)
		if err != nil {
			t.Fatal(err)
		}
		if report.Valid {
			t.Fatalf("repository A verification was redirected to repository B: %#v", report)
		}
		if _, err := InspectScope(repositoryA, commit, commit, commit, commit); err == nil {
			t.Fatal("scope inspection was redirected to a different repository by Git environment")
		}
	})
}

func TestVerifyRejectsRevisionExpressionsAsRetainingRefs(t *testing.T) {
	repository := fixtureRepository(t)
	commit := gitTest(t, repository, "rev-parse", "refs/heads/target")
	tree := gitTest(t, repository, "show", "-s", "--format=%T", commit)
	for _, refName := range []string{"refs/heads/target~1", "refs/heads/target@{0}"} {
		manifest := ClosureManifest{
			APIVersion: ClosureAPIVersion,
			Kind:       ClosureKind,
			Refs:       []RefExpectation{{Name: refName, OID: commit}},
			Objects:    []ObjectExpectation{{Name: "commit", OID: commit, Type: "commit", Tree: tree, RetainedBy: []string{refName}}},
		}
		if _, err := Verify(repository, manifest); err == nil {
			t.Fatalf("revision expression %q was accepted as a durable ref", refName)
		}
	}
	gitTest(t, repository, "branch", "reflog-only", commit)
	gitTest(t, repository, "branch", "-D", "reflog-only")
	refName := "refs/heads/reflog-only"
	manifest := ClosureManifest{
		APIVersion: ClosureAPIVersion,
		Kind:       ClosureKind,
		Refs:       []RefExpectation{{Name: refName, OID: commit}},
		Objects:    []ObjectExpectation{{Name: "commit", OID: commit, Type: "commit", Tree: tree, RetainedBy: []string{refName}}},
	}
	report, err := Verify(repository, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if report.Valid || report.Refs[0].Valid || !contains(report.Refs[0].Reasons, "ref_missing") {
		t.Fatalf("deleted reflog-only ref was accepted as durable retention: %#v", report)
	}
}

func TestInspectScopeClosesTreeEntryModesRenameEndpointsAndDroppedChanges(t *testing.T) {
	repository := t.TempDir()
	gitTest(t, repository, "init")
	gitTest(t, repository, "config", "user.name", "DAGrail Test")
	gitTest(t, repository, "config", "user.email", "test@example.invalid")
	path := filepath.Join(repository, "script.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "add", "script.sh")
	gitTest(t, repository, "commit", "-m", "base")
	base := gitTest(t, repository, "rev-parse", "HEAD")
	gitTest(t, repository, "update-index", "--chmod=+x", "script.sh")
	gitTest(t, repository, "commit", "-m", "candidate mode")
	candidate := gitTest(t, repository, "rev-parse", "HEAD")

	report, err := InspectScope(repository, base, candidate, base, base)
	if err != nil {
		t.Fatal(err)
	}
	if report.Closed || report.Counts["unexplained_prospective_delta"] != 1 || report.Entries[0].BaseMode != "100644" || report.Entries[0].CandidateMode != "100755" {
		t.Fatalf("dropped mode-only candidate change was not fail-closed: %#v", report)
	}

	gitTest(t, repository, "reset", "--hard", base)
	gitTest(t, repository, "mv", "script.sh", "renamed.sh")
	gitTest(t, repository, "commit", "-m", "candidate rename")
	renameCandidate := gitTest(t, repository, "rev-parse", "HEAD")
	report, err = InspectScope(repository, base, renameCandidate, base, renameCandidate)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Closed || report.Counts["candidate_only"] != 2 || len(report.Entries) != 2 || report.Entries[0].Path != "renamed.sh" || report.Entries[1].Path != "script.sh" {
		t.Fatalf("rename endpoints were not both represented: %#v", report)
	}
}

func TestScopeClassificationDoesNotCallOneSidedNovelContentAConflictResolution(t *testing.T) {
	entry := func(value string) treeEntry { return treeEntry{Mode: "100644", OID: value} }
	if got := classifyScope(entry("base"), entry("candidate"), entry("base"), entry("novel")); got != "unexplained_prospective_delta" {
		t.Fatalf("one-sided novel candidate result was misclassified as %s", got)
	}
	if got := classifyScope(entry("base"), entry("candidate"), entry("target"), entry("resolved")); got != "conflict_resolution" {
		t.Fatalf("two-sided resolution was classified as %s", got)
	}
	if got := classifyScope(entry("base"), entry("shared"), entry("shared"), entry("third")); got != "unexplained_prospective_delta" {
		t.Fatalf("single shared side with a third prospective value was misclassified as %s", got)
	}
	if got := classifyScope(entry("base"), entry("candidate"), entry("base"), entry("base")); got != "unexplained_prospective_delta" {
		t.Fatalf("discarded candidate result was misclassified as %s", got)
	}
	if got := classifyScope(treeEntry{Mode: "100644", OID: "same"}, treeEntry{Mode: "100755", OID: "same"}, treeEntry{Mode: "100644", OID: "same"}, treeEntry{Mode: "100644", OID: "same"}); got != "unexplained_prospective_delta" {
		t.Fatalf("discarded mode-only result was misclassified as %s", got)
	}
}

func TestInspectScopePreservesSymlinkAndGitlinkModes(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		repository := t.TempDir()
		gitTest(t, repository, "init")
		gitTest(t, repository, "config", "user.name", "DAGrail Test")
		gitTest(t, repository, "config", "user.email", "test@example.invalid")
		if err := os.WriteFile(filepath.Join(repository, "entry"), []byte("ordinary\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitTest(t, repository, "add", "entry")
		gitTest(t, repository, "commit", "-m", "ordinary base")
		base := gitTest(t, repository, "rev-parse", "HEAD")
		if err := os.WriteFile(filepath.Join(repository, "entry"), []byte("destination"), 0o600); err != nil {
			t.Fatal(err)
		}
		linkOID := gitTest(t, repository, "hash-object", "-w", "entry")
		gitTest(t, repository, "update-index", "--add", "--cacheinfo", "120000", linkOID, "entry")
		gitTest(t, repository, "commit", "-m", "symlink candidate")
		candidate := gitTest(t, repository, "rev-parse", "HEAD")
		report, err := InspectScope(repository, base, candidate, base, candidate)
		if err != nil {
			t.Fatal(err)
		}
		if !report.Closed || len(report.Entries) != 1 || report.Entries[0].BaseMode != "100644" || report.Entries[0].CandidateMode != "120000" || report.Entries[0].Class != "candidate_only" {
			t.Fatalf("symlink mode was not part of scope identity: %#v", report)
		}
	})

	t.Run("gitlink", func(t *testing.T) {
		module := t.TempDir()
		gitTest(t, module, "init")
		gitTest(t, module, "config", "user.name", "DAGrail Test")
		gitTest(t, module, "config", "user.email", "test@example.invalid")
		if err := os.WriteFile(filepath.Join(module, "module.txt"), []byte("module\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitTest(t, module, "add", "module.txt")
		gitTest(t, module, "commit", "-m", "module")

		repository := t.TempDir()
		gitTest(t, repository, "init")
		gitTest(t, repository, "config", "user.name", "DAGrail Test")
		gitTest(t, repository, "config", "user.email", "test@example.invalid")
		if err := os.WriteFile(filepath.Join(repository, "base.txt"), []byte("base\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitTest(t, repository, "add", "base.txt")
		gitTest(t, repository, "commit", "-m", "base")
		base := gitTest(t, repository, "rev-parse", "HEAD")
		gitTest(t, repository, "-c", "protocol.file.allow=always", "submodule", "add", module, "vendor/module")
		gitTest(t, repository, "commit", "-m", "gitlink candidate")
		candidate := gitTest(t, repository, "rev-parse", "HEAD")
		report, err := InspectScope(repository, base, candidate, base, candidate)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, entry := range report.Entries {
			if entry.Path == "vendor/module" && entry.CandidateMode == "160000" && entry.Class == "candidate_only" {
				found = true
			}
		}
		if !report.Closed || !found {
			t.Fatalf("gitlink mode was not part of scope identity: %#v", report)
		}
	})
}

func TestInspectScopeUsesBoundedBatchGitProcessesAndHonorsCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Git process-count wrapper uses a POSIX launcher")
	}
	repository := t.TempDir()
	gitTest(t, repository, "init")
	const pathCount = 10000
	var stream strings.Builder
	stream.WriteString("blob\nmark :1\ndata 5\nbase\n")
	stream.WriteString("blob\nmark :2\ndata 10\ncandidate\n")
	stream.WriteString("commit refs/heads/base\nmark :3\ncommitter DAGrail Test <test@example.invalid> 0 +0000\ndata 4\nbase\n")
	for index := 0; index < pathCount; index++ {
		fmt.Fprintf(&stream, "M 100644 :1 path-%04d.txt\n", index)
	}
	stream.WriteString("\ncommit refs/heads/candidate\nmark :4\ncommitter DAGrail Test <test@example.invalid> 1 +0000\ndata 9\ncandidate\nfrom :3\n")
	for index := 0; index < pathCount; index++ {
		fmt.Fprintf(&stream, "M 100644 :2 path-%04d.txt\n", index)
	}
	stream.WriteString("\ndone\n")
	gitTestInput(t, repository, stream.String(), "fast-import", "--quiet")
	base := gitTest(t, repository, "rev-parse", "refs/heads/base")
	candidate := gitTest(t, repository, "rev-parse", "refs/heads/candidate")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	originalPath := os.Getenv("PATH")
	wrapperDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "calls.log")
	wrapper := fmt.Sprintf("#!/bin/sh\nprintf 'call\\n' >> %q\nexec %q \"$@\"\n", logPath, realGit)
	if err := os.WriteFile(filepath.Join(wrapperDir, "git"), []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+originalPath)
	report, err := InspectScope(repository, base, candidate, base, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Closed || len(report.Entries) != pathCount {
		t.Fatalf("large batch scope was not closed: entries=%d closed=%v", len(report.Entries), report.Closed)
	}
	callLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if calls := strings.Count(string(callLog), "call\n"); calls > 24 {
		t.Fatalf("scope inspection spawned %d Git processes for %d paths", calls, pathCount)
	}

	cancelDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cancelDir, "git"), []byte("#!/bin/sh\nexec sleep 10\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", cancelDir+string(os.PathListSeparator)+originalPath)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := InspectScopeContext(ctx, repository, base, candidate, base, candidate); err == nil || ctx.Err() == nil {
		t.Fatalf("scope inspection ignored cancellation: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("scope cancellation took %s", elapsed)
	}
}

func TestVerifyUsesFixedGitProcessesAndPropagatesCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Git process-count wrapper uses a POSIX launcher")
	}
	repository := t.TempDir()
	gitTest(t, repository, "init")
	manifest := ClosureManifest{APIVersion: ClosureAPIVersion, Kind: ClosureKind, Refs: []RefExpectation{{Name: "refs/heads/proof"}}}
	var stream strings.Builder
	for index := 0; index < 64; index++ {
		blobMark, commitMark := index*2+1, index*2+2
		value := fmt.Sprintf("%d\n", index)
		message := fmt.Sprintf("commit %d\n", index)
		fmt.Fprintf(&stream, "blob\nmark :%d\ndata %d\n%s", blobMark, len(value), value)
		fmt.Fprintf(&stream, "commit refs/heads/work\nmark :%d\ncommitter DAGrail Test <test@example.invalid> %d +0000\ndata %d\n%s", commitMark, index, len(message), message)
		if index > 0 {
			fmt.Fprintf(&stream, "from :%d\n", commitMark-2)
		}
		fmt.Fprintf(&stream, "M 100644 :%d value.txt\n\n", blobMark)
	}
	stream.WriteString("done\n")
	gitTestInput(t, repository, stream.String(), "fast-import", "--quiet")
	commits := strings.Fields(gitTest(t, repository, "rev-list", "--reverse", "refs/heads/work"))
	if len(commits) != 64 {
		t.Fatalf("fast-import created %d commits, want 64", len(commits))
	}
	for index, commit := range commits {
		manifest.Objects = append(manifest.Objects, ObjectExpectation{Name: fmt.Sprintf("commit-%02d", index), OID: commit, Type: "commit", Tree: gitTest(t, repository, "show", "-s", "--format=%T", commit), Parents: strings.Fields(gitTest(t, repository, "show", "-s", "--format=%P", commit)), RetainedBy: []string{"refs/heads/proof"}})
	}
	head := commits[len(commits)-1]
	gitTest(t, repository, "update-ref", "refs/heads/proof", head)
	manifest.Refs[0].OID = head

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	originalPath := os.Getenv("PATH")
	wrapperDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "verify-calls.log")
	wrapper := fmt.Sprintf("#!/bin/sh\nprintf 'call\\n' >> %q\nexec %q \"$@\"\n", logPath, realGit)
	if err := os.WriteFile(filepath.Join(wrapperDir, "git"), []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+originalPath)
	report, err := Verify(repository, manifest)
	if err != nil || !report.Valid {
		t.Fatalf("batch closure failed: valid=%v err=%v", report.Valid, err)
	}
	callLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if calls := strings.Count(string(callLog), "call\n"); calls > 6 {
		t.Fatalf("closure verification spawned %d Git processes for 64 objects", calls)
	}

	blockingDir := t.TempDir()
	readyPath := filepath.Join(t.TempDir(), "blocked.ready")
	blocking := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n  *rev-parse*|*'cat-file --batch'*) exec %q \"$@\" ;;\n  *) printf ready > %q; exec sleep 10 ;;\nesac\n", realGit, readyPath)
	if err := os.WriteFile(filepath.Join(blockingDir, "git"), []byte(blocking), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", blockingDir+string(os.PathListSeparator)+originalPath)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := VerifyContext(ctx, repository, manifest)
		result <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("closure verification did not reach the blocked Git query")
		}
		time.Sleep(10 * time.Millisecond)
	}
	started := time.Now()
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("closure cancellation became a diagnostic result: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("closure cancellation took %s", elapsed)
	}
}

func TestInspectScopeDisablesPartialCloneLazyFetch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("partial-clone fixture uses file URLs")
	}
	source := t.TempDir()
	gitTest(t, source, "init")
	gitTest(t, source, "config", "user.name", "DAGrail Test")
	gitTest(t, source, "config", "user.email", "test@example.invalid")
	path := filepath.Join(source, "large.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("base\n", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, source, "add", "large.txt")
	gitTest(t, source, "commit", "-m", "base")
	base := gitTest(t, source, "rev-parse", "HEAD")
	if err := os.WriteFile(path, []byte(strings.Repeat("candidate\n", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, source, "commit", "-am", "candidate")
	candidate := gitTest(t, source, "rev-parse", "HEAD")

	bare := filepath.Join(t.TempDir(), "remote.git")
	gitCommandTest(t, "clone", "--bare", source, bare)
	gitTest(t, bare, "config", "uploadpack.allowFilter", "true")
	gitTest(t, bare, "config", "uploadpack.allowAnySHA1InWant", "true")
	partial := filepath.Join(t.TempDir(), "partial")
	gitCommandTest(t, "clone", "--filter=tree:0", "--no-checkout", "file://"+bare, partial)
	before := gitObjectSnapshot(t, filepath.Join(partial, ".git", "objects"))
	if _, err := InspectScope(partial, base, candidate, base, candidate); err == nil {
		t.Fatal("scope inspection fetched missing partial-clone objects instead of failing closed")
	}
	after := gitObjectSnapshot(t, filepath.Join(partial, ".git", "objects"))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("scope inspection mutated partial-clone object storage")
	}
}

func FuzzDecodeClosureManifest(f *testing.F) {
	f.Add([]byte(`{"apiVersion":"dagrail.io/git-artifact-closure/v1alpha1","kind":"GitArtifactClosure","objects":[{"name":"candidate","oid":"0000000000000000000000000000000000000000","type":"commit","tree":"0000000000000000000000000000000000000000","retainedBy":["refs/tags/handoff"]}],"refs":[{"name":"refs/tags/handoff","oid":"0000000000000000000000000000000000000000"}]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = decodeManifest(raw)
	})
}

func fixtureRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	gitTest(t, repository, "init")
	gitTest(t, repository, "config", "user.name", "DAGrail Test")
	gitTest(t, repository, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "add", "base.txt")
	gitTest(t, repository, "commit", "-m", "base")
	gitTest(t, repository, "tag", "base")
	gitTest(t, repository, "checkout", "-b", "candidate")
	if err := os.WriteFile(filepath.Join(repository, "candidate.txt"), []byte("candidate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "add", "candidate.txt")
	gitTest(t, repository, "commit", "-m", "candidate")
	gitTest(t, repository, "tag", "candidate")
	gitTest(t, repository, "checkout", "-b", "target", "base")
	if err := os.WriteFile(filepath.Join(repository, "target.txt"), []byte("target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "add", "target.txt")
	gitTest(t, repository, "commit", "-m", "target")
	gitTest(t, repository, "tag", "target")
	gitTest(t, repository, "merge", "--no-ff", "candidate", "-m", "prospective")
	gitTest(t, repository, "tag", "-a", "handoff", "-m", "immutable handoff")
	return repository
}

func gitTest(t *testing.T, repository string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repository}, args...)...)
	command.Env = append(os.Environ(), "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func gitTestInput(t *testing.T, repository, input string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repository}, args...)...)
	command.Env = append(os.Environ(), "LC_ALL=C")
	command.Stdin = strings.NewReader(input)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func gitCommandTest(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Env = append(os.Environ(), "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func gitObjectSnapshot(t *testing.T, root string) map[string][sha256.Size]byte {
	t.Helper()
	result := map[string][sha256.Size]byte{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = sha256.Sum256(raw)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func validateSchema(t *testing.T, name string, value any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "schemas", name))
	if err != nil {
		t.Fatal(err)
	}
	var document, instance any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &instance); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("urn:dagrail:"+name, document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("urn:dagrail:" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("%s rejected runtime output: %v", name, err)
	}
}
