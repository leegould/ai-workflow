package target

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These run real git and go against a throwaway workspace seeded with the
// sample app, so they exercise the same commands the pipeline does.

func newLocal(t *testing.T) Local {
	t.Helper()
	l := Local{Dir: t.TempDir()}
	// "true" as the CI binary: the hook fires but does nothing.
	if err := l.Init(context.Background(), "../sample/poster", "true"); err != nil {
		t.Fatal(err)
	}
	return l
}

// push checks out branch, applies change in the checkout and pushes it.
func push(t *testing.T, l Local, branch string, change func(dir string)) string {
	t.Helper()
	ctx := context.Background()
	dir, err := l.Checkout(ctx, "T-1", branch)
	if err != nil {
		t.Fatal(err)
	}
	change(dir)
	sha, err := l.CommitAndPush(ctx, dir, branch, "T-1: change")
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCIPassesAndFails(t *testing.T) {
	l := newLocal(t)
	ctx := context.Background()

	good := push(t, l, "ai/T-1/1", func(dir string) {
		write(t, filepath.Join(dir, "NOTES.md"), "hello\n")
	})
	if res := l.RunCI(ctx, good); !res.Passed || res.SHA != good {
		t.Fatalf("good commit failed CI:\n%s", res.Log)
	}

	unformatted := push(t, l, "ai/T-1/1", func(dir string) {
		write(t, filepath.Join(dir, "extra.go"), "package main\nfunc  extra() {}\n")
	})
	if res := l.RunCI(ctx, unformatted); res.Passed || !strings.Contains(res.Log, "extra.go") {
		t.Fatalf("unformatted commit passed CI:\n%s", res.Log)
	}

	// The next round starts from what was pushed, not from main.
	dir, err := l.Checkout(ctx, "T-1", "ai/T-1/1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "extra.go")); err != nil {
		t.Fatal("checkout lost the previous round's work")
	}
	if head, err := l.HeadSHA(ctx, "ai/T-1/1"); err != nil || head != unformatted {
		t.Fatalf("head %s, %v", head, err)
	}
}

func TestMergeDeploySmokeRevert(t *testing.T) {
	l := newLocal(t)
	ctx := context.Background()

	push(t, l, "ai/T-1/1", func(dir string) {
		write(t, filepath.Join(dir, "poster.json"), `{"title": "Merged Title"}`)
	})
	merged, err := l.Merge(ctx, "ai/T-1/1", "Merge T-1")
	if err != nil {
		t.Fatal(err)
	}
	deployed, err := l.Deploy(ctx)
	if err != nil || deployed != merged {
		t.Fatalf("deployed %s, merged %s, %v", deployed, merged, err)
	}
	smoke, err := l.Smoke(ctx)
	if err != nil || !smoke.Passed {
		t.Fatalf("smoke: %+v %v", smoke, err)
	}
	page, err := os.ReadFile(filepath.Join(l.DeployDir(), "poster.html"))
	if err != nil || !strings.Contains(string(page), "Merged Title") {
		t.Fatalf("page doesn't show the merged change: %v", err)
	}

	// A change that breaks the binary fails the smoke test...
	push(t, l, "ai/T-1/2", func(dir string) {
		write(t, filepath.Join(dir, "poster.json"), `{"date": "no title"}`)
	})
	broken, err := l.Merge(ctx, "ai/T-1/2", "Merge T-1 again")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Deploy(ctx); err != nil {
		t.Fatal(err)
	}
	if smoke, _ := l.Smoke(ctx); smoke.Passed {
		t.Fatal("smoke passed on a broken deploy")
	}
	// ...and reverting it brings the working version back.
	if _, err := l.Revert(ctx, broken); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Deploy(ctx); err != nil {
		t.Fatal(err)
	}
	if smoke, _ := l.Smoke(ctx); !smoke.Passed {
		t.Fatalf("smoke failed after revert: %s", smoke.Log)
	}
}

func TestInitRefusesExisting(t *testing.T) {
	l := newLocal(t)
	if err := l.Init(context.Background(), "../sample/poster", "true"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestTicketFromRef(t *testing.T) {
	tests := []struct {
		ref, want string
		ok        bool
	}{
		{"refs/heads/ai/ENG-1/2", "ENG-1", true},
		{"refs/heads/main", "", false},
		{"refs/heads/ai/ENG-1", "", false},
		{"refs/heads/ai//1", "", false},
	}
	for _, tc := range tests {
		got, ok := TicketFromRef(tc.ref)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got %q %v", tc.ref, got, ok)
		}
	}
}

func TestTail(t *testing.T) {
	if got := tail("abcdef", 3); got != "…def" {
		t.Fatalf("got %q", got)
	}
}
