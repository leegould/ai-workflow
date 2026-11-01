# Vendor comparison

How others structure the ticket → PR pipeline, read as field research: what they make
configurable (teams differ), what they hard-wire (they learned it's needed), what they
have that we don't. Researched 2026-10-27, scoped to the core loop (D29); integrations at
either end are noted but not weighed. These products change monthly; check the sources.

## Summary

| | Warp Factories | GitHub Copilot coding agent | Linear coding sessions | Factory Droids | **Ours** |
|---|---|---|---|---|---|
| Trigger | Issue tracker, forge, Slack, schedule | Assign issue to Copilot | Delegate issue / @mention / triage automation | Assign Linear/Jira ticket | `ai-ready` label |
| Stages | triage → spec → implement → review | implement (+ tests) | implement + test | plan → implement → test → PR (+ review/security per 3rd party) | readiness → build → CI → review (×3) → risk |
| Sandbox | Docker per run, hosted or self-hosted | Firewalled Actions environment | Cloud environment | — | Local worktree (containers later) |
| Output | PR for human review | **Draft** PR, requests review | **Draft** PR | PR for human review | PR; **low risk auto-merges** |
| Learning loop | Scorers + self-improvement agent → PRs to skills | — | — | — | Planned (D26) |
| Agent swappable | Yes (Claude Code, Codex, ...) | No | Claude Code or Codex | No | Yes, by design (D27) |

## What's hard-wired everywhere

- **A sandbox per run.** Nobody runs agents on a shared box. Confirms D12: containers are
  the real target, local worktrees are only for the demo.
- **A PR for human review is the output. None of them auto-merge by default.** Our low-risk
  auto-merge (D6) goes further than any vendor's default. It's defensible (a deterministic
  floor, evidence-based loosening), but expect pushback on it. The answer:
  "auto-merge low risk is the *goal state*, earned with overturn-rate data; day one, every
  PR gets a human."
- **Draft PRs.** Copilot and Linear both open drafts, so the PR is visibly not ready
  until the agent says it's done.

## Worth adopting

1. **Scorers: grade the agent's *process*, not just its output** (Warp). A cloud agent
   grades the full transcript (tool calls included) against a rubric, pass/fail with
   reasoning, on a sample (default 25%). Default scorers: code quality, efficiency,
   procedural compliance. Their example: an agent used 275 computer-use actions to check
   a one-line CSS fix, and the output was fine. Only a process scorer catches that kind of
   waste. This is the missing piece of our level-2 loop (D26): we planned to log review
   findings, but not to score how the work was done.
2. **"Make redrafts, not additions"** (Warp's rule for its self-improvement agent). Rule
   files bloat when every lesson is appended. Our "promote findings into rules" (D19) needs
   the same rule, or CLAUDE.md turns into a pile of addenda.
3. **The requester can't approve their own agent's PR** (Copilot). Separation of duties:
   whoever wrote or delegated the ticket shouldn't be its only approver. Cheap to add to our
   approval gate.
4. **CI doesn't run on agent pushes until a maintainer allows it** (Copilot). An agent that
   can edit `.github/workflows` and trigger CI can exfiltrate secrets. Our guard.sh flags gate
   changes as high risk, but CI would already have run. Worth hard-wiring: agent PRs that
   touch CI config don't run CI without a human.
5. **Network egress firewall on the sandbox** (Copilot: allowlist, blocked requests reported
   in the PR). We hadn't considered egress at all. It belongs with containers (D12).
6. **Delegate, not assignee** (Linear). The agent acts, a named human stays accountable for
   the ticket. Keep a human owner on every ticket, so escalations have somewhere to go.
7. **Unattended runs stop as "blocked" instead of hanging** (Warp changelog, Oct 2026). Same
   principle as our ScheduleToStartTimeout (D16), applied inside the agent too: an agent
   waiting for input that will never come should fail visibly.
8. **Feedback goes on the PR, not the issue** (Copilot: the agent doesn't see later issue
   comments). Matches D8 (the PR/branch is the memory). Make it explicit for humans.

## Where we differ, deliberately

- **Our guard against agents loosening their own rules is stricter.** Warp's only explicit
  check on self-improvement is human review of the diff. We also fail on deleted tests and
  new skips, and flag gate changes as high risk (D24).
- **The loop cap and cost budget are ours to set** (D4). None of the products expose a
  "rounds before escalation" knob in what I found.
- **Deterministic risk floor** (D6). No vendor describes a risk tier between "PR" and
  "merged". That's the part that encodes a team's own risk appetite, which is why we build it.

## Confirms our design

- **Warp's inner loop is our loop:** triage/spec → implement → review → PR. Their spec
  stage is the "refine" from the in-house video, and our readiness check is a thin version
  of it (D29).
- **Linear's own guidance:** the clearer the issue (outcome, scope, constraints), the less
  the agent explores or guesses. Input quality drives the loop (D29).
- **Heartbeats for long-running agents** (Warp added them for Slack-dispatched agents).
  Already in our activity options.
- **Swappable agent harness** (Warp is multi-harness for Claude Code and Codex; Linear runs
  either). Confirms buying the agent and building the orchestration (D27).

## Sources

- Warp: [Oz launch](https://www.warp.dev/blog/oz-orchestration-platform-cloud-agents) ·
  [Engineering self-improving software factories](https://www.warp.dev/blog/engineering-self-improving-software-factories) ·
  [Agent platform docs](https://docs.warp.dev/agent-platform/) ·
  [Factories docs](https://docs.warp.dev/factories/) ·
  [Multi-harness orchestration](https://www.warp.dev/blog/multi-harness-cloud-agent-orchestration) ·
  [2026 changelog](https://docs.warp.dev/changelog/2026/)
- GitHub Copilot coding agent: [Using the coding agent](https://docs.github.com/en/copilot/using-github-copilot/using-copilot-coding-agent-to-work-on-tasks) ·
  [Customize the firewall](https://docs.github.com/en/copilot/how-tos/copilot-on-github/customize-copilot/customize-the-firewall) ·
  [Troubleshooting](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/cloud-agent/troubleshoot-cloud-agent) ·
  [Responsible use](https://docs.github.com/en/copilot/responsible-use/agents)
- Linear: [Coding sessions docs](https://linear.app/docs/coding-sessions.md) ·
  [Agents in Linear](https://linear.app/docs/agents-in-linear) ·
  [Changelog 11 Jun 2026](https://linear.app/changelog/2026-06-11-coding-sessions)
- Factory: [Droids product page](https://factory.ai/product/droids) ·
  [Sacra analysis](https://sacra.com/research/factory) (third party; review/security stages
  are from here, not Factory's own docs)
