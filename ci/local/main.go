// Command local is CI for the local target repo. The repo's post-receive hook
// runs it in the background for each push to an ai/* branch: it checks out the
// pushed commit, runs the checks and signals the result to the ticket's
// workflow, the way a GitHub webhook receiver would.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"go.temporal.io/sdk/client"

	"aiworkflow/pipeline"
	"aiworkflow/target"
)

func main() {
	dir := flag.String("target", target.DefaultDir(), "target workspace")
	ref := flag.String("ref", "", "pushed ref, e.g. refs/heads/ai/ENG-1/1")
	sha := flag.String("sha", "", "pushed commit")
	flag.Parse()

	if err := run(target.Local{Dir: *dir}, *ref, *sha); err != nil {
		log.Fatalln(err)
	}
}

func run(local target.Local, ref, sha string) error {
	ticket, ok := target.TicketFromRef(ref)
	if !ok {
		return nil
	}
	ctx := context.Background()
	res := local.RunCI(ctx, sha)
	log.Printf("CI %s %s: passed=%v", ref, sha, res.Passed)

	c, err := client.Dial(client.Options{})
	if err != nil {
		return fmt.Errorf("dial temporal: %w", err)
	}
	defer c.Close()
	if err := c.SignalWorkflow(ctx, pipeline.WorkflowID(ticket), "", pipeline.SignalCI, res); err != nil {
		return fmt.Errorf("signal %s: %w", ticket, err)
	}
	return nil
}
