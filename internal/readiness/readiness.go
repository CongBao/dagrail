package readiness

import (
	"context"
	"fmt"

	"github.com/CongBao/dagrail/internal/compatibility"
	"github.com/CongBao/dagrail/internal/install"
	"github.com/CongBao/dagrail/internal/qualification"
	"github.com/CongBao/dagrail/internal/version"
)

const APIVersion = "dagrail.io/readiness-decision/v1alpha1"

type Options struct {
	SourceRoot          string
	ProjectRoot         string
	Installation        bool
	InstallationOptions install.Options
}

type Evidence struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Code   string `json:"code"`
	Source string `json:"source"`
}

type AdoptionGap struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	RequiredFor string `json:"requiredFor"`
}

type Report struct {
	APIVersion              string                 `json:"apiVersion"`
	Kind                    string                 `json:"kind"`
	Version                 string                 `json:"version"`
	Decision                string                 `json:"decision"`
	StructuralCandidate     bool                   `json:"structuralCandidate"`
	ExternalValidationReady bool                   `json:"externalValidationReady"`
	OneDotZeroReady         bool                   `json:"oneDotZeroReady"`
	ProductionValidated     bool                   `json:"productionValidated"`
	Compatibility           compatibility.Evidence `json:"compatibility"`
	Evidence                []Evidence             `json:"evidence"`
	AdoptionGaps            []AdoptionGap          `json:"adoptionGaps"`
}

func Evaluate(ctx context.Context, options Options) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	qualified, err := qualification.Run(options.SourceRoot, options.ProjectRoot)
	if err != nil {
		return Report{}, err
	}
	_, windowEvidence, err := compatibility.Current()
	if err != nil {
		return Report{}, err
	}
	report := Report{
		APIVersion: APIVersion, Kind: "ReadinessDecision", Version: version.Version,
		Decision: "not_ready", StructuralCandidate: qualified.StructuralCandidate,
		Compatibility: windowEvidence, Evidence: []Evidence{}, AdoptionGaps: []AdoptionGap{},
	}
	check := func(id string) qualification.Check {
		for _, item := range qualified.Checks {
			if item.ID == id {
				return item
			}
		}
		return qualification.Check{ID: id, Status: "fail", Code: "qualification_check_missing"}
	}
	combined := func(id, source string, checks ...string) {
		status, code := "pass", "checks_passed"
		for _, checkID := range checks {
			item := check(checkID)
			if item.Status != "pass" {
				status, code = item.Status, item.Code
				break
			}
		}
		report.Evidence = append(report.Evidence, Evidence{ID: id, Status: status, Code: code, Source: source})
	}
	combined("source-qualification", "source-inspection", "compatibility-contract", "plugin-metadata-versions", "linked-plugin-bundle", "workflow-action-pins")
	combined("public-documentation", "source-inspection", "source-layout")
	combined("api-contracts", "source-inspection", "compatibility-contract", "published-schema-digests")
	combined("distribution", "source-inspection", "ci-workflow", "release-workflow", "linked-plugin-bundle")
	combined("historical-binary-window", "source-inspection", "historical-compatibility")
	combined("browser-origin-boundary", "source-inspection", "localhost-origin-boundary")
	projectSecurity, projectRecovery := check("project-security"), check("project-recovery")
	report.Evidence = append(report.Evidence,
		Evidence{ID: "project-security", Status: projectSecurity.Status, Code: projectSecurity.Code, Source: "project-inspection"},
		Evidence{ID: "project-recovery", Status: projectRecovery.Status, Code: projectRecovery.Code, Source: "project-inspection"},
	)
	installationReady := true
	if options.Installation {
		diagnostic, diagnosticErr := install.Diagnose(ctx, options.InstallationOptions)
		if diagnosticErr != nil {
			return report, diagnosticErr
		}
		installationReady = diagnostic.Healthy
		status, code := "pass", "installation_healthy"
		if !diagnostic.Healthy {
			status, code = "fail", "installation_diagnostic_failed"
		}
		report.Evidence = append(report.Evidence, Evidence{ID: "installation", Status: status, Code: code, Source: "local-installation"})
	} else {
		report.Evidence = append(report.Evidence, Evidence{ID: "installation", Status: "not_run", Code: "optional_installation_not_requested", Source: "local-installation"})
	}
	for _, gap := range qualified.AdoptionGaps {
		report.AdoptionGaps = append(report.AdoptionGaps, AdoptionGap{ID: gap.ID, Status: gap.Status, RequiredFor: "production_validation"})
	}
	report.ExternalValidationReady = qualified.StructuralCandidate && check("historical-compatibility").Status == "pass" && check("localhost-origin-boundary").Status == "pass" && installationReady
	if report.ExternalValidationReady {
		report.Decision = "ready_for_external_validation"
	}
	report.OneDotZeroReady = report.ExternalValidationReady && report.ProductionValidated && len(report.AdoptionGaps) == 0
	if report.ProductionValidated || report.OneDotZeroReady {
		return Report{}, fmt.Errorf("readiness cannot infer production validation from structural evidence")
	}
	return report, nil
}
