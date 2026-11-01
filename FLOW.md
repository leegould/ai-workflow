# Flow of work

Two views of the system as it is today (2026-10-29, after the real build/CI/deploy slice). Update these when the workflow
changes; `pipeline/workflow.go` is the source of truth.

## 1. One ticket through the workflow

Each box is a step in `TicketWorkflow`. Colour shows which worker runs it;
hexagons are where the workflow waits for something outside it: a human, or CI.

```mermaid
flowchart TD
    src(["ticket/start<br/>(stand-in for Linear)"]) --> ready

    subgraph sgReady ["1 · Readiness loop"]
        ready["CheckReadiness<br/>real agent: steps/readiness.md"]
        ready -->|not ready| ask["AskQuestions<br/>stub: label ai-needs-info + comment"]
        ask --> waitInfo{{"wait for human:<br/>answers appended to ticket"}}
        waitInfo --> ready
    end

    ready -->|ready| cap

    subgraph sgLoop ["2 · Build loop, max 3 rounds"]
        cap{"3 rounds used?"}
        cap -->|no| build["Build<br/>real agent: steps/build.md,<br/>pipeline commits + pushes ai/ticket/n"]
        build --> pr["OpenOrUpdatePR<br/>local: the branch is the PR,<br/>the push already started CI"]
        pr --> waitCI{{"wait for CI signal<br/>for this commit SHA (30 min)"}}
        waitCI -->|red: logs become feedback| cap
        waitCI -->|green| review["Review<br/>stub"]
        review -->|changes: comments become feedback| cap
    end

    cap -->|yes| escalate["Escalate<br/>stub: label ai-escalated + alert"]
    waitCI -->|timed out| escalate
    review -->|approved| risk

    subgraph sgGate ["3 · Risk gate"]
        risk["AssessRisk<br/>stub"]
        risk -->|medium or high| approval["RequestApproval<br/>stub: label ai-needs-approval"]
    end

    risk -->|low| merge
    approval --> decide
    escalate --> decide

    subgraph sgHuman ["4 · Human decision"]
        decide{{"wait for human:<br/>approve · retry · takeover · abandon"}}
        decide -->|takeover| takenOver{{"human works on the branch,<br/>then sends resume"}}
        takenOver -->|resume| head["HeadSHA<br/>real: head of the branch"]
    end

    decide -->|retry: comment becomes feedback, rounds reset| cap
    head -->|human work goes through CI, review, gate| pr
    decide -->|approve, at the gate only| merge["Merge<br/>real: merge commit on main"]
    decide -->|abandon| abandoned(["outcome: abandoned"])
    takenOver -->|abandon| abandoned

    subgraph sgShip ["5 · Ship"]
        merge --> deploy["Deploy<br/>real: build main, swap binary"]
        deploy --> smoke["SmokeTest<br/>real: run it, check the page"]
        smoke -->|fails| revert["Revert + redeploy<br/>main back to known-good,<br/>next attempt gets a new branch"]
    end

    smoke -->|passes| deployed(["outcome: deployed"])
    revert -->|smoke failure + log as feedback| escalate

    classDef agent fill:#dbeafe,stroke:#2563eb,color:#1e3a8a
    classDef pipe fill:#dcfce7,stroke:#16a34a,color:#14532d
    classDef wait fill:#fef3c7,stroke:#d97706,color:#78350f
    classDef done fill:#f3f4f6,stroke:#6b7280,color:#111827
    class ready,build,review,risk agent
    class ask,pr,escalate,approval,merge,head,deploy,smoke,revert pipe
    class waitInfo,waitCI,decide,takenOver wait
    class src,abandoned,deployed done
```

**Key:** blue = `worker/agent` (queue `agent-sandbox`) · green = `worker/pipeline`
(queue `ai-pipeline`) · amber = waiting on a signal: `human` (HumanInput) or `ci`
(CIResult). The "3 rounds used?" diamond is workflow logic, not a step.

There are no dead ends short of deployed or abandoned: escalation and the risk gate
both hand the decision to a human, and a taken-over ticket rejoins at the PR step,
so human work goes through the same CI, review and risk gate as agent work.

Not shown, because they're Temporal settings rather than steps: every activity
has a 10-minute `ScheduleToStartTimeout` (fails if no worker picks it up), retries
up to 3 times, and heartbeats while an agent runs.

## 2. Who talks to whom

```mermaid
sequenceDiagram
    autonumber
    participant T as ticket/start<br/>(later: Linear)
    participant S as Temporal server<br/>(history, queues, timers)
    participant P as worker/pipeline<br/>(workflow + API stubs)
    participant A as worker/agent<br/>(sandbox)
    participant C as claude -p<br/>(Claude Code headless)
    participant G as CI<br/>(local: push hook → ci/local)
    actor H as Human

    T->>S: start TicketWorkflow (workflow ID = ticket ID)
    P->>S: long-poll ai-pipeline
    S-->>P: workflow task
    Note over P: replays history,<br/>runs workflow code up to<br/>the next activity
    P->>S: schedule CheckReadiness on agent-sandbox
    A->>S: long-poll agent-sandbox
    S-->>A: activity task
    A->>A: load steps/readiness.md,<br/>render prompt
    A->>C: run with model, schema, budget, no tools
    C-->>A: structured JSON + cost
    A->>S: activity result (recorded in history)
    S-->>P: workflow task: readiness result
    alt not ready
        P->>S: schedule AskQuestions (ai-pipeline)
        Note over S: workflow sleeps, costs nothing,<br/>survives restarts
        H->>S: signal "human" with answers
        S-->>P: workflow task: signal received
        Note over P: back to CheckReadiness
    else ready
        P->>S: schedule Build (agent-sandbox)
        S-->>A: activity task
        A-->>S: BuildResult: branch + commit SHA
        A->>G: push ai/ticket/n (the push hook starts CI)
        P->>S: schedule OpenOrUpdatePR (ai-pipeline)
        Note over S: workflow waits for a ci signal<br/>for that SHA, or 30 min timeout
        G->>S: signal "ci" with result + SHA<br/>(webhook receiver or poller)
        S-->>P: workflow task: CI result
        Note over P: red → back to Build<br/>green → Review → AssessRisk → Merge<br/>→ Deploy → SmokeTest
    end
```

The server never calls a worker. Workers pull work over outbound connections, so
they can run anywhere with a network path to the server. Every arrow into `S` is a
worker or client calling in.

## What's real vs stubbed

| Step | State |
|---|---|
| CheckReadiness | **Real**: Claude Code agent, `steps/readiness.md`, in a checkout of main with Read/Glob/Grep only |
| Build | **Real**: Claude Code agent, `steps/build.md`, in a checkout of the target. Edit tools plus `go test/vet/build/run` and `gofmt` only. The pipeline commits and pushes |
| CI | **Real**, local: the bare repo's post-receive hook runs `ci/local` (gofmt, vet, test on the pushed SHA), which signals `ci` |
| OpenOrUpdatePR | Local: the branch is the PR. On GitHub this opens a draft PR |
| HeadSHA, Merge, Revert | **Real**, local git against the bare repo |
| Deploy, SmokeTest | **Real**, local: build main into the deploy dir, run it, check the page |
| AskQuestions, RequestApproval, Escalate | Stub: logs what it would do in Linear |
| Review, AssessRisk | Stub: keywords in the ticket choose the path (`never-approve`, `migration`) |
