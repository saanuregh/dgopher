package main

import (
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fake is a system whose commands, files and installed programs are made up.
type fake struct {
	goos      string
	env       map[string]string
	installed []string
	outputs   map[string]string // "name arg…" → output; missing means the command fails
	files     map[string]string
	engine    string
	ran       []string
}

func (f *fake) system() system {
	return system{
		goos:   f.goos,
		getenv: func(k string) string { return f.env[k] },
		lookPath: func(name string) (string, error) {
			if slices.Contains(f.installed, name) {
				return "/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		output: func(name string, args ...string) (string, error) {
			out, ok := f.outputs[strings.Join(append([]string{name}, args...), " ")]
			if !ok {
				return "", errors.New("exit status 1")
			}
			return out, nil
		},
		exists: func(name string) bool {
			_, ok := f.files[name]
			return ok
		},
		readFile: func(name string) ([]byte, error) {
			s, ok := f.files[name]
			if !ok {
				return nil, fs.ErrNotExist
			}
			return []byte(s), nil
		},
		engine: func() (string, error) {
			if f.engine == "" {
				return "", errors.New("no container engine found")
			}
			return f.engine, nil
		},
		run: func(env []string, name string, args ...string) error {
			f.ran = append(f.ran, strings.Join(append(append([]string{}, env...), append([]string{name}, args...)...), " "))
			return nil
		},
	}
}

// gtkLib is where complete's GTK is, written as this OS writes paths.
var gtkLib = filepath.Join("/gtk/lib", "libgtk-3.so.0")

// complete is a Linux machine with everything doctor looks for.
func complete() *fake {
	return &fake{
		goos:      "linux",
		env:       map[string]string{"LD_LIBRARY_PATH": "/gtk/lib"},
		installed: []string{"gcc", "pg_dump"},
		outputs: map[string]string{
			"go env GOVERSION":       "go1.27.4\n",
			"go env CC":              "gcc\n",
			"podman compose version": "podman-compose version 1.6.0\npodman version 5.7.0\n",
			"pg_dump --version":      "pg_dump (PostgreSQL) 19.1\n",
			podmanSocket:             "/run/user/1000/podman/podman.sock\n",
		},
		files: map[string]string{
			"go.mod":       "module dgopher\n\ngo 1.27.4\n",
			"compose.yaml": "services:\n  postgres:\n    image: docker.io/library/postgres:19\n  timescaledb:\n    image: docker.io/timescale/timescaledb:latest-pg18\n",
			gtkLib:         "",
			podmanPolicy:   "",
		},
		engine: "podman",
	}
}

// results runs doctor and returns each check's status by name, and
// whether doctor passed.
func results(t *testing.T, f *fake) (map[string]string, bool) {
	t.Helper()
	got := map[string]string{}
	var b strings.Builder
	ok := doctor(f.system(), &b)
	for _, line := range strings.Split(b.String(), "\n") {
		status, rest, found := strings.Cut(line, "  ")
		if !found || strings.HasPrefix(line, " ") {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimSpace(rest), ":")
		got[name] = strings.TrimSpace(status)
	}
	t.Log(b.String())
	return got, ok
}

// A machine with every tool passes with no warnings.
func TestDoctorPassesOnCompleteSystem(t *testing.T) {
	got, ok := results(t, complete())
	if !ok {
		t.Error("doctor failed")
	}
	for name, status := range got {
		if status != "ok" {
			t.Errorf("%s: %s", name, status)
		}
	}
	if len(got) != len(checks) {
		t.Errorf("%d results for %d checks: %v", len(got), len(checks), got)
	}
}

// Without a C compiler nothing builds, so doctor fails.
func TestDoctorFailsWithoutCompiler(t *testing.T) {
	f := complete()
	f.installed = []string{"pg_dump"}
	got, ok := results(t, f)
	if ok || got["C compiler"] != "FAIL" {
		t.Errorf("passed %v, C compiler %q", ok, got["C compiler"])
	}
}

// A Go older than go.mod's fails when Go will not download the newer one.
func TestDoctorFailsOnOldGo(t *testing.T) {
	f := complete()
	for _, have := range []string{"go1.26.9", "go1.27rc1"} {
		f.outputs["go env GOVERSION"] = have + "\n"
		got, ok := results(t, f)
		if ok || got["Go"] != "FAIL" {
			t.Errorf("%s: passed %v, Go %q", have, ok, got["Go"])
		}
	}
}

// podman-compose before 1.6.0 cannot wait for the servers to be ready.
func TestDoctorWarnsOnOldPodmanCompose(t *testing.T) {
	f := complete()
	f.outputs["podman compose version"] = "podman-compose version 1.5.0\n"
	got, ok := results(t, f)
	if !ok || got["Container engine"] != "warn" {
		t.Errorf("passed %v, engine %q", ok, got["Container engine"])
	}
}

// podmanSocket is the command line doctor asks Podman's socket with.
var podmanSocket = "podman " + strings.Join(podmanSocketQuery, " ")

// podmanPolicy is where complete's Podman keeps its image policy.
var podmanPolicy = filepath.Join("/etc/containers", "policy.json")

// Podman with no policy.json refuses to pull any image.
func TestDoctorWarnsOnPodmanWithoutPolicy(t *testing.T) {
	f := complete()
	delete(f.files, podmanPolicy)
	got, ok := results(t, f)
	if !ok || got["Container engine"] != "warn" {
		t.Errorf("passed %v, engine %q", ok, got["Container engine"])
	}
}

// Podman without subordinate IDs maps one user, which breaks images
// that switch users.
func TestDoctorWarnsOnPodmanWithoutSubuid(t *testing.T) {
	f := complete()
	f.outputs[podmanSocket] = "level=error msg=\"cannot find UID/GID for user sr: open /etc/subuid: no such file or directory\"\n/run/user/1000/podman/podman.sock\n"
	if got, _ := results(t, f); got["Container engine"] != "warn" {
		t.Errorf("engine %q", got["Container engine"])
	}
}

// podman compose through docker-compose needs Podman's API socket.
func TestDoctorWarnsOnDockerComposeWithoutPodmanSocket(t *testing.T) {
	f := complete()
	f.outputs["podman compose version"] = ">>>> Executing external compose provider\nDocker Compose version v5.7.1\n"
	if got, _ := results(t, f); got["Container engine"] != "warn" {
		t.Errorf("without the socket: engine %q", got["Container engine"])
	}
	f.files["/run/user/1000/podman/podman.sock"] = ""
	if got, _ := results(t, f); got["Container engine"] != "ok" {
		t.Errorf("with the socket: engine %q", got["Container engine"])
	}
}

// Docker's compose is accepted as it is.
func TestDoctorAcceptsDockerCompose(t *testing.T) {
	f := complete()
	f.engine = "docker"
	f.outputs["docker compose version"] = "Docker Compose version v5.7.1\n"
	if got, _ := results(t, f); got["Container engine"] != "ok" {
		t.Errorf("engine %q", got["Container engine"])
	}
}

// A pg_dump of another major than the test server's warns.
func TestDoctorWarnsOnPgDumpMajorMismatch(t *testing.T) {
	f := complete()
	f.outputs["pg_dump --version"] = "pg_dump (PostgreSQL) 18.3\n"
	got, ok := results(t, f)
	if !ok || got["pg_dump"] != "warn" {
		t.Errorf("passed %v, pg_dump %q", ok, got["pg_dump"])
	}
}

// GTK is looked for only on Linux; macOS and Windows draw natively.
func TestDoctorChecksGTKOnlyOnLinux(t *testing.T) {
	f := complete()
	delete(f.files, gtkLib)
	got, ok := results(t, f)
	if ok || got["GTK 3"] != "FAIL" {
		t.Errorf("linux: passed %v, GTK %q", ok, got["GTK 3"])
	}
	f.goos = "darwin"
	got, ok = results(t, f)
	if !ok || got["GTK 3"] != "ok" {
		t.Errorf("darwin: passed %v, GTK %q", ok, got["GTK 3"])
	}
}

// GTK found through ldconfig's cache, as on most distributions, passes.
func TestDoctorFindsGTKThroughLdconfig(t *testing.T) {
	f := complete()
	delete(f.files, gtkLib)
	f.outputs["ldconfig -p"] = "\tlibgtk-3.so.0 (libc6,x86-64) => /lib/x86_64-linux-gnu/libgtk-3.so.0\n"
	if got, _ := results(t, f); got["GTK 3"] != "ok" {
		t.Errorf("GTK %q", got["GTK 3"])
	}
}

// A compiler path with spaces, as under Program Files, is found whole.
func TestDoctorFindsCompilerPathWithSpaces(t *testing.T) {
	f := complete()
	f.outputs["go env CC"] = "C:\\Program Files\\gcc.exe\n"
	f.installed = append(f.installed, "C:\\Program Files\\gcc.exe")
	if got, _ := results(t, f); got["C compiler"] != "ok" {
		t.Errorf("C compiler %q", got["C compiler"])
	}
}

// A flag after a service name would reach compose as a service; it is refused.
func TestServersRefusesFlagsAfterServices(t *testing.T) {
	f := complete()
	if err := servers(f.system(), []string{"up", "oracle", "--profile", "enterprise"}); err == nil {
		t.Error("no error")
	}
	if err := servers(f.system(), []string{"up", "--profile", "enterprise", "oracle"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"podman compose --profile enterprise up -d --wait oracle"}; !slices.Equal(f.ran, want) {
		t.Errorf("ran %q, want %q", f.ran, want)
	}
}

func TestTestArgs(t *testing.T) {
	for _, c := range []struct {
		it, serialized bool
		extra          []string
		want           string
	}{
		{false, true, nil, "test ./..."},
		{false, false, nil, "test ./..."},
		{true, true, nil, "test -timeout 30m ./..."},
		{true, false, nil, "test -timeout 30m -p 1 ./..."},
		{false, true, []string{"./internal/sqltext", "-run", "TestSplit"}, "test ./internal/sqltext -run TestSplit"},
	} {
		if got := strings.Join(testArgs(c.it, c.serialized, c.extra), " "); got != c.want {
			t.Errorf("it %v serialized %v %v: got %q, want %q", c.it, c.serialized, c.extra, got, c.want)
		}
	}
}

// --redis valkey runs the Redis tests against Valkey's servers.
func TestTestRedisValkey(t *testing.T) {
	f := complete()
	if err := test(f.system(), []string{"--it", "--redis", "valkey"}); err != nil {
		t.Fatal(err)
	}
	if len(f.ran) != 1 || !strings.HasPrefix(f.ran[0], "DGOPHER_IT=1 DGOPHER_REDIS=valkey go test ") {
		t.Fatalf("ran %q", f.ran)
	}
	if err := test(f.system(), []string{"--redis", "keydb"}); err == nil {
		t.Fatal("--redis keydb was accepted")
	}
}

func TestComposeArgs(t *testing.T) {
	for _, c := range []struct {
		profiles []string
		action   string
		services []string
		want     string
	}{
		{nil, "up", nil, "compose up -d --wait"},
		{[]string{"compat", "enterprise"}, "up", nil, "compose --profile compat --profile enterprise up -d --wait"},
		{[]string{"enterprise"}, "up", []string{"oracle"}, "compose --profile enterprise up -d --wait oracle"},
		{[]string{"compat"}, "down", nil, "compose --profile compat down"},
	} {
		if got := strings.Join(composeArgs(c.profiles, c.action, c.services), " "); got != c.want {
			t.Errorf("%v %s %v: got %q, want %q", c.profiles, c.action, c.services, got, c.want)
		}
	}
}

// check runs gofmt, then vet, then the tests, and stops at unformatted files.
func TestCheckStopsAtUnformattedFiles(t *testing.T) {
	f := complete()
	f.outputs["go env GOROOT"] = "/go\n"
	gofmt := filepath.Join("/go", "bin", "gofmt") + " -l ."
	f.outputs[gofmt] = ""
	if err := check(f.system()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"go vet ./...", "go test ./..."}; !slices.Equal(f.ran, want) {
		t.Errorf("ran %q, want %q", f.ran, want)
	}

	f.ran = nil
	f.outputs[gofmt] = "main.go\n"
	if err := check(f.system()); err == nil || !strings.Contains(err.Error(), "main.go") {
		t.Errorf("got %v, want an error naming main.go", err)
	}
	if len(f.ran) != 0 {
		t.Errorf("ran %q after gofmt failed", f.ran)
	}
}
