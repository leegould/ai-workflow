# Decision log

What we chose, what else was on the table, and why. Newest decisions at the bottom.
README.md describes the current design; this file records how it got that way.
VENDOR_COMPARISON.md holds the research behind D30.

Status: **Decided** (in place) · **Agreed** (direction set, not built) · **Open** (still to decide)

---

## 2026-10-26

### D1. Use durable execution for orchestration, and Temporal for that
**Decided.** The problem is a long-running business process with humans in the loop:
tickets can wait days for answers, the review loop is capped, and events arrive from
Linear and CI. That calls for durable execution; the agent framework is a detail
inside each step.
- **Considered:** Cloudflare Workflows/Durable Objects, AWS Step Functions, Restate, Inngest/Trigger.dev/Hatchet, DBOS, a home-made
  Postgres/NATS state machine, agent frameworks (LangGraph etc.), and buying (Copilot coding
  agent, Linear's agent, Devin, Cursor).
- **Why Temporal:** workflows are plain Go, so loops and caps are just code; testable with
  time-skipping; the event history is an audit trail; workers run anywhere with real
  tooling; it's mature.
- **Costs accepted:** another stateful system to run (or pay for Cloud), and workflow code
  must be deterministic.
- **When we'd choose differently:** zero ops capacity and no existing Temporal → Cloudflare
  Workflows or DBOS on an existing Postgres.

### D2. Temporal holds the state; Linear is a view of it for humans
**Decided.** Driving the state machine off labels breaks when a person edits a label, and
racing webhooks conflict.
- **Considered:** labels for every stage (building/reviewing/testing), as first sketched.
- **Why not:** those labels would churn every few minutes and duplicate what Temporal knows.
- **Result:** labels only where a human must act (`ai-ready`, `ai-needs-info`,
  `ai-needs-decision`, `ai-escalated`, `ai-needs-approval`); Linear states for coarse
  progress; a comment per round.

### D3. Workflow ID = ticket ID
**Decided.** Duplicate webhooks can't start a second run, so idempotency comes free.

### D4. Cap the build/review loop at 3 rounds, then escalate
**Decided** (cap built; escalation is a stub). **Agreed:** add a per-ticket token/cost
budget as a second breakout condition, since a loop that doesn't converge is expensive
(a spec→build→verify→review run on another project took ~676k tokens).

### D5. Blind acceptance-test agent, separate from the builder
**Agreed.** CI runs tests but doesn't judge whether they test the right thing. A builder
writing its own tests marks its own homework, so a separate agent writes acceptance tests
from the ticket without seeing the implementation.

### D6. Risk = deterministic floor + LLM judgement; the LLM can raise risk, never lower it
**Agreed** (stub in place). Signals: paths touched (from a risk map in the target repo),
diff size, coverage change. Low → auto-merge; medium and above → a human, at least until
the team trusts what medium means. Output includes a blast-radius note.

### D7. Post-merge CI failure → auto-revert, not fix-forward
**Agreed.** Revert, reopen the ticket with the logs, and let it go round the loop again.
- **Considered:** sending it back to the build agent on main; a separate infra-checker agent.
- **Why not:** fixing forward on main with an agent is where things go badly wrong. Flaky or
  infra failures go to a human instead. Smoke-test failure after deploy: roll back and
  escalate, no retries.

### D8. Agents hand off through artifacts, not a shared conversation
**Agreed.** The branch, diff, review comments and CI logs are the memory between steps.
Fresh context per agent makes the reviewer more independent. State lives in the repo and
in Temporal, never in an agent's context window.

### D9. Push the branch after every step rather than pinning a ticket to one worker
**Agreed.** Any worker can then pick up the next step, and a dead worker loses nothing.
- **Considered:** the sticky-worker pattern (a worker-specific task queue that keeps a
  ticket on the machine holding its worktree). More fragile.

### D10. Temporal dev server locally, not minikube
**Decided.** `temporal server start-dev` is one binary with UI and SQLite; we spend the
time on the workflow, not infrastructure.
- **Considered:** minikube/kind + Temporal's Helm chart (closer to a real GKE setup).
- **Why not yet:** a day of ops for no gain yet; Helm needs real persistence
  and Elasticsearch. Revisit to show team deployment. For a real team: Temporal Cloud or
  self-hosted, with only the workers in our infrastructure.
- History persists in `.temporal/dev.db`, so parked tickets survive restarts.

### D11. Linear free plan is enough
**Decided.** Unlimited members, 2 teams, 250 issues, API and webhooks. Poll until there's a
public URL (`cloudflared`/`ngrok`) for webhooks.
- **Considered:** Linear's own agent platform. We orchestrate ourselves to control the
  gates, the risk floor and the audit trail; Linear is only the ticket source.

### D12. Agents run as local processes + git worktrees for now; containers later
**Agreed.** Sandboxed containers are right for a team (isolation, secrets) but add a day
of setup. Local first for the demo, containers named as the next step.

### D13. Start with stubbed activities and build a thin slice end to end
**Decided.** Every side effect is a stub steered by keywords in the ticket (`never-approve`,
`migration`, short descriptions), so every path can be exercised and tested before any
real agent exists.

### D14. Order of work: Linear integration before the real build agent
**Agreed.** Linear is quick and turns this into "tickets in" end to end before the
expensive agent part.

### D15. Split into two workers on two task queues
**Decided.** `worker/pipeline` (workflow + cheap API activities, no repo access) and
`worker/agent` (Build/Review/AssessRisk, the sandbox). Only the sandbox will hold
dangerous permissions, and each pool can scale separately.
- **Came from:** realising workers pull from queues, so the workflow chooses where each
  activity runs.

### D16. Queue names in one constants file; fail fast with ScheduleToStartTimeout
**Decided.** Temporal doesn't validate queue names, and an activity on a queue nobody
polls waits forever. `pipeline/queues.go` holds the names (a typo is a compile error),
and every activity gets a 10-minute `ScheduleToStartTimeout`, so a missing worker pool
fails visibly instead of hanging.

### D17. Replay tests against real captured histories
**Decided.** Before the worker split we exported ENG-3's history (parked at the approval
gate) and replay it in `TestReplayInFlightHistory`.
- **Finding:** moving activities to a different queue *doesn't* break in-flight
  workflows; removing a step does (`TMPRL1100`, confirmed in a scratch copy).
