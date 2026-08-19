package project

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

const markerPath = ".dagrail/project.yaml"
const maxMarkerBytes = 64 * 1024
const authorityClaimFile = "authority-claim.json"
const authorityLineageFile = "authority-lineage.json"

var authorityRootTestOverride struct {
	sync.RWMutex
	root string
}

var directorySync = syncDirectoryPath

type authorityAnchor struct {
	APIVersion       string `json:"apiVersion"`
	Kind             string `json:"kind"`
	ProjectID        string `json:"projectId"`
	DataDir          string `json:"dataDir"`
	DataDirHash      string `json:"dataDirHash"`
	Origin           string `json:"origin"`
	ProvenanceDigest string `json:"provenanceDigest,omitempty"`
}

type authorityClaim struct {
	APIVersion    string `json:"apiVersion"`
	Kind          string `json:"kind"`
	ProjectID     string `json:"projectId"`
	DataDirHash   string `json:"dataDirHash"`
	LineageDigest string `json:"lineageDigest,omitempty"`
}

// AuthorityLineage is local recovery provenance for the replacement writer.
// It lives beside runtime state rather than in Project v1alpha1, so v0.21 can
// still open the repository locator during rollback.
type AuthorityLineage struct {
	Operation             string `json:"operation"`
	PreviousProjectID     string `json:"previousProjectId"`
	PreviousLocatorID     string `json:"previousLocatorProjectId,omitempty"`
	TargetRootDigest      string `json:"targetRootDigest,omitempty"`
	DestinationRootDigest string `json:"destinationRuntimeDigest,omitempty"`
	PreviousHead          string `json:"previousHead"`
	RecoveryHead          string `json:"recoveryHead"`
	RecoveryBackupDigest  string `json:"recoveryBackupDigest"`
	RotatedAt             string `json:"rotatedAt"`
	Reason                string `json:"reason"`
	IdempotencyKey        string `json:"idempotencyKey"`
}

// ResolveClaimedAuthority returns the canonical runtime directory authenticated by
// the fixed per-user anchor for projectID. Recovery relocation uses this path instead
// of resolving the source through the destination repository locator or DAGRAIL_HOME.
func ResolveClaimedAuthority(projectID string) (string, error) {
	parsed, err := uuid.Parse(projectID)
	if err != nil || parsed.String() != projectID {
		return "", fmt.Errorf("authority Project UUID is invalid")
	}
	path, err := authorityAnchorPath(projectID)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 16*1024 {
		return "", fmt.Errorf("per-user authority anchor is missing or invalid")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var anchor authorityAnchor
	if err := decoder.Decode(&anchor); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return "", fmt.Errorf("per-user authority anchor is malformed")
	}
	if anchor.APIVersion != "dagrail.io/authority-anchor/v1alpha1" || anchor.Kind != "AuthorityAnchor" || anchor.ProjectID != projectID || (anchor.Origin != "initialized" && anchor.Origin != "rotated") {
		return "", fmt.Errorf("per-user authority anchor does not identify a claimed authority")
	}
	canonical, canonicalErr := canonicalDataDir(anchor.DataDir)
	dataDirHash, hashErr := authorityDataDirHash(anchor.DataDir)
	if canonicalErr != nil || hashErr != nil || canonical != anchor.DataDir || dataDirHash != anchor.DataDirHash {
		return "", fmt.Errorf("per-user authority anchor is bound to unavailable or changed runtime state")
	}
	if err := ValidateAuthorityClaim(anchor.DataDir, projectID); err != nil {
		return "", err
	}
	return anchor.DataDir, nil
}

type Config struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       string   `yaml:"kind" json:"kind"`
	ProjectID  string   `yaml:"projectId" json:"projectId"`
	Name       string   `yaml:"name" json:"name"`
	Providers  []string `yaml:"providers" json:"providers"`
}

type Project struct {
	Root    string
	Config  Config
	DataDir string
}

func Init(root, name string) (Project, error) {
	return InitWithInitializer(root, name, nil)
}

