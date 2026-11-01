package pipeline

import "time"

// Task queues. Temporal doesn't validate these: a queue exists as soon as
// something polls or writes to it. Keeping them here means the workflow and
// the workers can only disagree at compile time.
const (
	// PipelineQueue runs the workflow itself and cheap API activities
	// (Linear, GitHub, merge). No repo or agent access needed.
	PipelineQueue = "ai-pipeline"
	// AgentQueue runs agent activities that need a repo, toolchain and
	// credentials. Workers here are the sandbox.
	AgentQueue = "agent-sandbox"
)

// ScheduleToStartTimeout fails an activity that no worker picks up, so a
// missing or crashed worker pool shows up as an error instead of a ticket
// that hangs forever.
const ScheduleToStartTimeout = 10 * time.Minute