- **Rule:** keep the replay test passing; gate intentional breaks with `workflow.GetVersion`.

### D18. Folder layout: `pipeline/`, `worker/`, `ticket/`
**Decided.**
- `cmd/worker` → `worker/pipeline` + `worker/agent`.
- `cmd/start` → briefly `client/start`, then **`ticket/start`**. "Client" felt like part of
  the process; it's really a stand-in event source for Linear, and the Linear poller or
  webhook will sit alongside it.
- Note: Go convention is `cmd/`. We chose readability for this project.

### D19. Quality rules live in the repo being changed; tools check what tools can
**Decided.** Applies to this repo and to every target repo the build agent works on. Each
target repo owns its rules, and the pipeline reads them rather than baking its own in.
- Mechanical rules → tool config, enforced by `make check`: golangci-lint, `workflowcheck`,
  `govulncheck`, `go test -race`, coverage minimum. Agents run it before opening a PR; CI
  is the authority.
- Judgement rules → `CLAUDE.md`, which is both builder instructions and reviewer rubric.
- Repeat review findings get promoted into lint rules over time.
- **Why it matters:** the gates encode the language judgement, so a reviewer doesn't need
  to be a Go specialist to review what they can't catch.

### D20. Temporal's `workflowcheck` enforces determinism
**Decided.** It turns "remember not to call `time.Now` in workflow code" into a failing
check. Confirmed: an injected `time.Now()` fails it.

### D21. Tool versions pinned in `tools/go.mod`, run via `go tool`
**Decided.** No global installs; CI and agents use identical versions. A separate module
keeps linter dependencies out of the main `go.mod`.

### D22. Pin the Go toolchain to a patched version
**Decided.** `govulncheck`'s first run found real HTTP/2 and x509 vulnerabilities in Go
1.27.1 and `x/net`. We pinned `toolchain go1.27.3` in `go.mod` (downloaded automatically,
system Go untouched) and bumped `x/net` to v0.60.0. System Go is still 1.27.1;
`brew upgrade go` at some point.

