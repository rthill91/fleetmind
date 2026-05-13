package aptdb

import (
	"strconv"
	"strings"
)

// CompareVersions implements the dpkg version comparison algorithm (see
// deb-version(7)). It returns -1, 0, or 1 if a is less than, equal to, or
// greater than b. Tilde (~) sorts before everything including the empty
// string; non-letter non-digit characters sort after letters.
func CompareVersions(a, b string) int {
	ea, ua, ra := split(a)
	eb, ub, rb := split(b)
	if c := compareInt(ea, eb); c != 0 {
		return c
	}
	if c := compareUpstream(ua, ub); c != 0 {
		return c
	}
	return compareUpstream(ra, rb)
}

// split returns (epoch, upstream, revision). Epoch defaults to 0 when absent;
// revision defaults to empty.
func split(v string) (epoch int, upstream, revision string) {
	if colon := strings.IndexByte(v, ':'); colon >= 0 {
		if n, err := strconv.Atoi(v[:colon]); err == nil {
			epoch = n
			v = v[colon+1:]
		}
	}
	if dash := strings.LastIndexByte(v, '-'); dash >= 0 {
		return epoch, v[:dash], v[dash+1:]
	}
	return epoch, v, ""
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// compareUpstream compares two upstream/revision strings byte-by-byte,
// alternating between non-digit and digit runs as dpkg does.
func compareUpstream(a, b string) int {
	for len(a) > 0 || len(b) > 0 {
		// Non-digit prefix.
		i := 0
		for i < len(a) && !isDigit(a[i]) {
			i++
		}
		j := 0
		for j < len(b) && !isDigit(b[j]) {
			j++
		}
		if c := compareNonDigit(a[:i], b[:j]); c != 0 {
			return c
		}
		a, b = a[i:], b[j:]

		// Digit prefix.
		i = 0
		for i < len(a) && isDigit(a[i]) {
			i++
		}
		j = 0
		for j < len(b) && isDigit(b[j]) {
			j++
		}
		if c := compareDigits(a[:i], b[:j]); c != 0 {
			return c
		}
		a, b = a[i:], b[j:]
	}
	return 0
}

// compareNonDigit applies dpkg's modified lexicographic order: ~ < (empty) <
// letters < other non-digit characters.
func compareNonDigit(a, b string) int {
	for k := 0; k < len(a) || k < len(b); k++ {
		var ca, cb byte
		if k < len(a) {
			ca = a[k]
		}
		if k < len(b) {
			cb = b[k]
		}
		if ca == cb {
			continue
		}
		return compareInt(weight(ca), weight(cb))
	}
	return 0
}

// weight maps a byte to its sort position. Tilde sorts before "nothing"
// (weight -1 below the empty-string weight of 0); letters keep their byte
// value; other bytes are pushed above all letters.
func weight(c byte) int {
	switch {
	case c == 0:
		return 0
	case c == '~':
		return -1
	case isLetter(c):
		return int(c)
	default:
		return int(c) + 256
	}
}

// compareDigits compares numeric runs as integers, trimming any leading
// zeros. An empty run is treated as 0.
func compareDigits(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return compareInt(len(a), len(b))
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
