// Package pipeline holds the ticket workflow: readiness → build/PR/CI/review
// loop → risk gate → merge → deploy and smoke test, with human decisions
// wherever it gets stuck.
// Workflow code must be deterministic; anything touching the outside world
// (LLMs, git, Linear, CI) lives in activities.
package pipeline

import (
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	MaxReviewRounds = 3
	// CITimeout escalates if CI never reports for the commit we pushed.
	CITimeout = 30 * time.Minute

	// SignalHuman carries a human's answers or decision (HumanInput).
	SignalHuman = "human"
	// SignalCI carries a CI result for a commit (CIResult), from a webhook
	// receiver or poller standing in for GitHub.
	SignalCI = "ci"
	// QueryStatus returns the current status for dashboards and the CLI.
	QueryStatus = "status"
)

// Action is what a human decides when the workflow hands them a ticket.
type Action string

const (
	ActionApprove  Action = "approve"  // at the risk gate: merge
	ActionRetry    Action = "retry"    // comment becomes feedback; build loop restarts
	ActionTakeOver Action = "takeover" // a human works on the branch, then sends resume
	ActionResume   Action = "resume"   // hand a taken-over ticket back: CI, review, gate
	ActionAbandon  Action = "abandon"
)

type Ticket struct {
	ID          string
	Title       string
	Description string
}

// WorkflowID maps a ticket to its workflow, so duplicate events for the same
// ticket can't start a second run and signals know where to go.
func WorkflowID(ticketID string) string { return "ticket-" + ticketID }

type HumanInput struct {
	Action  Action
	Comment string
}

type Result struct {
	Outcome string // deployed, abandoned
	Builds  int    // build agent runs in total, across retries
	PRURL   string
	Risk    RiskLevel
	SHA     string // the deployed commit
}

func activityOptions(queue string) workflow.ActivityOptions {
	return workflow.ActivityOptions{
		TaskQueue:              queue,
		ScheduleToStartTimeout: ScheduleToStartTimeout,
		StartToCloseTimeout:    30 * time.Minute,
		HeartbeatTimeout:       2 * time.Minute,
		RetryPolicy:            &temporal.RetryPolicy{MaximumAttempts: 3},
	}
}

type step int

const (
	stepBuild step = iota
	stepPR
	stepCI
	stepReview
	stepRisk
	stepDeploy
)

// run holds one ticket's state. Each step method returns the next step; steps
// that can end the ticket also return a final Result.
type run struct {
	ctx, pctx, actx workflow.Context
	t               Ticket
	status          string
	human, ci       workflow.ReceiveChannel

	round    int // build rounds since the last human retry/resume; capped
	builds   int
	attempt  int // which branch we're on; a revert starts a new one
	feedback string
	build    BuildResult
	pr       string
	risk     RiskLevel
	merged   string // merge commit on main
	deployed string
}

// Nil receivers: only used to name the activity methods.
var (
	p  *PipelineActivities
	ag *AgentActivities
)

func TicketWorkflow(ctx workflow.Context, t Ticket) (Result, error) {
	r := &run{
		ctx:     ctx,
		pctx:    workflow.WithActivityOptions(ctx, activityOptions(PipelineQueue)),
		actx:    workflow.WithActivityOptions(ctx, activityOptions(AgentQueue)),
		t:       t,
		status:  "starting",
		attempt: 1,
		human:   workflow.GetSignalChannel(ctx, SignalHuman),
		ci:      workflow.GetSignalChannel(ctx, SignalCI),
	}
	if err := workflow.SetQueryHandler(ctx, QueryStatus, func() (string, error) {
		return r.status, nil
	}); err != nil {
		return Result{}, fmt.Errorf("register status query: %w", err)
	}

	if err := r.readiness(); err != nil {
		return Result{}, err
	}
	next := stepBuild
	for {
		var done *Result
		var err error
		switch next {
		case stepBuild:
			next, done, err = r.doBuild()
		case stepPR:
			next, err = r.doPR()
		case stepCI:
			next, done, err = r.doCI()
		case stepReview:
			next, err = r.doReview()
		case stepRisk:
			next, done, err = r.doRisk()
		case stepDeploy:
			next, done, err = r.doDeploy()
		}
		if err != nil {
			return Result{}, err
		}
		if done != nil {
			return *done, nil
		}
	}
}

