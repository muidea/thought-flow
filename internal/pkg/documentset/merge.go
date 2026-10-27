package documentset

import "strings"

// Merge preserves independent edits. Overlapping changed regions remain a
// conflict, including ambiguous repeated text; it never guesses a winner.
func Merge(base, generated, manual string) (string, bool) {
	if manual == base {
		return generated, true
	}
	if generated == base || generated == manual {
		return manual, true
	}
	b, g, m := strings.Split(base, "\n"), strings.Split(generated, "\n"), strings.Split(manual, "\n")
	type edit struct {
		start, end int
		lines      []string
	}
	diff := func(next []string) edit {
		start := 0
		for start < len(b) && start < len(next) && b[start] == next[start] {
			start++
		}
		end, nend := len(b), len(next)
		for end > start && nend > start && b[end-1] == next[nend-1] {
			end--
			nend--
		}
		return edit{start, end, next[start:nend]}
	}
	a, c := diff(g), diff(m)
	if a.start > c.start {
		a, c = c, a
	}
	if a.end >= c.start {
		return "", false
	}
	out := append([]string{}, b[:a.start]...)
	out = append(out, a.lines...)
	out = append(out, b[a.end:c.start]...)
	out = append(out, c.lines...)
	out = append(out, b[c.end:]...)
	return strings.Join(out, "\n"), true
}
