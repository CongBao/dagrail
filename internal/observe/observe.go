package observe

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/CongBao/dagrail/internal/service"
	"github.com/gowebpki/jcs"
)

const (
	APIVersion             = "dagrail.io/v1beta1"
	maxAuthorityFiles      = 256
	maxAuthorityFileBytes  = 64 * 1024 * 1024
	maxAuthorityTotalBytes = 256 * 1024 * 1024
)

type FileObservation struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Snapshot struct {
	APIVersion     string            `json:"apiVersion"`
	Kind           string            `json:"kind"`
	SourceRoot     string            `json:"-"`
	GraphPath      string            `json:"-"`
	GraphSHA256    string            `json:"graphSha256"`
	GraphRevision  string            `json:"graphRevision"`
	NodeCount      int               `json:"nodeCount"`
	EdgeCount      int               `json:"edgeCount"`
	AuthorityFiles []FileObservation `json:"authorityFiles"`
	SnapshotDigest string            `json:"snapshotDigest"`
}

type Provenance struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Snapshot   Snapshot `json:"snapshot"`
}

type ShadowReport struct {
	Status        string   `json:"status"`
	ShadowRoot    string   `json:"shadowRoot"`
	ProjectID     string   `json:"projectId"`
	HeadSequence  uint64   `json:"headSequence"`
	GraphRevision string   `json:"graphRevision"`
	Snapshot      Snapshot `json:"snapshot"`
}

type VerifyReport struct {
	Valid                  bool   `json:"valid"`
	ShadowRoot             string `json:"shadowRoot"`
	ExpectedSnapshotDigest string `json:"expectedSnapshotDigest"`
	ObservedSnapshotDigest string `json:"observedSnapshotDigest"`
	GraphRevision          string `json:"graphRevision"`
}

type observationLocator struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	SourceRoot string `json:"sourceRoot"`
	GraphPath  string `json:"graphPath"`
}

func Assess(sourceRoot, graphPath string, authorityPaths []string) (Snapshot, error) {
	if sourceRoot == "" || graphPath == "" {
		return Snapshot{}, fmt.Errorf("source root and graph path are required")
	}
	if len(authorityPaths) == 0 || len(authorityPaths) > maxAuthorityFiles {
		return Snapshot{}, fmt.Errorf("between 1 and %d authority paths are required", maxAuthorityFiles)
	}
	root, err := resolveDirectory(sourceRoot)
	if err != nil {
		return Snapshot{}, err
	}
	graph, err := service.ValidateGraphFile(graphPath)
	if err != nil {
		return Snapshot{}, err
	}
	resolvedGraph, err := resolveRegularFile(graphPath)
	if err != nil {
		return Snapshot{}, err
	}
	graphDigest, _, err := digestFile(resolvedGraph, 8*1024*1024)
	if err != nil {
		return Snapshot{}, err
	}
	revision, err := service.GraphRevision(graph)
	if err != nil {
		return Snapshot{}, err
	}

	observations := make([]FileObservation, 0, len(authorityPaths))
	seen := map[string]bool{}
	var total int64
	for _, requested := range authorityPaths {
		if filepath.IsAbs(requested) {
			return Snapshot{}, fmt.Errorf("authority path %s must be relative to source root", requested)
		}
		clean := filepath.Clean(requested)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return Snapshot{}, fmt.Errorf("authority path %s escapes source root", requested)
		}
		resolved, err := resolveRegularFile(filepath.Join(root, clean))
		if err != nil {
			return Snapshot{}, err
		}
		if !within(root, resolved) {
			return Snapshot{}, fmt.Errorf("authority path %s resolves outside source root", requested)
		}
		relative, err := filepath.Rel(root, resolved)
		if err != nil {
			return Snapshot{}, err
		}
		portable := filepath.ToSlash(relative)
		if seen[portable] {
			return Snapshot{}, fmt.Errorf("duplicate authority path %s", portable)
		}
		seen[portable] = true
		digest, size, err := digestFile(resolved, maxAuthorityFileBytes)
		if err != nil {
			return Snapshot{}, err
		}
		total += size
		if total > maxAuthorityTotalBytes {
			return Snapshot{}, fmt.Errorf("authority files exceed %d total bytes", maxAuthorityTotalBytes)
		}
		observations = append(observations, FileObservation{Path: portable, SHA256: digest, Size: size})
	}
	sort.Slice(observations, func(i, j int) bool { return observations[i].Path < observations[j].Path })
	snapshot := Snapshot{
		APIVersion:     APIVersion,
		Kind:           "ObservationSnapshot",
		SourceRoot:     root,
		GraphPath:      resolvedGraph,
		GraphSHA256:    graphDigest,
		GraphRevision:  revision,
		NodeCount:      len(graph.Spec.Nodes),
		EdgeCount:      len(graph.Spec.Edges),
		AuthorityFiles: observations,
	}
	snapshot.SnapshotDigest, err = snapshotDigest(snapshot)
	return snapshot, err
}

