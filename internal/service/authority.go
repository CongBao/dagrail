package service

import (
	"fmt"

	"github.com/CongBao/dagrail/internal/domain"
)

func (s *Service) requireRoleCapability(state domain.State, roleID string, capabilities ...string) (domain.RoleLease, error) {
	lease, err := s.validLease(state, roleID)
	if err != nil {
		return domain.RoleLease{}, err
	}
	for _, capability := range capabilities {
		if domain.RoleHasCapability(state.Graph, roleID, capability) {
			return lease, nil
		}
	}
	return domain.RoleLease{}, fmt.Errorf("role %s lacks required capability %v", roleID, capabilities)
}
