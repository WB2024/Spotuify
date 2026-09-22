package match

import (
	"regexp"
	"strings"
)

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

// yearRemasterRe matches a remaster marker together with the year that
// commonly rides along with it outside of brackets — "Song - 2005
// Remaster", "Song Remastered 2011" — so the bare year doesn't survive as
// leftover noise of its own once "remaster(ed)" is stripped below (a plain
// substring replace on "remaster" alone would turn "Loot - 2005 Remaster"
// into "Loot - 2005 ", not "Loot"). Bracketed forms ("(Remastered 2011)")
// never reach here — stripBracketed already removed them.
var yearRemasterRe = regexp.MustCompile(`(?i)-?\s*((19|20)\d{2}\s*remaster(ed)?|remaster(ed)?\s*(19|20)\d{2})`)

// normalize lowercases, strips bracketed/parenthetical content and known
// noise phrases, drops punctuation, and collapses whitespace — enough to
// make "Song Title (Remastered 2011)" and "Song Title" compare equal. Used
// for track title and artist, where an edition/remaster marker doesn't
// change what song it is.
func normalize(s string) string {
	s = strings.ToLower(s)
	s = stripBracketed(s, '(', ')')
	s = stripBracketed(s, '[', ']')
	s = yearRemasterRe.ReplaceAllString(s, "")

	for _, n := range noise {
		s = strings.ReplaceAll(s, n, "")
	}

	return filterAlnum(s)
}

// filterAlnum strips everything but letters/digits from an already-
// lowercased string, folding any run of other characters (punctuation,
// brackets, ...) into a single space, then trims. Shared by normalize and
// normalizeAlbum, which differ only in what they do before calling this.
func filterAlnum(s string) string {
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

// normalizeAlbum is deliberately lighter than normalize: it lowercases and
// strips punctuation, but does NOT discard bracketed content or "noise"
// words like "remaster" — for an album name, that's exactly the signal
// that distinguishes one physical release from another ("Ready To Die" vs
// "Ready To Die (The Remaster CD And DVD)" vs a various-artists
// compilation that happens to reuse the same recording). Collapsing all of
// those to the same normalized string, the way normalize() does for track
// titles, would make AlbumSimilarity unable to tell them apart.
func normalizeAlbum(s string) string {
	return filterAlnum(strings.ToLower(s))
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

// Similarity returns a 0..1 score for how alike two strings are after
// normalization — the same comparison the fuzzy matcher uses, exported for
// other places that need to compare a Spotify name against a catalogue's
// (e.g. picking which MusicBrainz release group a Spotify album is).
func Similarity(a, b string) float64 { return similarity(a, b) }

// similarity returns a 0..1 score for how alike two strings are, based on
// normalized Levenshtein edit distance.
func similarity(a, b string) float64 {
	return levenshteinSimilarity(normalize(a), normalize(b))
}

// AlbumSimilarity compares two album names the way similarity compares
// titles/artists, but using normalizeAlbum's lighter normalization instead
// — see its doc comment for why an album needs edition markers kept
// rather than discarded. Exported for the same reason as Similarity: other
// packages (the Lidarr album resolver) need the identical comparison.
func AlbumSimilarity(a, b string) float64 {
	return levenshteinSimilarity(normalizeAlbum(a), normalizeAlbum(b))
}

func levenshteinSimilarity(na, nb string) float64 {
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
