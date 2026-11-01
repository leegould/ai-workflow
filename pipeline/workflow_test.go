package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
)

const longDesc = "Add a /health endpoint that returns 200 with build info"

// fakeAgent stands in for the agents. Readiness: tickets marked THIN are not
// ready until the prompt carries the human's answers. Build: always reports a
// change. It runs against the real step files, so a broken template or schema
// fails these tests.
type fakeAgent struct{}

func (fakeAgent) Run(_ context.Context, step StepDef, prompt, _ string) (AgentResult, error) {
	var v any = map[string]string{"summary": "did the thing"}
	if step.Name == "readiness" {
		rd := Readiness{Ready: true, Reason: "clear outcome and acceptance criteria"}
		if strings.Contains(prompt, "THIN") && !strings.Contains(prompt, "Answers:") {
			rd = Readiness{Reason: "no acceptance criteria", Questions: []string{"What should it return?"}}
		}
		v = rd
	}
	out, err := json.Marshal(v)
	return AgentResult{Output: out}, err
}

// fakeRepo stands in for the target repo, forge and deploy. Each push or merge
// makes a new SHA; smoke decides each smoke test's result.
type fakeRepo struct {
	n        int
	branches []string // build branches, in the order they were checked out
	reverted []string
	smoke    func(n int) bool
	smokes   int
}

func (f *fakeRepo) sha() string { f.n++; return fmt.Sprintf("sha%d", f.n) }

// Checkout records the build branches; readiness always reads main.
func (f *fakeRepo) Checkout(_ context.Context, _, branch string) (string, error) {
	if branch != "main" {
		f.branches = append(f.branches, branch)
	}
	return "", nil
}

func (f *fakeRepo) CommitAndPush(context.Context, string, string, string) (string, error) {
	return f.sha(), nil
}
func (f *fakeRepo) HeadSHA(context.Context, string) (string, error)       { return f.sha(), nil }
func (f *fakeRepo) Merge(context.Context, string, string) (string, error) { return f.sha(), nil }
func (f *fakeRepo) Deploy(context.Context) (string, error) {
	return fmt.Sprintf("deployed%d", f.n), nil
}
func (f *fakeRepo) Revert(_ context.Context, sha string) (string, error) {
	f.reverted = append(f.reverted, sha)
	return f.sha(), nil
}

func (f *fakeRepo) Smoke(context.Context) (SmokeResult, error) {
	f.smokes++
	if f.smoke != nil && !f.smoke(f.smokes) {
		return SmokeResult{Log: "poster exited: exit status 2"}, nil
	}
	return SmokeResult{Passed: true}, nil
}

// harness plays the outside world: CI reporting on each pushed commit, and a
// human answering whenever the workflow asks. Each reacts to activities
// completing, the way the real systems react to labels and pushes.
type harness struct {
	t      *testing.T
	env    *testsuite.TestWorkflowEnvironment
	repo   *fakeRepo
	sha    string // head commit as of the last Build or HeadSHA
	builds int
	pushes int

	// ci returns the CI signals to send after each push (nil: CI never reports).
	ci func(push int, sha string) []CIResult
	// Human replies, called each time the workflow asks.
	onAsk, onEscalate, onApproval func() []HumanInput
}

func newHarness(t *testing.T) *harness {
	var s testsuite.WorkflowTestSuite
	h := &harness{t: t, env: s.NewTestWorkflowEnvironment(), repo: &fakeRepo{}}
	h.env.RegisterActivity(&PipelineActivities{Forge: h.repo, Deployer: h.repo})
	h.env.RegisterActivity(&AgentActivities{StepsDir: "../steps", Runner: fakeAgent{}, Workspace: h.repo})
	h.ci = func(_ int, sha string) []CIResult { return []CIResult{{SHA: sha, Passed: true}} }
	h.env.SetOnActivityCompletedListener(h.completed)
	return h
}

func (h *harness) completed(info *activity.Info, result converter.EncodedValue, err error) {
	if err != nil {
		return
	}
	switch info.ActivityType.Name {
	case "Build":
		var b BuildResult
		h.decode(result, &b)
		h.sha = b.SHA
		h.builds++
	case "HeadSHA":
		h.decode(result, &h.sha)
	case "OpenOrUpdatePR":
		h.pushes++
		if h.ci != nil {
			for _, res := range h.ci(h.pushes, h.sha) {
				h.signal(SignalCI, res)
			}
		}
	case "AskQuestions":
		h.reply(h.onAsk)
	case "Escalate":
		h.reply(h.onEscalate)
	case "RequestApproval":
		h.reply(h.onApproval)
	}
}

func (h *harness) reply(f func() []HumanInput) {
	if f == nil {
		h.t.Errorf("workflow asked a human, but this test has no reply")
		return
	}
	for _, in := range f() {
		h.signal(SignalHuman, in)
	}
}

// signal delivers after a minute of workflow time, so replies arrive in order
// and after the workflow has started waiting.
func (h *harness) signal(name string, v any) {
	h.env.RegisterDelayedCallback(func() { h.env.SignalWorkflow(name, v) }, time.Minute)
}

func (h *harness) decode(v converter.EncodedValue, ptr any) {
	if err := v.Get(ptr); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) run(desc string) Result {
	h.t.Helper()
	h.env.ExecuteWorkflow(TicketWorkflow, Ticket{ID: "T-1", Description: desc})
	if !h.env.IsWorkflowCompleted() {
		h.t.Fatal("workflow did not complete")
	}
	if err := h.env.GetWorkflowError(); err != nil {
		h.t.Fatal(err)
	}
	var r Result
	if err := h.env.GetWorkflowResult(&r); err != nil {
		h.t.Fatal(err)
	}
	return r
}

