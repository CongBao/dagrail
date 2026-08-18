package gitartifact

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/gowebpki/jcs"
)

const (
	ClosureAPIVersion = "dagrail.io/git-artifact-closure/v1alpha1"
	ClosureKind       = "GitArtifactClosure"
	ScopeAPIVersion   = "dagrail.io/git-integration-scope/v1alpha1"
	maxManifestBytes  = 1 << 20
	maxGitOutputBytes = 4 << 20
	maxRawObjectBytes = 4 << 20
)

type ObjectExpectation struct {
	Name       string   `json:"name"`
	OID        string   `json:"oid"`
	Type       string   `json:"type"`
	Tree       string   `json:"tree,omitempty"`
	Parents    []string `json:"parents,omitempty"`
	RetainedBy []string `json:"retainedBy"`
}

type RefExpectation struct {
	Name   string `json:"name"`
	OID    string `json:"oid"`
	Peeled string `json:"peeled,omitempty"`
}

type ClosureManifest struct {
	APIVersion string              `json:"apiVersion"`
	Kind       string              `json:"kind"`
	Objects    []ObjectExpectation `json:"objects"`
	Refs       []RefExpectation    `json:"refs"`
}

type ObjectResult struct {
	Name       string   `json:"name"`
	OID        string   `json:"oid"`
	Type       string   `json:"type,omitempty"`
	RetainedBy []string `json:"retainedBy,omitempty"`
	Valid      bool     `json:"valid"`
	Reasons    []string `json:"reasons,omitempty"`
}

type RefResult struct {
	Name    string   `json:"name"`
	OID     string   `json:"oid,omitempty"`
	Peeled  string   `json:"peeled,omitempty"`
	Valid   bool     `json:"valid"`
	Reasons []string `json:"reasons,omitempty"`
}

type ClosureReport struct {
	APIVersion     string         `json:"apiVersion"`
	Kind           string         `json:"kind"`
	ManifestDigest string         `json:"manifestDigest"`
	Valid          bool           `json:"valid"`
	Objects        []ObjectResult `json:"objects"`
	Refs           []RefResult    `json:"refs"`
}

type ScopeEntry struct {
	Path            string `json:"path"`
	Class           string `json:"class"`
	BaseMode        string `json:"baseMode,omitempty"`
	BaseOID         string `json:"baseOid,omitempty"`
	CandidateMode   string `json:"candidateMode,omitempty"`
	CandidateOID    string `json:"candidateOid,omitempty"`
	TargetMode      string `json:"targetMode,omitempty"`
	TargetOID       string `json:"targetOid,omitempty"`
	ProspectiveMode string `json:"prospectiveMode,omitempty"`
	ProspectiveOID  string `json:"prospectiveOid,omitempty"`
}

type treeEntry struct {
	Mode string
	OID  string
}

type ScopeReport struct {
	APIVersion  string         `json:"apiVersion"`
	Kind        string         `json:"kind"`
	Base        string         `json:"base"`
	Candidate   string         `json:"candidate"`
	Target      string         `json:"target"`
	Prospective string         `json:"prospective"`
	Closed      bool           `json:"closed"`
	Counts      map[string]int `json:"counts"`
	Entries     []ScopeEntry   `json:"entries"`
}

func DecodeManifest(path string) (ClosureManifest, error) {
	return DecodeManifestContext(context.Background(), path)
}