// InitWithInitializer prepares the path-bound writer claim, invokes the
// controller's journal initializer, and only then publishes project.yaml.
// This keeps an older binary from observing a locator before the bootstrap
// fence exists.
func InitWithInitializer(root, name string, initialize func(Project, bool) error) (Project, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return Project{}, err
	}
	if name == "" {
		name = filepath.Base(abs)
	}
	marker := filepath.Join(abs, filepath.FromSlash(markerPath))
	if _, err := os.Stat(marker); err == nil {
		existing, openErr := Open(abs)
		if openErr != nil {
			return Project{}, openErr
		}
		if claimErr := ValidateAuthorityClaim(existing.DataDir, existing.Config.ProjectID); claimErr != nil {
			return Project{}, claimErr
		}
		if initialize != nil {
			if initializeErr := initialize(existing, true); initializeErr != nil {
				return Project{}, initializeErr
			}
		}
		if syncErr := SyncProjectLocator(existing.Root); syncErr != nil {
			return Project{}, syncErr
		}
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Project{}, err
	}
	config := Config{APIVersion: "dagrail.io/v1alpha1", Kind: "Project", ProjectID: uuid.NewString(), Name: name, Providers: []string{"core@0.1.0"}}
	if err := validateConfigAuthority(config); err != nil {
		return Project{}, err
	}
	data, err := yaml.Marshal(config)
	if err != nil {
		return Project{}, err
	}
	dataDir, err := projectDataDir(config.ProjectID)
	if err != nil {
		return Project{}, err
	}
	// Establish the non-portable writer claim before publishing the locator.
	// A crash may leave an unreachable data directory, but never a published
	// Project UUID that ordinary open silently promotes into a writer.
	if err := EstablishAuthorityClaim(dataDir, config.ProjectID); err != nil {
		return Project{}, err
	}
	prepared := Project{Root: abs, Config: config, DataDir: dataDir}
	if initialize != nil {
		if err := initialize(prepared, false); err != nil {
			return Project{}, err
		}
	}
	locatorDir := filepath.Dir(marker)
	if err := ensureDurableDirectory(locatorDir); err != nil {
		return Project{}, err
	}
	if err := writeAtomicExact(marker, data, 0o600); err != nil {
		return Project{}, err
	}
	created, err := Open(abs)
	if err != nil {
		return Project{}, err
	}
	return created, nil
}

func Open(root string) (Project, error) {
	return open(root, true)
}

// OpenInspection resolves an existing project without creating its runtime
// data directory. Read-only callers must fail rather than materialize missing
// authority state as an open-time side effect.
func OpenInspection(root string) (Project, error) {
	return open(root, false)
}

func open(root string, createDataDir bool) (Project, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return Project{}, err
	}
	projectRoot, err := findRoot(abs)
	if err != nil {
		return Project{}, err
	}
	marker := filepath.Join(projectRoot, filepath.FromSlash(markerPath))
	info, err := os.Lstat(marker)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxMarkerBytes {
		return Project{}, fmt.Errorf("open DAGrail project: project locator must be a regular non-symlink file no larger than %d bytes", maxMarkerBytes)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		return Project{}, fmt.Errorf("open DAGrail project: %w", err)
	}
	var config Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return Project{}, fmt.Errorf("decode project locator: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Project{}, fmt.Errorf("decode project locator: multiple YAML documents are not allowed")
	}
	if config.APIVersion != "dagrail.io/v1alpha1" || config.Kind != "Project" || config.ProjectID == "" || len(config.ProjectID) > 128 || len([]byte(config.Name)) > 1024 || len(config.Providers) > 256 {
		return Project{}, fmt.Errorf("invalid DAGrail project locator")
	}
	if err := validateConfigAuthority(config); err != nil {
		return Project{}, err
	}
	parsedProjectID, err := uuid.Parse(config.ProjectID)
	if err != nil || parsedProjectID.String() != config.ProjectID {
		return Project{}, fmt.Errorf("invalid DAGrail project UUID")
	}
	dataDir, err := projectDataDir(config.ProjectID)
	if err != nil {
		return Project{}, err
	}
	if createDataDir {
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			return Project{}, err
		}
	}
	if info, err := os.Lstat(dataDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Project{}, fmt.Errorf("project data directory must be a non-symlink directory")
	}
	return Project{Root: projectRoot, Config: config, DataDir: dataDir}, nil
}

func validateConfigAuthority(config Config) error {
	raw, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode project locator: %w", err)
	}
	if err := domain.RejectSensitiveFields(raw); err != nil {
		return fmt.Errorf("project locator contains prohibited material: %w", err)
	}
	return nil
}

