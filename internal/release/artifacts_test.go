package release

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	testVersion = "0.16.0"
	testCommit  = "0123456789abcdef0123456789abcdef01234567"
	testEpoch   = int64(1786665600)
)

func TestReleaseManifestAndVerificationCloseTheArtifactSet(t *testing.T) {
	root := createReleaseFixture(t)
	manifest, err := Generate(root, testVersion, "v"+testVersion, testCommit, testEpoch)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := MarshalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	validatePublishedSchema(t, "release-manifest-v1beta1.schema.json", raw)
	if err := WriteManifest(filepath.Join(root, ManifestFileName), raw); err != nil {
		t.Fatal(err)
	}
	report := Verify(root)
	if !report.Verified || report.Version != testVersion || report.Tag != "v"+testVersion || report.Commit != testCommit || report.Artifacts != 12 || report.Archives != 6 || report.SBOMs != 6 {
		t.Fatalf("unexpected release verification: %+v", report)
	}
	for _, check := range report.Checks {
		if check.Status != "pass" {
			t.Fatalf("release check did not pass: %+v", check)
		}
	}
	reportRaw, _ := json.Marshal(report)
	validatePublishedSchema(t, "release-verification-v1alpha1.schema.json", reportRaw)
	if err := WriteManifest(filepath.Join(root, ManifestFileName), raw); err == nil {
		t.Fatal("release manifest overwrite was accepted")
	}
}

func TestReleaseVerificationDetectsMutationAndStillMatchesSchema(t *testing.T) {
	root := createReleaseFixture(t)
	writeGeneratedManifest(t, root)
	mutated := filepath.Join(root, archiveFileName(targets[0]))
	if err := os.WriteFile(mutated, []byte("mutated after manifest"), 0o644); err != nil {
		t.Fatal(err)
	}
	report := Verify(root)
	if report.Verified || checkStatus(report, "artifacts") != "fail" || checkStatus(report, "archives") != "fail" {
		t.Fatalf("artifact mutation was not detected: %+v", report)
	}
	raw, _ := json.Marshal(report)
	validatePublishedSchema(t, "release-verification-v1alpha1.schema.json", raw)
}

func TestReleaseVerificationRejectsDuplicateManifestKeysAndExtraFiles(t *testing.T) {
	root := createReleaseFixture(t)
	writeGeneratedManifest(t, root)
	manifestPath := filepath.Join(root, ManifestFileName)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	duplicated := strings.Replace(string(raw), `"kind": "ReleaseManifest"`, `"kind": "ReleaseManifest", "kind": "ReleaseManifest"`, 1)
	if err := os.WriteFile(manifestPath, []byte(duplicated), 0o644); err != nil {
		t.Fatal(err)
	}
	if report := Verify(root); report.Verified || checkStatus(report, "manifest") != "fail" {
		t.Fatalf("duplicate manifest key was accepted: %+v", report)
	}

	root = createReleaseFixture(t)
	writeGeneratedManifest(t, root)
	if err := os.WriteFile(filepath.Join(root, "unexpected.txt"), []byte("unexpected"), 0o644); err != nil {
		t.Fatal(err)
	}
	if report := Verify(root); report.Verified || checkStatus(report, "file-set") != "fail" {
		t.Fatalf("extra release file was accepted: %+v", report)
	}
}

func TestManifestGenerationRejectsUnsafeArchivesAndIncompleteSBOMs(t *testing.T) {
	root := createReleaseFixture(t)
	writeTar(t, filepath.Join(root, archiveFileName(targets[0])), map[string][]byte{"../escape": []byte("escape"), "LICENSE": []byte("license"), "README.md": []byte("readme")})
	writeChecksums(t, root)
	if _, err := Generate(root, testVersion, "v"+testVersion, testCommit, testEpoch); err == nil {
		t.Fatal("path-traversing archive passed generation")
	}

	root = createReleaseFixture(t)
	invalidSBOM := []byte(`{"spdxVersion":"SPDX-2.3","dataLicense":"CC0-1.0","SPDXID":"SPDXRef-DOCUMENT","name":"dagrail","documentNamespace":"https://example.invalid/spdx","creationInfo":{"created":"2026-08-14T00:00:00Z","creators":["Tool: test"]},"packages":[]}`)
	if err := os.WriteFile(filepath.Join(root, sbomFileName(targets[0])), invalidSBOM, 0o644); err != nil {
		t.Fatal(err)
	}
	writeChecksums(t, root)
	if _, err := Generate(root, testVersion, "v"+testVersion, testCommit, testEpoch); err == nil {
		t.Fatal("empty SPDX package inventory passed generation")
	}
}

