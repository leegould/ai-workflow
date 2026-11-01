// Command setup creates the local target workspace: a bare repo standing in
// for GitHub, seeded with the sample poster app, with CI on push.
//
//	make target          # first time
//	make target RESET=1  # wipe and start again
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"aiworkflow/target"
)

func main() {
	dir := flag.String("target", target.DefaultDir(), "target workspace")
	seed := flag.String("seed", "sample/poster", "initial contents of the repo")
	ci := flag.String("ci", "", "CI binary for the push hook")
	reset := flag.Bool("reset", false, "delete the existing workspace first")
	flag.Parse()

	if err := run(*dir, *seed, *ci, *reset); err != nil {
		log.Fatalln(err)
	}
}

func run(dir, seed, ci string, reset bool) error {
	if reset {
		// Everything but bin/, where make has just built the CI binary.
		for _, p := range []string{"origin.git", "work", "deploy", "ci.log"} {
			if err := os.RemoveAll(filepath.Join(dir, p)); err != nil {
				return fmt.Errorf("reset: %w", err)
			}
		}
	}
	ci, err := filepath.Abs(ci)
	if err != nil {
		return fmt.Errorf("ci binary: %w", err)
	}
	local := target.Local{Dir: dir}
	if err := local.Init(context.Background(), seed, ci); err != nil {
		return fmt.Errorf("init %s: %w", dir, err)
	}
	log.Printf("target ready: %s", local.Origin())
	log.Printf("clone it to look around or take over a ticket: git clone %s", local.Origin())
	return nil
}
