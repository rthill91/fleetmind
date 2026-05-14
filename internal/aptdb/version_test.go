package aptdb

import "testing"

func TestCompareVersions(t *testing.T) {
	// Cases drawn from the dpkg test corpus and deb-version(7).
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0", 0},
		{"1.0", "1.1", -1},
		{"1.1", "1.0", 1},

		// Epoch dominates upstream.
		{"1:1.0", "2.0", 1},
		{"2.0", "1:1.0", -1},
		{"1:1.0", "1:1.0", 0},

		// Tilde sorts before empty (pre-release semantics).
		{"1.0~rc1", "1.0", -1},
		{"1.0", "1.0~rc1", 1},
		{"1.0~~", "1.0~", -1},
		{"1.0~~a", "1.0~", -1},

		// Numeric segments compare as numbers, not lexicographically.
		{"1.10", "1.9", 1},
		{"1.9", "1.10", -1},
		{"1.0.10", "1.0.9", 1},

		// Letters compare in ASCII order; non-letters sort after.
		{"1.0a", "1.0b", -1},
		{"1.0b", "1.0a", 1},
		{"1.0a", "1.0+", -1}, // letter < other non-digit
		{"1.0+", "1.0a", 1},

		// Revision (after the last dash).
		{"1.0-1", "1.0-2", -1},
		{"1.0-1ubuntu1", "1.0-1ubuntu2", -1},
		{"1.0-1ubuntu1", "1.0-1", 1},

		// Leading-zero numeric segments compare equal to their unpadded forms.
		{"1.01", "1.1", 0},
		{"1.001", "1.1", 0},

		// Real Ubuntu-style cases.
		{"2.39-0ubuntu8.4", "2.39-0ubuntu8.5", -1},
		{"5.15.0-91.101", "5.15.0-92.102", -1},
		{"1:2.34.1-1ubuntu1.11", "1:2.34.1-1ubuntu1.12", -1},
	}

	for _, c := range cases {
		got := CompareVersions(c.a, c.b)
		if got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestSplit(t *testing.T) {
	cases := []struct {
		in           string
		wantEpoch    int
		wantUpstream string
		wantRevision string
	}{
		{"1.0", 0, "1.0", ""},
		{"1.0-1", 0, "1.0", "1"},
		{"1:2.0-3ubuntu1", 1, "2.0", "3ubuntu1"},
		{"1:2.0", 1, "2.0", ""},
		{"1.2-3-4", 0, "1.2-3", "4"}, // last dash wins for revision
	}
	for _, c := range cases {
		e, u, r := split(c.in)
		if e != c.wantEpoch || u != c.wantUpstream || r != c.wantRevision {
			t.Errorf("split(%q) = (%d, %q, %q), want (%d, %q, %q)",
				c.in, e, u, r, c.wantEpoch, c.wantUpstream, c.wantRevision)
		}
	}
}
