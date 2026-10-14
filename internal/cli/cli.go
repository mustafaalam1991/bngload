// Package cli is a minimal sub-command dispatcher built on the standard
// library flag package.
//
// It exists so that the bngload binary has no third-party dependencies
// (design SR-24, ADR-0003). Commands form a tree; leaf commands parse their
// own flags with flag.ContinueOnError and return a process exit code.
//
// Exit codes are part of the tool contract and are used by scripts and CI:
//
//	0  success / verdict PASS or INFO
//	1  the check ran and its verdict is FAIL (or WARN with --strict)
//	2  usage error (bad flags or unknown command)
//	3  runtime error (the check could not run, e.g. missing privileges)
package cli

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/mustafaalam1991/bngload/internal/report"
)

// Exit codes. See the package documentation.
const (
	ExitOK    = 0
	ExitFail  = 1
	ExitUsage = 2
	ExitError = 3
)

// Env carries the output streams so that commands are testable.
type Env struct {
	Stdout io.Writer
	Stderr io.Writer
}

// Command is one node of the command tree.
type Command struct {
	// Name is the word typed on the command line.
	Name string
	// Summary is a one-line description shown in parent help.
	Summary string
	// Help is a longer description shown by `<cmd> -h`.
	Help string
	// Run executes a leaf command. args excludes the command path.
	Run func(env *Env, args []string) int
	// Sub lists child commands. A node has either Run or Sub.
	Sub []*Command
}

// Main dispatches args (without the program name) through the tree.
func Main(root *Command, args []string, env *Env) int {
	cmd := root
	path := []string{root.Name}
	for len(cmd.Sub) > 0 {
		if len(args) == 0 || isHelp(args[0]) {
			printHelp(env.Stdout, cmd, path)
			if len(args) == 0 {
				return ExitUsage
			}
			return ExitOK
		}
		next := find(cmd.Sub, args[0])
		if next == nil {
			fmt.Fprintf(env.Stderr, "%s: unknown command %q\n\n", strings.Join(path, " "), args[0])
			printHelp(env.Stderr, cmd, path)
			return ExitUsage
		}
		cmd = next
		path = append(path, next.Name)
		args = args[1:]
	}
	return cmd.Run(env, args)
}

// NewFlagSet returns a FlagSet that reports errors instead of exiting and
// prints the command's help text above the flag defaults.
func NewFlagSet(env *Env, name, help string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(env.Stderr, "Usage: %s [flags]\n\n%s\n\nFlags:\n", name, strings.TrimSpace(help))
		fs.PrintDefaults()
	}
	return fs
}

// Parse parses flags and maps the outcome to an exit code. ok is false when
// the caller should return code immediately (help requested or bad flags).
func Parse(fs *flag.FlagSet, args []string) (code int, ok bool) {
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return ExitOK, false
		}
		return ExitUsage, false
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(fs.Output(), "unexpected arguments: %s\n", strings.Join(fs.Args(), " "))
		return ExitUsage, false
	}
	return ExitOK, true
}

// ExitFor maps a verdict to an exit code. With strict, WARN also fails.
func ExitFor(v report.Verdict, strict bool) int {
	switch v {
	case report.Fail:
		return ExitFail
	case report.Warn:
		if strict {
			return ExitFail
		}
	}
	return ExitOK
}

// StringList is a repeatable string flag (e.g. --interface a --interface b).
// Comma-separated values are also accepted.
type StringList []string

// String implements flag.Value.
func (s *StringList) String() string { return strings.Join(*s, ",") }

// Set implements flag.Value.
func (s *StringList) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*s = append(*s, p)
		}
	}
	return nil
}

func find(cmds []*Command, name string) *Command {
	for _, c := range cmds {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func isHelp(a string) bool { return a == "-h" || a == "--help" || a == "help" }

func printHelp(w io.Writer, cmd *Command, path []string) {
	if cmd.Help != "" {
		fmt.Fprintf(w, "%s\n\n", strings.TrimSpace(cmd.Help))
	}
	fmt.Fprintf(w, "Usage: %s <command> [flags]\n\nCommands:\n", strings.Join(path, " "))
	subs := append([]*Command(nil), cmd.Sub...)
	sort.Slice(subs, func(i, j int) bool { return subs[i].Name < subs[j].Name })
	for _, c := range subs {
		fmt.Fprintf(w, "  %-12s %s\n", c.Name, c.Summary)
	}
	fmt.Fprintf(w, "\nRun '%s <command> -h' for command flags.\n", strings.Join(path, " "))
}