func DecodeManifestContext(ctx context.Context, path string) (ClosureManifest, error) {
	if err := ctx.Err(); err != nil {
		return ClosureManifest{}, err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return ClosureManifest{}, err
	}
	if !pathInfo.Mode().IsRegular() {
		return ClosureManifest{}, fmt.Errorf("artifact closure manifest must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return ClosureManifest{}, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return ClosureManifest{}, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) {
		return ClosureManifest{}, fmt.Errorf("artifact closure manifest changed while opening")
	}
	if openedInfo.Size() < 1 || openedInfo.Size() > maxManifestBytes {
		return ClosureManifest{}, fmt.Errorf("artifact closure manifest must be 1..%d bytes", maxManifestBytes)
	}
	raw, err := readBoundedContext(ctx, file, maxManifestBytes)
	if err != nil {
		return ClosureManifest{}, err
	}
	afterInfo, err := file.Stat()
	if err != nil {
		return ClosureManifest{}, err
	}
	if afterInfo.Size() != openedInfo.Size() || !afterInfo.ModTime().Equal(openedInfo.ModTime()) {
		return ClosureManifest{}, fmt.Errorf("artifact closure manifest changed while reading")
	}
	return decodeManifest(raw)
}

func readBoundedContext(ctx context.Context, reader io.Reader, limit int) ([]byte, error) {
	var output bytes.Buffer
	chunk := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining := limit + 1 - output.Len()
		if remaining <= 0 {
			return nil, fmt.Errorf("artifact closure manifest must be 1..%d bytes", limit)
		}
		if remaining < len(chunk) {
			chunk = chunk[:remaining]
		}
		count, err := reader.Read(chunk)
		if count > 0 {
			_, _ = output.Write(chunk[:count])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, fmt.Errorf("artifact closure manifest reader made no progress")
		}
	}
	if output.Len() == 0 || output.Len() > limit {
		return nil, fmt.Errorf("artifact closure manifest must be 1..%d bytes", limit)
	}
	return output.Bytes(), nil
}

