package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const replacementAuthorityProjectID = "22222222-2222-4222-8222-222222222222"

func TestPrepareReplacementAuthorityRecoversPartialAtomicState(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "replacement")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, ".dagrail-atomic-crash"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	lineage := AuthorityLineage{
		Operation:            "rotation",
		PreviousProjectID:    "11111111-1111-4111-8111-111111111111",
		PreviousHead:         strings.Repeat("1", 64),
		RecoveryHead:         strings.Repeat("2", 64),
		RecoveryBackupDigest: "sha256:" + strings.Repeat("3", 64),
		RotatedAt:            "2026-08-16T00:00:00Z",
		Reason:               "recover a retired authority",
		IdempotencyKey:       "rotation-1",
	}

	if err := prepareReplacementAuthority(dataDir, replacementAuthorityProjectID, lineage); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAuthorityClaim(dataDir, replacementAuthorityProjectID); err != nil {
		t.Fatalf("prepared replacement claim is invalid: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dataDir, ".dagrail-atomic-crash")); !os.IsNotExist(err) {
		t.Fatalf("crashed temporary file was not removed: %v", err)
	}

	// A crash after publishing lineage but before publishing the claim is
	// resumed by the exact same rotation intent.
	if err := os.Remove(filepath.Join(dataDir, authorityClaimFile)); err != nil {
		t.Fatal(err)
	}
	if err := prepareReplacementAuthority(dataDir, replacementAuthorityProjectID, lineage); err != nil {
		t.Fatalf("resume after lineage-only prefix: %v", err)
	}
	if err := ValidateAuthorityClaim(dataDir, replacementAuthorityProjectID); err != nil {
		t.Fatalf("resumed replacement claim is invalid: %v", err)
	}
	if err := prepareReplacementAuthority(dataDir, replacementAuthorityProjectID, lineage); err != nil {
		t.Fatalf("exact retry is not idempotent: %v", err)
	}

	// Copying the complete authority directory to another canonical path must
	// not duplicate the local writer capability.
	cloneDir := filepath.Join(t.TempDir(), "clone")
	if err := os.MkdirAll(cloneDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{authorityLineageFile, authorityClaimFile} {
		raw, err := os.ReadFile(filepath.Join(dataDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cloneDir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateAuthorityClaim(cloneDir, replacementAuthorityProjectID); err == nil || !strings.Contains(err.Error(), "different identity") {
		t.Fatalf("copied authority claim was accepted at another path: %v", err)
	}
}

func TestAuthorityClaimRejectsMalformedAndSymlinkEvidence(t *testing.T) {
	projectID := "33333333-3333-4333-8333-333333333333"
	dataDir := t.TempDir()
	if err := EstablishAuthorityClaim(dataDir, projectID); err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(dataDir, authorityClaimFile)
	valid, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claimPath, append(valid, []byte(`{"unexpected":true}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAuthorityClaim(dataDir, projectID); err == nil {
		t.Fatal("claim with trailing content was accepted")
	}
	if err := os.Remove(claimPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "claim-target"), valid, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dataDir, "claim-target"), claimPath); err == nil {
		if err := ValidateAuthorityClaim(dataDir, projectID); err == nil {
			t.Fatal("symlink authority claim was accepted")
		}
	}
}

func TestProductionAuthorityRootIgnoresProcessEnvironment(t *testing.T) {
	authorityRootTestOverride.Lock()
	saved := authorityRootTestOverride.root
	authorityRootTestOverride.root = ""
	authorityRootTestOverride.Unlock()
	defer func() {
		authorityRootTestOverride.Lock()
		authorityRootTestOverride.root = saved
		authorityRootTestOverride.Unlock()
	}()

	baseline, err := authorityRoot()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DAGRAIL_AUTHORITY_HOME", filepath.Join(t.TempDir(), "attacker-authority"))
	t.Setenv("HOME", filepath.Join(t.TempDir(), "attacker-home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "attacker-xdg"))
	actual, err := authorityRoot()
	if err != nil {
		t.Fatal(err)
	}
	if actual != baseline {
		t.Fatalf("production authority root changed with process environment: before=%q after=%q", baseline, actual)
	}
}

func TestEnsureDurableDirectoryFreshRetryResyncsEveryParent(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "a", "b", "c")
	failureBoundary := filepath.Join(base, "a")
	originalSync := directorySync
	defer func() { directorySync = originalSync }()
	directorySync = func(path string) error {
		if filepath.Clean(path) == filepath.Clean(failureBoundary) {
			return syscall.EIO
		}
		return syncDirectoryPath(path)
	}
	if err := EnsureDurableDirectoryWithin(target, base); !errors.Is(err, syscall.EIO) {
		t.Fatalf("initial directory creation did not expose its parent sync failure: %v", err)
	}
	if info, err := os.Lstat(failureBoundary); err != nil || !info.IsDir() {
		t.Fatalf("first created directory was not visible at the injected crash boundary: %v", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory creation continued beyond an unconfirmed boundary: %v", err)
	}

	visited := map[string]bool{}
	directorySync = func(path string) error {
		visited[filepath.Clean(path)] = true
		return syncDirectoryPath(path)
	}
	if err := EnsureDurableDirectoryWithin(target, base); err != nil {
		t.Fatalf("fresh durability retry failed: %v", err)
	}
	for _, expected := range []string{target, filepath.Dir(target), failureBoundary, base} {
		if !visited[filepath.Clean(expected)] {
			t.Fatalf("fresh retry did not resync visible parent %q: %#v", expected, visited)
		}
	}
}

func TestEnsureDurableDirectoryRecreatedPathResyncsAncestors(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "recreated", "leaf")
	if err := EnsureDurableDirectoryWithin(target, base); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(base, "recreated")); err != nil {
		t.Fatal(err)
	}

	originalSync := directorySync
	defer func() { directorySync = originalSync }()
	ancestorVisited := false
	directorySync = func(path string) error {
		if filepath.Clean(path) == filepath.Clean(base) {
			ancestorVisited = true
			return syscall.EIO
		}
		return syncDirectoryPath(path)
	}
	if err := EnsureDurableDirectoryWithin(target, base); !errors.Is(err, syscall.EIO) {
		t.Fatalf("recreated authority path reused stale durability proof: %v", err)
	}
	if !ancestorVisited {
		t.Fatal("recreated authority path did not resync its surviving ancestor")
	}
}

func TestEnsureDurableDirectoryWithinRejectsSymlinkBoundary(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	boundary := filepath.Join(base, "boundary")
	if err := os.Symlink(outside, boundary); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	target := filepath.Join(boundary, "must-not-exist")
	if err := EnsureDurableDirectoryWithin(target, boundary); err == nil || !strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("symlink boundary was accepted: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "must-not-exist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected symlink boundary created outside its lexical root: %v", err)
	}
}

func TestEnsureDurableDirectoryRejectsSymlinkAncestor(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(base, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	target := filepath.Join(link, "must-not-exist")
	if err := EnsureDurableDirectory(target); err == nil {
		t.Fatal("generic directory creation accepted a symbolic-link ancestor")
	}
	if _, err := os.Lstat(filepath.Join(outside, "must-not-exist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected symbolic-link ancestor created outside its lexical root: %v", err)
	}
}

func TestInitCreatesNestedProjectRootAndFreshDataHome(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "projects", "nested", "workspace")
	dataHome := filepath.Join(base, "runtime", "fresh", "dagrail")
	t.Setenv("DAGRAIL_HOME", dataHome)

	created, err := Init(root, "nested-init")
	if err != nil {
		t.Fatalf("init rejected nested fresh roots: %v", err)
	}
	if created.Root != root {
		t.Fatalf("init resolved the wrong project root: got %q want %q", created.Root, root)
	}
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(markerPath))); err != nil {
		t.Fatalf("nested project locator was not created: %v", err)
	}
	if _, err := os.Lstat(created.DataDir); err != nil {
		t.Fatalf("nested runtime data directory was not created: %v", err)
	}
}

func TestInitRetryRevalidatesAndResyncsVisibleLocator(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "data"))
	marker := filepath.Join(root, filepath.FromSlash(markerPath))
	markerDir, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	markerDir = filepath.Join(markerDir, ".dagrail")
	originalSync := directorySync
	defer func() { directorySync = originalSync }()

	initializers := 0
	initialize := func(Project, bool) error {
		initializers++
		return nil
	}
	failedAfterLink := false
	directorySync = func(path string) error {
		if filepath.Clean(path) == filepath.Clean(markerDir) && !failedAfterLink {
			if _, statErr := os.Lstat(marker); statErr == nil {
				failedAfterLink = true
				return syscall.EIO
			}
		}
		return syncDirectoryPath(path)
	}
	if _, err := InitWithInitializer(root, "durable-init", initialize); !errors.Is(err, syscall.EIO) || !failedAfterLink {
		t.Fatalf("initial locator publication did not expose the injected post-link sync failure: %v", err)
	}
	if _, err := os.Lstat(marker); err != nil {
		t.Fatalf("locator was not visible at the injected crash boundary: %v", err)
	}

	retryConsultedSync := false
	directorySync = func(path string) error {
		if filepath.Clean(path) == filepath.Clean(markerDir) {
			retryConsultedSync = true
			return syscall.EIO
		}
		return syncDirectoryPath(path)
	}
	if _, err := InitWithInitializer(root, "durable-init", initialize); !errors.Is(err, syscall.EIO) || !retryConsultedSync {
		t.Fatalf("fresh retry did not revalidate and resync the visible locator: %v", err)
	}
	directorySync = originalSync
	if _, err := InitWithInitializer(root, "durable-init", initialize); err != nil {
		t.Fatalf("durable retry failed: %v", err)
	}
	if initializers != 3 {
		t.Fatalf("initializer was not revalidated on every visible-locator retry: calls=%d", initializers)
	}
}

func TestReplacementRetryResyncsVisibleClaimLineageAndAnchor(t *testing.T) {
	projectID := "44444444-4444-4444-8444-444444444444"
	dataDir := filepath.Join(t.TempDir(), "replacement")
	resolvedDataDir, err := filepath.EvalSymlinks(filepath.Dir(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	resolvedDataDir = filepath.Join(resolvedDataDir, filepath.Base(dataDir))
	lineage := AuthorityLineage{
		Operation:            "rotation",
		PreviousProjectID:    "55555555-5555-4555-8555-555555555555",
		PreviousHead:         strings.Repeat("1", 64),
		RecoveryHead:         strings.Repeat("2", 64),
		RecoveryBackupDigest: "sha256:" + strings.Repeat("3", 64),
		RotatedAt:            "2026-08-16T01:02:03Z",
		Reason:               "prove durable replacement retry",
		IdempotencyKey:       "rotation/durable-replacement",
	}
	originalSync := directorySync
	defer func() { directorySync = originalSync }()

	failedAfterClaimLink := false
	directorySync = func(path string) error {
		if filepath.Clean(path) == filepath.Clean(resolvedDataDir) && !failedAfterClaimLink {
			if _, statErr := os.Lstat(filepath.Join(dataDir, authorityClaimFile)); statErr == nil {
				failedAfterClaimLink = true
				return syscall.EIO
			}
		}
		return syncDirectoryPath(path)
	}
	if err := prepareReplacementAuthority(dataDir, projectID, lineage); !errors.Is(err, syscall.EIO) || !failedAfterClaimLink {
		t.Fatalf("replacement did not expose the injected post-claim sync failure: %v", err)
	}

	retryConsultedSync := false
	directorySync = func(path string) error {
		if filepath.Clean(path) == filepath.Clean(resolvedDataDir) {
			retryConsultedSync = true
			return syscall.EIO
		}
		return syncDirectoryPath(path)
	}
	if err := prepareReplacementAuthority(dataDir, projectID, lineage); !errors.Is(err, syscall.EIO) || !retryConsultedSync {
		t.Fatalf("replacement shortcut did not resync its visible evidence: %v", err)
	}
	directorySync = originalSync
	if err := prepareReplacementAuthority(dataDir, projectID, lineage); err != nil {
		t.Fatalf("durable replacement retry failed: %v", err)
	}
}