// RotateAuthority atomically replaces only the repository locator. Runtime
// state under the previous Project UUID is never removed or rewritten.
func RotateAuthority(root, expectedProjectID, replacementProjectID string, lineage AuthorityLineage) (Project, error) {
	current, err := Open(root)
	if err != nil {
		return Project{}, err
	}
	if current.Config.ProjectID == replacementProjectID {
		if err := ValidateAuthorityClaim(current.DataDir, replacementProjectID); err != nil {
			return Project{}, err
		}
		stored, err := ReadAuthorityLineage(current.DataDir)
		if err != nil || stored != lineage {
			return Project{}, fmt.Errorf("replacement authority lineage does not match rotation intent")
		}
		if err := SyncProjectLocator(current.Root); err != nil {
			return Project{}, err
		}
		return current, nil
	}
	if current.Config.ProjectID != expectedProjectID {
		return Project{}, fmt.Errorf("project authority changed before rotation")
	}
	config := current.Config
	parsedReplacement, err := uuid.Parse(replacementProjectID)
	if err != nil || parsedReplacement.String() != replacementProjectID || replacementProjectID == expectedProjectID {
		return Project{}, fmt.Errorf("replacement project UUID is invalid")
	}
	config.ProjectID = replacementProjectID
	if err := validateConfigAuthority(config); err != nil {
		return Project{}, err
	}
	dataDir, err := projectDataDir(config.ProjectID)
	if err != nil {
		return Project{}, err
	}
	if err := prepareReplacementAuthority(dataDir, replacementProjectID, lineage); err != nil {
		return Project{}, err
	}
	data, err := yaml.Marshal(config)
	if err != nil {
		return Project{}, err
	}
	marker := filepath.Join(current.Root, filepath.FromSlash(markerPath))
	temporary, err := os.CreateTemp(filepath.Dir(marker), ".project.yaml.rotate-*")
	if err != nil {
		return Project{}, err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return Project{}, err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return Project{}, err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return Project{}, err
	}
	if err := temporary.Close(); err != nil {
		return Project{}, err
	}
	if err := replaceFileAtomic(temporaryName, marker); err != nil {
		return Project{}, err
	}
	if err := SyncProjectLocator(current.Root); err != nil {
		return Project{}, err
	}
	return Open(current.Root)
}

// SyncProjectLocator confirms durability of the repository-side authority
// pointer. Fresh-process idempotent recovery retries call it before returning a
// receipt, including when the original rename was visible but its sync failed.
func SyncProjectLocator(root string) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	marker := filepath.Join(abs, filepath.FromSlash(markerPath))
	info, err := os.Lstat(marker)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("project locator is missing or invalid")
	}
	return syncDirectoryChain(filepath.Dir(marker))
}

// EstablishAuthorityClaim creates the local writer capability for one Project
// UUID. The claim is deliberately excluded from portable backups: restoring a
// full backup in another data root must rotate to a new UUID instead of
// reviving the same writer authority.
func EstablishAuthorityClaim(dataDir, projectID string) error {
	if err := ensureProjectDataDirectory(dataDir); err != nil {
		return err
	}
	if err := establishAuthorityAnchor(dataDir, projectID, "initialized", ""); err != nil {
		return err
	}
	return establishAuthorityClaim(dataDir, projectID, "")
}

func establishAuthorityClaim(dataDir, projectID, lineageDigest string) error {
	if err := ensureProjectDataDirectory(dataDir); err != nil {
		return err
	}
	dataDirHash, err := authorityDataDirHash(dataDir)
	if err != nil {
		return err
	}
	claim := authorityClaim{APIVersion: "dagrail.io/authority-claim/v1alpha1", Kind: "AuthorityClaim", ProjectID: projectID, DataDirHash: dataDirHash, LineageDigest: lineageDigest}
	raw, err := json.Marshal(claim)
	if err != nil {
		return err
	}
	return writeAtomicExact(filepath.Join(dataDir, authorityClaimFile), raw, 0o600)
}