func decodeManifest(raw []byte) (ClosureManifest, error) {
	if len(raw) == 0 || len(raw) > maxManifestBytes {
		return ClosureManifest{}, fmt.Errorf("artifact closure manifest must be 1..%d bytes", maxManifestBytes)
	}
	if err := domain.ValidateAuthorityJSON(raw); err != nil {
		return ClosureManifest{}, fmt.Errorf("artifact closure manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest ClosureManifest
	if err := decoder.Decode(&manifest); err != nil {
		return ClosureManifest{}, fmt.Errorf("decode artifact closure manifest: %w", err)
	}
	if err := validateManifest(manifest); err != nil {
		return ClosureManifest{}, err
	}
	return manifest, nil
}

func Verify(repository string, manifest ClosureManifest) (ClosureReport, error) {
	return VerifyContext(context.Background(), repository, manifest)
}

func VerifyContext(ctx context.Context, repository string, manifest ClosureManifest) (ClosureReport, error) {
	if err := validateManifest(manifest); err != nil {
		return ClosureReport{}, err
	}
	repository, err := canonicalRepository(repository)
	if err != nil {
		return ClosureReport{}, fmt.Errorf("open Git repository: %w", err)
	}
	if _, err := gitContext(ctx, repository, "rev-parse", "--git-dir"); err != nil {
		return ClosureReport{}, fmt.Errorf("open Git repository: %w", err)
	}
	manifestRaw, _ := json.Marshal(manifest)
	canonical, err := jcs.Transform(manifestRaw)
	if err != nil {
		return ClosureReport{}, err
	}
	sum := sha256.Sum256(append([]byte("dagrail-git-artifact-closure-v1\x00"), canonical...))
	report := ClosureReport{APIVersion: ClosureAPIVersion, Kind: "GitArtifactClosureReport", ManifestDigest: "sha256:" + hex.EncodeToString(sum[:]), Valid: true}
	objectReader, err := newRawObjectReader(ctx, repository)
	if err != nil {
		return ClosureReport{}, err
	}
	defer objectReader.Close()
	refValues, err := readRefs(ctx, repository, manifest.Refs)
	if err != nil {
		return ClosureReport{}, err
	}
	objectTypes, err := readObjectTypes(ctx, repository, manifest.Objects)
	if err != nil {
		return ClosureReport{}, err
	}
	refs := make(map[string]RefResult, len(manifest.Refs))
	refTargets := make(map[string]string, len(manifest.Refs))
	for _, expected := range manifest.Refs {
		result := RefResult{Name: expected.Name, Valid: true}
		result.OID = refValues[expected.Name]
		if result.OID == "" {
			result.Valid = false
			result.Reasons = append(result.Reasons, "ref_missing")
		} else {
			if result.OID != expected.OID {
				result.Valid = false
				result.Reasons = append(result.Reasons, "ref_oid_mismatch")
			}
			peeled, _, peeledErr := peelRawObject(objectReader, result.OID)
			if peeledErr != nil {
				if err := ctx.Err(); err != nil {
					return ClosureReport{}, err
				}
				result.Valid = false
				result.Reasons = append(result.Reasons, "ref_peel_failed")
			} else {
				refTargets[expected.Name] = peeled
				if expected.Peeled != "" {
					result.Peeled = peeled
				}
				if expected.Peeled != "" && peeled != expected.Peeled {
					result.Valid = false
					result.Reasons = append(result.Reasons, "ref_peeled_mismatch")
				}
			}
		}
		refs[expected.Name] = result
		report.Refs = append(report.Refs, result)
		report.Valid = report.Valid && result.Valid
	}
	objects := make(map[string]ObjectExpectation, len(manifest.Objects))
	for _, object := range manifest.Objects {
		objects[object.OID] = object
	}
	ancestorSets := map[string]map[string]bool{}
	for _, expected := range manifest.Objects {
		result := ObjectResult{Name: expected.Name, OID: expected.OID, Valid: true}
		result.Type = objectTypes[expected.OID]
		if result.Type == "" {
			result.Valid = false
			result.Reasons = append(result.Reasons, "object_missing")
		} else {
			if result.Type != expected.Type {
				result.Valid = false
				result.Reasons = append(result.Reasons, "object_type_mismatch")
			}
		}
		if expected.Type == "commit" && result.Type == "commit" {
			commit, commitErr := objectReader.Commit(expected.OID)
			if commitErr != nil {
				if err := ctx.Err(); err != nil {
					return ClosureReport{}, err
				}
			}
			if commitErr != nil || commit.Tree != expected.Tree {
				result.Valid = false
				result.Reasons = append(result.Reasons, "commit_tree_mismatch")
			}
			if commitErr != nil || !equalStrings(commit.Parents, expected.Parents) {
				result.Valid = false
				result.Reasons = append(result.Reasons, "commit_parent_order_mismatch")
			}
		}
		for _, refName := range expected.RetainedBy {
			refResult, ok := refs[refName]
			if !ok || !refResult.Valid {
				result.Valid = false
				result.Reasons = append(result.Reasons, "retaining_ref_invalid:"+refName)
				continue
			}
			retained := false
			switch expected.Type {
			case "commit":
				target := refTargets[refName]
				ancestors, ok := ancestorSets[target]
				if !ok {
					var closureErr error
					ancestors, closureErr = rawCommitClosure(objectReader, target)
					if closureErr != nil {
						if err := ctx.Err(); err != nil {
							return ClosureReport{}, err
						}
						ancestors = map[string]bool{}
					}
					ancestorSets[target] = ancestors
				}
				retained = ancestors[expected.OID]
			case "tag":
				retained = refResult.OID == expected.OID
			case "tree":
				for _, commit := range objects {
					if commit.Type == "commit" && commit.Tree == expected.OID && contains(commit.RetainedBy, refName) {
						retained = true
						break
					}
				}
			}
			if retained {
				result.RetainedBy = append(result.RetainedBy, refName)
			} else {
				result.Valid = false
				result.Reasons = append(result.Reasons, "object_not_retained_by:"+refName)
			}
		}
		sort.Strings(result.RetainedBy)
		sort.Strings(result.Reasons)
		report.Objects = append(report.Objects, result)
		report.Valid = report.Valid && result.Valid
	}
	if err := ctx.Err(); err != nil {
		return ClosureReport{}, err
	}
	return report, nil
}

func InspectScope(repository, base, candidate, target, prospective string) (ScopeReport, error) {
	return InspectScopeContext(context.Background(), repository, base, candidate, target, prospective)
}

func InspectScopeContext(ctx context.Context, repository, base, candidate, target, prospective string) (ScopeReport, error) {
	resolvedRepository, err := canonicalRepository(repository)
	if err != nil {
		return ScopeReport{}, fmt.Errorf("open Git repository: %w", err)
	}
	repository = resolvedRepository
	objectReader, err := newRawObjectReader(ctx, repository)
	if err != nil {
		return ScopeReport{}, err
	}
	defer objectReader.Close()
	identities := []*string{&base, &candidate, &target, &prospective}
	for _, identity := range identities {
		if !validOID(*identity) {
			return ScopeReport{}, fmt.Errorf("scope identities must be exact 40- or 64-character Git object IDs")
		}
		if _, err := objectReader.Commit(*identity); err != nil {
			return ScopeReport{}, fmt.Errorf("scope identity %s is not an exact commit object", *identity)
		}
	}
	paths := map[string]bool{}
	for _, pair := range [][2]string{{base, candidate}, {base, target}, {target, prospective}} {
		output, err := gitContext(ctx, repository, "diff", "--name-only", "--no-renames", "-z", pair[0], pair[1])
		if err != nil {
			return ScopeReport{}, err
		}
		for _, path := range strings.Split(output, "\x00") {
			if path != "" {
				paths[path] = true
			}
		}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		if !utf8.ValidString(path) || len(path) > 4096 {
			return ScopeReport{}, fmt.Errorf("git path is not valid bounded UTF-8")
		}
		ordered = append(ordered, path)
		if len(ordered) > 100000 {
			return ScopeReport{}, fmt.Errorf("integration scope exceeds 100000 paths")
		}
	}
	sort.Strings(ordered)
	trees := make([]map[string]treeEntry, 0, len(identities))
	for _, identity := range identities {
		entries, err := treeEntriesAtCommit(ctx, repository, *identity)
		if err != nil {
			return ScopeReport{}, err
		}
		trees = append(trees, entries)
	}
	report := ScopeReport{APIVersion: ScopeAPIVersion, Kind: "GitIntegrationScope", Base: base, Candidate: candidate, Target: target, Prospective: prospective, Closed: true, Counts: map[string]int{}, Entries: []ScopeEntry{}}
	for _, path := range ordered {
		b, c, t, p := trees[0][path], trees[1][path], trees[2][path], trees[3][path]
		class := classifyScope(b, c, t, p)
		if class == "unexplained_prospective_delta" {
			report.Closed = false
		}
		report.Counts[class]++
		report.Entries = append(report.Entries, ScopeEntry{Path: path, Class: class, BaseMode: b.Mode, BaseOID: b.OID, CandidateMode: c.Mode, CandidateOID: c.OID, TargetMode: t.Mode, TargetOID: t.OID, ProspectiveMode: p.Mode, ProspectiveOID: p.OID})
	}
	return report, nil
}

func readRefs(ctx context.Context, repository string, expected []RefExpectation) (map[string]string, error) {
	args := []string{"for-each-ref", "--format=%(refname)%00%(objectname)"}
	for _, ref := range expected {
		args = append(args, ref.Name)
	}
	output, err := gitContext(ctx, repository, args...)
	if err != nil {
		return nil, err
	}
	refs := make(map[string]string, len(expected))
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\x00")
		if len(fields) != 2 || !validFullRefName(fields[0]) || !validOID(fields[1]) {
			return nil, fmt.Errorf("git for-each-ref returned malformed bounded output")
		}
		refs[fields[0]] = fields[1]
	}
	return refs, nil
}

func readObjectTypes(ctx context.Context, repository string, expected []ObjectExpectation) (map[string]string, error) {
	var input strings.Builder
	for _, object := range expected {
		input.WriteString(object.OID)
		input.WriteByte('\n')
	}
	output, err := gitContextInput(ctx, repository, input.String(), "cat-file", "--batch-check")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(lines) != len(expected) {
		return nil, fmt.Errorf("git cat-file returned an incomplete type batch")
	}
	types := make(map[string]string, len(expected))
	for index, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == expected[index].OID && fields[1] == "missing" {
			continue
		}
		if len(fields) != 3 || fields[0] != expected[index].OID || !oneOf(fields[1], "commit", "tree", "blob", "tag") {
			return nil, fmt.Errorf("git cat-file returned a malformed type batch")
		}
		types[fields[0]] = fields[1]
	}
	return types, nil
}

