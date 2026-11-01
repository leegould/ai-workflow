// Command start kicks off a ticket workflow by hand, standing in for the
// Linear webhook until that exists.
//
//	go run ./ticket/start -id ENG-1 -desc "Add a /health endpoint returning 200"
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"go.temporal.io/sdk/client"

	"aiworkflow/pipeline"
)

func main() {
	id := flag.String("id", "ENG-1", "ticket ID (also the workflow ID)")
	title := flag.String("title", "Demo ticket", "ticket title")
	desc := flag.String("desc", "", "ticket description")
	flag.Parse()

	if err := run(pipeline.Ticket{ID: *id, Title: *title, Description: *desc}); err != nil {
		log.Fatalln(err)
	}
}

func run(t pipeline.Ticket) error {
	c, err := client.Dial(client.Options{})
	if err != nil {
		return fmt.Errorf("dial temporal: %w", err)
	}
	defer c.Close()

	// Workflow ID = ticket ID, so a duplicate webhook can't start a second run.
	run, err := c.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{
		ID:        pipeline.WorkflowID(t.ID),
		TaskQueue: pipeline.PipelineQueue,
	}, pipeline.TicketWorkflow, t)
	if err != nil {
		return fmt.Errorf("start workflow: %w", err)
	}
	log.Printf("started %s (run %s)", run.GetID(), run.GetRunID())
	log.Printf("UI: http://localhost:8233/namespaces/default/workflows/%s", run.GetID())
	return nil
}
