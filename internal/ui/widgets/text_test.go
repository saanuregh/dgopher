package widgets

import "testing"

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
