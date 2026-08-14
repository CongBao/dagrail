package effects

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/CongBao/dagrail/sdk"
)

type GitMerge struct{}

type gitMergeRequest struct {
	Repository   string `json:"repository"`
	TargetBranch string `json:"targetBranch"`
	Candidate    string `json:"candidate"`
	Strategy     string `json:"strategy"`
}

type gitMergeBinding struct {
	Repository      string `json:"repository"`
	TargetBranch    string `json:"targetBranch"`
	Base            string `json:"base"`
	Candidate       string `json:"candidate"`
	Strategy        string `json:"strategy"`
	ProspectiveTree string `json:"prospectiveTree"`
}

var objectID = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

func (GitMerge) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: "git.merge", Version: "0.1.0", SchemaHash: "sha256:git-merge-effect-v1"}
}

func (GitMerge) Prepare(_ context.Context, effect sdk.EffectRequest) (sdk.PreparedEffect, error) {
	request, repository, err := decodeGitMergeRequest(effect)
	if err != nil {
		return sdk.PreparedEffect{}, err
	}
	if err := requireClean(repository); err != nil {
		return sdk.PreparedEffect{}, err
	}
	if _, err := gitRun(repository, "check-ref-format", "--branch", request.TargetBranch); err != nil {
		return sdk.PreparedEffect{}, fmt.Errorf("invalid target branch: %w", err)
	}
	base, err := gitOne(repository, "rev-parse", "refs/heads/"+request.TargetBranch+"^{commit}")
	if err != nil {
		return sdk.PreparedEffect{}, fmt.Errorf("resolve target branch: %w", err)
	}
	candidate, err := gitOne(repository, "rev-parse", request.Candidate+"^{commit}")
	if err != nil || candidate != request.Candidate || !objectID.MatchString(candidate) {
		return sdk.PreparedEffect{}, fmt.Errorf("candidate must be an exact full commit ID available in the repository")
	}
	tree, err := prospectiveTree(repository, base, candidate, request.Strategy)
	if err != nil {
		return sdk.PreparedEffect{}, err
	}
	binding := gitMergeBinding{Repository: repository, TargetBranch: request.TargetBranch, Base: base, Candidate: candidate, Strategy: request.Strategy, ProspectiveTree: tree}
	raw, _ := json.Marshal(binding)
	return sdk.PreparedEffect{AdapterID: "git.merge", Binding: raw}, nil
}

func (GitMerge) Dispatch(_ context.Context, effect sdk.EffectRequest, prepared sdk.PreparedEffect) (sdk.EffectReceipt, error) {
	binding, err := decodeGitBinding(prepared)
	if err != nil {
		return sdk.EffectReceipt{}, err
	}
	if err := requireClean(binding.Repository); err != nil {
		return sdk.EffectReceipt{}, err
	}
	branch, err := gitOne(binding.Repository, "symbolic-ref", "--short", "HEAD")
	if err != nil || branch != binding.TargetBranch {
		return sdk.EffectReceipt{}, fmt.Errorf("target branch %s must be checked out", binding.TargetBranch)
	}
	head, err := gitOne(binding.Repository, "rev-parse", "HEAD")
	if err != nil || head != binding.Base {
		return sdk.EffectReceipt{}, fmt.Errorf("target branch moved after prepare")
	}
	hooksDir, err := os.MkdirTemp("", "dagrail-empty-hooks-")
	if err != nil {
		return sdk.EffectReceipt{}, err
	}
	defer os.RemoveAll(hooksDir)
	switch binding.Strategy {
	case "merge-commit":
		message := "DAGrail merge\n\nDAGrail-Action: " + effect.ActionID
		if _, err := gitRun(binding.Repository, "-c", "core.hooksPath="+hooksDir, "-c", "user.name=DAGrail", "-c", "user.email=dagrail@invalid", "merge", "--no-ff", "--no-edit", "-m", message, binding.Candidate); err != nil {
			return sdk.EffectReceipt{}, err
		}
	case "ff-only":
		if _, err := gitRun(binding.Repository, "-c", "core.hooksPath="+hooksDir, "merge", "--ff-only", binding.Candidate); err != nil {
			return sdk.EffectReceipt{}, err
		}
	default:
		return sdk.EffectReceipt{}, fmt.Errorf("unsupported Git merge strategy %s", binding.Strategy)
	}
	return inspectGitResult(effect.ActionID, binding)
}

func (GitMerge) Reconcile(_ context.Context, effect sdk.EffectRequest, prepared sdk.PreparedEffect, _ json.RawMessage) (sdk.EffectReceipt, error) {
	binding, err := decodeGitBinding(prepared)
	if err != nil {
		return sdk.EffectReceipt{}, err
	}
	return inspectGitResult(effect.ActionID, binding)
}