func validateManifest(manifest ClosureManifest) error {
	if manifest.APIVersion != ClosureAPIVersion || manifest.Kind != ClosureKind {
		return fmt.Errorf("artifact closure manifest must be %s %s", ClosureAPIVersion, ClosureKind)
	}
	if len(manifest.Objects) == 0 || len(manifest.Objects) > 256 || len(manifest.Refs) == 0 || len(manifest.Refs) > 256 {
		return fmt.Errorf("artifact closure manifest requires 1..256 objects and refs")
	}
	refs := map[string]bool{}
	for _, ref := range manifest.Refs {
		if !validFullRefName(ref.Name) || len(ref.Name) > 1024 || refs[ref.Name] || !validOID(ref.OID) || (ref.Peeled != "" && !validOID(ref.Peeled)) {
			return fmt.Errorf("artifact closure refs must be unique full refs with exact object IDs")
		}
		refs[ref.Name] = true
	}
	objects := map[string]bool{}
	for _, object := range manifest.Objects {
		if object.Name == "" || len(object.Name) > 128 || !validOID(object.OID) || objects[object.OID] || !oneOf(object.Type, "commit", "tree", "tag") || len(object.RetainedBy) == 0 {
			return fmt.Errorf("artifact closure objects require unique IDs, bounded names, supported types and retaining refs")
		}
		if object.Type == "commit" && (!validOID(object.Tree) || len(object.Parents) > 64) {
			return fmt.Errorf("commit expectations require a tree and at most 64 ordered parents")
		}
		if object.Type != "commit" && (object.Tree != "" || len(object.Parents) != 0) {
			return fmt.Errorf("only commit expectations may declare a tree or parents")
		}
		for _, parent := range object.Parents {
			if !validOID(parent) {
				return fmt.Errorf("commit parent is not an exact object ID")
			}
		}
		seenRefs := map[string]bool{}
		for _, ref := range object.RetainedBy {
			if !refs[ref] || seenRefs[ref] {
				return fmt.Errorf("object %s has an unknown or duplicate retaining ref", object.Name)
			}
			seenRefs[ref] = true
		}
		objects[object.OID] = true
	}
	return nil
}

