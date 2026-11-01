// Command agent runs the agent activities: build, review, risk. This is the
// sandbox, the only worker that will hold repo access and credentials.
package main

import (
	"flag"
	"fmt"
	"log"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"aiworkflow/pipeline"
	"aiworkflow/target"
)

func main() {
	stepsDir := flag.String("steps", "steps", "directory of agent step definitions")
	concurrency := flag.Int("concurrency", 2, "max agent runs at once on this machine")
	flag.Parse()

	if err := run(*stepsDir, *concurrency); err != nil {
		log.Fatalln(err)
	}
}

func run(stepsDir string, concurrency int) error {
	c, err := client.Dial(client.Options{})
	if err != nil {
		return fmt.Errorf("dial temporal: %w", err)
	}
	defer c.Close()

	// Agent runs are heavy (CPU, memory, disk, API spend), so cap how many this
	// machine takes. Extra work waits visibly in the queue.
	w := worker.New(c, pipeline.AgentQueue, worker.Options{
		MaxConcurrentActivityExecutionSize: concurrency,
	})
	w.RegisterActivity(&pipeline.AgentActivities{
		StepsDir:  stepsDir,
		Runner:    pipeline.Runners{"claude-cli": pipeline.ClaudeCLI{}},
		Workspace: target.Local{Dir: target.DefaultDir()},
	})

	if err := w.Run(worker.InterruptCh()); err != nil {
		return fmt.Errorf("worker: %w", err)
	}
	return nil
}
