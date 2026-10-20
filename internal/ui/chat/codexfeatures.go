package chat

import (
	"bufio"
	"context"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// codexKnown are the features the installed Codex has, read once from
// `codex features list`; nil where it could not be read.
var codexKnown = sync.OnceValue(func() map[string]bool {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "codex", "features", "list").Output()
	if err != nil {
		return nil
	}
	return parseCodexFeatures(string(out))
})

// parseCodexFeatures reads `codex features list`: a feature a line, its
// name, its stage and whether it is on; those removed it leaves out,
// as there is nothing left of them to turn off.
func parseCodexFeatures(out string) map[string]bool {
	known := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 || slices.Contains(fields[1:len(fields)-1], "removed") {
			continue
		}
		known[fields[0]] = true
	}
	return known
}

// codexOff are the features of codexFeaturesOff the installed Codex has:
// it refuses to start with one it does not know. Unread, all of them.
func codexOff(known map[string]bool) []string {
	if len(known) == 0 {
		return codexFeaturesOff
	}
	return slices.DeleteFunc(slices.Clone(codexFeaturesOff), func(f string) bool { return !known[f] })
}
