package cli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CongBao/dagrail/internal/cli"
	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/CongBao/dagrail/internal/service"
	"github.com/google/uuid"
)

func TestPublicCLIVerifiesLargeBackupItCreates(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(t.TempDir(), "runtime"))
	svc, err := service.Init(root, "large-public-backup")
	if err != nil {
		t.Fatal(err)
	}

	valuesPerSegment := domain.MaxAuthorityValues/2 + 1_000
	dependencyCut := strings.Repeat(`"node",`, valuesPerSegment-1) + `"node"`
	for index := 0; index < 2; index++ {
		key := fmt.Sprintf("large-public-backup/%d", index)
		now := svc.Now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
		payload := json.RawMessage(fmt.Sprintf(`{"id":"large-backup-%d","sourceType":"project","sourceId":"project","status":"open","classification":"unknown","attemptBudget":2,"attempts":0,"dependencyCut":[%s],"openedAt":"%s","updatedAt":"%s"}`, index, dependencyCut, now, now))
		if _, err := svc.Journal.Append(journal.Command{ID: uuid.NewString(), Kind: "test.large-backup", ActorRole: "test", IdempotencyKey: key}, []journal.Event{{Type: "incident.opened", Payload: payload}}, svc.Now().UTC()); err != nil {
			t.Fatalf("seed independently bounded segment %d: %v", index, err)
		}
	}

	run := func(args ...string) (string, error) {
		var stdout, stderr bytes.Buffer
		err := cli.Run(args, strings.NewReader(""), &stdout, &stderr)
		if err != nil && stderr.Len() > 0 {
			return stdout.String() + stderr.String(), err
		}
		return stdout.String(), err
	}
	backupPath := filepath.Join(t.TempDir(), "large-backup.json")
	created, err := run("backup", "create", "--root", root, "--output", backupPath)
	if err != nil || !strings.Contains(created, `"valid":true`) {
		t.Fatalf("public backup create failed: %v %s", err, created)
	}
	verified, err := run("backup", "verify", "--root", root, "--file", backupPath)
	if err != nil || !strings.Contains(verified, `"valid":true`) || !strings.Contains(verified, `"segments":3`) {
		t.Fatalf("public backup verify rejected the created artifact: %v %s", err, verified)
	}
}