func ValidateAuthorityClaim(dataDir, projectID string) error {
	path := filepath.Join(dataDir, authorityClaimFile)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 4096 {
		return fmt.Errorf("local authority claim is missing or invalid; use recovery rotation instead of reviving this Project UUID")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var claim authorityClaim
	if err := decoder.Decode(&claim); err != nil {
		return fmt.Errorf("decode local authority claim: %w", err)
	}
	dataDirHash, hashErr := authorityDataDirHash(dataDir)
	if decoder.Decode(&struct{}{}) != io.EOF || claim.APIVersion != "dagrail.io/authority-claim/v1alpha1" || claim.Kind != "AuthorityClaim" || claim.ProjectID != projectID || hashErr != nil || claim.DataDirHash != dataDirHash {
		return fmt.Errorf("local authority claim is bound to different identity")
	}
	if claim.LineageDigest != "" {
		lineage, raw, err := readAuthorityLineage(dataDir)
		if err != nil || lineageDigest(raw) != claim.LineageDigest || validateAuthorityLineage(lineage, projectID) != nil {
			return fmt.Errorf("rotated authority lineage is missing, corrupt, or not claim-bound")
		}
	}
	origin, provenance := "initialized", ""
	if claim.LineageDigest != "" {
		origin, provenance = "rotated", claim.LineageDigest
	}
	if err := validateAuthorityAnchor(dataDir, projectID, origin, provenance); err != nil {
		return err
	}
	return nil
}

func prepareReplacementAuthority(dataDir, projectID string, lineage AuthorityLineage) error {
	if err := validateAuthorityLineage(lineage, projectID); err != nil {
		return err
	}
	if info, err := os.Lstat(dataDir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("replacement project data directory is invalid")
		}
		if ValidateAuthorityClaim(dataDir, projectID) == nil {
			stored, readErr := ReadAuthorityLineage(dataDir)
			if readErr == nil && stored == lineage {
				return syncAuthorityEvidence(dataDir, projectID)
			}
		}
		entries, err := os.ReadDir(dataDir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".dagrail-atomic-") {
				info, err := entry.Info()
				if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("replacement project temporary state is invalid")
				}
				if err := os.Remove(filepath.Join(dataDir, entry.Name())); err != nil {
					return err
				}
				continue
			}
			if entry.Name() != authorityClaimFile && entry.Name() != authorityLineageFile {
				return fmt.Errorf("replacement project data directory already exists with unexpected runtime state")
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ensureProjectDataDirectory(dataDir); err != nil {
		return err
	}
	lineageRaw, err := establishAuthorityLineage(dataDir, lineage)
	if err != nil {
		return err
	}
	digest := lineageDigest(lineageRaw)
	if err := establishAuthorityAnchor(dataDir, projectID, "rotated", digest); err != nil {
		return err
	}
	return establishAuthorityClaim(dataDir, projectID, digest)
}

// PrepareReplacementAuthority durably creates the claim and lineage for an
// unpublished replacement UUID. The caller must establish its journal fence
// before publishing the repository locator.
func PrepareReplacementAuthority(projectID string, lineage AuthorityLineage) (string, error) {
	dataDir, err := projectDataDir(projectID)
	if err != nil {
		return "", err
	}
	if err := prepareReplacementAuthority(dataDir, projectID, lineage); err != nil {
		return "", err
	}
	return dataDir, nil
}

// ReserveLegacyRetirement selects exactly one local pre-v0.22 authority root.
// The old UUID is never made writable by v0.22; the reservation only permits a
// schema-4 retirement fence followed by a fresh replacement identity.
func ReserveLegacyRetirement(dataDir, projectID, provenanceDigest string) error {
	if !validAuthorityProvenanceDigest(provenanceDigest) {
		return fmt.Errorf("legacy retirement provenance digest is invalid")
	}
	if _, err := os.Lstat(filepath.Join(dataDir, authorityClaimFile)); err == nil {
		return fmt.Errorf("authority already has a v0.22 claim and is not eligible for legacy retirement")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Lstat(filepath.Join(dataDir, authorityLineageFile)); err == nil {
		return fmt.Errorf("rotated authority is not eligible for legacy retirement")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := establishAuthorityAnchor(dataDir, projectID, "legacy-retired", provenanceDigest); err != nil {
		return err
	}
	return ValidateLegacyRetirementReservation(dataDir, projectID, provenanceDigest)
}

// ValidateLegacyRetirementReservation proves that the recovery-only journal
// handle is operating on the exact unclaimed, non-rotated legacy data root
// reserved by the caller's authenticated migration intent.
func ValidateLegacyRetirementReservation(dataDir, projectID, provenanceDigest string) error {
	if provenanceDigest != "" && !validAuthorityProvenanceDigest(provenanceDigest) {
		return fmt.Errorf("legacy retirement provenance digest is invalid")
	}
	if _, err := os.Lstat(filepath.Join(dataDir, authorityClaimFile)); err == nil {
		return fmt.Errorf("authority already has a v0.22 claim and is not eligible for legacy retirement")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Lstat(filepath.Join(dataDir, authorityLineageFile)); err == nil {
		return fmt.Errorf("rotated authority is not eligible for legacy retirement")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if provenanceDigest != "" {
		return validateAuthorityAnchor(dataDir, projectID, "legacy-retired", provenanceDigest)
	}
	return validateLegacyRetirementAnchor(dataDir, projectID)
}

func validateLegacyRetirementAnchor(dataDir, projectID string) error {
	path, err := authorityAnchorPath(projectID)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 16*1024 {
		return fmt.Errorf("legacy retirement reservation is missing or invalid")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var anchor authorityAnchor
	if err := decoder.Decode(&anchor); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("legacy retirement reservation is malformed")
	}
	canonical, canonicalErr := canonicalDataDir(dataDir)
	dataDirHash, hashErr := authorityDataDirHash(dataDir)
	if canonicalErr != nil || hashErr != nil || anchor.APIVersion != "dagrail.io/authority-anchor/v1alpha1" || anchor.Kind != "AuthorityAnchor" || anchor.ProjectID != projectID || anchor.DataDir != canonical || anchor.DataDirHash != dataDirHash || anchor.Origin != "legacy-retired" || !validAuthorityProvenanceDigest(anchor.ProvenanceDigest) {
		return fmt.Errorf("legacy retirement reservation is bound to a different local authority")
	}
	return nil
}

func validAuthorityProvenanceDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func establishAuthorityAnchor(dataDir, projectID, origin, provenanceDigest string) error {
	canonical, err := canonicalDataDir(dataDir)
	if err != nil {
		return err
	}
	dataDirHash, err := authorityDataDirHash(dataDir)
	if err != nil {
		return err
	}
	anchor := authorityAnchor{APIVersion: "dagrail.io/authority-anchor/v1alpha1", Kind: "AuthorityAnchor", ProjectID: projectID, DataDir: canonical, DataDirHash: dataDirHash, Origin: origin, ProvenanceDigest: provenanceDigest}
	raw, err := json.Marshal(anchor)
	if err != nil {
		return err
	}
	path, err := authorityAnchorPath(projectID)
	if err != nil {
		return err
	}
	return writeAtomicExact(path, raw, 0o600)
}

func validateAuthorityAnchor(dataDir, projectID, origin, provenanceDigest string) error {
	path, err := authorityAnchorPath(projectID)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 16*1024 {
		return fmt.Errorf("per-user authority anchor is missing or invalid; rotate identity instead of recreating this Project UUID")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var anchor authorityAnchor
	if err := decoder.Decode(&anchor); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("per-user authority anchor is malformed")
	}
	canonical, canonicalErr := canonicalDataDir(dataDir)
	dataDirHash, hashErr := authorityDataDirHash(dataDir)
	if canonicalErr != nil || hashErr != nil || anchor.APIVersion != "dagrail.io/authority-anchor/v1alpha1" || anchor.Kind != "AuthorityAnchor" || anchor.ProjectID != projectID || anchor.DataDir != canonical || anchor.DataDirHash != dataDirHash || anchor.Origin != origin || anchor.ProvenanceDigest != provenanceDigest {
		return fmt.Errorf("per-user authority anchor is bound to a different local authority")
	}
	return nil
}

// SetAuthorityRootForTesting redirects per-user authority anchors for one test
// process. It is an internal test seam, not a CLI or production configuration
// surface; shipped binaries never consult an environment variable for this root.
func SetAuthorityRootForTesting(root string) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	authorityRootTestOverride.Lock()
	authorityRootTestOverride.root = filepath.Clean(abs)
	authorityRootTestOverride.Unlock()
	return nil
}

func authorityRoot() (string, error) {
	authorityRootTestOverride.RLock()
	override := authorityRootTestOverride.root
	authorityRootTestOverride.RUnlock()
	if override != "" {
		return override, nil
	}
	if override = authorityRootFromTestEnvironment(); override != "" {
		return override, nil
	}
	current, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("resolve stable OS-user authority root: %w", err)
	}
	if current.HomeDir == "" {
		return "", fmt.Errorf("resolve stable OS-user authority root: OS account has no home directory")
	}
	home, err := filepath.Abs(current.HomeDir)
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "dagrail-authority"), nil
	case "windows":
		return filepath.Join(home, "AppData", "Roaming", "DAGrail", "authority"), nil
	default:
		return filepath.Join(home, ".config", "dagrail-authority"), nil
	}
}

func authorityAnchorPath(projectID string) (string, error) {
	root, err := authorityRoot()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	anchorDir := filepath.Join(abs, "anchors")
	if err := ensureDurableDirectory(anchorDir); err != nil {
		return "", err
	}
	if info, err := os.Lstat(anchorDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("authority anchor root must be a non-symlink directory")
	}
	return filepath.Join(anchorDir, projectID+".json"), nil
}

func syncAuthorityEvidence(dataDir, projectID string) error {
	if err := ValidateAuthorityClaim(dataDir, projectID); err != nil {
		return err
	}
	anchorPath, err := authorityAnchorPath(projectID)
	if err != nil {
		return err
	}
	if err := syncDirectoryChain(dataDir); err != nil {
		return err
	}
	return syncDirectoryChain(filepath.Dir(anchorPath))
}

// EnsureDurableDirectory creates one path component at a time and confirms
// every newly created namespace entry. A retry rechecks the deepest visible
// component and its parent before it creates anything else; unrelated system
// ancestors are never traversed to the volume root.
func EnsureDurableDirectory(path string) error {
	return ensureDurableDirectory(path)
}

// EnsureDurableDirectoryWithin creates and revalidates path without walking
// above a caller-authenticated, already-existing namespace boundary.
func EnsureDurableDirectoryWithin(path, boundary string) error {
	return ensureDurableDirectoryWithin(path, boundary)
}

// SyncDirectory confirms one already-visible directory. Journal segment
// commits use this after rename; directory creation uses EnsureDurableDirectory.
func SyncDirectory(path string) error {
	return syncDirectoryChain(path)
}

func canonicalDataDir(dataDir string) (string, error) {
	canonical, err := filepath.Abs(dataDir)
	if err != nil {
		return "", err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(canonical); resolveErr == nil {
		canonical = resolved
	}
	return filepath.Clean(canonical), nil
}

func establishAuthorityLineage(dataDir string, lineage AuthorityLineage) ([]byte, error) {
	raw, err := json.Marshal(lineage)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, authorityLineageFile)
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, raw) {
			return existing, syncDirectoryPath(dataDir)
		}
		return nil, fmt.Errorf("replacement authority lineage is already bound to different intent")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := writeAtomicExact(path, raw, 0o600); err != nil {
		return nil, err
	}
	return raw, nil
}

