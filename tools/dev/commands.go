package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func setup(s system) error {
	if err := s.run(nil, "go", "mod", "download"); err != nil {
		return err
	}
	// The hook skips itself on a branch older than the script.
	if err := s.run(nil, "git", "config", "hook.dgopher-checks.command",
		"test ! -e tools/githooks/pre-commit || tools/githooks/pre-commit"); err != nil {
		return err
	}
	if err := s.run(nil, "git", "config", "hook.dgopher-checks.event", "pre-commit"); err != nil {
		return err
	}
	fmt.Println("Registered tools/githooks/pre-commit as a pre-commit check.")
	if !doctor(s, os.Stdout) {
		return errors.New("doctor found missing tools")
	}
	return nil
}

func check(s system) error {
	// The gofmt of go.mod's Go: another version may lay files out
	// differently.
	goroot, err := s.output("go", "env", "GOROOT")
	if err != nil {
		return fmt.Errorf("go env GOROOT: %w", err)
	}
	gofmt := filepath.Join(strings.TrimSpace(goroot), "bin", "gofmt")
	if s.goos == "windows" {
		gofmt += ".exe"
	}
	out, err := s.output(gofmt, "-l", ".")
	if err != nil {
		return fmt.Errorf("gofmt: %w\n%s", err, out)
	}
	if out = strings.TrimSpace(out); out != "" {
		return fmt.Errorf("not gofmt-formatted (run %s -w on them):\n%s", gofmt, out)
	}
	if err := s.run(nil, "go", "vet", "./..."); err != nil {
		return err
	}
	return s.run(nil, "go", "test", "./...")
}

func test(s system, args []string) error {
	fs := newFlags("test")
	it := fs.Bool("it", false, "also run the integration tests")
	snapshots := fs.Bool("snapshots", false, "write PNGs of the UI under test")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var env []string
	if *it {
		env = append(env, "DGOPHER_IT=1")
	}
	if *snapshots {
		dir := filepath.Join(os.TempDir(), "dgopher-snapshots")
		env = append(env, "DGOPHER_SNAPSHOTS="+dir)
		fmt.Println("Snapshots go to", dir)
	}
	return s.run(env, "go", testArgs(*it, serializedServers, fs.Args())...)
}

// testArgs returns go test's arguments. Integration tests get a longer
// timeout, and run one package at a time where serverlock cannot make
// them take turns.
func testArgs(it, serialized bool, extra []string) []string {
	args := []string{"test"}
	if it {
		args = append(args, "-timeout", "30m")
		if !serialized {
			args = append(args, "-p", "1")
		}
	}
	if len(extra) == 0 {
		extra = []string{"./..."}
	}
	return append(args, extra...)
}

func servers(s system, args []string) error {
	if len(args) == 0 || (args[0] != "up" && args[0] != "down") {
		return errors.New("servers: want up or down")
	}
	action := args[0]
	fs := newFlags("servers")
	var p []string
	fs.Func("profile", "also the servers of a compose.yaml profile: compat or enterprise", func(v string) error {
		p = append(p, v)
		return nil
	})
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	for _, a := range fs.Args() {
		if strings.HasPrefix(a, "-") {
			return fmt.Errorf("servers: %s after a service name; put flags first", a)
		}
	}
	engine, err := s.engine()
	if err != nil {
		return err
	}
	return s.run(nil, engine, composeArgs(p, action, fs.Args())...)
}

// composeArgs returns the compose arguments that start the servers of
// compose.yaml, or only the services named, and wait until they are
// ready, or stop and remove them.
func composeArgs(profiles []string, action string, services []string) []string {
	args := []string{"compose"}
	for _, p := range profiles {
		args = append(args, "--profile", p)
	}
	args = append(args, action)
	if action == "up" {
		args = append(args, "-d", "--wait")
	}
	return append(args, services...)
}

func runApp(s system, args []string) error {
	fs := newFlags("run")
	scratch := fs.Bool("scratch", false, "keep settings in a new temporary folder")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var env []string
	if *scratch {
		dir, err := os.MkdirTemp("", "dgopher-config-")
		if err != nil {
			return err
		}
		env = append(env, "DGOPHER_CONFIG_DIR="+dir)
		fmt.Println("Settings go to", dir)
	}
	return s.run(env, "go", "run", ".")
}