func TestManifestIdentityAndSortedChecksumsFailClosed(t *testing.T) {
	root := createReleaseFixture(t)
	if _, err := Generate(root, "01.16.0", "v01.16.0", testCommit, testEpoch); err == nil {
		t.Fatal("non-canonical SemVer passed")
	}
	if _, err := Generate(root, testVersion, "v0.15.0", testCommit, testEpoch); err == nil {
		t.Fatal("mismatched tag passed")
	}
	raw, err := os.ReadFile(filepath.Join(root, ChecksumsFileName))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	lines[0], lines[1] = lines[1], lines[0]
	if err := os.WriteFile(filepath.Join(root, ChecksumsFileName), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(root, testVersion, "v"+testVersion, testCommit, testEpoch); err == nil {
		t.Fatal("unsorted checksum manifest passed")
	}
}

func TestReleaseMetadataEpochAndSymlinkPayloadFailClosed(t *testing.T) {
	root := createReleaseFixture(t)
	if _, err := Generate(root, testVersion, "v"+testVersion, testCommit, testEpoch+2); err == nil {
		t.Fatal("archive metadata unrelated to the declared source epoch passed")
	}

	root = createReleaseFixture(t)
	target := filepath.Join(root, sbomFileName(targets[0]))
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, sbomFileName(targets[1])), target); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Generate(root, testVersion, "v"+testVersion, testCommit, testEpoch); err == nil {
		t.Fatal("symlinked release payload passed")
	}
}

func TestSystemZipMatchesReleaseMetadataContract(t *testing.T) {
	zipCommand, err := exec.LookPath("zip")
	if err != nil {
		t.Skip("system zip unavailable")
	}
	root := t.TempDir()
	for name, mode := range map[string]os.FileMode{"LICENSE": 0o644, "README.md": 0o644, "dagrail.exe": 0o755} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(name), mode); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(testEpoch, 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(root, "system.zip")
	command := exec.Command(zipCommand, "-X", "-q", archive, "LICENSE", "README.md", "dagrail.exe")
	command.Dir = root
	command.Env = append(os.Environ(), "TZ=UTC")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("system zip: %v %s", err, output)
	}
	if err := verifyZip(archive, map[string]struct{}{"LICENSE": {}, "README.md": {}, "dagrail.exe": {}}, testEpoch); err != nil {
		t.Fatalf("release workflow ZIP does not satisfy verifier: %v", err)
	}
}

func TestReleaseFailureReportRemainsSchemaValid(t *testing.T) {
	report := Verify(t.TempDir())
	if report.Verified || checkStatus(report, "manifest") != "fail" {
		t.Fatalf("empty directory unexpectedly verified: %+v", report)
	}
	raw, _ := json.Marshal(report)
	validatePublishedSchema(t, "release-verification-v1alpha1.schema.json", raw)
}

