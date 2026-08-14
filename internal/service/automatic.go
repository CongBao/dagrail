package service

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/google/uuid"
)

// settleAutomatic advances only deterministic, executor-free nodes. It is
// invoked at command boundaries and on startup recovery; it is not a scheduler
// and never chooses semantic work.
func (s *Service) settleAutomatic() error {
	for {
		state, _, err := s.load()
		if err != nil {
			return err
		}
		frontier := domain.ComputeFrontier(state)
		settled := false
		for _, nodeID := range frontier.Ready {
			node, ok := state.NodeDefinition(nodeID)
			if !ok || (node.Kind != "join" && node.Kind != "milestone") {
				continue
			}
			outcome := ""
			for _, candidate := range node.Outcomes {
				if candidate.Class == "success" {
					outcome = candidate.ID
					break
				}
			}
			if outcome == "" {
				return fmt.Errorf("deterministic node %s has no success outcome", node.ID)
			}
			payload, _ := json.Marshal(map[string]string{"nodeId": node.ID, "outcome": outcome, "completedAt": s.Now().UTC().Format(time.RFC3339Nano)})
			expectedHead := state.HeadHash
			_, created, err := s.Journal.AppendOnce(journal.Command{ID: uuid.NewString(), Kind: "node.auto-complete", ActorRole: "dagrail.controller", IdempotencyKey: "auto/" + state.GraphRevision + "/" + node.ID}, []journal.Event{{Type: "node.auto-completed", Payload: payload}}, s.Now(), &expectedHead)
			if err != nil {
				return err
			}
			settled = created
			break
		}
		if !settled {
			return nil
		}
	}
}
