// Package containercli chooses the container engine that runs the
// integration servers of compose.yaml: Podman, or Docker.
package containercli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
)

// Override is the environment variable that names the engine to use.
const Override = "DGOPHER_CONTAINER"

// The engines in order of preference.
var engines = []string{"podman", "docker"}

// Engine returns "podman" or "docker": the one Override names, otherwise
// the first of them installed.
func Engine() (string, error) {
	if name := os.Getenv(Override); name != "" {
		if slices.Contains(engines, name) {
			return name, nil
		}
		return "", fmt.Errorf("%s=%q: want podman or docker", Override, name)
	}
	for _, e := range engines {
		if _, err := exec.LookPath(e); err == nil {
			return e, nil
		}
	}
	return "", errors.New("no container engine found: install Podman, or Docker")
}