func ReadAuthorityLineage(dataDir string) (AuthorityLineage, error) {
	lineage, _, err := readAuthorityLineage(dataDir)
	return lineage, err
}

// RestoreAuthorityLineage is an explicit recovery primitive. It rewrites only
// lineage bytes whose digest is already authenticated by the existing local
// claim, then re-validates the complete claim. Ordinary open never calls it.
func RestoreAuthorityLineage(dataDir, projectID string, lineage AuthorityLineage) error {
	raw, err := json.Marshal(lineage)
	if err != nil {
		return err
	}
	claimPath := filepath.Join(dataDir, authorityClaimFile)
	claimInfo, err := os.Lstat(claimPath)
	if err != nil || !claimInfo.Mode().IsRegular() || claimInfo.Mode()&os.ModeSymlink != 0 || claimInfo.Size() <= 0 || claimInfo.Size() > 4096 {
		return fmt.Errorf("local authority claim cannot authenticate lineage recovery")
	}
	claimRaw, err := os.ReadFile(claimPath)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(claimRaw))
	decoder.DisallowUnknownFields()
	var claim authorityClaim
	if err := decoder.Decode(&claim); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("local authority claim cannot authenticate lineage recovery")
	}
	dataDirHash, err := authorityDataDirHash(dataDir)
	if err != nil {
		return err
	}
	if claim.APIVersion != "dagrail.io/authority-claim/v1alpha1" || claim.Kind != "AuthorityClaim" || claim.ProjectID != projectID || claim.DataDirHash != dataDirHash || claim.LineageDigest != lineageDigest(raw) {
		return fmt.Errorf("local authority claim cannot authenticate lineage recovery")
	}
	lineagePath := filepath.Join(dataDir, authorityLineageFile)
	if existing, readErr := os.ReadFile(lineagePath); readErr == nil && bytes.Equal(existing, raw) {
		return ValidateAuthorityClaim(dataDir, projectID)
	}
	if err := writeAtomicReplace(lineagePath, raw, 0o600); err != nil {
		return err
	}
	return ValidateAuthorityClaim(dataDir, projectID)
}

