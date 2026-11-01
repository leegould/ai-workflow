# ai-workflow: rules for agents

Read README.md for what this is and why. These are the rules for changing it.
The reviewer uses this file as its rubric, so keep it to rules that matter.

## Before you finish

Run `make check` and make it pass. It runs lint, `workflowcheck`, `govulncheck`,
tests with `-race`, and a coverage minimum. Run `make fmt` to fix formatting.
Never weaken a gate to get green: don't edit `.golangci.yml`, `Makefile`,
`scripts/`, `.github/` or `tools/go.mod`, don't delete tests and don't add
`t.Skip`, unless the task is explicitly about that.

## Decision log

When a choice is made (a design direction, a tool, a trade-off, a rule), append it
to `DECISION_LOG.md` under today's date: the decision, what else was considered, and
why. Record how it was arrived at, not just the outcome.

## Temporal

- **Workflow code must be deterministic.** In `TicketWorkflow` and anything it
  calls: no `time.Now`/`time.Sleep` (use `workflow.Now`/`workflow.Sleep`), no
  goroutines or channels (use `workflow.Go`/`workflow.Channel`), no ranging over
  maps where order matters, no `rand`, no I/O. `make check` runs `workflowcheck`.
- **Every side effect is an activity**: LLM calls, git, HTTP, Linear, GitHub.
- **Pipeline activities vs agent activities.** Anything needing a repo,
  toolchain or credentials goes on `AgentActivities` (`AgentQueue`). Ticket and
  API work goes on `PipelineActivities` (`PipelineQueue`). Queue names live only
  in `pipeline/queues.go`.
- **Changing the workflow can break in-flight tickets.** Keep
  `TestReplayInFlightHistory` passing. If a change is intentionally
  incompatible, gate it with `workflow.GetVersion` rather than editing history.
- Long-running activities must heartbeat (`activity.RecordHeartbeat`).

## Go

- Wrap errors with context: `fmt.Errorf("build ticket %s: %w", id, err)`.
  Compare with `errors.Is`/`errors.As`, never `==` or string matching.
- Pass `context.Context` as the first argument through anything that does I/O.
- No package-level mutable state. Configuration comes in via structs.
- Small packages with clear jobs. `pipeline/` is the domain; `worker/` and
  `ticket/` are thin `main`s that wire things up.
- Tests: table-driven where there are several cases. Workflow behaviour is
  tested with `testsuite` and time-skipping, not real sleeps. Assert on
  outcomes, not that a function was called.
- Comments say why, not what. Match the density of the surrounding code.