func classifyScope(base, candidate, target, prospective treeEntry) string {
	switch {
	case candidate == base && target == base && prospective == base:
		return "unchanged_or_equivalent"
	case candidate == base && target != base && prospective == target:
		return "target_history_only"
	case candidate != base && target == base && prospective == candidate:
		return "candidate_only"
	case candidate != base && target != base && prospective == candidate:
		return "candidate_result"
	case candidate != base && target != base && prospective == target:
		return "target_result"
	case candidate != base && target != base && candidate != target && prospective != candidate && prospective != target:
		return "conflict_resolution"
	default:
		return "unexplained_prospective_delta"
	}
}

func treeEntriesAtCommit(ctx context.Context, repository, commit string) (map[string]treeEntry, error) {
	value, err := gitContext(ctx, repository, "ls-tree", "-r", "-z", "--full-tree", commit)
	if err != nil {
		return nil, fmt.Errorf("inspect Git tree %s: %w", commit, err)
	}
	if value == "" {
		return map[string]treeEntry{}, nil
	}
	if !strings.HasSuffix(value, "\x00") {
		return nil, fmt.Errorf("git tree %s is not NUL terminated", commit)
	}
	entries := make(map[string]treeEntry)
	for _, record := range strings.Split(strings.TrimSuffix(value, "\x00"), "\x00") {
		header, path, ok := strings.Cut(record, "\t")
		fields := strings.Fields(header)
		if !ok || path == "" || len(fields) != 3 || len(fields[0]) != 6 || !validOID(fields[2]) {
			return nil, fmt.Errorf("git tree %s contains a malformed entry", commit)
		}
		if _, duplicate := entries[path]; duplicate {
			return nil, fmt.Errorf("git tree %s repeats path %q", commit, path)
		}
		entries[path] = treeEntry{Mode: fields[0], OID: fields[2]}
	}
	return entries, nil
}

type rawGitObject struct {
	OID  string
	Type string
	Data []byte
}

type rawObjectReader struct {
	ctx     context.Context
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	stderr  *limitedBuffer
	cache   map[string]rawGitObject
	commits map[string]rawCommit
	closed  bool
}

type rawCommit struct {
	Tree    string
	Parents []string
}