func readAuthorityLineage(dataDir string) (AuthorityLineage, []byte, error) {
	path := filepath.Join(dataDir, authorityLineageFile)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 64*1024 {
		return AuthorityLineage{}, nil, fmt.Errorf("replacement authority lineage is missing or invalid")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return AuthorityLineage{}, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var lineage AuthorityLineage
	if err := decoder.Decode(&lineage); err != nil {
		return AuthorityLineage{}, nil, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return AuthorityLineage{}, nil, fmt.Errorf("replacement authority lineage has trailing content")
	}
	return lineage, raw, nil
}

func authorityDataDirHash(dataDir string) (string, error) {
	canonical, err := canonicalDataDir(dataDir)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte("dagrail-authority-data-root-v1\x00" + filepath.Clean(canonical)))
	return fmt.Sprintf("sha256:%x", sum[:]), nil
}

func lineageDigest(raw []byte) string {
	sum := sha256.Sum256(append([]byte("dagrail-authority-lineage-v1\x00"), raw...))
	return fmt.Sprintf("sha256:%x", sum[:])
}

func validJournalHead(value string) bool {
	if value == "" {
		return true
	}
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func validAuthorityDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && validJournalHead(strings.TrimPrefix(value, "sha256:")) && value != "sha256:"
}

func validateAuthorityLineage(lineage AuthorityLineage, projectID string) error {
	previous, previousErr := uuid.Parse(lineage.PreviousProjectID)
	current, currentErr := uuid.Parse(projectID)
	if previousErr != nil || currentErr != nil || previous.String() != lineage.PreviousProjectID || current.String() != projectID || lineage.PreviousProjectID == projectID || !validAuthorityDigest(lineage.RecoveryBackupDigest) || strings.TrimSpace(lineage.Reason) == "" || len([]byte(lineage.Reason)) > 1024 || strings.TrimSpace(lineage.IdempotencyKey) == "" || len([]byte(lineage.IdempotencyKey)) > 256 {
		return fmt.Errorf("replacement authority lineage is structurally invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, lineage.RotatedAt); err != nil {
		return fmt.Errorf("replacement authority lineage timestamp is invalid")
	}
	switch lineage.Operation {
	case "rotation":
		if !validJournalHead(lineage.PreviousHead) || lineage.PreviousHead == "" || !validJournalHead(lineage.RecoveryHead) || lineage.RecoveryHead == "" {
			return fmt.Errorf("replacement authority rotation lineage is invalid")
		}
	case "legacy-adoption":
		if !validJournalHead(lineage.PreviousHead) || lineage.RecoveryHead != lineage.PreviousHead {
			return fmt.Errorf("replacement legacy-adoption lineage is invalid")
		}
	case "relocation":
		locator, locatorErr := uuid.Parse(lineage.PreviousLocatorID)
		if !validJournalHead(lineage.PreviousHead) || lineage.PreviousHead == "" || !validJournalHead(lineage.RecoveryHead) || lineage.RecoveryHead == "" || locatorErr != nil || locator.String() != lineage.PreviousLocatorID || lineage.PreviousLocatorID == projectID || !validAuthorityProvenanceDigest(lineage.TargetRootDigest) || !validAuthorityProvenanceDigest(lineage.DestinationRootDigest) {
			return fmt.Errorf("replacement authority relocation lineage is invalid")
		}
	default:
		return fmt.Errorf("replacement authority lineage operation is invalid")
	}
	return nil
}

func writeAtomicExact(path string, data []byte, mode os.FileMode) error {
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, data) {
			return syncDirectoryChain(filepath.Dir(path))
		}
		return fmt.Errorf("%s is already bound to different content", filepath.Base(path))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ensureDurableDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".dagrail-atomic-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := publishPathExclusive(temporaryName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, readErr := os.ReadFile(path)
			if readErr == nil && bytes.Equal(existing, data) {
				return syncDirectoryChain(filepath.Dir(path))
			}
		}
		return err
	}
	return syncDirectoryChain(filepath.Dir(path))
}