func FuzzReleaseMetadataInputs(f *testing.F) {
	valid := Manifest{
		APIVersion: ManifestAPIVersion, Kind: "ReleaseManifest", Version: testVersion,
		Tag: "v" + testVersion, Commit: testCommit, SourceDateEpoch: testEpoch,
		Checksums: FileEvidence{Name: ChecksumsFileName, SHA256: "sha256:" + strings.Repeat("0", 64), Bytes: 1},
	}
	for _, target := range targets {
		valid.Artifacts = append(valid.Artifacts,
			Artifact{Name: archiveFileName(target), Kind: "binary-archive", OS: target.OS, Arch: target.Arch, Format: target.Format, SHA256: "sha256:" + strings.Repeat("1", 64), Bytes: 1},
			Artifact{Name: sbomFileName(target), Kind: "spdx-sbom", OS: target.OS, Arch: target.Arch, Format: "spdx-json", SHA256: "sha256:" + strings.Repeat("2", 64), Bytes: 1},
		)
	}
	sort.Slice(valid.Artifacts, func(i, j int) bool { return valid.Artifacts[i].Name < valid.Artifacts[j].Name })
	validRaw, _ := json.Marshal(valid)
	checksumLines := []string{}
	for _, name := range ExpectedPayloadNames() {
		checksumLines = append(checksumLines, strings.Repeat("0", 64)+"  "+name)
	}
	f.Add(validRaw)
	f.Add([]byte(strings.Join(checksumLines, "\n") + "\n"))
	f.Add([]byte(`{"kind":"ReleaseManifest","kind":"duplicate"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > maxManifestBytes {
			t.Skip()
		}
		_, _ = decodeManifest(raw)
		_, _ = parseChecksums(raw)
	})
}

func createReleaseFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, target := range targets {
		binary := "dagrail"
		if target.OS == "windows" {
			binary = "dagrail.exe"
		}
		contents := map[string][]byte{"LICENSE": []byte("Apache-2.0\n"), "README.md": []byte("# DAGrail\n"), binary: []byte("static-binary-" + target.OS + "-" + target.Arch)}
		archive := filepath.Join(root, archiveFileName(target))
		if target.Format == "zip" {
			writeZip(t, archive, contents)
		} else {
			writeTar(t, archive, contents)
		}
		sbom := map[string]any{
			"spdxVersion": "SPDX-2.3", "dataLicense": "CC0-1.0", "SPDXID": "SPDXRef-DOCUMENT",
			"name":              "dagrail-" + target.OS + "-" + target.Arch,
			"documentNamespace": "https://dagrail.dev/spdx/" + target.OS + "/" + target.Arch,
			"creationInfo":      map[string]any{"created": "2026-08-14T00:00:00Z", "creators": []string{"Tool: fixture"}},
			"packages":          []map[string]any{{"SPDXID": "SPDXRef-Package-dagrail", "name": "dagrail", "versionInfo": testVersion}},
		}
		raw, _ := json.Marshal(sbom)
		if err := os.WriteFile(filepath.Join(root, sbomFileName(target)), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeChecksums(t, root)
	return root
}

func writeGeneratedManifest(t *testing.T, root string) {
	t.Helper()
	manifest, err := Generate(root, testVersion, "v"+testVersion, testCommit, testEpoch)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := MarshalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(filepath.Join(root, ManifestFileName), raw); err != nil {
		t.Fatal(err)
	}
}

func writeTar(t *testing.T, path string, contents map[string][]byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	names := sortedKeys(contents)
	for _, name := range names {
		raw := contents[name]
		mode := int64(0o644)
		if name == "dagrail" {
			mode = 0o755
		}
		header := &tar.Header{Name: name, Mode: mode, Size: int64(len(raw)), ModTime: time.Unix(testEpoch, 0).UTC(), Typeflag: tar.TypeReg}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeZip(t *testing.T, path string, contents map[string][]byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, name := range sortedKeys(contents) {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetModTime(time.Unix(testEpoch, 0).UTC())
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(contents[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeChecksums(t *testing.T, root string) {
	t.Helper()
	lines := []string{}
	for _, name := range ExpectedPayloadNames() {
		file, err := os.Open(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, fmt.Sprintf("%s  %s", hex.EncodeToString(hash.Sum(nil)), name))
	}
	if err := os.WriteFile(filepath.Join(root, ChecksumsFileName), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sortedKeys(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func checkStatus(report Verification, id string) string {
	for _, check := range report.Checks {
		if check.ID == id {
			return check.Status
		}
	}
	return ""
}

func validatePublishedSchema(t *testing.T, name string, raw []byte) {
	t.Helper()
	schemaRaw, err := os.ReadFile(filepath.Join("..", "..", "schemas", name))
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(schemaRaw, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	resource := "urn:dagrail:" + name
	if err := compiler.AddResource(resource, document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(resource)
	if err != nil {
		t.Fatal(err)
	}
	var instance any
	if err := json.Unmarshal(raw, &instance); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Fatalf("%s does not match its schema: %v", name, err)
	}
}