func CreateShadow(sourceRoot, graphPath, shadowRoot string, authorityPaths []string) (ShadowReport, error) {
	if shadowRoot == "" {
		return ShadowReport{}, fmt.Errorf("shadow root is required")
	}
	snapshot, err := Assess(sourceRoot, graphPath, authorityPaths)
	if err != nil {
		return ShadowReport{}, err
	}
	shadow, err := resolveShadowPath(shadowRoot)
	if err != nil {
		return ShadowReport{}, err
	}
	if within(snapshot.SourceRoot, shadow) {
		return ShadowReport{}, fmt.Errorf("shadow root must be outside source root")
	}
	if entries, err := os.ReadDir(shadow); err == nil && len(entries) > 0 {
		return ShadowReport{}, fmt.Errorf("shadow root must be absent or empty")
	} else if err != nil && !os.IsNotExist(err) {
		return ShadowReport{}, err
	}
	svc, err := service.Init(shadow, "shadow-"+filepath.Base(snapshot.SourceRoot))
	if err != nil {
		return ShadowReport{}, err
	}
	provenance := Provenance{APIVersion: APIVersion, Kind: "ObserveImport", Snapshot: snapshot}
	if _, err := svc.ImportGraphWithProvenance(snapshot.GraphPath, "observe/"+snapshot.SnapshotDigest, "", provenance); err != nil {
		return ShadowReport{}, err
	}
	if err := writeLocator(svc.Project.DataDir, observationLocator{APIVersion: APIVersion, Kind: "ObservationLocator", SourceRoot: snapshot.SourceRoot, GraphPath: snapshot.GraphPath}); err != nil {
		return ShadowReport{}, err
	}
	state, err := svc.State()
	if err != nil {
		return ShadowReport{}, err
	}
	return ShadowReport{Status: "created", ShadowRoot: shadow, ProjectID: state.ProjectID, HeadSequence: state.HeadSequence, GraphRevision: state.GraphRevision, Snapshot: snapshot}, nil
}

func VerifyShadow(shadowRoot string) (VerifyReport, error) {
	if shadowRoot == "" {
		return VerifyReport{}, fmt.Errorf("shadow root is required")
	}
	svc, err := service.Open(shadowRoot)
	if err != nil {
		return VerifyReport{}, err
	}
	segments, err := svc.VerifyJournal()
	if err != nil {
		return VerifyReport{}, err
	}
	var provenance Provenance
	found := false
	for _, segment := range segments {
		for _, event := range segment.Events {
			if event.Type != "graph.imported" {
				continue
			}
			var payload struct {
				Source Provenance `json:"source"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return VerifyReport{}, err
			}
			provenance, found = payload.Source, true
			break
		}
		if found {
			break
		}
	}
	if !found || provenance.APIVersion != APIVersion || provenance.Kind != "ObserveImport" {
		return VerifyReport{}, fmt.Errorf("shadow journal has no compatible observation provenance")
	}
	expectedDigest, err := snapshotDigest(provenance.Snapshot)
	if err != nil || expectedDigest != provenance.Snapshot.SnapshotDigest {
		return VerifyReport{}, fmt.Errorf("stored observation snapshot digest is invalid")
	}
	locator, err := readLocator(svc.Project.DataDir)
	if err != nil {
		return VerifyReport{}, err
	}
	paths := make([]string, 0, len(provenance.Snapshot.AuthorityFiles))
	for _, observed := range provenance.Snapshot.AuthorityFiles {
		paths = append(paths, observed.Path)
	}
	current, err := Assess(locator.SourceRoot, locator.GraphPath, paths)
	if err != nil {
		return VerifyReport{}, err
	}
	state, err := svc.State()
	if err != nil {
		return VerifyReport{}, err
	}
	valid := current.SnapshotDigest == provenance.Snapshot.SnapshotDigest && state.GraphRevision == provenance.Snapshot.GraphRevision
	shadow, _ := filepath.Abs(shadowRoot)
	return VerifyReport{Valid: valid, ShadowRoot: filepath.Clean(shadow), ExpectedSnapshotDigest: provenance.Snapshot.SnapshotDigest, ObservedSnapshotDigest: current.SnapshotDigest, GraphRevision: state.GraphRevision}, nil
}

func snapshotDigest(snapshot Snapshot) (string, error) {
	snapshot.SnapshotDigest = ""
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("dagrail-observation-snapshot-v1\x00"), canonical...))
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func resolveDirectory(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("source root must be a directory")
	}
	return filepath.Clean(resolved), nil
}

func resolveRegularFile(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s must be a regular file", path)
	}
	return filepath.Clean(resolved), nil
}

func digestFile(path string, limit int64) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return "", 0, fmt.Errorf("%s exceeds the regular-file observation limit", path)
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, limit+1))
	if err != nil || written > limit {
		return "", 0, fmt.Errorf("read authority file %s: size limit exceeded", path)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), written, nil
}

func within(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func resolveShadowPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err == nil {
		resolved, resolveErr := filepath.EvalSymlinks(abs)
		if resolveErr != nil {
			return "", resolveErr
		}
		return filepath.Clean(resolved), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	parent, err := resolveDirectory(filepath.Dir(abs))
	if err != nil {
		return "", fmt.Errorf("shadow parent must already exist: %w", err)
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}

func writeLocator(dataDir string, locator observationLocator) error {
	if locator.APIVersion != APIVersion || locator.Kind != "ObservationLocator" || locator.SourceRoot == "" || locator.GraphPath == "" {
		return fmt.Errorf("invalid observation locator")
	}
	raw, err := json.Marshal(locator)
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, "observation-locator.json")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	complete = true
	return nil
}

func readLocator(dataDir string) (observationLocator, error) {
	path := filepath.Join(dataDir, "observation-locator.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 64*1024 {
		return observationLocator{}, fmt.Errorf("shadow observation locator is unavailable")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return observationLocator{}, err
	}
	var locator observationLocator
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&locator); err != nil || locator.APIVersion != APIVersion || locator.Kind != "ObservationLocator" || locator.SourceRoot == "" || locator.GraphPath == "" {
		return observationLocator{}, fmt.Errorf("invalid observation locator")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return observationLocator{}, fmt.Errorf("observation locator has trailing content")
	}
	return locator, nil
}