// readiness loops until the ticket has enough detail to build.
func (r *run) readiness() error {
	for {
		r.status = "checking readiness"
		var rd Readiness
		if err := workflow.ExecuteActivity(r.actx, ag.CheckReadiness, r.t).Get(r.ctx, &rd); err != nil {
			return err
		}
		if rd.Ready {
			return nil
		}
		r.status = "waiting for more detail"
		if err := workflow.ExecuteActivity(r.pctx, p.AskQuestions, r.t.ID, rd.Questions).Get(r.ctx, nil); err != nil {
			return err
		}
		var in HumanInput
		r.human.Receive(r.ctx, &in)
		r.t.Description += "\n\nAnswers:\n" + in.Comment
	}
}

func (r *run) doBuild() (step, *Result, error) {
	if r.round >= MaxReviewRounds {
		return r.escalate(fmt.Sprintf("no clean build after %d rounds; last feedback: %s", MaxReviewRounds, r.feedback))
	}
	r.round++
	r.builds++
	r.status = fmt.Sprintf("building (round %d)", r.round)
	req := BuildRequest{Ticket: r.t, Branch: r.branch(), Feedback: r.feedback}
	err := workflow.ExecuteActivity(r.actx, ag.Build, req).Get(r.ctx, &r.build)
	return stepPR, nil, err
}

// branch is ai/<ticket>/<attempt>. Pushes to ai/* trigger CI, and CI maps the
// branch back to the ticket's workflow.
func (r *run) branch() string { return fmt.Sprintf("ai/%s/%d", r.t.ID, r.attempt) }

// doPR pushes the PR forward; on GitHub that's what triggers CI.
func (r *run) doPR() (step, error) {
	r.status = "updating PR"
	err := workflow.ExecuteActivity(r.pctx, p.OpenOrUpdatePR, r.t, r.build).Get(r.ctx, &r.pr)
	return stepCI, err
}

// doCI waits for CI to report on the commit we pushed. CI is external, so this
// is a wait for a signal, not an activity. Results for older commits are
// ignored: they can arrive late or out of order.
func (r *run) doCI() (step, *Result, error) {
	r.status = "waiting for CI on " + r.build.SHA
	timerCtx, cancel := workflow.WithCancel(r.ctx)
	defer cancel()
	timer := workflow.NewTimer(timerCtx, CITimeout)
	for {
		var res CIResult
		timedOut := false
		sel := workflow.NewSelector(r.ctx)
		sel.AddReceive(r.ci, func(c workflow.ReceiveChannel, _ bool) { c.Receive(r.ctx, &res) })
		sel.AddFuture(timer, func(workflow.Future) { timedOut = true })
		sel.Select(r.ctx)

		if timedOut {
			return r.escalate(fmt.Sprintf("CI didn't report on %s within %s", r.build.SHA, CITimeout))
		}
		if res.SHA != r.build.SHA {
			workflow.GetLogger(r.ctx).Info("ignoring CI result for stale commit", "got", res.SHA, "want", r.build.SHA)
			continue
		}
		if !res.Passed {
			// Back to the builder without a review: no point reviewing broken code.
			r.feedback = "CI failed:\n" + res.Log
			return stepBuild, nil, nil
		}
		return stepReview, nil, nil
	}
}

func (r *run) doReview() (step, error) {
	r.status = fmt.Sprintf("reviewing (round %d)", r.round)
	var rv Review
	if err := workflow.ExecuteActivity(r.actx, ag.Review, r.t, r.pr).Get(r.ctx, &rv); err != nil {
		return 0, err
	}
	if !rv.Approved {
		r.feedback = rv.Comments
		return stepBuild, nil
	}
	return stepRisk, nil
}

func (r *run) doRisk() (step, *Result, error) {
	r.status = "assessing risk"
	if err := workflow.ExecuteActivity(r.actx, ag.AssessRisk, r.t, r.pr).Get(r.ctx, &r.risk); err != nil {
		return 0, nil, err
	}
	if r.risk == RiskLow {
		return r.merge()
	}
	r.status = "waiting for approval (" + r.risk.String() + " risk)"
	if err := workflow.ExecuteActivity(r.pctx, p.RequestApproval, r.t.ID, r.pr, r.risk).Get(r.ctx, nil); err != nil {
		return 0, nil, err
	}
	return r.decide(true)
}

