package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	rootbundle "github.com/CongBao/dagrail"
	"github.com/CongBao/dagrail/internal/project"
	"github.com/CongBao/dagrail/internal/version"
	"gopkg.in/yaml.v3"
)

const (
	pluginBundleDomain   = "dagrail-plugin-bundle-v1\x00"
	maxPluginBundleFile  = 1 * 1024 * 1024
	maxPluginBundleBytes = 8 * 1024 * 1024
)

type BundleResult struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
	Root    string `json:"root,omitempty"`
	Files   int    `json:"files"`
	Bytes   int    `json:"bytes"`
}

type bundleFile struct {
	Path string
	Data []byte
}

var pluginRuntimeStatus = RuntimeStatus

// LinkedPluginBundleStatus validates the exact embedded public file set without
// consulting or writing per-user runtime state.
func LinkedPluginBundleStatus() (BundleResult, error) {
	files, digest, total, err := linkedPluginBundle()
	if err != nil {
		return BundleResult{}, err
	}
	return BundleResult{Status: "linked", Version: version.Version, Digest: digest, Files: len(files), Bytes: total}, nil
}

func MaterializePluginBundle() (BundleResult, error) {
	files, digest, total, err := projectedPluginBundle()
	if err != nil {
		return BundleResult{}, err
	}
	dataRoot, err := runtimeDataRoot()
	if err != nil {
		return BundleResult{}, err
	}
	parent := filepath.Join(dataRoot, "plugin-bundles")
	target := filepath.Join(parent, version.Version+"-"+strings.TrimPrefix(digest, "sha256:"))
	result := BundleResult{Status: "materialized", Version: version.Version, Digest: digest, Root: target, Files: len(files), Bytes: total}
	if info, statErr := os.Lstat(target); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return BundleResult{}, fmt.Errorf("plugin bundle target must be a non-symlink directory")
		}
		if err := verifyMaterializedBundle(target, files); err != nil {
			return BundleResult{}, err
		}
		result.Status = "verified"
		return result, nil
	} else if !os.IsNotExist(statErr) {
		return BundleResult{}, statErr
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return BundleResult{}, err
	}
	temporary, err := os.MkdirTemp(parent, ".plugin-bundle-*")
	if err != nil {
		return BundleResult{}, err
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0o700); err != nil {
		return BundleResult{}, err
	}
	for _, file := range files {
		destination := filepath.Join(temporary, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return BundleResult{}, err
		}
		if err := os.WriteFile(destination, file.Data, 0o644); err != nil {
			return BundleResult{}, err
		}
	}
	if err := verifyMaterializedBundle(temporary, files); err != nil {
		return BundleResult{}, err
	}
	if err := project.PublishDirectoryExclusive(temporary, target); err != nil {
		if info, statErr := os.Lstat(target); statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return BundleResult{}, fmt.Errorf("publish plugin bundle: %w", err)
		}
		if err := verifyMaterializedBundle(target, files); err != nil {
			return BundleResult{}, err
		}
		result.Status = "verified"
		return result, nil
	}
	if err := syncDirectory(parent); err != nil {
		return BundleResult{}, err
	}
	return result, nil
}

// PlanPluginBundle computes the exact receipt-bound bundle identity and target
// without creating either the bundle directory or any of its files.
func PlanPluginBundle(runtimePath string) (BundleResult, error) {
	files, digest, total, err := projectedPluginBundleForRuntime(runtimePath)
	if err != nil {
		return BundleResult{}, err
	}
	dataRoot, err := runtimeDataRoot()
	if err != nil {
		return BundleResult{}, err
	}
	target := filepath.Join(dataRoot, "plugin-bundles", version.Version+"-"+strings.TrimPrefix(digest, "sha256:"))
	return BundleResult{Status: "planned", Version: version.Version, Digest: digest, Root: target, Files: len(files), Bytes: total}, nil
}

func PluginBundleStatus() (BundleResult, error) {
	files, digest, total, err := projectedPluginBundle()
	if err != nil {
		return BundleResult{}, err
	}
	dataRoot, err := runtimeDataRoot()
	if err != nil {
		return BundleResult{}, err
	}
	target := filepath.Join(dataRoot, "plugin-bundles", version.Version+"-"+strings.TrimPrefix(digest, "sha256:"))
	result := BundleResult{Status: "not_materialized", Version: version.Version, Digest: digest, Root: target, Files: len(files), Bytes: total}
	if _, err := os.Lstat(target); os.IsNotExist(err) {
		return result, nil
	} else if err != nil {
		return BundleResult{}, err
	}
	if err := verifyMaterializedBundle(target, files); err != nil {
		return BundleResult{}, err
	}
	result.Status = "verified"
	return result, nil
}

