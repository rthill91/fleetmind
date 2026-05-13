package tools

import (
	"math"
	"testing"
)

func almost(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestParseDurationSecs(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"234ms", 0.234, true},
		{"1.234s", 1.234, true},
		{"1min 23.456s", 83.456, true},
		{"2h 3min 4s", 2*3600 + 3*60 + 4, true},
		{"", 0, false},
		{"garbage", 0, false},
		{"5xy", 0, false},
	}
	for _, c := range cases {
		got, ok := parseDurationSecs(c.in)
		if ok != c.ok || !almost(got, c.want) {
			t.Errorf("parseDurationSecs(%q) = (%v, %v); want (%v, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseBootTime(t *testing.T) {
	t.Run("typical bare-metal", func(t *testing.T) {
		input := "Startup finished in 5.219s (firmware) + 234ms (loader) + 2.137s (kernel) + 12.345s (userspace) = 19.935s\n" +
			"multi-user.target reached after 12.123s in userspace.\n"
		out := parseBootTime(input)
		if !almost(out.FirmwareSec, 5.219) {
			t.Errorf("firmware = %v", out.FirmwareSec)
		}
		if !almost(out.LoaderSec, 0.234) {
			t.Errorf("loader = %v", out.LoaderSec)
		}
		if !almost(out.KernelSec, 2.137) {
			t.Errorf("kernel = %v", out.KernelSec)
		}
		if !almost(out.UserspaceSec, 12.345) {
			t.Errorf("userspace = %v", out.UserspaceSec)
		}
		if !almost(out.TotalSec, 19.935) {
			t.Errorf("total = %v", out.TotalSec)
		}
		if out.TargetReached != "multi-user.target" {
			t.Errorf("target = %q", out.TargetReached)
		}
		if !almost(out.TargetReachedSec, 12.123) {
			t.Errorf("target_reached = %v", out.TargetReachedSec)
		}
	})
	t.Run("vm without firmware/loader", func(t *testing.T) {
		input := "Startup finished in 1.876s (kernel) + 4.234s (userspace) = 6.110s\n"
		out := parseBootTime(input)
		if out.FirmwareSec != 0 || out.LoaderSec != 0 {
			t.Errorf("expected zero firmware/loader, got %v/%v", out.FirmwareSec, out.LoaderSec)
		}
		if !almost(out.TotalSec, 6.110) {
			t.Errorf("total = %v", out.TotalSec)
		}
	})
	t.Run("with initrd", func(t *testing.T) {
		input := "Startup finished in 1s (kernel) + 2s (initrd) + 3s (userspace) = 6s\n"
		out := parseBootTime(input)
		if !almost(out.InitrdSec, 2) {
			t.Errorf("initrd = %v", out.InitrdSec)
		}
	})
}

func TestParseBlame(t *testing.T) {
	input := "12.345s NetworkManager-wait-online.service\n" +
		" 4.567s systemd-networkd-wait-online.service\n" +
		"   234ms snapd.service\n" +
		"1min 23.456s some-slow.service\n" +
		"\n"
	got := parseBlame(input)
	if len(got) != 4 {
		t.Fatalf("got %d entries, want 4", len(got))
	}
	// Must be sorted descending.
	if got[0].Unit != "some-slow.service" || !almost(got[0].InitSeconds, 83.456) {
		t.Errorf("entry[0] = %+v", got[0])
	}
	if got[1].Unit != "NetworkManager-wait-online.service" || !almost(got[1].InitSeconds, 12.345) {
		t.Errorf("entry[1] = %+v", got[1])
	}
	if got[3].Unit != "snapd.service" || !almost(got[3].InitSeconds, 0.234) {
		t.Errorf("entry[3] = %+v", got[3])
	}
}

func TestParseCriticalChain(t *testing.T) {
	input := "The time when unit became active or started is printed after the \"@\" character.\n" +
		"The time the unit took to start is printed after the \"+\" character.\n" +
		"\n" +
		"multi-user.target @23.476s\n" +
		"└─NetworkManager.service @17.234s +6.234s\n" +
		"  └─dbus.service @17.123s\n" +
		"    └─basic.target @17.012s\n"
	got := parseCriticalChain(input)
	if len(got) != 4 {
		t.Fatalf("got %d units, want 4: %+v", len(got), got)
	}
	if got[0].Unit != "multi-user.target" || !almost(got[0].ActiveAtSeconds, 23.476) {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].Unit != "NetworkManager.service" || !almost(got[1].ActiveAtSeconds, 17.234) || !almost(got[1].StartupSeconds, 6.234) {
		t.Errorf("got[1] = %+v", got[1])
	}
	if got[3].StartupSeconds != 0 {
		t.Errorf("got[3] should have no startup seconds: %+v", got[3])
	}
}