func (r *run) escalate(reason string) (step, *Result, error) {
	r.status = "escalated: " + reason
	if err := workflow.ExecuteActivity(r.pctx, p.Escalate, r.t.ID, reason).Get(r.ctx, nil); err != nil {
		return 0, nil, err
	}
	return r.decide(false)
}

// decide waits for a human to say what happens next. Approve only counts at
// the risk gate; anything that doesn't apply is ignored and we keep waiting.
func (r *run) decide(atGate bool) (step, *Result, error) {
	for {
		var in HumanInput
		r.human.Receive(r.ctx, &in)
		switch in.Action {
		case ActionRetry:
			r.feedback = strings.TrimSpace(r.feedback + "\n\nHuman guidance:\n" + in.Comment)
			r.round = 0
			return stepBuild, nil, nil
		case ActionTakeOver:
			return r.takeOver()
		case ActionAbandon:
			return r.finish("abandoned")
		case ActionApprove:
			if atGate {
				return r.merge()
			}
		case ActionResume:
		}
		workflow.GetLogger(r.ctx).Warn("ignoring human action here", "action", in.Action, "status", r.status)
	}
}

// takeOver parks the ticket while a human works on the branch. On resume it
// re-enters at the PR step, so human work goes through CI, review and the risk
// gate like agent work does.
func (r *run) takeOver() (step, *Result, error) {
	r.status = "taken over: push to " + r.build.Branch + ", then send resume"
	for {
		var in HumanInput
		r.human.Receive(r.ctx, &in)
		switch in.Action {
		case ActionResume:
			if err := workflow.ExecuteActivity(r.pctx, p.HeadSHA, r.build.Branch).Get(r.ctx, &r.build.SHA); err != nil {
				return 0, nil, err
			}
			r.round = 0
			r.feedback = ""
			return stepPR, nil, nil
		case ActionAbandon:
			return r.finish("abandoned")
		case ActionApprove, ActionRetry, ActionTakeOver:
		}
		workflow.GetLogger(r.ctx).Warn("ignoring human action while taken over", "action", in.Action)
	}
}

func (r *run) merge() (step, *Result, error) {
	r.status = "merging"
	if err := workflow.ExecuteActivity(r.pctx, p.Merge, r.t, r.build.Branch).Get(r.ctx, &r.merged); err != nil {
		return 0, nil, err
	}
	return stepDeploy, nil, nil
}

// doDeploy ships main and smoke tests it. A failed smoke test reverts rather
// than fixing forward: main goes back to known-good first, then a human
// decides. A retry builds on a fresh branch, because re-merging a reverted
// branch wouldn't bring its changes back.
func (r *run) doDeploy() (step, *Result, error) {
	r.status = "deploying " + r.merged
	if err := workflow.ExecuteActivity(r.pctx, p.Deploy).Get(r.ctx, &r.deployed); err != nil {
		return 0, nil, err
	}
	r.status = "smoke testing " + r.deployed
	var smoke SmokeResult
	if err := workflow.ExecuteActivity(r.pctx, p.SmokeTest).Get(r.ctx, &smoke); err != nil {
		return 0, nil, err
	}
	if smoke.Passed {
		return r.finish("deployed")
	}

	r.status = "smoke test failed, reverting " + r.merged
	var reverted string
	if err := workflow.ExecuteActivity(r.pctx, p.Revert, r.merged).Get(r.ctx, &reverted); err != nil {
		return 0, nil, err
	}
	if err := workflow.ExecuteActivity(r.pctx, p.Deploy).Get(r.ctx, &r.deployed); err != nil {
		return 0, nil, err
	}
	r.attempt++
	r.build = BuildResult{Branch: r.branch()}
	r.feedback = "Your last change was merged, failed the smoke test after deploy and was reverted:\n" + smoke.Log
	return r.escalate(fmt.Sprintf("smoke test failed on %s, reverted to %s: %s", r.merged, reverted, smoke.Log))
}

func (r *run) finish(outcome string) (step, *Result, error) {
	r.status = outcome
	return 0, &Result{Outcome: outcome, Builds: r.builds, PRURL: r.pr, Risk: r.risk, SHA: r.deployed}, nil
}
