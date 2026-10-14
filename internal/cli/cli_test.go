package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mustafaalam1991/bngload/internal/report"
)

func tree(ran *[]string) *Command {
	leaf := &Command{Name: "check", Summary: "leaf", Run: func(env *Env, args []string) int {
		fs := NewFlagSet(env, "x check", "help")
		n := fs.Int("n", 1, "")
		if code, ok := Parse(fs, args); !ok {
			return code
		}
		*ran = append(*ran, strings.Repeat("x", *n))
		return ExitOK
	}}
	return &Command{Name: "x", Sub: []*Command{{Name: "clock", Summary: "group", Sub: []*Command{leaf}}}}
}

func TestDispatch(t *testing.T) {
	var ran []string
	var out, errb bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &errb}
	if code := Main(tree(&ran), []string{"clock", "check", "-n", "3"}, env); code != ExitOK {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if len(ran) != 1 || ran[0] != "xxx" {
		t.Fatalf("leaf not run correctly: %v", ran)
	}
	if code := Main(tree(&ran), []string{"nope"}, env); code != ExitUsage {
		t.Fatalf("unknown command exit %d", code)
	}
	if code := Main(tree(&ran), nil, env); code != ExitUsage {
		t.Fatalf("missing command exit %d", code)
	}
	if code := Main(tree(&ran), []string{"-h"}, env); code != ExitOK {
		t.Fatalf("help exit %d", code)
	}
	if code := Main(tree(&ran), []string{"clock", "check", "-bad"}, env); code != ExitUsage {
		t.Fatalf("bad flag exit %d", code)
	}
	if code := Main(tree(&ran), []string{"clock", "check", "extra"}, env); code != ExitUsage {
		t.Fatalf("positional arg exit %d", code)
	}
}

func TestExitFor(t *testing.T) {
	if ExitFor(report.Fail, false) != ExitFail || ExitFor(report.Warn, false) != ExitOK ||
		ExitFor(report.Warn, true) != ExitFail || ExitFor(report.Pass, true) != ExitOK {
		t.Fatal("ExitFor mapping")
	}
}

func TestStringList(t *testing.T) {
	var s StringList
	_ = s.Set("a, b")
	_ = s.Set("c")
	if s.String() != "a,b,c" {
		t.Fatalf("got %q", s.String())
	}
}
