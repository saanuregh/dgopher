// Dev runs DGopher's contributor commands the same way on Linux, macOS and
// Windows, with no shell syntax:
//
//	go run ./tools/dev <command>
//
// Run it without a command for the list.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"dgopher/internal/containercli"
)

const usage = `Usage: go run ./tools/dev <command>

  doctor                  check the tools a build and the tests need
  setup                   download modules, register the pre-commit check, run doctor
  check                   gofmt, go vet and the tests, as CI runs them
  test [--it] [--snapshots] [--redis redis|valkey] [-- <go test arguments>]
                          the tests (./... by default); --it also against the
                          integration servers; --snapshots writes PNGs of the UI;
                          --redis valkey runs the Redis tests against Valkey's
                          servers (profile compat)
  servers up|down [--profile compat|enterprise]... [service]...
                          (flags before the services)
                          start or stop the integration servers, or only the
                          services named (Podman, or Docker;
                          DGOPHER_CONTAINER=podman|docker chooses)
  run [--scratch]         run the app; --scratch keeps its settings in a new
                          temporary folder, kept after the app quits
`

// system is how the commands reach the machine, so tests can fake it.
type system struct {
	goos     string
	getenv   func(string) string
	lookPath func(string) (string, error)
	// output runs a command and returns its combined output.
	output   func(name string, args ...string) (string, error)
	readFile func(string) ([]byte, error)
	exists   func(string) bool
	engine   func() (string, error)
	// run runs a command attached to the terminal, with CGO_ENABLED=1 and
	// env added to the environment.
	run func(env []string, name string, args ...string) error
}

func realSystem() system {
	return system{
		goos:     runtime.GOOS,
		getenv:   os.Getenv,
		lookPath: exec.LookPath,
		output: func(name string, args ...string) (string, error) {
			out, err := exec.Command(name, args...).CombinedOutput()
			return string(out), err
		},
		readFile: os.ReadFile,
		exists: func(name string) bool {
			_, err := os.Stat(name)
			return err == nil
		},
		engine: containercli.Engine,
		run: func(env []string, name string, args ...string) error {
			cmd := exec.Command(name, args...)
			cmd.Env = append(append(os.Environ(), "CGO_ENABLED=1"), env...)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
			}
			return nil
		},
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	s := realSystem()
	command, args := os.Args[1], os.Args[2:]
	var err error
	switch command {
	case "doctor":
		if !doctor(s, os.Stdout) {
			os.Exit(1)
		}
	case "setup":
		err = setup(s)
	case "check":
		err = check(s)
	case "test":
		err = test(s, args)
	case "servers":
		err = servers(s, args)
	case "run":
		err = runApp(s, args)
	case "help", "-h", "-help", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", command, usage)
		os.Exit(2)
	}
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "dev:", err)
		os.Exit(1)
	}
}

// newFlags returns a flag set for a command that reports errors instead of
// exiting.
func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	return fs
}
