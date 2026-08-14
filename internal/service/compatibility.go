package service

import (
	"github.com/CongBao/dagrail/internal/journal"
	"github.com/CongBao/dagrail/internal/projection"
)

type CompatibilityStatus struct {
	Journal                        journal.CompatibilityReport `json:"journal"`
	ProjectionSchemaVersion        int                         `json:"projectionSchemaVersion"`
	CurrentProjectionSchemaVersion int                         `json:"currentProjectionSchemaVersion"`
}

func (s *Service) Compatibility() (CompatibilityStatus, error) {
	report, err := s.Journal.Compatibility()
	if err != nil {
		return CompatibilityStatus{}, err
	}
	projectionVersion, err := s.Projection.SchemaVersion()
	if err != nil {
		return CompatibilityStatus{}, err
	}
	return CompatibilityStatus{
		Journal:                        report,
		ProjectionSchemaVersion:        projectionVersion,
		CurrentProjectionSchemaVersion: projection.CurrentSchemaVersion,
	}, nil
}