func newRawObjectReader(ctx context.Context, repository string) (*rawObjectReader, error) {
	command := gitCommand(ctx, repository, "cat-file", "--batch")
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &limitedBuffer{limit: 64 * 1024}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start git cat-file: %w", err)
	}
	return &rawObjectReader{ctx: ctx, command: command, stdin: stdin, stdout: bufio.NewReader(stdout), stderr: stderr, cache: map[string]rawGitObject{}, commits: map[string]rawCommit{}}, nil
}

func (reader *rawObjectReader) Read(oid string) (rawGitObject, error) {
	if object, ok := reader.cache[oid]; ok {
		return object, nil
	}
	if reader.closed || !validOID(oid) {
		return rawGitObject{}, fmt.Errorf("invalid raw Git object request")
	}
	if err := reader.ctx.Err(); err != nil {
		return rawGitObject{}, err
	}
	if _, err := io.WriteString(reader.stdin, oid+"\n"); err != nil {
		return rawGitObject{}, reader.commandError("request raw Git object", err)
	}
	header, err := reader.stdout.ReadString('\n')
	if err != nil {
		return rawGitObject{}, reader.commandError("read raw Git object header", err)
	}
	fields := strings.Fields(strings.TrimSuffix(header, "\n"))
	if len(fields) == 2 && fields[0] == oid && fields[1] == "missing" {
		return rawGitObject{}, fmt.Errorf("Git object %s is missing", oid)
	}
	if len(fields) != 3 || fields[0] != oid || !oneOf(fields[1], "commit", "tree", "blob", "tag") {
		return rawGitObject{}, fmt.Errorf("git cat-file returned a malformed raw object header")
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 || size > maxRawObjectBytes {
		return rawGitObject{}, fmt.Errorf("raw Git object %s exceeds the %d-byte evidence limit", oid, maxRawObjectBytes)
	}
	data := make([]byte, size+1)
	if _, err := io.ReadFull(reader.stdout, data); err != nil {
		return rawGitObject{}, reader.commandError("read raw Git object", err)
	}
	if data[len(data)-1] != '\n' {
		return rawGitObject{}, fmt.Errorf("git cat-file raw object is not newline terminated")
	}
	object := rawGitObject{OID: oid, Type: fields[1], Data: data[:len(data)-1]}
	reader.cache[oid] = object
	return object, nil
}

func (reader *rawObjectReader) Commit(oid string) (rawCommit, error) {
	if commit, ok := reader.commits[oid]; ok {
		return commit, nil
	}
	object, err := reader.Read(oid)
	if err != nil {
		return rawCommit{}, err
	}
	if object.Type != "commit" {
		return rawCommit{}, fmt.Errorf("Git object %s is not a commit", oid)
	}
	tree, parents, err := parseRawCommit(object.Data)
	if err != nil {
		return rawCommit{}, err
	}
	commit := rawCommit{Tree: tree, Parents: parents}
	reader.commits[oid] = commit
	delete(reader.cache, oid)
	return commit, nil
}

func (reader *rawObjectReader) commandError(action string, cause error) error {
	if err := reader.ctx.Err(); err != nil {
		return err
	}
	message := strings.TrimSpace(reader.stderr.String())
	if len(message) > 1024 {
		message = message[:1024]
	}
	if message == "" {
		return fmt.Errorf("%s: %w", action, cause)
	}
	return fmt.Errorf("%s: %s", action, message)
}

func (reader *rawObjectReader) Close() {
	if reader == nil || reader.closed {
		return
	}
	reader.closed = true
	_ = reader.stdin.Close()
	if reader.command.Process != nil {
		_ = reader.command.Process.Kill()
	}
	_ = reader.command.Wait()
}

func peelRawObject(reader *rawObjectReader, oid string) (string, string, error) {
	seen := map[string]bool{}
	for depth := 0; depth < 32; depth++ {
		if seen[oid] {
			return "", "", fmt.Errorf("raw Git tag chain contains a cycle")
		}
		seen[oid] = true
		object, err := reader.Read(oid)
		if err != nil {
			return "", "", err
		}
		if object.Type != "tag" {
			return oid, object.Type, nil
		}
		target, err := parseRawTagTarget(object.Data)
		if err != nil {
			return "", "", err
		}
		oid = target
	}
	return "", "", fmt.Errorf("raw Git tag chain exceeds 32 objects")
}

func parseRawTagTarget(data []byte) (string, error) {
	header, _, ok := bytes.Cut(data, []byte("\n\n"))
	if !ok {
		return "", fmt.Errorf("raw tag object has no header boundary")
	}
	var target string
	for _, line := range bytes.Split(header, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("object ")) {
			if target != "" {
				return "", fmt.Errorf("raw tag object repeats its target")
			}
			target = string(bytes.TrimPrefix(line, []byte("object ")))
		}
	}
	if !validOID(target) {
		return "", fmt.Errorf("raw tag object has an invalid target")
	}
	return target, nil
}