### D23. Coverage measured on `pipeline/` only (70% minimum)
**Decided, and worth questioning.** Including the thin `main`s (0%, need a live server)
dragged the total to 58.4%. We excluded them and documented it in the Makefile.
- **The tension:** "exclude code to pass coverage" is exactly what an agent might do to get
  green. It's acceptable here because it's explicit, documented and reviewable, and the
  rule says keep logic out of the `main`s.
- **Agreed for target repos:** diff coverage (changed lines only) and mutation testing
  (`gremlins`) for medium risk and above. A global % is easy to game.

### D24. Anti-gaming guard on PRs
**Decided** (not yet exercised: needs commits and a base branch). `scripts/guard.sh` fails
on deleted test files or new `t.Skip`, and warns when the gates themselves change
(`.golangci.yml`, `Makefile`, `scripts/`, `.github/`, `CLAUDE.md`, `tools/go.mod`). The risk
assessment should treat gate changes as high risk.

### D25. Agent worker concurrency: cap it, then consider a resource tuner
**Open.** Workers take as many activities as allowed by default. Agent runs are heavy and
vary in cost, so cap `MaxConcurrentActivityExecutionSize` (e.g. 2 per box), or use the Go
SDK's resource-based tuner. Use `TaskQueueActivitiesPerSecond` to stay under LLM API rate
limits across all workers. Observability: alert on schedule-to-start latency, autoscale on
backlog (KEDA).

---

## 2026-10-27

### D26. Feedback loops at three levels
**Agreed direction**, prompted by Warp's pitch on agent feedback loops.
1. Within a ticket: the capped review loop (built).
2. Across tickets: a retro activity records review findings, rounds and CI failures; when a
   pattern repeats, an agent opens a PR to CLAUDE.md or lint config. Gate changes are high
   risk, so a human approves.
3. Across the process: rounds per ticket, escalation rate, and **how often humans overturn
   the risk rating** become the evidence for loosening or tightening gates.
- **Next concrete step:** a retro activity that writes rounds and findings to a file.

### D27. Buy vs build is decided per stage, not once
**Agreed principle.** Buy the commodity that improves every quarter without us (the coding
agent). Build what encodes how *we* decide something is safe to ship (gates, risk floor,
routing, rules). All-in-one products (Warp etc.) are faster to start, but the process ends
up fitting their shape.
- **Adapting an existing process:** map the real process; put agents at existing handoffs;
  keep every human gate at first and remove them one at a time on evidence.

### D28. Keep this decision log
**Decided.** The reasoning and the alternatives matter as much as where we ended up,
and they're what gets lost first.

### D29. Scope: the core is "ready ticket in → merged change + verified-deploy signal out"
**Decided.** Prompted by a video of an in-house pipeline with much more at both ends:
ingestion/triage (Slack, Sentry, webhooks, humans) → refine → prioritise before build,
and full staging/production deploy plus observability after.
- **Front end left out:** the Linear ticket is the contract. Every intake channel is just
  another event source that creates a ticket (it sits beside `ticket/start`), so it can be
  added later without touching the core. Prioritisation stays with humans in Linear.
- **But watch refine:** our readiness check is a thin version of it, and input quality is
  the biggest driver of whether the build loop converges. Revisit if loops don't converge.
- **Back end left out:** deploy mechanics and observability. Teams already have CI/CD; the
  pipeline hands off to it rather than replacing it.
- **Kept, as an interface (stub):** watch the deploy, receive pass/fail, revert on failure.
  It's part of "is this change safe", it closes the loop, and it feeds level-3 feedback.
- **Reading vendors:** products differ most at the ends (integrations sell); weigh their
  core design more than their integration list.

### D30. Vendor comparison: what we take from it
**Agreed** (none built yet). Full notes and sources in `VENDOR_COMPARISON.md`. Compared
Warp Factories, the GitHub Copilot coding agent, Linear coding sessions and Factory Droids.
- **Biggest finding: no vendor auto-merges by default.** Our low-risk auto-merge (D6) is
  further than any of them. We keep it as the goal state, earned with overturn-rate data;
  day one, every PR gets a human.
