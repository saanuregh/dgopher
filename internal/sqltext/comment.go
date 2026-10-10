package sqltext

import "strings"

// CommentText makes s safe to write after "--": every character some
// engine takes as the end of a line becomes a space, so text from names,
// defaults or errors cannot end the comment and run what follows.
func CommentText(s string) string {
	return strings.Map(func(r rune) rune {
		if (r < ' ' && r != '\t') || r == 0x7f || r == '\u0085' || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s)
}

// LineComment is s as a line comment, "-- " and CommentText(s), which no
// character of s can end.
func LineComment(s string) string {
	return "-- " + CommentText(s)
}
