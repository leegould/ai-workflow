# ai-workflow

A team-usable AI development pipeline (diagrams of the flow: [FLOW.md](FLOW.md)). A Linear ticket goes in; a reviewed, risk-assessed,
merged and deployed change comes out, with humans pulled in only where they're needed.

Built in Go on Temporal. Started 2026-10-26 as a hands-on exploration of what it takes
for a team, not just one person in an editor, to run coding agents through a real
pipeline with gates the team controls. Any company adopting AI development will need
something like it.

**Status (2026-10-29):** a ticket typed at the command line becomes a deployed change.
The readiness and build agents are real, as are CI, merge, deploy, smoke test and
revert. They run against a local stand-in for GitHub and a toy poster app, so every
merge has a visible result. Review, risk and Linear are still stubs. See
[Demo tickets](#demo-tickets) to try it.

## The flow

```
Linear webhook (ai-ready label) → StartWorkflow(id = ticket ID)
  Readiness agent ── gaps? → ask questions → wait for human answer → recheck
  loop round 1..3:
     Build agent (checkout of ai/<ticket>/<attempt>) → pipeline commits + pushes → PR
     wait for CI signal for that SHA ── red → logs back to the builder, next round
     Review agent ── changes? → comments back to the builder, next round
  3 rounds used, or CI silent for 30 min → escalate, wait for a human:
     retry (with guidance) · take over (human pushes, then resume) · abandon
  Risk assessment → low: merge | medium+: wait for human approval (or retry/take over/abandon)
  Merge → deploy → smoke test ── fails → revert main, redeploy, escalate (retry = fresh branch)
```

Every path ends in **deployed** or **abandoned**. There are no dead ends, and human work
goes back through CI, review and the risk gate like agent work does. Diagrams:
[FLOW.md](FLOW.md).

## Design decisions

- **Temporal is the source of truth for state; Linear is a view for humans.** Driving the
  state machine off labels breaks when a person edits a label, and racing webhooks conflict.
- **Labels only where a human must act:** `ai-ready` (trigger), `ai-needs-info`,
  `ai-needs-decision`, `ai-escalated`, `ai-needs-approval`. Coarse progress uses Linear
  states (In Progress → In Review → Done). No building/reviewing/testing labels: they'd
  churn every few minutes. Post a comment per round instead; the Temporal UI has the detail.
- **Workflow ID = ticket ID.** Duplicate webhooks can't start a second run, so idempotency
  comes for free.
- **Two breakout conditions:** a 3-round cap on the build/review loop, and a token/cost
  budget per ticket. A loop that never converges is expensive (one spec→build→verify→review
  run on another project took ~676k tokens).
- **Test agent writes acceptance tests from the ticket, blind to the implementation.** CI
  runs the tests; it doesn't decide whether they test the right thing. A builder writing
  its own tests is marking its own homework.
- **Risk = deterministic floor + LLM judgement.** Signals: paths touched (from a risk map in
  the repo: auth, payments, migrations, protos/public API, infra config), diff size, coverage
  change. **The LLM can raise risk above the floor but never lower it.** Output includes a
  short blast-radius note: what could break, who'd notice, how to roll back.
- **Post-merge CI failure → auto-revert first,** then reopen the ticket with the logs and
  let it go round the loop again. No fixing forward on main with an agent, and no separate
  infra-checker agent. Flaky/infra failures go to a human.
- **Smoke test failure after deploy → revert, redeploy, then escalate.** Main goes back to
  known-good before anyone is asked. A retry starts a fresh branch, because git won't
  re-merge a reverted branch (D41).
- **The pipeline owns git; the agent only edits.** The push is what starts CI, so the
  activity commits and pushes, not the agent. CI's checks live with CI, not in the target
  repo, so the agent can't loosen the gate it's judged by (D40).
- **Shared guidelines.** Repo-level CLAUDE.md plus golangci-lint in CI. The reviewer gets the
  same guidelines file as its rubric, so builder and reviewer judge against the same standard.
- **Agents don't share a conversation.** They hand off through artifacts: the branch, the
  diff, review comments, CI logs. Fresh context per agent makes the reviewer more
  independent. State between steps lives in the repo and in Temporal, never in an agent's
  context window.
- **Push the branch after every step,** so any worker can pick up the next activity and a
  dead worker loses nothing. (The alternative is pinning a ticket's activities to the worker
  holding its git worktree via a worker-specific task queue. That's the "sticky worker"
  pattern, and it's more fragile.)

## Why Temporal

The problem is a **long-running business process with humans in the loop**: a ticket can
wait days for answers, there's a capped review loop, and external events arrive from Linear
and CI. So the tool needed is durable execution; the agent framework is a detail inside
each step.

Within that category, Temporal because:

- Workflows are plain Go code, so loops and caps are just code, testable with Temporal's
  test suite (see `pipeline/workflow_test.go`: three days of human silence runs in under a
  second).
- The event history is an audit trail of every agent decision.
- It runs anywhere, so agents get real tooling (git, Go, Docker) on our own workers.
- Mature at scale, with versioning tools for changing workflows that are already running.

Honest costs: it's another stateful system to operate unless you pay for Temporal Cloud,
and workflow code must be deterministic, so every LLM call goes in an activity.

When I *wouldn't* pick it: with zero ops capacity and no existing Temporal, I'd seriously
look at Cloudflare Workflows, or DBOS on an existing Postgres. The orchestrator is the easy
part to swap. The agents and the gates are the real design.

### Alternatives considered

**Durable-execution engines (same job):**
- **Cloudflare Workflows + Sandbox:** zero ops, serverless; locked into Cloudflare's
  runtime, agents must run in its containers.
- **AWS Step Functions:** managed, but flows are JSON/YAML state machines; painful for
  loops with caps and custom logic. AWS lock-in.
- **Restate:** lightweight single binary, genuinely nice; younger, smaller ecosystem.
- **Inngest / Trigger.dev / Hatchet:** developer-friendly, mostly TypeScript-first; good for
  app background jobs, less proven for long, complex processes.
- **DBOS:** durable execution as a Postgres-backed library; no separate server.

**Build it yourself:** a Postgres state machine plus a queue, or NATS JetStream. Works, but
you rebuild retries, timers, human waits and history by hand, which is what Temporal is.

**Agent frameworks** (LangGraph, Claude Agent SDK, OpenAI Agents SDK): orchestrate *within*
one agent run. They can live *inside* a Temporal activity; they aren't the multi-day process.

**Buy it** (GitHub Copilot coding agent, Linear's own agent, Devin, Cursor background agents,
Claude Code GitHub Action): fastest start, but no control over the gates: no custom risk
floor, no loop cap, no audit trail you own.

### Cloudflare in more detail

"Durable workers" can mean several products:

- **Durable Objects:** a stateful actor with its own SQLite, one instance per ID (e.g. per
  ticket). Alarms, hibernation.
- **Agents SDK:** built on Durable Objects; per-agent state, scheduling, chat history.
- **Workflows:** durable execution with steps, retries, `sleep`, `waitForEvent`. The direct
  Temporal equivalent.
- **Sandbox SDK / Containers:** real Linux containers, needed because a plain Worker can't
  run git, Go or Claude Code (CPU/time limits, no filesystem).

The trade: **Cloudflare = less ops, but inside its runtime. Temporal = more ops, but agents
run anywhere with full tooling.** Temporal orchestrates; it doesn't provide the sandbox. The
workers you run *are* the sandbox: a Mac now, containers on Kubernetes for a team.

### Determinism: Temporal vs Cloudflare Workflows

Both need deterministic orchestration code with side effects pushed into steps/activities.
In Cloudflare Workflows the function re-runs from the top after a restart, and completed
`step.do("name", fn)` calls return their saved result, matched by step name. So step names
must be deterministic, code outside steps re-runs, and step results are serialised and
size-capped.

| | Temporal | Cloudflare Workflows |
|---|---|---|
| Replay model | Replays event history; checks code issues the same commands | Re-runs the function; skips completed steps by name |
| Breaking the rules | Non-determinism error: the workflow blocks loudly | No check: can silently diverge |
| Helpers | `workflow.Now`, `workflow.Sleep`, `workflow.Go`, side-effect APIs | You're on your own outside steps |
| Changing code mid-flight | `workflow.GetVersion` for running workflows | Little tooling |

This matters here because a ticket's workflow can outlive several deploys of the pipeline
itself.

## Layout

```
pipeline/workflow.go       the process: readiness loop, capped build/CI/review loop, risk gate,
                           merge, deploy + smoke test (revert on failure)
pipeline/activities.go     every side effect, split into PipelineActivities and AgentActivities, over
                           small interfaces (Workspace, Forge, Deployer) so local and GitHub are swappable
pipeline/queues.go         task queue names + ScheduleToStartTimeout (the one place routing is defined)
pipeline/agent.go          agent steps: StepDef loaded from steps/<name>.md, Runner interface, Runners by name
pipeline/claudecli.go      Runner for Claude Code headless (structured output, budget cap, tool allowlist, heartbeats)
steps/                     agent step definitions: YAML settings (runner, model, tools, permissions, budget,
                           schema) + prompt. Readiness (read-only, on main) and build are real; review/risk are still stubs.
target/                    local stand-in for GitHub, CI and deploy: a bare repo, a push hook, a deploy dir.
                           Lives in ~/.cache/ai-workflow-target (D40). target/setup creates it
ci/local                   CI for the local target: the push hook runs it; signals the result to the workflow
sample/poster              the toy app the pipeline works on (own go.mod, so our gates don't include it)
pipeline/workflow_test.go  every path through the workflow (happy, thin ticket, CI red → escalate, retry,
                           takeover → resume, approval gate, stale CI result, CI timeout, smoke fail →
                           revert → fresh branch) plus a replay test against a real captured history
target/local_test.go       the local stand-in against real git and go: CI pass/fail, merge, deploy,
                           smoke, revert
pipeline/testdata/         captured workflow histories for replay tests
worker/pipeline            runs the workflow + API activities: Linear (stub), forge, deploy (queue ai-pipeline)
worker/agent               runs CheckReadiness/Build/Review/AssessRisk (queue agent-sandbox); the sandbox.
                           Run from the repo root (reads steps/); -concurrency caps agent runs (default 2)
ticket/start               starts a workflow by hand, standing in for the Linear webhook
```

### Workers and task queues

- **The server stores history, queues and timers; it runs none of our code.** Workers run
  both the workflow code (replaying history to rebuild state) and the activities. Clients
  (`ticket/start`, a stand-in event source until the Linear webhook exists, and the
  `temporal` CLI) start, signal and query workflows.
- **Workers pull; the server never pushes.** A worker long-polls its task queue over an
  outbound connection, so it can sit behind a firewall next to its secrets. Scale by
  starting more workers on the same queue.
- **Two queues so only the sandbox holds dangerous permissions.** The workflow picks the
  queue per activity via `ActivityOptions.TaskQueue`.
- **Routing is by convention, so make it fail fast.** Queue names are unvalidated strings,
  and an activity on a queue nobody polls waits forever by default. The names live in
  `pipeline/queues.go` so a typo is a compile error, and every activity has a
  `ScheduleToStartTimeout` (10 min) so a missing worker pool fails visibly. Check live
  pollers with `temporal task-queue describe --task-queue agent-sandbox`.
- **Replay tests guard in-flight tickets.** `TestReplayInFlightHistory` replays a real
  history against the current code. Moving activities to a new queue passed (the queue
  isn't part of the determinism check); removing a step fails with `TMPRL1100`. Capture
  more with `temporal workflow show -w <id> --output json > pipeline/testdata/<name>.json`.

The remaining stubs are driven by the ticket description so their paths can be exercised:
`never-approve` makes review always ask for changes (forcing escalation), and `migration`
forces medium risk.

## Quality gates

Rules live in the repo being changed, and anything a tool can check is checked by a tool,
not an agent. The same applies to target repos the build agent works on: each owns its
rules, and the pipeline reads them rather than baking its own in.

- **Mechanical rules → tool config, enforced by `make check`** (agents run it before opening
  a PR; CI runs it on every push):
  - `golangci-lint` (`.golangci.yml`): standard set plus gosec, errorlint, wrapcheck,
    gocritic, revive and others. Formatting via gofmt/goimports (`make fmt`).
  - **`workflowcheck`** (Temporal's own analyser): flags non-deterministic calls
    (`time.Now`, goroutines, map ranges, `rand`) reachable from workflow code. Verified: a
    `time.Now()` in `TicketWorkflow` fails it.
  - `govulncheck`: on first run it found real HTTP/2 and x509 vulnerabilities in Go 1.27.1
    and `x/net`. Fixed by pinning `toolchain go1.27.3` in `go.mod` (Go downloads it
    automatically) and bumping `x/net`.
  - `go test -race` plus a coverage minimum (70%) on domain code (`pipeline/`, `target/`).
    The `main`s in `worker/`, `ticket/`, `ci/` and `target/setup` are excluded as thin
    wiring; keep logic out of them.
- **Judgement rules → `CLAUDE.md`.** Builder instructions and the reviewer's rubric: the
  same file, so both judge against the same standard.
- **Promote repeat review findings into lint rules** over time.
- **Anti-gaming: `make guard BASE=<ref>`** (PRs only). Fails on deleted test files or new
  `t.Skip`; warns when the gates themselves change (`.golangci.yml`, `Makefile`, `scripts/`,
  `.github/`, `CLAUDE.md`, `tools/go.mod`). The risk assessment should treat that as high risk.
- **Tool versions pinned in `tools/go.mod`** (run via `go tool -modfile=tools/go.mod`), so
  there are no global installs and CI and agents use identical versions.

Not yet: diff coverage (coverage on changed lines, harder to game than a global %) and
mutation testing (`gremlins`) for medium-risk changes and above. Both make more sense on the
target repo than here.

## Running it

Needs Go and the Temporal CLI (`brew install temporal`; on this Mac brew lives at
`/opt/homebrew/bin`, which may not be on PATH).

```sh
# 1. Dev server (UI at http://localhost:8233); history persists in .temporal/dev.db
temporal server start-dev --db-filename .temporal/dev.db

# 2. The target: a local "GitHub" seeded with sample/poster, CI on push (once; RESET=1 to wipe)
make target

# 3. Workers (separate terminals, from the repo root)
go run ./worker/pipeline
go run ./worker/agent

# 4. Start a ticket (more in "Demo tickets" below)
go run ./ticket/start -id ENG-30 -title "Dark mode poster" \
  -desc "Add a theme field to poster.json; dark = light text on near-black. Tests for both."

# All gates (lint, workflowcheck, govulncheck, race tests, coverage)
make check
```

Stop for the night: `pkill -f '\.temporal/(pipeline|agent)-worker'; pkill -f 'temporal server start-dev'`
(when the workers were started as built binaries in `.temporal/`). Parked tickets survive
in `.temporal/dev.db`.

### Demo tickets

Each one shows a different path. Watch them in the UI (http://localhost:8233) and
`tail -f .temporal/*-worker.log`. In the commands below, `T` is the Temporal CLI:
`T=/opt/homebrew/bin/temporal`.

| Ticket | Shows | What you do |
|---|---|---|
| `-title "Dark mode poster" -desc "Add a theme field to poster.json; dark = light text on near-black, light border. Set it to dark. Tests for both."` | **Happy path.** Ready → build → CI green → merge → deploy → smoke → deployed (ENG-30: ~45 s, $0.23) | Open the poster |
| `-desc "make it purple"` | **Readiness reads the code first.** It worked out "blue" from the existing themes with no questions (ENG-32). Something the code can't answer still gets questions, e.g. `-desc "add the sponsor's logo"` | Answer: `$T workflow signal -w ticket-<ID> --name human --input '{"Comment":"Background purple, white text"}'` |
| `-desc "Add a subtitle field to the poster. never-approve"` | **Escalation.** The review stub never approves, so 3 real build rounds (~$0.60), then escalated | Retry with guidance, take over, or abandon (below) |
| `-desc "Add a venue field shown under the date. Mention the migration of poster.json."` | **Risk gate.** The risk stub says medium, so it waits for approval | `--input '{"Action":"approve"}'`, or retry/abandon |

Human decisions are signals on `human`:

```sh
$T workflow query  -w ticket-ENG-33 --type status
$T workflow signal -w ticket-ENG-33 --name human --input '{"Action":"retry","Comment":"keep it to one field"}'
$T workflow signal -w ticket-ENG-33 --name human --input '{"Action":"abandon"}'

# Take over: push your own fix to the ticket's branch; CI runs on your push. Then resume,
# and it goes through review and the risk gate like the agent's work would.
$T workflow signal -w ticket-ENG-33 --name human --input '{"Action":"takeover"}'
git clone ~/.cache/ai-workflow-target/origin.git /tmp/poster && cd /tmp/poster
git checkout ai/ENG-33/1    # edit, commit, git push
$T workflow signal -w ticket-ENG-33 --name human --input '{"Action":"resume"}'
```

What happened, after any of them:

```sh
open ~/.cache/ai-workflow-target/deploy/poster.html                       # the deployed poster
git --git-dir ~/.cache/ai-workflow-target/origin.git log --oneline --graph main
cat ~/.cache/ai-workflow-target/ci.log                                    # CI runs from the push hook
make target RESET=1                                                       # start the poster over
```

Not demoable by hand yet: CI red going back to the builder, and smoke failure → revert.
The real agent usually gets CI green first time, and smoke only fails if the binary
breaks. Both are covered by workflow tests.

Minikube/kind with Temporal's Helm chart is a later step, worth it only to show team
deployment (closer to a real GKE setup). The Helm install needs a real persistence store
(Postgres/Cassandra) and Elasticsearch for visibility. For a real team: Temporal Cloud or
self-hosted on the cluster, with only the workers (which hold git and agent access) in our
infrastructure.

## Linear

The free plan is enough: unlimited members, 2 teams, 250 issues (archived ones shouldn't
count), and the API/webhooks. Setup: Settings → API → personal API key. Webhooks need a
public URL (`cloudflared` or `ngrok` tunnel locally); poll the API until then.

Linear now has its own agent platform. Why orchestrate it ourselves? To control the gates,
the risk floor and the audit trail. Linear is just the ticket source.

## Next steps

1. ~~**Readiness reads the repo**~~: done (D47). "Make it blue" went from 3 questions to none.
2. **Real review agent** with the target's CLAUDE.md as rubric; blind acceptance-test agent.
3. **Deterministic risk map** (paths touched, diff size) + LLM risk note.
4. **Rollback as its own action:** a deploy workflow per environment that records each
   deploy and can roll back to the previous binary on a human's say-so (D44).
5. **Classify failures:** infrastructure (retry, alert the platform owner) vs code (feedback
   to the builder). A failed Deploy build should revert, not fail the ticket (D41, D45).
6. **Linear integration:** questions as comments, replies as signals, a nudge/close timer
   on every human wait, and who-answered on every signal (D46).
7. Token budget as a second breakout condition.
8. A GitHub-backed Forge and a webhook receiver for CI behind the same interfaces, with
   branch protection and CODEOWNERS as the backstop (D41).
9. Per-step environments: images or fresh sandboxes per agent run (D45).

## Principles

What this build has taught so far, in short:

- The LLM can raise risk but not lower the deterministic floor.
- Revert first, never fix forward on main with an agent.
- Where does state live between agent steps? In the repo and in Temporal, never in an
  agent's context window.
- Multi-day human-in-the-loop processes are unit-testable with Temporal's time-skipping.
- **Director agent or pipeline?** Both, at different layers. Put control flow in code
  where the order is known and the gate matters; let the agent decide where the path is
  open-ended. A team needs a predictable *process*, not predictable *output* (D42).
- **Locks sit at boundaries the agent can't change:** branch protection, CODEOWNERS,
  credentials, harness permissions, the workflow. Prompts are requests.
- **Start with a thin real slice, not the whole factory.** The all-stubs version was hard
  to reason about even for its author. One real ticket end to end made it click (D40).
- **Agents need the right context, not just tools.** Readiness running blind asked
  questions the code would have answered (D43).
- **Infra failure is not an agent failure.** Don't feed a registry outage back to the
  builder or burn one of its rounds (D45).
- **Roll back the deploy first, revert the change second.** Neither involves an agent (D44).
