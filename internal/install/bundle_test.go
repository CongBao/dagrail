package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinkedPluginBundleMaterializesAndVerifiesExactBytes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DAGRAIL_HOME", filepath.Join(root, "data"))
	before, err := PluginBundleStatus()
	if err != nil || before.Status != "not_materialized" || !filepath.IsAbs(before.Root) || before.Files < 10 || before.Bytes == 0 {
		t.Fatalf("unexpected initial bundle status: %+v %v", before, err)
	}
	created, err := MaterializePluginBundle()
	if err != nil || created.Status != "materialized" || !filepath.IsAbs(created.Root) || !strings.HasPrefix(created.Digest, "sha256:") {
		t.Fatalf("materialize bundle: %+v %v", created, err)
	}
	verified, err := PluginBundleStatus()
	if err != nil || verified.Status != "verified" || verified.Digest != created.Digest || verified.Root != created.Root {
		t.Fatalf("verify bundle: %+v %v", verified, err)
	}
	manifest := filepath.Join(created.Root, "plugins", "dagrail", ".codex-plugin", "plugin.json")
	if err := os.WriteFile(manifest, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PluginBundleStatus(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mutated plugin bundle was accepted: %v", err)
	}
}

func TestBundledMarketplaceSourcesAreRelativeAndHostSpecific(t *testing.T) {
	files, _, _, err := linkedPluginBundle()
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string][]byte{}
	for _, file := range files {
		byPath[file.Path] = file.Data
	}
	var codex struct {
		Name    string `json:"name"`
		Plugins []struct {
			Source struct {
				Source string `json:"source"`
				Path   string `json:"path"`
			} `json:"source"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(byPath[".agents/plugins/marketplace.json"], &codex); err != nil || codex.Name != BundledMarketplaceName || len(codex.Plugins) != 1 || codex.Plugins[0].Source.Source != "local" || codex.Plugins[0].Source.Path != "./plugins/dagrail" {
		t.Fatalf("invalid Codex local marketplace: %+v %v", codex, err)
	}
	for _, path := range []string{".claude-plugin/marketplace.json", ".github/plugin/marketplace.json"} {
		var marketplace struct {
			Name    string `json:"name"`
			Plugins []struct {
				Source string `json:"source"`
			} `json:"plugins"`
		}
		if err := json.Unmarshal(byPath[path], &marketplace); err != nil || marketplace.Name != BundledMarketplaceName || len(marketplace.Plugins) != 1 || marketplace.Plugins[0].Source != "./plugins/dagrail" {
			t.Fatalf("invalid relative marketplace %s: %+v %v", path, marketplace, err)
		}
	}
}

func TestLinkedPluginBundleContainsOnlyPublicProjectionRoots(t *testing.T) {
	files, _, _, err := linkedPluginBundle()
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		allowed := false
		for _, prefix := range []string{".agents/plugins/", ".claude-plugin/", ".github/plugin/", "plugins/dagrail/"} {
			allowed = allowed || strings.HasPrefix(file.Path, prefix)
		}
		if !allowed {
			t.Fatalf("non-public file entered plugin bundle: %s", file.Path)
		}
	}
}
