package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.temporal.io/sdk/activity"
)

type RiskLevel int

const (
	RiskLow RiskLevel = iota
	RiskMedium
	RiskHigh
)

func (r RiskLevel) String() string {
	return [...]string{"low", "medium", "high"}[r]
}

type Readiness struct {
	Ready     bool     `json:"ready"`
	Reason    string   `json:"reason"`
	Questions []string `json:"questions"`
}

type CIResult struct {
	SHA    string
	Passed bool
	Log    string
}

// BuildRequest is everything the build agent gets. The workflow picks the
// branch, so a fresh attempt after a revert gets a fresh branch.
type BuildRequest struct {
	Ticket   Ticket
	Branch   string
	Feedback string
}

type BuildResult struct {
	Branch string
	SHA    string // head commit after the build pushed
}

type Review struct {
	Approved bool
	Comments string
}

type SmokeResult struct {
	Passed bool
	Log    string
}

// The systems the activities drive, as small interfaces so the local stand-in
// (package target) and a later GitHub/CD version are interchangeable.

// Workspace gives the build agent a checkout to work in, and publishes it.
type Workspace interface {
	// Checkout returns a clean checkout of branch in a directory named for key.
	Checkout(ctx context.Context, key, branch string) (dir string, err error)
	CommitAndPush(ctx context.Context, dir, branch, message string) (sha string, err error)
}

// Forge is where branches live and get merged: GitHub's API, or a local repo.
type Forge interface {
	HeadSHA(ctx context.Context, branch string) (string, error)
	Merge(ctx context.Context, branch, message string) (sha string, err error)
	Revert(ctx context.Context, mergeSHA string) (sha string, err error)
}

// Deployer ships main and checks the running version is alive.
type Deployer interface {
	Deploy(ctx context.Context) (sha string, err error)
	Smoke(ctx context.Context) (SmokeResult, error)
}

// PipelineActivities run on PipelineQueue: calls to other systems' APIs. The
// local Forge and Deployer happen to shell out to git and go, but they stand
// in for GitHub and a CD system, which is why they live here and not with the
// agent.
type PipelineActivities struct {
	Forge    Forge
	Deployer Deployer
}

// Linear isn't wired up yet: these log what they'd do.

func (a *PipelineActivities) AskQuestions(ctx context.Context, ticketID string, qs []string) error {
	activity.GetLogger(ctx).Info("would label ai-needs-info and comment", "ticket", ticketID, "questions", qs)
	return nil
}

func (a *PipelineActivities) RequestApproval(ctx context.Context, ticketID, pr string, risk RiskLevel) error {
	activity.GetLogger(ctx).Info("would label ai-needs-approval", "ticket", ticketID, "risk", risk.String())
	return nil
}

func (a *PipelineActivities) Escalate(ctx context.Context, ticketID, reason string) error {
	activity.GetLogger(ctx).Warn("would label ai-escalated and alert", "ticket", ticketID, "reason", reason)
	return nil
}

// OpenOrUpdatePR would open a draft PR on GitHub. Locally the branch is the
// PR, and CI was already started by the push.
func (a *PipelineActivities) OpenOrUpdatePR(ctx context.Context, t Ticket, b BuildResult) (string, error) {
	activity.GetLogger(ctx).Info("PR updated", "ticket", t.ID, "branch", b.Branch, "sha", b.SHA)
	return "local:" + b.Branch, nil
}

// HeadSHA returns the branch's head commit, after a human has pushed to it.
func (a *PipelineActivities) HeadSHA(ctx context.Context, branch string) (string, error) {
	sha, err := a.Forge.HeadSHA(ctx, branch)
	if err != nil {
		return "", fmt.Errorf("head of %s: %w", branch, err)
	}
	return sha, nil
}

func (a *PipelineActivities) Merge(ctx context.Context, t Ticket, branch string) (string, error) {
	sha, err := a.Forge.Merge(ctx, branch, fmt.Sprintf("Merge %s: %s", t.ID, t.Title))
	if err != nil {
		return "", fmt.Errorf("merge %s: %w", branch, err)
	}
	return sha, nil
}

func (a *PipelineActivities) Revert(ctx context.Context, mergeSHA string) (string, error) {
	sha, err := a.Forge.Revert(ctx, mergeSHA)
	if err != nil {
		return "", fmt.Errorf("revert %s: %w", mergeSHA, err)
	}
	return sha, nil
}