- **Adopt:** process scorers that grade agent transcripts, not just output (Warp); "redrafts,
  not additions" when updating rule files; requester can't approve their own agent's PR;
  no CI run on agent pushes touching CI config without a human; egress firewall on the
  sandbox; a named human owner on every delegated ticket; agents that would wait forever
  stop as "blocked"; draft PRs until the agent is done.
- **Keep where we differ:** stricter anti-gaming guard than Warp's review-only check; our
  own loop cap and cost budget; the deterministic risk floor, which is the part that
  encodes a team's risk appetite.

### D31. Not every step is an agent; agent steps are a generic runner + a step file
**Decided.** Prompted by Warp's outer loop editing each step's agent settings.
- **Agent steps (judgement):** readiness, build, review, the LLM half of risk, later
  acceptance tests and the retro. **Deterministic steps (code):** waiting for CI, merge,
  labels/comments, escalation, approval requests, the risk floor.
- Each agent step is one generic activity path (`runStep`) plus `steps/<name>.md`:
  YAML frontmatter (runner, model, tools, budget, output schema) and a prompt template.
  That file is what the outer loop proposes changes to; deterministic steps change by
  normal code PRs.
- Step files are read on every run, so edits take effect without a redeploy, and
  in-flight workflows are unaffected (activity results are already in their history).
- `steps/` is a quality gate: guard.sh flags changes, and `TestStepFilesLoad` fails CI if a
  step file doesn't parse or render.

### D32. Runner is an interface chosen per step, not tied to one CLI
**Decided** (Claude Code CLI implemented). Lee's point: teams will want OpenRouter or a
model not tied to one vendor's CLI. The step file names its `runner`; `Runners` maps names
to implementations, and an unknown runner is an error.
- **Nuance:** single-shot judgement steps (readiness, risk) can use any model API. Build
  needs an agent *harness* (the edit/run loop), which is where a CLI earns its place, and
  there are harnesses that can use OpenRouter models.
