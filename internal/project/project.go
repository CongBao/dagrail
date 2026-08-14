package project

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/CongBao/dagrail/internal/domain"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

const markerPath = ".dagrail/project.yaml"
const maxMarkerBytes = 64 * 1024

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
	abs, err := filepath.Abs(root)
	if err != nil {
		return Project{}, err
	}
	if name == "" {
		name = filepath.Base(abs)
	}
	marker := filepath.Join(abs, filepath.FromSlash(markerPath))
	if _, err := os.Stat(marker); err == nil {
		return Open(abs)
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
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		return Project{}, err
	}
	if err := os.WriteFile(marker, data, 0o600); err != nil {
		return Project{}, err
	}
	return Open(abs)
}

func Open(root string) (Project, error) {
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
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return Project{}, err
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
