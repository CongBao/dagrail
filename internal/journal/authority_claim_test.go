package journal

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CongBao/dagrail/internal/project"
)

func openClaimed(t testing.TB, dataDir, projectID string) (*Store, error) {
	t.Helper()
	if err := project.SetAuthorityRootForTesting(filepath.Join(dataDir, ".test-authority-home")); err != nil {
		return nil, err
	}
	if err := project.EstablishAuthorityClaim(dataDir, projectID); err != nil {
		return nil, err
	}
	store, err := open(dataDir, projectID, storeCapabilityOrdinary)
	if err != nil {
		return nil, err
	}
	store.testAllowUnestablished = true
	return store, nil
}

func TestAuthorityEstablishmentHandleRejectsEveryOtherWriteCapability(t *testing.T) {
	dataDir := t.TempDir()
	projectID := "77777777-7777-4777-8777-777777777777"
	if err := project.SetAuthorityRootForTesting(filepath.Join(dataDir, ".test-authority-home")); err != nil {
		t.Fatal(err)
	}
	if err := project.EstablishAuthorityClaim(dataDir, projectID); err != nil {
		t.Fatal(err)
	}
	store, err := OpenForAuthorityEstablishment(dataDir, projectID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	if _, err := store.Append(Command{ID: "forbidden", Kind: "forbidden", IdempotencyKey: "forbidden"}, nil, now); err == nil || !strings.Contains(err.Error(), "establishment store") {
		t.Fatalf("establishment handle accepted Append: %v", err)
	}
	if err := store.RestoreSegments(nil); err == nil || !strings.Contains(err.Error(), "establishment store") {
		t.Fatalf("establishment handle accepted RestoreSegments: %v", err)
	}
	retirement := []byte(`{"apiVersion":"test/v1","kind":"AuthorityRetirement"}`)
	if _, err := store.RetireAuthority("", retirement, now, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "ordinary journal store") {
		t.Fatalf("establishment handle accepted RetireAuthority: %v", err)
	}
	if _, err := store.RetireLegacyAuthority("", retirement, "sha256:"+strings.Repeat("1", 64), now, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "recovery journal store") {
		t.Fatalf("establishment handle accepted RetireLegacyAuthority: %v", err)
	}
	segments, err := store.ReadAll()
	if err != nil || len(segments) != 0 {
		t.Fatalf("forbidden establishment-handle writes changed the journal: %#v %v", segments, err)
	}
	if _, exists, err := store.AuthorityRetirement(); err != nil || exists {
		t.Fatalf("forbidden establishment-handle writes created retirement evidence: exists=%v err=%v", exists, err)
	}
}

func TestJournalStoreCapabilityMatrixIsClosed(t *testing.T) {
	projectID := "88888888-8888-4888-8888-888888888888"
	now := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	retirement := []byte(`{"apiVersion":"test/v1","kind":"AuthorityRetirement"}`)

	rehearsal, err := OpenRehearsal(t.TempDir(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := rehearsal.RestoreSegments(nil); err != nil {
		t.Fatalf("rehearsal store rejected verified restore: %v", err)
	}
	if _, err := rehearsal.Append(Command{ID: "forbidden", Kind: "forbidden", IdempotencyKey: "forbidden"}, nil, now); err == nil {
		t.Fatal("rehearsal store accepted ordinary append")
	}
	if _, err := rehearsal.EstablishAuthority([]byte(`{}`), now); err == nil || !strings.Contains(err.Error(), "establishment-only") {
		t.Fatalf("rehearsal store established authority: %v", err)
	}
	if _, err := rehearsal.RetireAuthority("", retirement, now, func([]Segment) error { return nil }, nil, func([]byte) error { return nil }); err == nil || !strings.Contains(err.Error(), "ordinary journal store") {
		t.Fatalf("rehearsal store retired claimed authority: %v", err)
	}
	if _, err := rehearsal.RetireLegacyAuthority("", retirement, "sha256:"+strings.Repeat("1", 64), now, func([]Segment) error { return nil }, nil, func([]byte) error { return nil }); err == nil || !strings.Contains(err.Error(), "recovery journal store") {
		t.Fatalf("rehearsal store retired authority: %v", err)
	}

	ordinary, err := openClaimed(t, t.TempDir(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ordinary.EstablishAuthority([]byte(`{}`), now); err == nil || !strings.Contains(err.Error(), "establishment-only") {
		t.Fatalf("ordinary store established authority: %v", err)
	}
	if _, err := ordinary.RetireAuthority("", retirement, now, nil, nil, func([]byte) error { return nil }); err == nil || !strings.Contains(err.Error(), "reservation validator") {
		t.Fatalf("ordinary store bypassed first-retirement validation: %v", err)
	}
	if _, err := ordinary.RetireLegacyAuthority("", retirement, "sha256:"+strings.Repeat("1", 64), now, func([]Segment) error { return nil }, nil, func([]byte) error { return nil }); err == nil || !strings.Contains(err.Error(), "recovery journal store") {
		t.Fatalf("ordinary store bypassed its claimed authority through legacy retirement: %v", err)
	}
	recoveryOverClaimed, err := OpenRecovery(filepath.Dir(ordinary.dir), projectID)
	if err != nil {
		t.Fatal(err)
	}
	appliedOverClaimed := false
	if _, err := recoveryOverClaimed.RetireLegacyAuthority("", retirement, "sha256:"+strings.Repeat("1", 64), now, func([]Segment) error { return nil }, nil, func([]byte) error {
		appliedOverClaimed = true
		return nil
	}); err == nil || !strings.Contains(err.Error(), "v0.22 claim") {
		t.Fatalf("recovery handle retired a claimed authority: %v", err)
	}
	if appliedOverClaimed {
		t.Fatal("rejected recovery retirement executed its replacement callback")
	}

	recovery, err := OpenRecovery(t.TempDir(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recovery.Append(Command{ID: "forbidden", Kind: "forbidden", IdempotencyKey: "forbidden"}, nil, now); err == nil {
		t.Fatal("recovery store accepted ordinary append")
	}
	if _, err := recovery.EstablishAuthority([]byte(`{}`), now); err == nil || !strings.Contains(err.Error(), "establishment-only") {
		t.Fatalf("recovery store established authority: %v", err)
	}
	if err := recovery.RestoreSegments(nil); err == nil {
		t.Fatal("recovery store accepted restore")
	}
	if _, err := recovery.RetireAuthority("", retirement, now, func([]Segment) error { return nil }, nil, func([]byte) error { return nil }); err == nil || !strings.Contains(err.Error(), "ordinary journal store") {
		t.Fatalf("recovery store accepted claimed authority retirement: %v", err)
	}
	if _, err := recovery.RetireLegacyAuthority("", retirement, "sha256:"+strings.Repeat("1", 64), now, nil, nil, func([]byte) error { return nil }); err == nil || !strings.Contains(err.Error(), "reservation validator") {
		t.Fatalf("first legacy retirement omitted its reservation: %v", err)
	}
	if _, err := recovery.RetireLegacyAuthority("", retirement, "sha256:"+strings.Repeat("1", 64), now, func([]Segment) error { return nil }, nil, nil); err == nil || !strings.Contains(err.Error(), "replacement application") {
		t.Fatalf("first legacy retirement omitted its replacement: %v", err)
	}
	segments, err := recovery.ReadAll()
	if err != nil || len(segments) != 0 {
		t.Fatalf("rejected recovery mutations changed the journal: %#v %v", segments, err)
	}
	if _, exists, err := recovery.AuthorityRetirement(); err != nil || exists {
		t.Fatalf("rejected recovery mutations created a sidecar: exists=%v err=%v", exists, err)
	}

	nonemptyDir := t.TempDir()
	nonempty, err := openClaimed(t, nonemptyDir, "99999999-9999-4999-8999-999999999999")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := nonempty.Append(Command{ID: "existing", Kind: "existing", IdempotencyKey: "existing"}, []Event{{Type: "existing", Payload: []byte(`{}`)}}, now)
	if err != nil {
		t.Fatal(err)
	}
	nonempty.capability = storeCapabilityRecovery
	if _, err := nonempty.RetireLegacyAuthority(initial.SegmentHash, retirement, "sha256:"+strings.Repeat("1", 64), now, nil, nil, func([]byte) error { return nil }); err == nil || !strings.Contains(err.Error(), "reservation validator") {
		t.Fatalf("nonempty legacy retirement omitted its reservation: %v", err)
	}
	if _, err := nonempty.RetireLegacyAuthority(initial.SegmentHash, retirement, "sha256:"+strings.Repeat("1", 64), now, func([]Segment) error { return nil }, nil, nil); err == nil || !strings.Contains(err.Error(), "replacement application") {
		t.Fatalf("nonempty legacy retirement omitted its replacement: %v", err)
	}
	segments, err = nonempty.ReadAll()
	if err != nil || len(segments) != 1 || segments[0].Command.ID != "existing" {
		t.Fatalf("rejected nonempty retirement changed the source journal: %#v %v", segments, err)
	}
	if _, exists, err := nonempty.AuthorityRetirement(); err != nil || exists {
		t.Fatalf("rejected nonempty retirement created a sidecar: exists=%v err=%v", exists, err)
	}
}