func writeAtomicReplace(path string, data []byte, mode os.FileMode) error {
	if err := ensureDurableDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".dagrail-atomic-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := replaceFileAtomic(temporaryName, path); err != nil {
		return err
	}
	return syncDirectoryChain(filepath.Dir(path))
}

// PublishPathExclusive atomically publishes a same-volume staged path without
// replacing an existing destination. Callers must still call SyncDirectory on
// the containing directory after the publication point. Windows performs the
// namespace move with MOVEFILE_WRITE_THROUGH; POSIX callers close durability
// with the containing-directory fsync.
func PublishPathExclusive(source, destination string) error {
	return publishPathExclusive(source, destination)
}

// PublishPathAtomic moves a same-volume staged path into place. The caller
// owns any no-replacement invariant and must still call SyncDirectory after
// the publication point. Windows makes the move write-through.
func PublishPathAtomic(source, destination string) error {
	return publishPathAtomic(source, destination)
}

// PublishDirectoryExclusive atomically publishes a staged directory without
// replacing a populated destination. It exists separately from
// PublishPathExclusive because POSIX hard links cannot target directories.
func PublishDirectoryExclusive(source, destination string) error {
	return publishDirectoryExclusive(source, destination)
}

// ReplacePathAtomic atomically replaces a same-volume destination. Callers
// must still call SyncDirectory on the containing directory. Windows uses
// MOVEFILE_REPLACE_EXISTING|MOVEFILE_WRITE_THROUGH.
func ReplacePathAtomic(source, destination string) error {
	return replaceFileAtomic(source, destination)
}

