package widgets

import (
	"testing"
	"time"
)

func TestSentence(t *testing.T) {
	for in, want := range map[string]string{
		"a dashboard runs reads only":    "A dashboard runs reads only.",
		"The project has no connection.": "The project has no connection.",
		" is it? ":                       "Is it?",
		"":                               "",
		"élan":                           "Élan.",
	} {
		if got := Sentence(in); got != want {
			t.Errorf("Sentence(%q) = %q", in, got)
		}
	}
}

func TestClock(t *testing.T) {
	for in, want := range map[time.Duration]string{
		0:                       "0:00",
		7400 * time.Millisecond: "0:07",
		67 * time.Second:        "1:07",
		10 * time.Minute:        "10:00",
		time.Hour + 2*time.Minute + 3*time.Second: "1:02:03",
	} {
		if got := Clock(in); got != want {
			t.Errorf("Clock(%v) = %q, want %q", in, got, want)
		}
	}
}