func (a *PipelineActivities) Deploy(ctx context.Context) (string, error) {
	sha, err := a.Deployer.Deploy(ctx)
	if err != nil {
		return "", fmt.Errorf("deploy: %w", err)
	}
	return sha, nil
}

func (a *PipelineActivities) SmokeTest(ctx context.Context) (SmokeResult, error) {
	res, err := a.Deployer.Smoke(ctx)
	if err != nil {
		return SmokeResult{}, fmt.Errorf("smoke test: %w", err)
	}
	return res, nil
}

// AgentActivities run on AgentQueue: anything that runs an agent or needs the
// repo, a toolchain or credentials. Agent steps are defined in StepsDir.
type AgentActivities struct {
	StepsDir  string
	Runner    Runner
	Workspace Workspace
}

// CheckReadiness judges the ticket against the code as it is on main, so it
// can answer "which page?" itself instead of asking. Its tools can only read.
func (a *AgentActivities) CheckReadiness(ctx context.Context, t Ticket) (Readiness, error) {
	dir, err := a.Workspace.Checkout(ctx, t.ID+"-readiness", "main")
	if err != nil {
		return Readiness{}, fmt.Errorf("checkout main: %w", err)
	}
	var rd Readiness
	if err := a.runStep(ctx, "readiness", t, dir, &rd); err != nil {
		return Readiness{}, err
	}
	activity.GetLogger(ctx).Info("readiness", "ticket", t.ID, "ready", rd.Ready, "reason", rd.Reason)
	return rd, nil
}

// Build checks out the branch, lets the build agent change it, then commits
// and pushes. The agent never touches git: the push is what fires CI, so the
// pipeline owns it.
func (a *AgentActivities) Build(ctx context.Context, req BuildRequest) (BuildResult, error) {
	dir, err := a.Workspace.Checkout(ctx, req.Ticket.ID, req.Branch)
	if err != nil {
		return BuildResult{}, fmt.Errorf("checkout %s: %w", req.Branch, err)
	}
	var out struct {
		Summary string `json:"summary"`
	}
	if err := a.runStep(ctx, "build", req, dir, &out); err != nil {
		return BuildResult{}, err
	}
	msg := fmt.Sprintf("%s: %s\n\n%s", req.Ticket.ID, req.Ticket.Title, out.Summary)
	sha, err := a.Workspace.CommitAndPush(ctx, dir, req.Branch, msg)
	if err != nil {
		return BuildResult{}, fmt.Errorf("push %s: %w", req.Branch, err)
	}
	activity.GetLogger(ctx).Info("pushed", "ticket", req.Ticket.ID, "branch", req.Branch, "sha", sha, "summary", out.Summary)
	return BuildResult{Branch: req.Branch, SHA: sha}, nil
}

// runStep loads a step definition, renders its prompt with data, runs it in
// dir and decodes the structured output into out.
func (a *AgentActivities) runStep(ctx context.Context, name string, data any, dir string, out any) error {
	step, err := LoadStep(a.StepsDir, name)
	if err != nil {
		return err
	}
	prompt, err := step.Render(data)
	if err != nil {
		return err
	}
	res, err := a.Runner.Run(ctx, step, prompt, dir)
	if err != nil {
		return fmt.Errorf("run step %s: %w", name, err)
	}
	activity.GetLogger(ctx).Info("agent step done", "step", name, "model", step.Model, "cost_usd", res.CostUSD, "turns", res.Turns)
	if err := json.Unmarshal(res.Output, out); err != nil {
		return fmt.Errorf("step %s: decode output: %w", name, err)
	}
	return nil
}

// Review and AssessRisk are still stubs, steered by keywords in the ticket.

// Review approves unless the ticket says "never-approve", which forces the
// escalation path.
func (a *AgentActivities) Review(ctx context.Context, t Ticket, pr string) (Review, error) {
	if strings.Contains(t.Description, "never-approve") {
		return Review{Comments: "handle the error path"}, nil
	}
	return Review{Approved: true}, nil
}

// AssessRisk returns medium if the ticket mentions "migration", forcing the
// human approval gate.
func (a *AgentActivities) AssessRisk(ctx context.Context, t Ticket, pr string) (RiskLevel, error) {
	if strings.Contains(t.Description, "migration") {
		return RiskMedium, nil
	}
	return RiskLow, nil
}
