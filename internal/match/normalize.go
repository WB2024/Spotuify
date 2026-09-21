package match

import "strings"

// noise is a small set of common suffixes/markers that vary between a
// Spotify listing and a locally-tagged file for what is otherwise the same
// recording (remaster years, edition notes, feature credits formatted
// differently, etc). Stripping them before comparing meaningfully improves
// fuzzy-match accuracy without needing a full NLP normalizer.
var noise = []string{
	"remastered", "remaster", "radio edit", "album version", "single version",
	"deluxe edition", "deluxe", "bonus track", "explicit", "clean",
	"live", "acoustic", "mono", "stereo",
}

// normalize lowercases, strips bracketed/parenthetical content and known
// noise phrases, drops punctuation, and collapses whitespace — enough to
// make "Song Title (Remastered 2011)" and "Song Title" compare equal.
func normalize(s string) string {
	s = strings.ToLower(s)
	s = stripBracketed(s, '(', ')')
	s = stripBracketed(s, '[', ']')

	for _, n := range noise {
		s = strings.ReplaceAll(s, n, "")
	}

	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevSpace = false
		default:
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func stripBracketed(s string, open, close byte) string {
	for {
		i := strings.IndexByte(s, open)
		if i < 0 {
			return s
		}
		j := strings.IndexByte(s[i:], close)
		if j < 0 {
			return s
		}
		s = s[:i] + s[i+j+1:]
	}
}

// similarity returns a 0..1 score for how alike two strings are, based on
// normalized Levenshtein edit distance.
func similarity(a, b string) float64 {
	na, nb := normalize(a), normalize(b)
	if na == "" && nb == "" {
		return 0
	}
	if na == nb {
		return 1
	}
	dist := levenshtein(na, nb)
	maxLen := len(na)
	if len(nb) > maxLen {
		maxLen = len(nb)
	}
	if maxLen == 0 {
		return 0
	}
	return 1 - float64(dist)/float64(maxLen)
}

// levenshtein computes the edit distance between two strings (byte-wise;
// adequate here since normalize already reduces input to ASCII).
func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			curr[j] = m
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}