func once(in ...HumanInput) func() []HumanInput { return func() []HumanInput { return in } }

func ciFails(_ int, sha string) []CIResult {
	return []CIResult{{SHA: sha, Log: "FAIL TestHealth"}}
}

func TestHappyPathMerges(t *testing.T) {
	h := newHarness(t)
	if r := h.run(longDesc); r.Outcome != "deployed" || r.Builds != 1 {
		t.Fatalf("got %+v", r)
	}
}

func TestThinTicketWaitsForAnswers(t *testing.T) {
	h := newHarness(t)
	h.onAsk = once(HumanInput{Comment: "return 200 and the git SHA as JSON"})
	if r := h.run("health THIN"); r.Outcome != "deployed" {
		t.Fatalf("got %+v", r)
	}
}

func TestCIFailureGoesBackToBuilderThenEscalates(t *testing.T) {
	h := newHarness(t)
	h.ci = ciFails
	h.onEscalate = once(HumanInput{Action: ActionAbandon})
	if r := h.run(longDesc); r.Outcome != "abandoned" || r.Builds != MaxReviewRounds {
		t.Fatalf("got %+v", r)
	}
}

func TestRetryWithGuidanceRestartsTheLoop(t *testing.T) {
	h := newHarness(t)
	guided := false
	h.ci = func(push int, sha string) []CIResult {
		if guided {
			return []CIResult{{SHA: sha, Passed: true}}
		}
		return ciFails(push, sha)
	}
	h.onEscalate = func() []HumanInput {
		guided = true
		return []HumanInput{{Action: ActionRetry, Comment: "use the existing router"}}
	}
	if r := h.run(longDesc); r.Outcome != "deployed" || r.Builds != MaxReviewRounds+1 {
		t.Fatalf("got %+v", r)
	}
}

// A taken-over ticket comes back through CI, review and the risk gate; the
// build agent doesn't run again.
func TestTakeOverThenResumeRunsTheRemainingSteps(t *testing.T) {
	h := newHarness(t)
	resumed := false
	h.ci = func(push int, sha string) []CIResult {
		if resumed {
			return []CIResult{{SHA: sha, Passed: true}}
		}
		return ciFails(push, sha)
	}
	h.onEscalate = func() []HumanInput {
		resumed = true // the human's fix is on the branch by the time CI runs
		return []HumanInput{{Action: ActionTakeOver}, {Action: ActionResume}}
	}
	r := h.run(longDesc)
	if r.Outcome != "deployed" || r.Builds != MaxReviewRounds || h.pushes != MaxReviewRounds+1 {
		t.Fatalf("got %+v, pushes %d", r, h.pushes)
	}
}

func TestMediumRiskRetryThenApprove(t *testing.T) {
	h := newHarness(t)
	asked := 0
	h.onApproval = func() []HumanInput {
		asked++
		if asked == 1 {
			return []HumanInput{{Action: ActionRetry, Comment: "split the migration into its own PR"}}
		}
		return []HumanInput{{Action: ActionApprove}}
	}
	r := h.run(longDesc + " plus a migration")
	if r.Outcome != "deployed" || r.Risk != RiskMedium || r.Builds != 2 {
		t.Fatalf("got %+v", r)
	}
}

func TestApproveIgnoredOutsideTheGate(t *testing.T) {
	h := newHarness(t)
	h.ci = ciFails
	h.onEscalate = once(HumanInput{Action: ActionApprove}, HumanInput{Action: ActionAbandon})
	if r := h.run(longDesc); r.Outcome != "abandoned" {
		t.Fatalf("got %+v", r)
	}
}

// A late failure for an older commit must not send the ticket back to build.
func TestStaleCIResultIgnored(t *testing.T) {
	h := newHarness(t)
	h.ci = func(_ int, sha string) []CIResult {
		return []CIResult{{SHA: "old-commit", Log: "FAIL"}, {SHA: sha, Passed: true}}
	}
	if r := h.run(longDesc); r.Outcome != "deployed" || r.Builds != 1 {
		t.Fatalf("got %+v", r)
	}
}

func TestCITimeoutEscalates(t *testing.T) {
	h := newHarness(t)
	h.ci = nil
	h.onEscalate = once(HumanInput{Action: ActionAbandon})
	if r := h.run(longDesc); r.Outcome != "abandoned" || r.Builds != 1 {
		t.Fatalf("got %+v", r)
	}
}

// A change that breaks the deployed app is reverted before anyone is asked,
// and the retry starts a fresh branch from main.
func TestSmokeFailureRevertsThenRetriesOnFreshBranch(t *testing.T) {
	h := newHarness(t)
	h.repo.smoke = func(n int) bool { return n != 1 }
	h.onEscalate = once(HumanInput{Action: ActionRetry, Comment: "keep the -in flag"})
	r := h.run(longDesc)
	if r.Outcome != "deployed" || r.Builds != 2 || len(h.repo.reverted) != 1 {
		t.Fatalf("got %+v, reverted %v", r, h.repo.reverted)
	}
	if want := []string{"ai/T-1/1", "ai/T-1/2"}; !slices.Equal(h.repo.branches, want) {
		t.Fatalf("branches %v, want %v", h.repo.branches, want)
	}
}

// TestReplayInFlightHistory replays a real history captured from the dev
// server against the current code. It fails if a code change would break
// tickets already in flight.
func TestReplayInFlightHistory(t *testing.T) {
	r := worker.NewWorkflowReplayer()
	r.RegisterWorkflow(TicketWorkflow)
	if err := r.ReplayWorkflowHistoryFromJSONFile(nil, "testdata/awaiting-approval.json"); err != nil {
		t.Fatal(err)
	}
}