func ensureDurableDirectory(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	abs = filepath.Clean(abs)
	current := abs
	for {
		info, statErr := os.Lstat(current)
		if statErr == nil {
			if !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("authority directory chain contains a non-directory entry")
			}
			if current == abs && info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("authority directory path must not be a symbolic link")
			}
			if info.Mode()&os.ModeSymlink != 0 {
				parent := filepath.Dir(current)
				if parent == current {
					return fmt.Errorf("durable directory path has no non-symlink existing ancestor")
				}
				current = parent
				continue
			}
			relative, relErr := filepath.Rel(current, abs)
			if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
				return fmt.Errorf("durable directory path escapes its discovered boundary")
			}
			if current == filepath.Dir(current) && current != abs {
				return fmt.Errorf("durable directory path has no user-managed existing ancestor")
			}
			return ensureDurableDirectoryWithin(filepath.Join(current, relative), current)
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("authority directory chain has no existing ancestor")
		}
		current = parent
	}
}

func ensureDurableDirectoryWithin(path, boundary string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	boundaryAbs, err := filepath.Abs(boundary)
	if err != nil {
		return err
	}
	abs = filepath.Clean(abs)
	boundaryAbs = filepath.Clean(boundaryAbs)
	relative, err := filepath.Rel(boundaryAbs, abs)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("durable directory path escapes its authenticated boundary")
	}
	boundaryInfo, err := os.Lstat(boundaryAbs)
	if err != nil || !boundaryInfo.IsDir() || boundaryInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("durable directory boundary must be an existing non-symlink directory")
	}
	current := boundaryAbs
	if err := syncOneDirectory(current); err != nil {
		return err
	}
	if relative == "." {
		return nil
	}
	for _, component := range strings.Split(filepath.Clean(relative), string(filepath.Separator)) {
		next := filepath.Join(current, component)
		if err := createDirectoryEntry(next, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(next)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("authority directory chain contains a missing or non-directory entry")
		}
		if err := syncOneDirectory(next); err != nil {
			return err
		}
		if err := syncOneDirectory(current); err != nil {
			return err
		}
		current = next
	}
	return nil
}

func ensureProjectDataDirectory(dataDir string) error {
	return ensureDurableDirectory(dataDir)
}

func syncDirectoryChain(path string) error {
	current, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	// macOS commonly exposes /var as a symlink to /private/var. Resolve the
	// already-created directory before confirming it so a
	// normal OS layout is not mistaken for an attacker-controlled leaf link.
	current, err = filepath.EvalSymlinks(current)
	if err != nil {
		return fmt.Errorf("resolve authority directory chain: %w", err)
	}
	return syncOneDirectory(current)
}

func syncOneDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("authority directory chain contains a missing or non-directory entry")
	}
	return directorySync(path)
}

// DataDirForProjectID resolves one local authority store without creating it.
func DataDirForProjectID(projectID string) (string, error) {
	return projectDataDir(projectID)
}

func findRoot(start string) (string, error) {
	current := filepath.Clean(start)
	if info, err := os.Stat(current); err == nil && !info.IsDir() {
		current = filepath.Dir(current)
	}
	for {
		marker := filepath.Join(current, filepath.FromSlash(markerPath))
		if info, err := os.Stat(marker); err == nil && !info.IsDir() {
			return current, nil
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("locate DAGrail project: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("open DAGrail project: %s was not found at or above %s", markerPath, start)
		}
		current = parent
	}
}

func projectDataDir(projectID string) (string, error) {
	if override := os.Getenv("DAGRAIL_HOME"); override != "" {
		return filepath.Join(override, "projects", projectID), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	var root string
	switch runtime.GOOS {
	case "darwin":
		root = filepath.Join(home, "Library", "Application Support", "dagrail")
	case "windows":
		root = os.Getenv("LOCALAPPDATA")
		if root == "" {
			root = filepath.Join(home, "AppData", "Local")
		}
		root = filepath.Join(root, "DAGrail")
	default:
		root = os.Getenv("XDG_DATA_HOME")
		if root == "" {
			root = filepath.Join(home, ".local", "share")
		}
		root = filepath.Join(root, "dagrail")
	}
	return filepath.Join(root, "projects", projectID), nil
}
