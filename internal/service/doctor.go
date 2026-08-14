package service

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/gowebpki/jcs"
)

type DoctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type DoctorReport struct {
	Healthy bool          `json:"healthy"`
	Checks  []DoctorCheck `json:"checks"`
}

func (s *Service) Doctor() DoctorReport {
	report := DoctorReport{Healthy: true}
	add := func(name, status, detail string) {
		report.Checks = append(report.Checks, DoctorCheck{Name: name, Status: status, Detail: detail})
		if status == "fail" {
			report.Healthy = false
		}
	}
	if _, err := os.Stat(filepath.Join(s.Project.Root, ".dagrail", "project.yaml")); err != nil {
		add("project-locator", "fail", err.Error())
	} else {
		add("project-locator", "pass", s.Project.Config.ProjectID)
	}
	if segments, err := s.Journal.ReadAll(); err != nil {
		add("journal-chain", "fail", err.Error())
	} else {
		add("journal-chain", "pass", fmt.Sprintf("%d verified segments", len(segments)))
	}
	if err := s.Projection.Integrity(); err != nil {
		add("sqlite-projection", "fail", err.Error())
	} else {
		version, versionErr := s.Projection.SchemaVersion()
		if versionErr != nil {
			add("sqlite-projection", "fail", versionErr.Error())
		} else {
			add("sqlite-projection", "pass", fmt.Sprintf("integrity_check=ok schema=%d", version))
		}
	}
	if executable, err := os.Executable(); err != nil || !filepath.IsAbs(executable) {
		add("runtime-path", "fail", "runtime path is not an absolute executable path")
	} else {
		add("runtime-path", "pass", executable)
	}
	if hookRuntime, err := exec.LookPath("dagrail"); err != nil || !filepath.IsAbs(hookRuntime) {
		add("hook-runtime", "warn", "dagrail is not resolvable by an absolute path in a fresh host PATH; MCP remains absolute but hooks need PATH configuration")
	} else if output, probeErr := exec.Command(hookRuntime, "version").Output(); probeErr != nil || len(output) == 0 {
		add("hook-runtime", "fail", "fresh hook runtime version probe failed")
	} else {
		add("hook-runtime", "pass", hookRuntime)
	}
	for _, providerID := range []string{"manual", "git.merge", "harness.codex", "harness.claude-code", "harness.copilot-cli"} {
		if _, ok := s.Providers.Effect(providerID); !ok {
			add("effect-provider:"+providerID, "fail", "not registered")
		} else {
			add("effect-provider:"+providerID, "pass", "registered")
		}
	}
	for _, harnessID := range []string{"codex", "claude-code", "copilot-cli"} {
		if _, ok := s.Providers.Harness("harness." + harnessID); !ok {
			add("harness-provider:"+harnessID, "fail", "not registered")
		} else {
			add("harness-provider:"+harnessID, "pass", "registered; native capabilities require a separate probe")
		}
	}
	return report
}

func (s *Service) ExportJournal() ([]byte, error) {
	segments, err := s.Journal.ReadAll()
	if err != nil {
		return nil, err
	}
	result := make([]byte, 0)
	for _, segment := range segments {
		raw, err := json.Marshal(segment)
		if err != nil {
			return nil, err
		}
		canonical, err := jcs.Transform(raw)
		if err != nil {
			return nil, err
		}
		result = append(result, canonical...)
		result = append(result, '\n')
	}
	return result, nil
}
