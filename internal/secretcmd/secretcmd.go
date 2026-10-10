// Package secretcmd runs a command that prints a secret, such as a password manager's CLI.
package secretcmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const Timeout = 30 * time.Second

const (
	outputLimit = 64 << 10
	stderrShown = 300
)

// Split splits a command line into words as a POSIX shell would, honouring single and double
// quotes and backslash escapes, without expanding variables, globs or substitutions.
func Split(line string) ([]string, error) {
	var words []string
	var word strings.Builder
	inWord := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		case c == '\\':
			if i+1 >= len(line) {
				return nil, errors.New("the command ends with a lone backslash")
			}
			i++
			word.WriteByte(line[i])
			inWord = true
		case c == '\'':
			end := strings.IndexByte(line[i+1:], '\'')
			if end < 0 {
				return nil, errors.New("the command has an unterminated single quote")
			}
			word.WriteString(line[i+1 : i+1+end])
			i += end + 1
			inWord = true
		case c == '"':
			i++
			for ; i < len(line) && line[i] != '"'; i++ {
				if line[i] == '\\' && i+1 < len(line) && strings.IndexByte("\"\\$`", line[i+1]) >= 0 {
					i++
				}
				word.WriteByte(line[i])
			}
			if i >= len(line) {
				return nil, errors.New("the command has an unterminated double quote")
			}
			inWord = true
		default:
			word.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, word.String())
	}
	if len(words) == 0 {
		return nil, errors.New("the command is empty")
	}
	return words, nil
}

// cappedBuffer keeps reading past its limit, so that the child is not blocked or killed by a
// broken pipe, and only records that it overflowed.
type cappedBuffer struct {
	data     []byte
	overflow bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	room := outputLimit - len(b.data)
	if n > room {
		b.overflow = true
		p = p[:max(room, 0)]
	}
	b.data = append(b.data, p...)
	return n, nil
}

// Run runs the command line directly (no shell) with ctx, a Timeout deadline if ctx has none
// sooner, and returns stdout with one trailing "\n" or "\r\n" removed. The error never contains
// stdout; a command that fails is a *CommandError, with the exit status and at most 300 bytes of
// stderr.
func Run(ctx context.Context, line string) (string, error) {
	argv, err := Split(line)
	if err != nil {
		return "", err
	}
	return RunArgv(ctx, argv)
}

// RunArgv runs a command given as its words, as Run runs a line.
func RunArgv(ctx context.Context, argv []string) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("the command is empty")
	}
	argv = slices.Clone(argv)
	if strings.HasPrefix(argv[0], "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expanding ~ in the command: %w", err)
		}
		argv[0] = filepath.Join(home, argv[0][2:])
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	limit := time.Until(deadline)

	var stdout, stderr cappedBuffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// A child that hands its pipes to a grandchild would otherwise keep Wait blocked after the kill.
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", fmt.Errorf("the command took longer than %.3g seconds", limit.Seconds())
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			msg := stderr.data[:min(len(stderr.data), stderrShown)]
			detail := maskTokens(strings.TrimSpace(strings.ToValidUTF8(string(msg), "")))
			return "", &CommandError{Command: argv[0], Status: exitErr.ExitCode(), Stderr: detail}
		}
		return "", fmt.Errorf("running %s: %w", argv[0], err)
	}
	if stdout.overflow {
		return "", errors.New("the command printed more than 64 KiB")
	}
	out := string(stdout.data)
	if s, ok := strings.CutSuffix(out, "\r\n"); ok {
		out = s
	} else {
		out = strings.TrimSuffix(out, "\n")
	}
	if strings.TrimSpace(out) == "" {
		return "", errors.New("the command printed nothing")
	}
	return out, nil
}

// CommandError is a command that exited with a status other than 0. Its
// Error shows what the command printed to stderr, for the user to see why
// it failed; long runs that look like tokens are masked, but a short
// secret may remain, so the audit log keeps AuditText instead.
type CommandError struct {
	Command string // the program, argv[0]
	Status  int
	Stderr  string
}

func (e *CommandError) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("%s exited with status %d", e.Command, e.Status)
	}
	return fmt.Sprintf("%s exited with status %d: %s", e.Command, e.Status, e.Stderr)
}

// AuditText is what the audit log keeps of an error: a command's failure
// without what it printed to stderr, as "the password command failed (exit
// 2)" when the caller wrapped it as "the password command failed: %w", else
// what the caller wrapped it with followed by "sh failed (exit 2)"; any other
// error as it is.
func AuditText(err error) string {
	var ce *CommandError
	if !errors.As(err, &ce) {
		return err.Error()
	}
	failed := fmt.Sprintf("%s failed (exit %d)", ce.Command, ce.Status)
	before, after, found := strings.Cut(err.Error(), ce.Error())
	if !found {
		// A wrapper that rewrote the text may hold the stderr anywhere.
		return failed
	}
	before = strings.TrimSuffix(before, ": ")
	after = strings.ReplaceAll(after, ce.Error(), failed)
	switch {
	case before == "":
		return failed + after
	case strings.HasSuffix(before, "failed"):
		return before + fmt.Sprintf(" (exit %d)", ce.Status) + after
	}
	return before + ": " + failed + after
}

var tokenRe = regexp.MustCompile(`[A-Za-z0-9_\-+/=.:]{20,}`)

// maskTokens hides long runs that may be a secret a tool printed in its
// diagnostics, as a token in a verbose log.
func maskTokens(s string) string {
	return tokenRe.ReplaceAllString(s, "[hidden]")
}