func decodeGitMergeRequest(effect sdk.EffectRequest) (gitMergeRequest, string, error) {
	var request gitMergeRequest
	if err := json.Unmarshal(effect.Request, &request); err != nil {
		return request, "", fmt.Errorf("decode Git merge request: %w", err)
	}
	if request.Strategy == "" {
		request.Strategy = "merge-commit"
	}
	if request.Strategy != "merge-commit" && request.Strategy != "ff-only" {
		return request, "", fmt.Errorf("Git strategy must be merge-commit or ff-only")
	}
	if request.Repository == "" {
		request.Repository = effect.ProjectRoot
	}
	projectRoot, err := filepath.EvalSymlinks(effect.ProjectRoot)
	if err != nil {
		return request, "", err
	}
	repository, err := filepath.EvalSymlinks(request.Repository)
	if err != nil {
		return request, "", err
	}
	relative, err := filepath.Rel(projectRoot, repository)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return request, "", fmt.Errorf("Git repository must be inside the DAGrail project root")
	}
	if request.TargetBranch == "" || request.Candidate == "" {
		return request, "", fmt.Errorf("targetBranch and exact candidate are required")
	}
	return request, repository, nil
}

func prospectiveTree(repository, base, candidate, strategy string) (string, error) {
	if strategy == "ff-only" {
		if _, err := gitRun(repository, "merge-base", "--is-ancestor", base, candidate); err != nil {
			return "", fmt.Errorf("candidate is not a fast-forward of target")
		}
		return gitOne(repository, "rev-parse", candidate+"^{tree}")
	}
	temporary, err := os.MkdirTemp("", "dagrail-git-prepare-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temporary)
	if _, err := gitRun(repository, "clone", "--no-checkout", "--local", repository, temporary); err != nil {
		return "", fmt.Errorf("create prospective clone: %w", err)
	}
	if _, err := gitRun(temporary, "checkout", "--detach", base); err != nil {
		return "", err
	}
	hooksDir, err := os.MkdirTemp("", "dagrail-empty-hooks-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(hooksDir)
	if _, err := gitRun(temporary,
		"-c", "core.hooksPath="+hooksDir,
		"-c", "user.name=DAGrail",
		"-c", "user.email=dagrail@invalid",
		"merge", "--no-commit", "--no-ff", candidate,
	); err != nil {
		return "", fmt.Errorf("prospective merge has conflicts: %w", err)
	}
	tree, err := gitOne(temporary, "write-tree")
	if err != nil || !objectID.MatchString(tree) {
		return "", fmt.Errorf("prospective merge did not produce an exact tree")
	}
	return tree, nil
}

func inspectGitResult(actionID string, binding gitMergeBinding) (sdk.EffectReceipt, error) {
	current, err := gitOne(binding.Repository, "rev-parse", "refs/heads/"+binding.TargetBranch+"^{commit}")
	if err != nil {
		return sdk.EffectReceipt{}, err
	}
	if current == binding.Base {
		return sdk.EffectReceipt{Status: "failed", CompletionStatus: "failed", Detail: jsonMessage("target branch remains at prepared base")}, nil
	}
	if binding.Strategy == "ff-only" {
		if current == binding.Candidate {
			return sdk.EffectReceipt{Status: "confirmed", ExternalID: current, CompletionStatus: "completed", Detail: jsonMessage("fast-forward target matches candidate")}, nil
		}
		return sdk.EffectReceipt{Status: "unknown", ExternalID: current, CompletionStatus: "unknown", Detail: jsonMessage("target branch advanced to an unexpected commit")}, nil
	}
	parentsRaw, err := gitOne(binding.Repository, "show", "-s", "--format=%P", current)
	if err != nil {
		return sdk.EffectReceipt{}, err
	}
	parents := strings.Fields(parentsRaw)
	tree, treeErr := gitOne(binding.Repository, "rev-parse", current+"^{tree}")
	message, messageErr := gitRun(binding.Repository, "show", "-s", "--format=%B", current)
	if treeErr == nil && messageErr == nil && len(parents) == 2 && parents[0] == binding.Base && parents[1] == binding.Candidate && tree == binding.ProspectiveTree && strings.Contains(message, "DAGrail-Action: "+actionID) {
		return sdk.EffectReceipt{Status: "confirmed", ExternalID: current, CompletionStatus: "completed", Detail: jsonMessage("merge parents, tree and action trailer match")}, nil
	}
	return sdk.EffectReceipt{Status: "unknown", ExternalID: current, CompletionStatus: "unknown", Detail: jsonMessage("target branch does not match the prepared merge identity")}, nil
}

func decodeGitBinding(prepared sdk.PreparedEffect) (gitMergeBinding, error) {
	var binding gitMergeBinding
	if prepared.AdapterID != "git.merge" {
		return binding, fmt.Errorf("prepared effect adapter mismatch")
	}
	if err := json.Unmarshal(prepared.Binding, &binding); err != nil {
		return binding, err
	}
	return binding, nil
}

func requireClean(repository string) error {
	status, err := gitRun(repository, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("Git working tree must be clean")
	}
	return nil
}

func gitOne(repository string, args ...string) (string, error) {
	output, err := gitRun(repository, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func gitRun(repository string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", repository}, args...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "LANG=C")
	output, err := command.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func jsonMessage(message string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"message": message})
	return raw
}
