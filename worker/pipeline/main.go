// Command pipeline runs the workflow and the API activities: Linear (stubbed),
// the forge and the deployer. Locally those are the target workspace.
package main

import (
	"fmt"
	"log"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"aiworkflow/pipeline"
	"aiworkflow/target"
)

func main() {
	if err := run(); err != nil {
		log.Fatalln(err)
	}
}

func run() error {
	c, err := client.Dial(client.Options{})
	if err != nil {
		return fmt.Errorf("dial temporal: %w", err)
	}
	defer c.Close()

	w := worker.New(c, pipeline.PipelineQueue, worker.Options{})
	w.RegisterWorkflow(pipeline.TicketWorkflow)
	local := target.Local{Dir: target.DefaultDir()}
	w.RegisterActivity(&pipeline.PipelineActivities{Forge: local, Deployer: local})

	if err := w.Run(worker.InterruptCh()); err != nil {
		return fmt.Errorf("worker: %w", err)
	}
	return nil
}
