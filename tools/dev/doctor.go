package main

import (
	"fmt"
	"go/version"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A doctorCheck looks for one tool. A failed required check stops builds or the
// tests; an optional one only skips some tests.
type doctorCheck struct {
	name     string
	required bool
	// run returns whether the tool is there, what was found, and, when it
	// is not, what to do.
	run func(s system) (ok bool, detail, fix string)
}

var checks = []doctorCheck{
	{"Go", true, checkGo},
	{"C compiler", true, checkCompiler},
	{"GTK 3", true, checkGTK},
	{"Container engine", false, checkEngine},
	{"pg_dump", false, checkPgDump},
}

// doctor prints one line per check, with what to do under each that
// failed, and returns whether every required check passed.
func doctor(s system, w io.Writer) bool {
	passed := true
	for _, c := range checks {
		ok, detail, fix := c.run(s)
		status := "ok"
		if !ok {
			status = "warn"
			if c.required {
				status, passed = "FAIL", false
			}
		}
		fmt.Fprintf(w, "%-4s  %s: %s\n", status, c.name, detail)
		if !ok {
			fmt.Fprintf(w, "      %s\n", fix)
		}
	}
	if !passed {
		fmt.Fprintln(w, "\nInstall commands for each OS: docs/development.md#set-up")
	}
	return passed
}

var goLine = regexp.MustCompile(`(?m)^go (\S+)$`)

func checkGo(s system) (bool, string, string) {
	mod, err := s.readFile("go.mod")
	if err != nil {
		return false, "cannot read go.mod: " + err.Error(), "run this from the repository's root"
	}
	m := goLine.FindSubmatch(mod)
	if m == nil {
		return false, "no go line in go.mod", "restore go.mod's go line"
	}
	want := string(m[1])
	out, err := s.output("go", "env", "GOVERSION")
	have := strings.TrimPrefix(strings.TrimSpace(out), "go")
	if err != nil || have == "" {
		return false, fmt.Sprintf("go env GOVERSION failed: %v %s", err, strings.TrimSpace(out)), "install Go " + want + " or newer"
	}
	detail := fmt.Sprintf("%s (go.mod wants %s)", have, want)
	if version.Compare("go"+have, "go"+want) < 0 {
		return false, detail, "install Go " + want + " or newer, or unset GOTOOLCHAIN so Go downloads it"
	}
	return true, detail, ""
}

// olderVersion says whether dotted version a is older than b.
func olderVersion(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(as), len(bs)) {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

func checkCompiler(s system) (bool, string, string) {
	out, _ := s.output("go", "env", "CC")
	cc := strings.TrimSpace(out)
	if cc == "" {
		cc = "gcc"
	}
	fix := map[string]string{
		"darwin":  "xcode-select --install",
		"windows": "install MSYS2's UCRT64 gcc and put C:\\msys64\\ucrt64\\bin on PATH",
	}[s.goos]
	if fix == "" {
		fix = "install gcc and g++ (Debian/Ubuntu: build-essential)"
	}
	// CC may hold flags after the program, or be a path with spaces.
	if _, err := s.lookPath(cc); err != nil {
		if _, err := s.lookPath(strings.Fields(cc)[0]); err != nil {
			return false, cc + " not found; cgo needs it for DuckDB", fix
		}
	}
	return true, cc, ""
}

func checkGTK(s system) (bool, string, string) {
	if s.goos != "linux" {
		return true, "not needed on " + s.goos, ""
	}
	const lib = "libgtk-3.so.0"
	for _, dir := range filepath.SplitList(s.getenv("LD_LIBRARY_PATH")) {
		if dir == "" {
			continue
		}
		if s.exists(filepath.Join(dir, lib)) {
			return true, "in " + dir, ""
		}
	}
	if out, err := s.output("ldconfig", "-p"); err == nil && strings.Contains(out, lib) {
		return true, "in the loader's cache", ""
	}
	return false, lib + " not found; the window cannot open",
		"install GTK 3 (Debian/Ubuntu: libgtk-3-0), or on NixOS run nix develop"
}

var podmanCompose = regexp.MustCompile(`podman-compose version (\d+)\.(\d+)`)

func checkEngine(s system) (bool, string, string) {
	const fix = "install Podman with a compose provider, or Docker; the integration tests need one"
	engine, err := s.engine()
	if err != nil {
		return false, err.Error(), fix
	}
	out, err := s.output(engine, "compose", "version")
	if err != nil {
		return false, engine + " compose does not run",
			"install a compose provider: docker-compose, or podman-compose 1.6.0 or newer"
	}
	if m := podmanCompose.FindStringSubmatch(out); m != nil {
		v := m[1] + "." + m[2]
		if olderVersion(v, "1.6") {
			return false, engine + " with podman-compose " + v + ", which cannot wait for the servers",
				"install podman-compose 1.6.0 or newer: pipx install podman-compose==1.6.0"
		}
	}
	if engine == "podman" {
		if ok, detail, fix := checkPodmanSetup(s, strings.Contains(out, "Docker Compose")); !ok {
			return false, detail, fix
		}
	}
	return true, engine + " compose", ""
}

// podmanSocketQuery asks Podman where its API socket is.
var podmanSocketQuery = []string{"info", "--format", "{{.Host.RemoteSocket.Path}}"}

// checkPodmanSetup finds what stops an installed Podman from running the
// servers: on Linux its image policy and subordinate IDs, which the
// distribution's setup provides, and the API socket docker-compose talks to.
func checkPodmanSetup(s system, viaDockerCompose bool) (bool, string, string) {
	const nixos = "on NixOS: virtualisation.podman.enable = true"
	if s.goos == "linux" {
		dirs := []string{filepath.Join(s.getenv("HOME"), ".config"), "/etc", "/usr/share"}
		if !slices.ContainsFunc(dirs, func(dir string) bool { return s.exists(filepath.Join(dir, "containers", "policy.json")) }) {
			return false, "podman has no policy.json, so it pulls no image",
				"install your distribution's containers-common, or " + nixos
		}
	}
	out, _ := s.output("podman", podmanSocketQuery...)
	if strings.Contains(out, "cannot find UID/GID") {
		return false, "podman has no subordinate IDs for your user, which images that switch users need",
			"sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 $USER, or " + nixos
	}
	// podman info reports the socket as there whether it is or not.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if socket := lines[len(lines)-1]; viaDockerCompose && s.goos != "windows" && !s.exists(socket) {
		return false, "podman compose runs docker-compose, which needs Podman's socket, and it is not running",
			"systemctl --user enable --now podman.socket, or set PODMAN_COMPOSE_PROVIDER=podman-compose"
	}
	return true, "", ""
}

var (
	postgresImage = regexp.MustCompile(`image:\s*(?:\S*/)?postgres:(\d+)`)
	pgDumpVersion = regexp.MustCompile(`\(PostgreSQL\) (\d+)`)
)

func checkPgDump(s system) (bool, string, string) {
	compose, err := s.readFile("compose.yaml")
	m := postgresImage.FindSubmatch(compose)
	if err != nil || m == nil {
		return false, "no postgres image in compose.yaml", "run this from the repository's root"
	}
	want := string(m[1])
	fix := "install PostgreSQL " + want + "'s client tools"
	if _, err := s.lookPath("pg_dump"); err != nil {
		return false, "not found; the backup tests skip", fix
	}
	out, _ := s.output("pg_dump", "--version")
	v := pgDumpVersion.FindStringSubmatch(out)
	if v == nil || v[1] != want {
		return false, fmt.Sprintf("%s, but the test server is PostgreSQL %s; the backup tests fail",
			strings.TrimSpace(out), want), fix
	}
	return true, "PostgreSQL " + want, ""
}
