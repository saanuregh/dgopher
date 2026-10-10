package secretcmd

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSplit(t *testing.T) {
	tests := []struct {
		line    string
		want    []string
		wantErr bool
	}{
		{`op read "op://Prod/pg/password"`, []string{"op", "read", "op://Prod/pg/password"}, false},
		{"a\tb\nc  d", []string{"a", "b", "c", "d"}, false},
		{`'a "b" \c'`, []string{`a "b" \c`}, false},
		{`"a \" \\ \$ \` + "`" + ` \n"`, []string{"a \" \\ $ ` \\n"}, false},
		{`a\ b\"c`, []string{`a b"c`}, false},
		{`a"b c"`, []string{"ab c"}, false},
		{`x '' y`, []string{"x", "", "y"}, false},
		{`echo $HOME * ; | $(id)`, []string{"echo", "$HOME", "*", ";", "|", "$(id)"}, false},
		{`echo "abc`, nil, true},
		{`echo 'abc`, nil, true},
		{`echo abc\`, nil, true},
		{"", nil, true},
		{" \t\n", nil, true},
	}
	for _, tt := range tests {
		got, err := Split(tt.line)
		if (err != nil) != tt.wantErr {
			t.Errorf("Split(%q) error = %v, wantErr %v", tt.line, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Split(%q) = %q, want %q", tt.line, got, tt.want)
		}
	}
}

func skipWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh and printf")
	}
}

func TestRunTrimsOneNewline(t *testing.T) {
	skipWindows(t)
	got, err := Run(context.Background(), `printf 'pw\n\n'`)
	if err != nil || got != "pw\n" {
		t.Fatalf("Run = %q, %v; want %q", got, err, "pw\n")
	}
}

func TestRunErrorHidesStdout(t *testing.T) {
	skipWindows(t)
	_, err := Run(context.Background(), `sh -c 'printf secret; echo oops >&2; exit 3'`)
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "oops") || !strings.Contains(msg, "3") || strings.Contains(msg, "secret") {
		t.Fatalf("error = %q", msg)
	}
}

// What a failing command printed to stderr is in its error, for the user
// to see, and out of the text the audit log keeps.
func TestCommandErrorAuditText(t *testing.T) {
	skipWindows(t)
	_, err := Run(context.Background(), `sh -c 'echo refused hunter2 >&2; exit 3'`)
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Status != 3 || !strings.Contains(ce.Stderr, "hunter2") || !strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("error %#v", err)
	}
	for wrapped, want := range map[error]string{
		fmt.Errorf("the password command failed: %w", err): "the password command failed (exit 3)",
		fmt.Errorf("AWS IAM (RDS, Aurora): %w", err):       "AWS IAM (RDS, Aurora): sh failed (exit 3)",
		err:                        "sh failed (exit 3)",
		errors.New("no such host"): "no such host",
	} {
		if got := AuditText(wrapped); got != want {
			t.Errorf("AuditText(%q) = %q, want %q", wrapped, got, want)
		}
	}
}

func TestRunTimeout(t *testing.T) {
	skipWindows(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Run(ctx, "sleep 5")
	if err == nil {
		t.Fatal("want an error")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Run took %v", d)
	}
}

func TestRunNoShell(t *testing.T) {
	skipWindows(t)
	got, err := Run(context.Background(), "echo a; echo b")
	if err != nil || got != "a; echo b" {
		t.Fatalf("Run = %q, %v", got, err)
	}
}

func TestRunEmptyOutput(t *testing.T) {
	skipWindows(t)
	_, err := Run(context.Background(), `printf '\n'`)
	if err == nil || !strings.Contains(err.Error(), "printed nothing") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunTooMuchOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	_, err := Run(context.Background(), `sh -c 'head -c 200000 /dev/zero | tr "\0" x'`)
	if err == nil || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("got %v", err)
	}
}
