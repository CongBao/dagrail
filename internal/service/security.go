package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/CongBao/dagrail/internal/journal"
)

const SecurityAPIVersion = "dagrail.io/security/v1alpha1"

type SecurityBoundary struct {
	Mode                  string `json:"mode"`
	IsolationBoundary     string `json:"isolationBoundary"`
	PermissionsVerified   bool   `json:"permissionsVerified"`
	MultiTenantIsolation  bool   `json:"multiTenantIsolation"`
	MaliciousPeerProcess  bool   `json:"maliciousPeerProcessIsolation"`
	PermissionEnforcement string `json:"permissionEnforcement"`
}

type SecurityCheck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type SecurityAuditReport struct {
	APIVersion string           `json:"apiVersion"`
	Kind       string           `json:"kind"`
	Secure     bool             `json:"secure"`
	Boundary   SecurityBoundary `json:"boundary"`
	Checks     []SecurityCheck  `json:"checks"`
}

type JournalVerificationReport struct {
	APIVersion    string              `json:"apiVersion"`
	Kind          string              `json:"kind"`
	Valid         bool                `json:"valid"`
	Segments      int                 `json:"segments"`
	Events        int                 `json:"events"`
	HeadSequence  uint64              `json:"headSequence"`
	HeadHash      string              `json:"headHash,omitempty"`
	ExportBytes   int                 `json:"canonicalExportBytes"`
	ExportSHA256  string              `json:"canonicalExportSha256"`
	Compatibility CompatibilityStatus `json:"compatibility"`
}

func (s *Service) VerifyJournalReport() (JournalVerificationReport, error) {
	segments, err := s.VerifyJournal()
	if err != nil {
		return JournalVerificationReport{}, err
	}
	compatibility, err := s.Compatibility()
	if err != nil {
		return JournalVerificationReport{}, err
	}
	exported, err := s.ExportJournal()
	if err != nil {
		return JournalVerificationReport{}, err
	}
	digest := sha256.Sum256(exported)
	report := JournalVerificationReport{
		APIVersion:    "dagrail.io/journal-verification/v1alpha1",
		Kind:          "JournalVerification",
		Valid:         true,
		Segments:      len(segments),
		Events:        compatibility.Journal.EventCount,
		ExportBytes:   len(exported),
		ExportSHA256:  "sha256:" + hex.EncodeToString(digest[:]),
		Compatibility: compatibility,
	}
	if len(segments) > 0 {
		report.HeadSequence = segments[len(segments)-1].Sequence
		report.HeadHash = segments[len(segments)-1].SegmentHash
	}
	return report, nil
}

func (s *Service) SecurityAudit() SecurityAuditReport {
	permissionMode := "unix-mode-bits"
	if runtime.GOOS == "windows" {
		permissionMode = "structural-only; inspect ACLs with host tooling"
	}
	report := SecurityAuditReport{
		APIVersion: SecurityAPIVersion,
		Kind:       "SecurityAudit",
		Secure:     true,
		Boundary: SecurityBoundary{
			Mode:                  "cooperative-single-os-user",
			IsolationBoundary:     "host OS account and filesystem policy",
			PermissionsVerified:   runtime.GOOS != "windows",
			MultiTenantIsolation:  false,
			MaliciousPeerProcess:  false,
			PermissionEnforcement: permissionMode,
		},
	}
	add := func(id, status, detail string) {
		report.Checks = append(report.Checks, SecurityCheck{ID: id, Status: status, Detail: detail})
		if status == "fail" {
			report.Secure = false
		}
	}

	add("trust-boundary", "pass", "roles and leases prevent accidental collision; they do not isolate another process running as the same OS user")
	checkPath := func(id, path, kind string, forbidden os.FileMode, required bool) {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) && !required {
			add(id, "pass", "not present; created lazily with restrictive permissions")
			return
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || (kind == "file" && !info.Mode().IsRegular()) || (kind == "directory" && !info.IsDir()) {
			add(id, "fail", kind+" is missing, has the wrong type, or is a symlink")
			return
		}
		if runtime.GOOS == "windows" {
			add(id, "warn", "structure is valid; Windows ACL enforcement requires host-specific inspection")
			return
		}
		if info.Mode().Perm()&forbidden != 0 {
			add(id, "fail", fmt.Sprintf("mode %04o grants forbidden group/other permissions", info.Mode().Perm()))
			return
		}
		add(id, "pass", fmt.Sprintf("mode %04o", info.Mode().Perm()))
	}

	checkPath("project-locator", filepath.Join(s.Project.Root, ".dagrail", "project.yaml"), "file", 0o022, true)
	checkPath("project-data-directory", s.Project.DataDir, "directory", 0o077, true)
	checkPath("journal-directory", filepath.Join(s.Project.DataDir, "journal"), "directory", 0o077, true)
	checkPath("projection-database", filepath.Join(s.Project.DataDir, "projection.sqlite"), "file", 0o077, true)
	checkPath("action-secret", filepath.Join(s.Project.DataDir, "action-secret"), "file", 0o077, false)
	checkPath("observation-locator", filepath.Join(s.Project.DataDir, "observation-locator.json"), "file", 0o077, false)

	if _, err := s.VerifyJournalReport(); err != nil {
		add("journal-integrity", "fail", "journal verification failed; no authority content or filesystem path is included in this diagnostic")
	} else {
		add("journal-integrity", "pass", "hash chain, schemas, canonical bytes, and export digest verified")
	}
	if err := s.Projection.Integrity(); err != nil {
		add("projection-integrity", "fail", "SQLite integrity verification failed; the projection is disposable and should be rebuilt only after the journal verifies")
	} else {
		add("projection-integrity", "pass", "SQLite integrity_check returned ok")
	}

	entries, err := os.ReadDir(filepath.Join(s.Project.DataDir, "journal"))
	if err != nil {
		add("journal-segment-permissions", "fail", "journal directory could not be inspected")
	} else {
		segments, temporary, insecure := 0, 0, 0
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".json") {
				segments++
				info, infoErr := entry.Info()
				if infoErr != nil || entry.Type()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > journal.MaxSegmentBytes || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
					insecure++
				}
			} else if strings.HasPrefix(entry.Name(), ".segment-") || strings.HasPrefix(entry.Name(), ".restore-") {
				temporary++
				info, infoErr := entry.Info()
				if infoErr != nil || entry.Type()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > journal.MaxSegmentBytes || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
					insecure++
				}
			}
		}
		if insecure > 0 {
			add("journal-segment-permissions", "fail", fmt.Sprintf("%d of %d segment and %d temporary files violate type, size, symlink, or permission policy", insecure, segments, temporary))
		} else if runtime.GOOS == "windows" {
			add("journal-segment-permissions", "warn", fmt.Sprintf("%d segment files are structurally valid; inspect Windows ACLs with host tooling", segments))
		} else {
			detail := fmt.Sprintf("%d segment files are owner-only", segments)
			if temporary > 0 {
				detail += fmt.Sprintf("; %d orphan temporary files should be removed after confirming no writer is active", temporary)
			}
			add("journal-segment-permissions", "pass", detail)
		}
	}

	add("compile-in-providers", "pass", "providers are trusted code and receive no journal or projection handles; they are not sandboxed")
	sort.Slice(report.Checks, func(i, j int) bool { return report.Checks[i].ID < report.Checks[j].ID })
	return report
}
