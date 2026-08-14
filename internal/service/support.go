package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode"

	"github.com/CongBao/dagrail/internal/version"
	"github.com/gowebpki/jcs"
)

const SupportAPIVersion = "dagrail.io/support/v1alpha1"

type SupportBuild struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

type SupportPrivacy struct {
	AuthorityPayloadsIncluded bool `json:"authorityPayloadsIncluded"`
	AbsolutePathsIncluded     bool `json:"absolutePathsIncluded"`
	PromptsIncluded           bool `json:"promptsIncluded"`
	ArtifactBodiesIncluded    bool `json:"artifactBodiesIncluded"`
	HarnessOutputIncluded     bool `json:"harnessOutputIncluded"`
}

type SupportDoctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type SupportCounts struct {
	Nodes             map[string]int `json:"nodes"`
	Attempts          map[string]int `json:"attempts"`
	Effects           map[string]int `json:"effects"`
	Incidents         map[string]int `json:"incidents"`
	Ready             int            `json:"ready"`
	Blocked           int            `json:"blocked"`
	ResourceBlocked   int            `json:"resourceBlocked"`
	DependencyCuts    int            `json:"dependencyCuts"`
	OverdueIncidents  int            `json:"overdueIncidents"`
	ExpiredRoleLeases int            `json:"expiredRoleLeases"`
}

type SupportReport struct {
	APIVersion string                    `json:"apiVersion"`
	Kind       string                    `json:"kind"`
	Shareable  bool                      `json:"shareable"`
	ProjectRef string                    `json:"projectRef"`
	Build      SupportBuild              `json:"build"`
	Privacy    SupportPrivacy            `json:"privacy"`
	Security   SecurityAuditReport       `json:"security"`
	Journal    JournalVerificationReport `json:"journal"`
	Doctor     []SupportDoctorCheck      `json:"doctor"`
	Counts     SupportCounts             `json:"counts"`
}

func (s *Service) SupportReport() (SupportReport, error) {
	journalReport, err := s.VerifyJournalReport()
	if err != nil {
		return SupportReport{}, err
	}
	status, err := s.Status()
	if err != nil {
		return SupportReport{}, err
	}
	projectDigest := sha256.Sum256(append([]byte("dagrail-support-project-v1\x00"), []byte(s.Project.Config.ProjectID)...))
	doctor := s.Doctor()
	doctorChecks := make([]SupportDoctorCheck, 0, len(doctor.Checks))
	for _, check := range doctor.Checks {
		doctorChecks = append(doctorChecks, SupportDoctorCheck{Name: check.Name, Status: check.Status})
	}
	report := SupportReport{
		APIVersion: SupportAPIVersion,
		Kind:       "SupportReport",
		Shareable:  true,
		ProjectRef: "sha256:" + hex.EncodeToString(projectDigest[:]),
		Build: SupportBuild{
			Version: safeSupportBuildValue(version.Version),
			Commit:  safeSupportBuildValue(version.Commit),
			Date:    safeSupportBuildValue(version.Date),
		},
		Privacy: SupportPrivacy{
			AuthorityPayloadsIncluded: false,
			AbsolutePathsIncluded:     false,
			PromptsIncluded:           false,
			ArtifactBodiesIncluded:    false,
			HarnessOutputIncluded:     false,
		},
		Security: s.SecurityAudit(),
		Journal:  journalReport,
		Doctor:   doctorChecks,
		Counts: SupportCounts{
			Nodes:             copyCounts(status.Nodes),
			Attempts:          copyCounts(status.Attempts),
			Effects:           copyCounts(status.Effects),
			Incidents:         copyCounts(status.Incidents),
			Ready:             len(status.Frontier.Ready),
			Blocked:           len(status.Frontier.Blocked),
			ResourceBlocked:   len(status.Frontier.ResourceBlocked),
			DependencyCuts:    len(status.Frontier.DependencyCuts),
			OverdueIncidents:  len(status.OverdueIncidents),
			ExpiredRoleLeases: len(status.ExpiredRoleLeases),
		},
	}
	return report, nil
}

func safeSupportBuildValue(value string) string {
	if value == "" || len([]byte(value)) > 128 {
		return "redacted"
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			continue
		}
		switch character {
		case '.', '-', '_', '+', ':':
		default:
			return "redacted"
		}
	}
	return value
}

func (s *Service) SupportBytes() ([]byte, SupportReport, error) {
	report, err := s.SupportReport()
	if err != nil {
		return nil, SupportReport{}, err
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return nil, SupportReport{}, err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return nil, SupportReport{}, err
	}
	return append(canonical, '\n'), report, nil
}

func copyCounts(source map[string]int) map[string]int {
	result := make(map[string]int, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