// projectedPluginBundle binds the Codex MCP launcher to the exact runtime
// artifact already authenticated by the local runtime receipt. The repository
// manifest stays portable; only the immutable per-user bundle receives an
// absolute launcher, so no host placeholder or PATH lookup can drift.
func projectedPluginBundle() ([]bundleFile, string, int, error) {
	runtimeStatus, err := pluginRuntimeStatus()
	if err != nil {
		return nil, "", 0, fmt.Errorf("bind plugin MCP launcher to verified runtime: %w", err)
	}
	return projectedPluginBundleForRuntime(runtimeStatus.RuntimePath)
}

func projectedPluginBundleForRuntime(runtimePath string) ([]bundleFile, string, int, error) {
	if !filepath.IsAbs(runtimePath) {
		return nil, "", 0, fmt.Errorf("bind plugin MCP launcher to an absolute runtime path")
	}
	files, _, _, err := linkedPluginBundle()
	if err != nil {
		return nil, "", 0, err
	}
	manifestPath := "plugins/dagrail/.codex-plugin/plugin.json"
	for index := range files {
		if files[index].Path != manifestPath {
			continue
		}
		var manifest map[string]any
		if err := json.Unmarshal(files[index].Data, &manifest); err != nil {
			return nil, "", 0, err
		}
		manifest["mcpServers"] = map[string]any{
			MCPServerName: map[string]any{"type": "stdio", "command": runtimePath, "args": []string{"mcp", "--stdio"}},
		}
		files[index].Data, err = json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return nil, "", 0, err
		}
		files[index].Data = append(files[index].Data, '\n')
		break
	}
	total := 0
	hash := sha256.New()
	_, _ = hash.Write([]byte(pluginBundleDomain))
	for _, file := range files {
		if len(file.Data) > maxPluginBundleFile || total > maxPluginBundleBytes-len(file.Data) {
			return nil, "", 0, fmt.Errorf("projected plugin bundle exceeds its public size limit")
		}
		total += len(file.Data)
		content := sha256.Sum256(file.Data)
		_, _ = hash.Write([]byte(file.Path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(content[:])
	}
	return files, "sha256:" + hex.EncodeToString(hash.Sum(nil)), total, nil
}

func linkedPluginBundle() ([]bundleFile, string, int, error) {
	files := []bundleFile{}
	total := 0
	err := fs.WalkDir(rootbundle.PluginFS, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !fs.ValidPath(path) || path == "." || strings.Contains(path, "\\") {
			return fmt.Errorf("invalid embedded plugin path")
		}
		data, err := fs.ReadFile(rootbundle.PluginFS, path)
		if err != nil {
			return err
		}
		if len(data) > maxPluginBundleFile || total > maxPluginBundleBytes-len(data) {
			return fmt.Errorf("embedded plugin bundle exceeds its public size limit")
		}
		total += len(data)
		files = append(files, bundleFile{Path: "plugins/dagrail/" + path, Data: data})
		return nil
	})
	if err != nil {
		return nil, "", 0, err
	}
	marketplaces, err := bundledMarketplaceFiles()
	if err != nil {
		return nil, "", 0, err
	}
	for _, file := range marketplaces {
		if total > maxPluginBundleBytes-len(file.Data) {
			return nil, "", 0, fmt.Errorf("embedded plugin bundle exceeds its public size limit")
		}
		total += len(file.Data)
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	required := map[string]bool{
		".agents/plugins/marketplace.json":                              false,
		".claude-plugin/marketplace.json":                               false,
		".github/plugin/marketplace.json":                               false,
		"plugins/dagrail/.codex-plugin/plugin.json":                     false,
		"plugins/dagrail/.claude-plugin/plugin.json":                    false,
		"plugins/dagrail/.plugin/plugin.json":                           false,
		"plugins/dagrail/assets/composer-icon.svg":                      false,
		"plugins/dagrail/assets/logo-dark.svg":                          false,
		"plugins/dagrail/assets/logo.svg":                               false,
		"plugins/dagrail/skills/govern-dag/SKILL.md":                    false,
		"plugins/dagrail/skills/govern-dag/agents/openai.yaml":          false,
		"plugins/dagrail/skills/govern-dag/assets/icon-large.svg":       false,
		"plugins/dagrail/skills/govern-dag/assets/icon-small.svg":       false,
		"plugins/dagrail/skills/execute-dag-node/agents/openai.yaml":    false,
		"plugins/dagrail/skills/execute-dag-node/SKILL.md":              false,
		"plugins/dagrail/skills/execute-dag-node/assets/icon-large.svg": false,
		"plugins/dagrail/skills/execute-dag-node/assets/icon-small.svg": false,
		"plugins/dagrail/skills/review-dag-node/agents/openai.yaml":     false,
		"plugins/dagrail/skills/review-dag-node/SKILL.md":               false,
		"plugins/dagrail/skills/review-dag-node/assets/icon-large.svg":  false,
		"plugins/dagrail/skills/review-dag-node/assets/icon-small.svg":  false,
		"plugins/dagrail/hooks/hooks.json":                              false,
		"plugins/dagrail/hooks/claude-hooks.json":                       false,
		"plugins/dagrail/hooks/copilot-hooks.json":                      false,
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(pluginBundleDomain))
	for _, file := range files {
		if _, ok := required[file.Path]; ok {
			required[file.Path] = true
		}
		content := sha256.Sum256(file.Data)
		_, _ = hash.Write([]byte(file.Path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(content[:])
	}
	for path, present := range required {
		if !present {
			return nil, "", 0, fmt.Errorf("embedded plugin bundle is missing %s", path)
		}
	}
	if err := validateBundledManifestReferences(files); err != nil {
		return nil, "", 0, err
	}
	return files, "sha256:" + hex.EncodeToString(hash.Sum(nil)), total, nil
}

func validateBundledManifestReferences(files []bundleFile) error {
	byPath := make(map[string][]byte, len(files))
	for _, file := range files {
		byPath[file.Path] = file.Data
	}
	for _, manifestPath := range []string{
		"plugins/dagrail/.codex-plugin/plugin.json",
		"plugins/dagrail/.claude-plugin/plugin.json",
		"plugins/dagrail/.plugin/plugin.json",
	} {
		var manifest struct {
			Skills    string `json:"skills"`
			Hooks     string `json:"hooks"`
			Interface struct {
				ComposerIcon string `json:"composerIcon"`
				Logo         string `json:"logo"`
				LogoDark     string `json:"logoDark"`
			} `json:"interface"`
		}
		if err := json.Unmarshal(byPath[manifestPath], &manifest); err != nil {
			return fmt.Errorf("embedded plugin manifest %s is invalid: %w", manifestPath, err)
		}
		if manifest.Skills == "" || !bundleDirectoryContains(files, manifest.Skills, "SKILL.md") {
			return fmt.Errorf("embedded plugin manifest %s has an unresolved skills reference", manifestPath)
		}
		for _, reference := range []string{manifest.Hooks, manifest.Interface.ComposerIcon, manifest.Interface.Logo, manifest.Interface.LogoDark} {
			if reference == "" {
				continue
			}
			resolved, err := resolvePluginBundleReference(reference)
			if err != nil {
				return fmt.Errorf("embedded plugin manifest %s: %w", manifestPath, err)
			}
			if _, ok := byPath[resolved]; !ok {
				return fmt.Errorf("embedded plugin manifest %s references missing %s", manifestPath, resolved)
			}
		}
	}
	for path, data := range byPath {
		if !strings.HasSuffix(path, "/agents/openai.yaml") {
			continue
		}
		var metadata struct {
			Interface struct {
				DisplayName      string `yaml:"display_name"`
				ShortDescription string `yaml:"short_description"`
				IconSmall        string `yaml:"icon_small"`
				IconLarge        string `yaml:"icon_large"`
				BrandColor       string `yaml:"brand_color"`
				DefaultPrompt    string `yaml:"default_prompt"`
			} `yaml:"interface"`
		}
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&metadata); err != nil {
			return fmt.Errorf("embedded skill metadata %s is invalid: %w", path, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return fmt.Errorf("embedded skill metadata %s contains multiple YAML documents", path)
		}
		for _, reference := range []string{metadata.Interface.IconSmall, metadata.Interface.IconLarge} {
			resolved, err := resolveBundleReferenceFrom(path, reference)
			if err != nil {
				return fmt.Errorf("embedded skill metadata %s: %w", path, err)
			}
			if _, ok := byPath[resolved]; !ok {
				return fmt.Errorf("embedded skill metadata %s references missing %s", path, resolved)
			}
		}
	}
	return nil
}

func resolveBundleReferenceFrom(declaringPath, reference string) (string, error) {
	if reference == "" || filepath.IsAbs(reference) {
		return "", fmt.Errorf("plugin bundle reference must be relative")
	}
	base := filepath.Dir(filepath.FromSlash(declaringPath))
	if strings.HasSuffix(filepath.ToSlash(declaringPath), "/agents/openai.yaml") {
		base = filepath.Dir(base)
	}
	resolved := filepath.ToSlash(filepath.Clean(filepath.Join(base, filepath.FromSlash(reference))))
	if resolved == "plugins/dagrail" || !strings.HasPrefix(resolved, "plugins/dagrail/") {
		return "", fmt.Errorf("plugin bundle reference escapes its plugin root")
	}
	return resolved, nil
}

func bundleDirectoryContains(files []bundleFile, reference, base string) bool {
	resolved, err := resolvePluginBundleReference(reference)
	if err != nil {
		return false
	}
	prefix := strings.TrimSuffix(resolved, "/") + "/"
	for _, file := range files {
		if strings.HasPrefix(file.Path, prefix) && filepath.Base(file.Path) == base {
			return true
		}
	}
	return false
}

func resolvePluginBundleReference(reference string) (string, error) {
	if reference == "" || filepath.IsAbs(reference) {
		return "", fmt.Errorf("plugin bundle reference must be relative")
	}
	normalized := filepath.ToSlash(filepath.Clean(filepath.FromSlash(reference)))
	normalized = strings.TrimPrefix(normalized, "./")
	if normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", fmt.Errorf("plugin bundle reference escapes its plugin root")
	}
	return "plugins/dagrail/" + normalized, nil
}

func bundledMarketplaceFiles() ([]bundleFile, error) {
	pluginSource := "./plugins/dagrail"
	pluginMetadata := map[string]any{
		"name":        "dagrail",
		"source":      pluginSource,
		"description": "Advance recoverable agent DAGs through typed, auditable actions.",
		"version":     version.Version,
		"author":      map[string]string{"name": "CongBao", "url": "https://github.com/CongBao"},
		"homepage":    "https://github.com/CongBao/dagrail#readme",
		"repository":  "https://github.com/CongBao/dagrail",
		"license":     "Apache-2.0",
		"keywords":    []string{"dag", "agent-governance", "mcp", "multi-agent", "llm", "developer-tools"},
		"category":    "Productivity",
	}
	claude := map[string]any{
		"name":        BundledMarketplaceName,
		"owner":       map[string]string{"name": "CongBao"},
		"description": "Offline marketplace materialized from a verified DAGrail binary.",
		"plugins":     []any{pluginMetadata},
	}
	copilot := map[string]any{
		"name":     BundledMarketplaceName,
		"owner":    map[string]string{"name": "CongBao"},
		"metadata": map[string]string{"description": "Offline marketplace materialized from a verified DAGrail binary.", "version": version.Version},
		"plugins":  []any{pluginMetadata},
	}
	codexPlugin := map[string]any{
		"name":     "dagrail",
		"source":   map[string]string{"source": "local", "path": pluginSource},
		"policy":   map[string]string{"installation": "AVAILABLE", "authentication": "ON_INSTALL"},
		"category": "Productivity",
	}
	codex := map[string]any{
		"name":      BundledMarketplaceName,
		"interface": map[string]string{"displayName": "DAGrail bundled marketplace"},
		"plugins":   []any{codexPlugin},
	}
	values := []struct {
		path  string
		value any
	}{
		{".agents/plugins/marketplace.json", codex},
		{".claude-plugin/marketplace.json", claude},
		{".github/plugin/marketplace.json", copilot},
	}
	result := make([]bundleFile, 0, len(values))
	for _, value := range values {
		raw, err := json.Marshal(value.value)
		if err != nil {
			return nil, err
		}
		result = append(result, bundleFile{Path: value.path, Data: append(raw, '\n')})
	}
	return result, nil
}

func verifyMaterializedBundle(root string, expected []bundleFile) error {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("materialized plugin root is not a non-symlink directory")
	}
	actual := map[string][]byte{}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxPluginBundleFile {
			return fmt.Errorf("materialized plugin contains an invalid file")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		actual[filepath.ToSlash(relative)] = data
		return nil
	})
	if err != nil {
		return err
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("materialized plugin file set does not match the linked bundle")
	}
	for _, file := range expected {
		if !bytes.Equal(actual[file.Path], file.Data) {
			return fmt.Errorf("materialized plugin file %s does not match the linked bundle", file.Path)
		}
	}
	return nil
}