- Claude CLI flags that map to our design: `--json-schema` (structured output),
  `--max-budget-usd` (per-step cost cap: the start of D4's budget), `--tools` (least
  privilege), `--setting-sources ""` (the step file is the whole config, not the machine's).
- `--bare` skips the subscription login and needs an API key; a team deployment would use
  API keys anyway.

### D33. Readiness moved to the agent queue
**Decided.** Rule: anything that runs an agent runs on the sandbox worker, even with no
tools, so credentials and spend live in one place. The replay test confirmed moving an
activity between queues doesn't break in-flight tickets.

### D34. Agent worker concurrency capped at 2 per machine
**Decided** (narrows D25). `MaxConcurrentActivityExecutionSize`, set by a `-concurrency`
flag. Resource tuner and rate limits stay open.

### D35. Step instructions decide what "ready" means
**Observed.** With no instructions the readiness agent asked 10 questions about "health".
With the step file (outcome, scope, done-criteria; don't ask what an engineer would
decide; max 3 questions, enforced by `maxItems` in the schema) it asked 3, and passed a
clear ticket while listing what it left to the builder. ~$0.02 per check on Sonnet.
- **Seen in the live run:** with no tools, the agent still mentioned this repo's folders
  (`worker/`, `tools/`, `pipeline/`), so some environment context from the working
  directory reached it. Agents must run in the *target* repo checkout (or an empty dir),
  never the pipeline's own. Fix when the build step introduces target checkouts.

---

## 2026-10-28

### D36. No dead ends: escalation and the risk gate hand a decision to a human
**Decided.** Spotted from the first flow diagram: escalate and reject were terminal, but
a stopped workflow is a dead ticket with its history split across runs. The README said
"wait for human"; the code didn't.
- Both now wait for a human decision: **approve** (risk gate only), **retry** (comment
  becomes feedback, round count resets), **takeover**, **abandon**. Anything that doesn't
  apply is ignored and the workflow keeps waiting. Outcomes are now merged or abandoned.
- **Takeover is a handback, not an exit** (Lee's point): a human works on the branch, sends
  `resume`, and the ticket rejoins at the PR step, so human work goes through the same CI,
  review and risk gate as agent work. Taking over can't be used to skip the gates.

### D37. CI is an event we wait for, not a step we run
**Decided.** Lee's point: CI happens on an external system. `RunCI` (a stub pretending CI
was a function call) became Build (push) → `OpenOrUpdatePR` (draft PR; the push triggers
CI) → wait for a `ci` signal.
- CI is just another outside party sending signals, like a human. A webhook receiver (or
  a poller) turns GitHub's "checks finished" into a signal; the workflow doesn't care which.
- **Wait for the signal or a 30-minute timer**; timeout escalates. Same fail-visibly
  principle as ScheduleToStartTimeout (D16).
- **Signals carry the commit SHA, and results for other commits are ignored.** Events
  arrive late and out of order: push twice and the first commit's result can land after
  the second push. `TestStaleCIResultIgnored` covers it.
- **CI red goes straight back to the builder, skipping review:** no point paying a
  reviewer for broken code.
- For local demos, `OpenOrUpdatePR` acts as fake CI: it signals a result ~2s later
  (fails if the ticket says `ci-fail`).

### D38. Workflow structure: a step machine over a per-ticket `run` struct
**Decided.** With more steps and loop-backs, the single function with nested loops got
hard to follow. Each step is a method returning the next step (and, for steps that can
end a ticket, a final Result). The lint gate (`unparam`) pushed steps that can't end a
ticket onto a narrower signature, so the type says which steps can finish one.
- Tests now use a harness that plays the outside world (fake CI and a fake human reacting
  to activities completing): 9 workflow paths, including stale SHA, CI timeout, takeover
  → resume, and approve ignored outside the gate.

### D39. Rolling out a breaking workflow change: terminate the old demo tickets
**Decided for this change** (Lee's call). Options considered, smallest to biggest:
`workflow.GetVersion` patches; a new workflow type; worker versioning (pin each workflow
to the build it started on, keep old workers until drained); reset; terminate and restart.
- **What we observed live:**
  - **ENG-3** (parked at approval, past the changed steps) **failed outright**, not blocked.
    The old `Build` returned a string, the new one a struct, so decoding the recorded result
    failed, and the workflow returns activity errors. **Changing an activity's result type
    is a data break, and can fail tickets rather than block them.** The replay test caught it
    (as `TMPRL1100`) before deploy.
  - **ENG-10** (parked at readiness, before anything changed) **carried on fine** under the
    new code and merged. A ticket is only affected by changes to steps it has already
    passed.
  - ENG-2 terminated. New replay history captured from ENG-22 (parked at the approval gate).
- **For a team:** worker versioning, so tickets finish on the code they started on. The
  catch, which Lee spotted: you can't retire an old build while tickets are parked on it.
  That's a question you ask Temporal (drainage status, list running workflows per build),
  not something you remember. Same for removing a GetVersion branch.
- **Still to do:** practise GetVersion on a smaller change.

## 2026-10-29

### D40. Go deep on one real slice, locally, instead of more stubs
**Decided** (Lee's call, after watching a "director agent" video). The breadth-first
skeleton got the shape right (gates, human decisions, versioning) but was hard to reason
about: only readiness was real, so nothing visibly built, pushed or merged. Lee: *"it's
still quite abstract… difficult to see a piece running through it end to end."* The fix
was depth on one path, not a restart: real Build, CI, Merge, Deploy, SmokeTest and
Revert. Review and AssessRisk stay stubs.
- **No GitHub** (Lee's constraint). A local stand-in with the same shape: a bare repo as
  the forge, a `post-receive` hook that runs CI on pushes to `ai/*` and signals the
  workflow, merge commits on main, and a deploy dir with the built binary. The pipeline
  sees it only through `Workspace`, `Forge` and `Deployer` interfaces, so a GitHub version
  is a new implementation, not a workflow change.
- **The toy app is a poster renderer** (`sample/poster`): every merge has a visible result
  (open `deploy/poster.html`), and tickets are easy to write and judge by eye. It has its
  own `go.mod` and CLAUDE.md, so the rules live in the target repo (D19), not ours.
- **The workspace lives in `~/.cache/ai-workflow-target`**, outside the project tree,
  because Claude Code reads CLAUDE.md from parent directories. The build agent should see
  the target's rules, not ours. This and running readiness in an empty dir fix the D35 leak.
- **The pipeline does git, not the agent.** Build checks out, the agent edits, then the
  activity commits and pushes. The push fires CI, so it belongs to the pipeline. The agent
  gets edit tools plus `go test/vet/build/run` and `gofmt`. Headless runs use
  `--permission-prompts none` and `--restricted`, so anything else is denied, not hung.
  Observed: read-only git (`git status`) is still auto-allowed, which is harmless.
- **CI checks live with CI, not in the target repo**, so the agent can't loosen the gate
  it's judged by (it can edit any file in the checkout).
- **First live run (ENG-30, dark mode):** readiness → build (11 turns) → CI green →
  merge → deploy → smoke → deployed in ~45s for $0.23. ENG-21 and ENG-22 were terminated:
  their recorded Merge would have run against branches that don't exist.

### D41. Revert on a failed smoke test, then escalate. Retry gets a fresh branch
**Decided.** Following D7 (auto-revert, not fix-forward): main goes back to
known-good *before* anyone is asked, then it escalates with the smoke log. A retry builds
on `ai/<ticket>/<attempt+1>` from main. Re-merging the reverted branch wouldn't bring its
changes back (git treats them as already merged). That's why the workflow now names
branches, not the build agent.
- Retry feedback now keeps the last failure *and* adds the human's guidance, rather than
  replacing one with the other. A fresh branch needs to know what went wrong.
- Not done yet: a failed Deploy (build error) fails the activity and, after retries, the
  workflow. It should revert like a smoke failure.
- **For a real team:** branch protection on main as the backstop even with the pipeline,
  so a bug in our gate logic can't merge on its own. And CODEOWNERS on the files that
  define the process (`steps/`, CI config, CLAUDE.md), so the agent can't edit its own
  rules. `scripts/guard.sh` only warns today.

### D42. Director agents vs defined pipelines: different layers, not rivals
**Position**, from discussing a "director agent with roles" approach:
- The director approach is quicker to set up and adapts per task. It's good for one person.
- A team needs **a predictable process, not predictable output**. The agent's code will
  differ run to run, and that's fine. Which checks run, who approves and when it stops
  must not differ.
- Rule of thumb: **put control flow in code where the order is known and the gate
  matters, and let the agent decide where the path is open-ended.** The outer workflow
  enforces the gates. `Build` can be a director inside it, handing back a branch and SHA.
- Locks must sit at a boundary the agent can't change (branch protection, CODEOWNERS,
  credentials, harness permissions, the workflow). Prompts are requests.
- Careful with "deterministic": in Temporal it means replay, not agent output.

### D43. Readiness needs to see the repo, read-only
**Found live, fix agreed, not built yet.** ENG-31 ("make it purple") got three questions,
including *"What should be purple? Name the specific element, component, page, or app"*
and *"Which exact purple do you want?"*. The first is answerable from the code; the second
breaks the step's own "don't ask what a competent engineer would decide" rule. Cause: since
D40, readiness runs in an empty directory to stop the D35 leak, so it judges every ticket
blind. After one answer it was ready, and the poster went purple (readiness 2× ~$0.006,
build $0.20, 14 turns).
- **Fix:** run readiness in a read-only checkout of main with Read/Glob/Grep only. Expect
  at most one question ("background, text or border?"), or none.
- **Lesson:** each step needs the right *context*, not just the right tools. An agent that
  asks questions the code answers trains people to ignore it.

### D44. Rollback is its own action: roll back the deploy first, revert the change second
**Direction agreed, not built** (Lee: *"sometimes there's also just a human saying can we
roll it back?"*). Two different operations that get conflated:
- **Roll back the deploy:** put the previous binary back. Fast, no git, no build. Today's
  revert rebuilds from main, which is slower and can itself fail.
- **Revert the change:** fix main so the next deploy doesn't bring the bug back.
- **Where it lives:** considered keeping each ticket workflow open for a bake window that
  listens for a `rollback` signal. That's simple, but every ticket stays open a day, and
  rolling back ENG-30 after ENG-31 shipped on top of it is a different problem. **Preferred:
  a deploy workflow per environment** that records each deploy, rolls back to the previous
  one whichever ticket caused it, and later serialises deploys. A deploy timeline belongs
  to the environment, not the ticket. No agent anywhere in rollback.
- Related, seen on ENG-31: each ticket builds on whatever main is at the time (purple on
  top of dark mode). Fine one at a time. Concurrent tickets touching the same files is
  the next problem: merge conflicts, or a merge silently undoing another's change.

### D45. Step environments, and telling infrastructure failures from agent failures
**Logged for later** (Lee, from a real CI outage: an image registry being down took out CI).
Each step should declare its environment, like a GitHub Actions job: readiness =
read-only checkout, no toolchain or network. Build = writable checkout, Go, no secrets.
Deploy = no agent, deploy credentials. Two options:
- **Workers per image**, a task queue per environment (reuses our queue routing). Simple,
  but tickets share long-lived machines.
- **A fresh sandbox per activity** (Kubernetes Job, Firecracker, hosted sandboxes). Clean
  isolation, more moving parts, start-up cost.
- Leaning: fresh sandboxes for agent steps (they run arbitrary code), workers per image
  for the rest.
- **The important part is error classification.** An infrastructure failure must not go
  back to the builder as feedback (it would try to "fix" an outage), use up a round, or
  escalate to the ticket's author. It should retry with backoff, then alert whoever owns the
  platform. Temporal supplies the mechanics: retries, `ScheduleToStartTimeout` for "nothing
  can start this", heartbeats for "the machine died". The design work is marking errors
  retryable vs non-retryable. Mitigations: images pinned by digest, a registry mirror or
  node cache, warm capacity.

### D46. Human waits: reply where asked, put a timer on it, record who answered
**Direction for the Linear integration.** The workflow only waits for a `human` signal.
What sits in between is a choice:
- **Reply where the question was asked.** Questions go back as a Linear comment, and the
  next comment becomes the signal (via webhook). Slack is the nudge, linking to the ticket;
  the ticket stays the one record of the conversation.
- **A UI is for the overview** ("what's waiting on a human, and for how long?"), built on
  the `status` query. The Temporal UI is an engineer's tool, not a team one.
- **Every wait gets a timer**: nudge after a day, park or close after a week. Durable,
  free waits can rot unnoticed.
- **Signals carry who answered**, with rules on who may approve at the risk gate.
- **Consider Temporal Updates instead of signals** once there's a UI. Our `decide` loop
  silently ignores an invalid action; an Update can reject it and say why.

## 2026-11-01

### D47. Readiness reads main, read-only (D43, built)
**Done.** CheckReadiness gets a checkout of main through the same `Workspace.Checkout` the
build uses. Its tools are Read/Glob/Grep only, so read-only comes from having no tool that
writes, not from filesystem permissions. The prompt says to look before asking and never
to ask what the code answers.
- **Live comparison:** ENG-31 "make it purple" (blind): 3 questions, starting with "what
  should be purple?", ~$0.006. ENG-32 "make it blue" (reading main): **no questions.** It
  found the theme mechanism (dark, and purple from ENG-31), decided to add a blue theme the
  same way, and explained why the shade isn't a blocker. ~$0.17, 6 turns. Built, CI green,
  deployed blue.
- **Trade-off:** about 25× the cost of a blind check. Still pennies, but a candidate for a
  cheaper model, since reading and judging doesn't need the strongest one.
- **Guardrail seen working:** the build agent tried to edit files with a Python script,
  was denied (not in its allow-list, and `--permission-prompts none` means no hang), and
  switched to the edit tool. It said so in its own summary.

### D48. Databases change the revert story: guard data before deploy, keep code revertable
**Direction, not built** (Lee's point). Revert-first (D7, D41) works here because reverting
code is cheap. Once a change includes a migration, you "live in a different world": old
code may not understand the new schema, so many teams never revert in practice.
- **Keep code revertable with expand/contract:** add the new structure without removing
  anything, deploy code that uses it, and remove the old structure later in a separate
  change. Migrations only go forward. A code revert is always safe, because the old code
  ignores the new structure.
- **Stop before deploy rather than revert after:**
  - Migrations already raise the risk floor (D6), so a human sees them.
  - Add a mechanical check: fail a change that ships a migration together with the code
    that depends on it, or one that drops or renames something.
  - Run the migration as its own step, against a copy of the database first. A failure
    there stops the ticket before anything irreversible happens.
- Revert stays the safety net for code. Gates before deploy protect data.
