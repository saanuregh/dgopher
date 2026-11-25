package widgets

import (
	"fmt"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// KeyLabel writes a shortcut as the platform does: ⌘ on macOS, Ctrl
// elsewhere.
func KeyLabel(k string) string {
	if runtime.GOOS == "darwin" {
		return k
	}
	r := strings.NewReplacer("⌘", "Ctrl+", "⌥", "Alt+", "⇧", "Shift+", "↵", "Enter")
	return r.Replace(k)
}

func FirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func HumanCount(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprint(n)
}

func HumanBytes(n int64) string {
	switch {
	case n < 0:
		return "—"
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// Count is a number of things, the noun in the plural unless one: "1
// row", "3 rows".
func Count[N ~int | ~int64](n N, noun string) string {
	return fmt.Sprintf("%d %s%s", n, noun, Plural(int(n)))
}

func Plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func FormatDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

// Clock writes how long something has run as a clock does, in minutes and
// seconds, with hours once there are any: as wide from one second to the
// next, so that what is beside it keeps its place.
func Clock(d time.Duration) string {
	s := int(d / time.Second)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// RelativeTime says how long ago d was, or how far ahead, roughly.
func RelativeTime(d time.Duration) string {
	ago := d >= 0
	if !ago {
		d = -d
	}
	var s string
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		s = Count(int(d/time.Minute), "minute")
	case d < 48*time.Hour:
		s = Count(int(d/time.Hour), "hour")
	case d < 60*24*time.Hour:
		s = Count(int(d/(24*time.Hour)), "day")
	case d < 730*24*time.Hour:
		s = Count(int(d/(30*24*time.Hour)), "month")
	default:
		s = Count(int(d/(365*24*time.Hour)), "year")
	}
	if ago {
		return s + " ago"
	}
	return "in " + s
}

func OneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// TextStart is a text's first n bytes, then an ellipsis: a text area lays
// a larger text out slowly.
func TextStart(text string, n int) string {
	if len(text) <= n {
		return text
	}
	return strings.ToValidUTF8(text[:n], "") + "\n…"
}

// Sentence makes an error's text a sentence: a capital first, a stop
// last, unless it ends in one.
func Sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	s = string(unicode.ToUpper(r)) + s[n:]
	if strings.ContainsAny(s[len(s)-1:], ".!?") {
		return s
	}
	return s + "."
}
