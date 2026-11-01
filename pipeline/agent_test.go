package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The real step files must load and render; this is what stops a bad edit
// (by a human or the improvement loop) reaching a worker.
func TestStepFilesLoad(t *testing.T) {
	ticket := Ticket{ID: "ENG-9", Title: "Health", Description: "Add /health"}
	tests := []struct {
		step string
		data any
		want []string
	}{
		{"readiness", ticket, []string{"Ticket ENG-9: Health", "Add /health"}},
		{"build", BuildRequest{Ticket: ticket, Branch: "ai/ENG-9/1"}, []string{"Ticket ENG-9: Health", "branch ai/ENG-9/1"}},
		{"build", BuildRequest{Ticket: ticket, Feedback: "CI failed: vet"}, []string{"follow-up round", "CI failed: vet"}},
	}
	for _, tc := range tests {
		t.Run(tc.step, func(t *testing.T) {
			s, err := LoadStep("../steps", tc.step)
			if err != nil {
				t.Fatal(err)
			}
			if s.Schema == nil {
				t.Fatal("step has no schema")
			}
			p, err := s.Render(tc.data)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(p, want) {
					t.Errorf("prompt missing %q:\n%s", want, p)
				}
			}
		})
	}
}

func TestUnknownRunner(t *testing.T) {
	_, err := Runners{}.Run(context.Background(), StepDef{Name: "x", Runner: "openrouter"}, "", "")
	if !errors.Is(err, ErrUnknownRunner) {
		t.Fatalf("got %v", err)
	}
}