func parseRawCommit(data []byte) (string, []string, error) {
	header, _, ok := bytes.Cut(data, []byte("\n\n"))
	if !ok {
		return "", nil, fmt.Errorf("raw commit object has no header boundary")
	}
	var tree string
	parents := []string{}
	for _, line := range bytes.Split(header, []byte("\n")) {
		switch {
		case bytes.HasPrefix(line, []byte("tree ")):
			if tree != "" {
				return "", nil, fmt.Errorf("raw commit object repeats its tree")
			}
			tree = string(bytes.TrimPrefix(line, []byte("tree ")))
		case bytes.HasPrefix(line, []byte("parent ")):
			parent := string(bytes.TrimPrefix(line, []byte("parent ")))
			if !validOID(parent) {
				return "", nil, fmt.Errorf("raw commit object has an invalid parent")
			}
			parents = append(parents, parent)
		}
	}
	if !validOID(tree) {
		return "", nil, fmt.Errorf("raw commit object has an invalid tree")
	}
	return tree, parents, nil
}

func rawCommitClosure(reader *rawObjectReader, target string) (map[string]bool, error) {
	result := map[string]bool{}
	if target == "" {
		return result, nil
	}
	queue := []string{target}
	for len(queue) > 0 {
		if err := reader.ctx.Err(); err != nil {
			return nil, err
		}
		current := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if result[current] {
			continue
		}
		result[current] = true
		commit, err := reader.Commit(current)
		if err != nil {
			return nil, err
		}
		queue = append(queue, commit.Parents...)
	}
	return result, nil
}

func validFullRefName(value string) bool {
	if !strings.HasPrefix(value, "refs/") || strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.Contains(value, "//") {
		return false
	}
	for _, forbidden := range []string{" ", "~", "^", ":", "?", "*", "[", "\\"} {
		if strings.Contains(value, forbidden) {
			return false
		}
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func gitContext(ctx context.Context, repository string, args ...string) (string, error) {
	return gitContextInput(ctx, repository, "", args...)
}

func gitContextInput(ctx context.Context, repository, input string, args ...string) (string, error) {
	command := gitCommand(ctx, repository, args...)
	if input != "" {
		command.Stdin = strings.NewReader(input)
	}
	stdout := limitedBuffer{limit: maxGitOutputBytes}
	stderr := limitedBuffer{limit: 64 * 1024}
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if stdout.exceeded || stderr.exceeded {
			return "", fmt.Errorf("git %s output exceeds its bounded limit", args[0])
		}
		message := strings.TrimSpace(stderr.String())
		if len(message) > 1024 {
			message = message[:1024]
		}
		return "", fmt.Errorf("git %s: %s", args[0], message)
	}
	return stdout.String(), nil
}

func gitCommand(ctx context.Context, repository string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, "git", append([]string{"--no-replace-objects", "-C", repository}, args...)...)
	environment := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(name), "GIT_") || strings.EqualFold(name, "LC_ALL") {
			continue
		}
		environment = append(environment, entry)
	}
	command.Env = append(environment, "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1", "LC_ALL=C")
	return command
}

func canonicalRepository(repository string) (string, error) {
	if strings.TrimSpace(repository) == "" {
		return "", fmt.Errorf("Git repository path is empty")
	}
	absolute, err := filepath.Abs(repository)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("Git repository path is not a directory")
	}
	return resolved, nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	if len(data) > buffer.limit-buffer.Len() {
		buffer.exceeded = true
		return 0, fmt.Errorf("output exceeds %d bytes", buffer.limit)
	}
	return buffer.Buffer.Write(data)
}

func validOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
