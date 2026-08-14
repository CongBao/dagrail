package release

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/CongBao/dagrail/internal/domain"
)

const (
	ManifestAPIVersion     = "dagrail.io/release-manifest/v1beta1"
	VerificationAPIVersion = "dagrail.io/release-verification/v1alpha1"

	ManifestFileName  = "release-manifest.json"
	ChecksumsFileName = "checksums.txt"

	maxManifestBytes  = 1 << 20
	maxChecksumsBytes = 1 << 20
	maxArchiveBytes   = 512 << 20
	maxSBOMBytes      = 64 << 20
	maxArchiveContent = 320 << 20
)

type target struct {
	OS     string
	Arch   string
	Format string
}

var targets = []target{
	{OS: "darwin", Arch: "amd64", Format: "tar.gz"},
	{OS: "darwin", Arch: "arm64", Format: "tar.gz"},
	{OS: "linux", Arch: "amd64", Format: "tar.gz"},
	{OS: "linux", Arch: "arm64", Format: "tar.gz"},
	{OS: "windows", Arch: "amd64", Format: "zip"},
	{OS: "windows", Arch: "arm64", Format: "zip"},
}

type FileEvidence struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type Artifact struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Format string `json:"format"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type Manifest struct {
	APIVersion      string       `json:"apiVersion"`
	Kind            string       `json:"kind"`
	Version         string       `json:"version"`
	Tag             string       `json:"tag"`
	Commit          string       `json:"commit"`
	SourceDateEpoch int64        `json:"sourceDateEpoch"`
	Checksums       FileEvidence `json:"checksums"`
	Artifacts       []Artifact   `json:"artifacts"`
}

type Check struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Code   string `json:"code"`
}

type Verification struct {
	APIVersion     string  `json:"apiVersion"`
	Kind           string  `json:"kind"`
	Verified       bool    `json:"verified"`
	Version        string  `json:"version,omitempty"`
	Tag            string  `json:"tag,omitempty"`
	Commit         string  `json:"commit,omitempty"`
	ManifestSHA256 string  `json:"manifestSha256,omitempty"`
	Artifacts      int     `json:"artifacts"`
	Archives       int     `json:"archives"`
	SBOMs          int     `json:"sboms"`
	Checks         []Check `json:"checks"`
}

var (
	semverPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	commitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
	digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	checksumLine  = regexp.MustCompile(`^([a-f0-9]{64})  ([A-Za-z0-9_.-]+)$`)
)

func archiveFileName(target target) string {
	return fmt.Sprintf("dagrail_%s_%s.%s", target.OS, target.Arch, target.Format)
}

func sbomFileName(target target) string {
	return fmt.Sprintf("dagrail_%s_%s.spdx.json", target.OS, target.Arch)
}

func ExpectedPayloadNames() []string {
	names := make([]string, 0, len(targets)*2)
	for _, target := range targets {
		names = append(names, archiveFileName(target), sbomFileName(target))
	}
	sort.Strings(names)
	return names
}

func Generate(directory, releaseVersion, tag, commit string, sourceDateEpoch int64) (Manifest, error) {
	manifest := Manifest{
		APIVersion:      ManifestAPIVersion,
		Kind:            "ReleaseManifest",
		Version:         releaseVersion,
		Tag:             tag,
		Commit:          commit,
		SourceDateEpoch: sourceDateEpoch,
		Artifacts:       []Artifact{},
	}
	if err := validateIdentity(manifest); err != nil {
		return Manifest{}, err
	}
	root, err := resolveDirectory(directory)
	if err != nil {
		return Manifest{}, err
	}
	if err := validateDirectorySet(root, false); err != nil {
		return Manifest{}, err
	}
	checksumsRaw, checksumsSize, err := readBoundedRegular(root, ChecksumsFileName, maxChecksumsBytes)
	if err != nil {
		return Manifest{}, err
	}
	declared, err := parseChecksums(checksumsRaw)
	if err != nil {
		return Manifest{}, err
	}
	manifest.Checksums = FileEvidence{Name: ChecksumsFileName, SHA256: digestBytes(checksumsRaw), Bytes: checksumsSize}
	for _, target := range targets {
		archiveName := archiveFileName(target)
		archiveDigest, archiveSize, err := verifyFileDigest(root, archiveName, maxArchiveBytes, declared)
		if err != nil {
			return Manifest{}, err
		}
		if err := verifyArchive(filepath.Join(root, archiveName), target, sourceDateEpoch); err != nil {
			return Manifest{}, err
		}
		manifest.Artifacts = append(manifest.Artifacts, Artifact{Name: archiveName, Kind: "binary-archive", OS: target.OS, Arch: target.Arch, Format: target.Format, SHA256: archiveDigest, Bytes: archiveSize})

		sbomName := sbomFileName(target)
		sbomDigest, sbomSize, err := verifyFileDigest(root, sbomName, maxSBOMBytes, declared)
		if err != nil {
			return Manifest{}, err
		}
		if err := verifySPDX(filepath.Join(root, sbomName)); err != nil {
			return Manifest{}, err
		}
		manifest.Artifacts = append(manifest.Artifacts, Artifact{Name: sbomName, Kind: "spdx-sbom", OS: target.OS, Arch: target.Arch, Format: "spdx-json", SHA256: sbomDigest, Bytes: sbomSize})
	}
	sort.Slice(manifest.Artifacts, func(i, j int) bool { return manifest.Artifacts[i].Name < manifest.Artifacts[j].Name })
	return manifest, nil
}

func Verify(directory string) Verification {
	report := newVerification()
	root, err := resolveDirectory(directory)
	if err != nil {
		failAll(&report, "directory_invalid")
		return report
	}
	manifestRaw, _, err := readBoundedRegular(root, ManifestFileName, maxManifestBytes)
	if err != nil {
		failAll(&report, "manifest_missing_or_invalid")
		return report
	}
	report.ManifestSHA256 = digestBytes(manifestRaw)
	manifest, err := decodeManifest(manifestRaw)
	if err != nil {
		failAll(&report, "manifest_schema_invalid")
		return report
	}
	report.Version, report.Tag, report.Commit = manifest.Version, manifest.Tag, manifest.Commit
	setCheck(&report, "manifest", true, "manifest_valid", "manifest_schema_invalid")

	setOK := validateDirectorySet(root, true) == nil
	setCheck(&report, "file-set", setOK, "file_set_closed", "file_set_invalid")
	if !setOK {
		markRemainingNotRun(&report, "file_set_invalid")
		return report
	}
	checksumsRaw, checksumsSize, checksumsErr := readBoundedRegular(root, ChecksumsFileName, maxChecksumsBytes)
	declared, parseErr := parseChecksums(checksumsRaw)
	checksumsOK := checksumsErr == nil && parseErr == nil && manifest.Checksums.Name == ChecksumsFileName && manifest.Checksums.Bytes == checksumsSize && manifest.Checksums.SHA256 == digestBytes(checksumsRaw)
	setCheck(&report, "checksums", checksumsOK, "checksums_valid", "checksums_invalid")
	if !checksumsOK {
		markRemainingNotRun(&report, "checksums_invalid")
		return report
	}

	artifactsByName := map[string]Artifact{}
	for _, artifact := range manifest.Artifacts {
		if _, exists := artifactsByName[artifact.Name]; exists {
			setCheck(&report, "artifacts", false, "artifacts_valid", "artifact_manifest_invalid")
			markRemainingNotRun(&report, "artifact_manifest_invalid")
			return report
		}
		artifactsByName[artifact.Name] = artifact
	}
	artifactsOK, archivesOK, sbomsOK := true, true, true
	for _, target := range targets {
		archiveName := archiveFileName(target)
		archive := artifactsByName[archiveName]
		digest, size, digestErr := verifyFileDigest(root, archiveName, maxArchiveBytes, declared)
		if digestErr != nil || !matchesArtifact(archive, archiveName, "binary-archive", target.OS, target.Arch, target.Format, digest, size) {
			artifactsOK, archivesOK = false, false
		} else if err := verifyArchive(filepath.Join(root, archiveName), target, manifest.SourceDateEpoch); err != nil {
			archivesOK = false
		}
		sbomName := sbomFileName(target)
		sbom := artifactsByName[sbomName]
		digest, size, digestErr = verifyFileDigest(root, sbomName, maxSBOMBytes, declared)
		if digestErr != nil || !matchesArtifact(sbom, sbomName, "spdx-sbom", target.OS, target.Arch, "spdx-json", digest, size) {
			artifactsOK, sbomsOK = false, false
		} else if err := verifySPDX(filepath.Join(root, sbomName)); err != nil {
			sbomsOK = false
		}
	}
	if len(artifactsByName) != len(targets)*2 {
		artifactsOK = false
	}
	setCheck(&report, "artifacts", artifactsOK, "artifacts_valid", "artifact_manifest_invalid")
	setCheck(&report, "archives", archivesOK, "archives_closed", "archive_invalid")
	setCheck(&report, "sboms", sbomsOK, "sboms_valid", "sbom_invalid")
	report.Artifacts = len(manifest.Artifacts)
	report.Archives = len(targets)
	report.SBOMs = len(targets)
	report.Verified = allChecksPass(report.Checks)
	return report
}

func MarshalManifest(manifest Manifest) ([]byte, error) {
	if err := validateManifestStructure(manifest); err != nil {
		return nil, err
	}
	return json.MarshalIndent(manifest, "", "  ")
}

func WriteManifest(path string, raw []byte) error {
	if filepath.Base(path) != ManifestFileName || len(raw) == 0 || len(raw) > maxManifestBytes {
		return fmt.Errorf("release manifest output is invalid")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("release manifest output already exists or is unavailable")
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("write release manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync release manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close release manifest: %w", err)
	}
	ok = true
	return nil
}

func newVerification() Verification {
	checks := []Check{}
	for _, id := range []string{"manifest", "file-set", "checksums", "artifacts", "archives", "sboms"} {
		checks = append(checks, Check{ID: id, Status: "not_run", Code: "prerequisite_not_met"})
	}
	return Verification{APIVersion: VerificationAPIVersion, Kind: "ReleaseVerification", Checks: checks}
}

func failAll(report *Verification, code string) {
	for index := range report.Checks {
		report.Checks[index].Status = "not_run"
		report.Checks[index].Code = "prerequisite_not_met"
	}
	report.Checks[0] = Check{ID: "manifest", Status: "fail", Code: code}
}

func setCheck(report *Verification, id string, passed bool, success, failure string) {
	for index := range report.Checks {
		if report.Checks[index].ID != id {
			continue
		}
		report.Checks[index].Status = "pass"
		report.Checks[index].Code = success
		if !passed {
			report.Checks[index].Status = "fail"
			report.Checks[index].Code = failure
		}
		return
	}
}

func markRemainingNotRun(report *Verification, code string) {
	for index := range report.Checks {
		if report.Checks[index].Status == "not_run" {
			report.Checks[index].Code = code
		}
	}
}

func allChecksPass(checks []Check) bool {
	for _, check := range checks {
		if check.Status != "pass" {
			return false
		}
	}
	return true
}

func validateIdentity(manifest Manifest) error {
	if manifest.APIVersion != ManifestAPIVersion || manifest.Kind != "ReleaseManifest" || !semverPattern.MatchString(manifest.Version) || manifest.Tag != "v"+manifest.Version || !commitPattern.MatchString(manifest.Commit) || manifest.SourceDateEpoch <= 0 || manifest.SourceDateEpoch > 9_007_199_254_740_991 {
		return fmt.Errorf("release identity is invalid")
	}
	if time.Unix(manifest.SourceDateEpoch, 0).UTC().Year() < 2000 || time.Unix(manifest.SourceDateEpoch, 0).UTC().Year() > 3000 {
		return fmt.Errorf("release source date epoch is outside the supported range")
	}
	return nil
}

func decodeManifest(raw []byte) (Manifest, error) {
	if err := domain.ValidateAuthorityJSON(raw); err != nil {
		return Manifest{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Manifest{}, fmt.Errorf("release manifest has trailing content")
	}
	if err := validateManifestStructure(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateManifestStructure(manifest Manifest) error {
	if err := validateIdentity(manifest); err != nil {
		return err
	}
	if manifest.Checksums.Name != ChecksumsFileName || !digestPattern.MatchString(manifest.Checksums.SHA256) || manifest.Checksums.Bytes <= 0 || manifest.Checksums.Bytes > maxChecksumsBytes || len(manifest.Artifacts) != len(targets)*2 {
		return fmt.Errorf("release manifest evidence is invalid")
	}
	expected := make([]Artifact, 0, len(targets)*2)
	for _, target := range targets {
		expected = append(expected,
			Artifact{Name: archiveFileName(target), Kind: "binary-archive", OS: target.OS, Arch: target.Arch, Format: target.Format},
			Artifact{Name: sbomFileName(target), Kind: "spdx-sbom", OS: target.OS, Arch: target.Arch, Format: "spdx-json"},
		)
	}
	sort.Slice(expected, func(i, j int) bool { return expected[i].Name < expected[j].Name })
	for index, artifact := range manifest.Artifacts {
		identity := expected[index]
		limit := int64(maxArchiveBytes)
		if identity.Kind == "spdx-sbom" {
			limit = maxSBOMBytes
		}
		if artifact.Name != identity.Name || artifact.Kind != identity.Kind || artifact.OS != identity.OS || artifact.Arch != identity.Arch || artifact.Format != identity.Format || !digestPattern.MatchString(artifact.SHA256) || artifact.Bytes <= 0 || artifact.Bytes > limit {
			return fmt.Errorf("release manifest artifact evidence is invalid")
		}
	}
	return nil
}

func resolveDirectory(directory string) (string, error) {
	root, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("release directory is invalid")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("release directory is invalid")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("release directory is invalid")
	}
	return root, nil
}

func validateDirectorySet(root string, withManifest bool) error {
	want := map[string]struct{}{ChecksumsFileName: {}}
	for _, name := range ExpectedPayloadNames() {
		want[name] = struct{}{}
	}
	if withManifest {
		want[ManifestFileName] = struct{}{}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != len(want) {
		return fmt.Errorf("release directory file set is invalid")
	}
	for _, entry := range entries {
		if _, ok := want[entry.Name()]; !ok || entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("release directory file set is invalid")
		}
	}
	return nil
}

func readBoundedRegular(root, name string, limit int64) ([]byte, int64, error) {
	if filepath.Base(name) != name {
		return nil, 0, fmt.Errorf("release filename is invalid")
	}
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > limit {
		return nil, 0, fmt.Errorf("release file is missing or invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("release file is unavailable")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, 0, fmt.Errorf("release file exceeds its limit")
	}
	return raw, int64(len(raw)), nil
}

func verifyFileDigest(root, name string, limit int64, declared map[string]string) (string, int64, error) {
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > limit {
		return "", 0, fmt.Errorf("release payload is missing or invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("release payload is unavailable")
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, limit+1))
	if err != nil || written != info.Size() || written > limit {
		return "", 0, fmt.Errorf("release payload cannot be hashed safely")
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if declared[name] != digest {
		return "", 0, fmt.Errorf("release payload checksum mismatch")
	}
	return "sha256:" + digest, written, nil
}

func parseChecksums(raw []byte) (map[string]string, error) {
	if len(raw) == 0 || raw[len(raw)-1] != '\n' || bytes.Contains(raw, []byte{'\r'}) {
		return nil, fmt.Errorf("checksum manifest must use canonical LF lines")
	}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	scanner.Buffer(make([]byte, 1024), int(maxChecksumsBytes))
	declared := map[string]string{}
	observedOrder := []string{}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		match := checksumLine.FindStringSubmatch(line)
		if len(match) != 3 {
			return nil, fmt.Errorf("checksum manifest line is invalid")
		}
		if _, exists := declared[match[2]]; exists {
			return nil, fmt.Errorf("checksum manifest contains a duplicate filename")
		}
		declared[match[2]] = match[1]
		observedOrder = append(observedOrder, match[2])
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("checksum manifest is invalid")
	}
	want := ExpectedPayloadNames()
	if len(observedOrder) != len(want) {
		return nil, fmt.Errorf("checksum manifest is incomplete")
	}
	for index := range want {
		if observedOrder[index] != want[index] {
			return nil, fmt.Errorf("checksum manifest is not sorted or contains an unexpected file")
		}
	}
	return declared, nil
}

func verifyArchive(path string, target target, sourceDateEpoch int64) error {
	wantedBinary := "dagrail"
	if target.OS == "windows" {
		wantedBinary = "dagrail.exe"
	}
	wanted := map[string]struct{}{"LICENSE": {}, "README.md": {}, wantedBinary: {}}
	if target.Format == "zip" {
		return verifyZip(path, wanted, sourceDateEpoch)
	}
	if target.Format == "tar.gz" {
		return verifyTarGzip(path, wanted, wantedBinary, sourceDateEpoch)
	}
	return fmt.Errorf("release archive format is unsupported")
}

func verifyTarGzip(path string, wanted map[string]struct{}, wantedBinary string, sourceDateEpoch int64) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("release archive is unavailable")
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("release archive gzip envelope is invalid")
	}
	defer gzipReader.Close()
	if !gzipReader.ModTime.IsZero() {
		return fmt.Errorf("release gzip timestamp is not deterministic")
	}
	seen := map[string]struct{}{}
	var total int64
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil || header == nil || header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > maxArchiveContent {
			return fmt.Errorf("release archive entry is invalid")
		}
		if _, ok := wanted[header.Name]; !ok {
			return fmt.Errorf("release archive contains an unexpected path")
		}
		if _, duplicate := seen[header.Name]; duplicate {
			return fmt.Errorf("release archive contains a duplicate path")
		}
		seen[header.Name] = struct{}{}
		if header.ModTime.Unix() != sourceDateEpoch || header.Uid != 0 || header.Gid != 0 || (header.Name == wantedBinary && header.Mode&0o111 == 0) {
			return fmt.Errorf("release archive metadata is not deterministic")
		}
		total += header.Size
		if total > maxArchiveContent {
			return fmt.Errorf("release archive expands beyond its limit")
		}
	}
	if len(seen) != len(wanted) {
		return fmt.Errorf("release archive is incomplete")
	}
	return nil
}

func verifyZip(path string, wanted map[string]struct{}, sourceDateEpoch int64) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("release zip is invalid")
	}
	defer reader.Close()
	seen := map[string]struct{}{}
	var total uint64
	for _, file := range reader.File {
		if file.FileInfo().IsDir() || file.Mode()&os.ModeSymlink != 0 || file.UncompressedSize64 == 0 || file.UncompressedSize64 > maxArchiveContent {
			return fmt.Errorf("release zip entry is invalid")
		}
		if _, ok := wanted[file.Name]; !ok {
			return fmt.Errorf("release zip contains an unexpected path")
		}
		if _, duplicate := seen[file.Name]; duplicate {
			return fmt.Errorf("release zip contains a duplicate path")
		}
		seen[file.Name] = struct{}{}
		delta := file.Modified.Unix() - sourceDateEpoch
		if delta < -1 || delta > 1 {
			return fmt.Errorf("release zip timestamp is not deterministic")
		}
		total += file.UncompressedSize64
		if total > maxArchiveContent {
			return fmt.Errorf("release zip expands beyond its limit")
		}
		entry, err := file.Open()
		if err != nil {
			return fmt.Errorf("release zip entry cannot be opened")
		}
		read, copyErr := io.Copy(io.Discard, io.LimitReader(entry, maxArchiveContent+1))
		closeErr := entry.Close()
		if copyErr != nil || closeErr != nil || read != int64(file.UncompressedSize64) || read > maxArchiveContent {
			return fmt.Errorf("release zip entry content is invalid")
		}
	}
	if len(seen) != len(wanted) {
		return fmt.Errorf("release zip is incomplete")
	}
	return nil
}

func verifySPDX(path string) error {
	root, name := filepath.Dir(path), filepath.Base(path)
	raw, _, err := readBoundedRegular(root, name, maxSBOMBytes)
	if err != nil || !json.Valid(raw) {
		return fmt.Errorf("SPDX document is invalid")
	}
	if err := domain.ValidateAuthorityJSON(raw); err != nil {
		return fmt.Errorf("SPDX document is invalid")
	}
	var document struct {
		SPDXVersion       string `json:"spdxVersion"`
		DataLicense       string `json:"dataLicense"`
		SPDXID            string `json:"SPDXID"`
		Name              string `json:"name"`
		DocumentNamespace string `json:"documentNamespace"`
		CreationInfo      struct {
			Created  string   `json:"created"`
			Creators []string `json:"creators"`
		} `json:"creationInfo"`
		Packages []json.RawMessage `json:"packages"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("SPDX document is invalid")
	}
	_, timeErr := time.Parse(time.RFC3339, document.CreationInfo.Created)
	if (document.SPDXVersion != "SPDX-2.2" && document.SPDXVersion != "SPDX-2.3") || document.DataLicense != "CC0-1.0" || document.SPDXID != "SPDXRef-DOCUMENT" || strings.TrimSpace(document.Name) == "" || strings.TrimSpace(document.DocumentNamespace) == "" || timeErr != nil || len(document.CreationInfo.Creators) == 0 || len(document.Packages) == 0 {
		return fmt.Errorf("SPDX document is incomplete")
	}
	for _, creator := range document.CreationInfo.Creators {
		if strings.TrimSpace(creator) == "" {
			return fmt.Errorf("SPDX document creator is invalid")
		}
	}
	for _, rawPackage := range document.Packages {
		var value struct {
			SPDXID string `json:"SPDXID"`
			Name   string `json:"name"`
		}
		if err := json.Unmarshal(rawPackage, &value); err != nil || !strings.HasPrefix(value.SPDXID, "SPDXRef-") || strings.TrimSpace(value.Name) == "" {
			return fmt.Errorf("SPDX package inventory is invalid")
		}
	}
	return nil
}

func matchesArtifact(artifact Artifact, name, kind, targetOS, arch, format, digest string, size int64) bool {
	return artifact.Name == name && artifact.Kind == kind && artifact.OS == targetOS && artifact.Arch == arch && artifact.Format == format && artifact.SHA256 == digest && artifact.Bytes == size && digestPattern.MatchString(artifact.SHA256) && artifact.Bytes > 0
}

func digestBytes(raw []byte) string {
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}
