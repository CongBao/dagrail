package compatibility

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/CongBao/dagrail/internal/version"
)

const APIVersion = "dagrail.io/historical-binary-matrix/v1alpha1"

var (
	//go:embed beta-window.json
	windowFiles      embed.FS
	commitPattern    = regexp.MustCompile(`^[a-f0-9]{40}$`)
	expectedVersions = []string{"0.10.0", "0.11.0", "0.12.0", "0.13.0", "0.14.0", "0.15.0", "0.16.0", "0.17.0", "0.18.0", "0.19.0", "0.20.0", "0.21.0", "0.22.0", "0.22.1", "0.22.2", "0.23.0", "0.23.1", "0.24.0", "0.25.0", "0.25.1", "0.25.2", "0.25.3", "0.26.0", "0.26.1", "0.26.2"}
)

type Entry struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

type Window struct {
	APIVersion     string  `json:"apiVersion"`
	Kind           string  `json:"kind"`
	FromVersion    string  `json:"fromVersion"`
	CurrentVersion string  `json:"currentVersion"`
	Entries        []Entry `json:"entries"`
}

type Evidence struct {
	FromVersion    string `json:"fromVersion"`
	CurrentVersion string `json:"currentVersion"`
	Historical     int    `json:"historical"`
	Digest         string `json:"digest"`
}

func Current() (Window, Evidence, error) {
	raw, err := windowFiles.ReadFile("beta-window.json")
	if err != nil {
		return Window{}, Evidence{}, err
	}
	window, err := Decode(raw, version.Version)
	if err != nil {
		return Window{}, Evidence{}, err
	}
	digest := sha256.Sum256(raw)
	return window, Evidence{FromVersion: window.FromVersion, CurrentVersion: window.CurrentVersion, Historical: len(window.Entries), Digest: "sha256:" + hex.EncodeToString(digest[:])}, nil
}

func Decode(raw []byte, currentVersion string) (Window, error) {
	if len(raw) == 0 || len(raw) > 64*1024 {
		return Window{}, fmt.Errorf("historical matrix must be 1..65536 bytes")
	}
	if err := domain.ValidateAuthorityJSON(raw); err != nil {
		return Window{}, fmt.Errorf("historical matrix: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var window Window
	if err := decoder.Decode(&window); err != nil {
		return Window{}, fmt.Errorf("decode historical matrix: %w", err)
	}
	if window.APIVersion != APIVersion || window.Kind != "HistoricalBinaryMatrix" || window.FromVersion != "0.10.0" || window.CurrentVersion != currentVersion || len(window.Entries) != len(expectedVersions) {
		return Window{}, fmt.Errorf("historical matrix identity or closed window is invalid")
	}
	seenCommits := map[string]bool{}
	for index, entry := range window.Entries {
		if entry.Version != expectedVersions[index] || !commitPattern.MatchString(entry.Commit) || seenCommits[entry.Commit] {
			return Window{}, fmt.Errorf("historical matrix entry %d is invalid", index)
		}
		seenCommits[entry.Commit] = true
	}
	return window, nil
}
