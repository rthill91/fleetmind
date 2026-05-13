package aptdb

import (
	"sort"
	"testing"
)

func TestInstalledPackages(t *testing.T) {
	r := NewRoot("testdata/root")
	pkgs, err := r.InstalledPackages()
	if err != nil {
		t.Fatalf("InstalledPackages: %v", err)
	}
	names := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		names = append(names, p.Name)
	}
	sort.Strings(names)

	// "removed-pkg" must be excluded (deinstall ok config-files).
	// "held-pkg" must be included (hold ok installed contains "installed").
	want := []string{"bash", "held-pkg", "linux-image-6.8.0-45-generic", "openssl"}
	if len(names) != len(want) {
		t.Fatalf("got %d packages (%v), want %d (%v)", len(names), names, len(want), want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("package[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

func TestAvailablePackages(t *testing.T) {
	r := NewRoot("testdata/root")
	avail, err := r.AvailablePackages()
	if err != nil {
		t.Fatalf("AvailablePackages: %v", err)
	}
	// openssl should appear twice (updates + security), bash once, kernel once.
	if got := len(avail["openssl"]); got != 2 {
		t.Errorf("openssl candidate count = %d, want 2", got)
	}
	if got := len(avail["bash"]); got != 1 {
		t.Errorf("bash candidate count = %d, want 1", got)
	}
	// Security index must be tagged Suite=noble-security.
	foundSecurity := false
	for _, c := range avail["openssl"] {
		if c.Suite == "noble-security" && c.Origin == "Ubuntu" {
			foundSecurity = true
		}
	}
	if !foundSecurity {
		t.Errorf("openssl: no noble-security candidate found, got %+v", avail["openssl"])
	}
}

func TestUpgradablePackages(t *testing.T) {
	r := NewRoot("testdata/root")
	up, err := r.UpgradablePackages()
	if err != nil {
		t.Fatalf("UpgradablePackages: %v", err)
	}
	// bash 5.2.21-2ubuntu4 → 5.2.21-2ubuntu4.1 (updates, not security)
	// openssl 3.0.13-0ubuntu3.4 → 3.0.13-0ubuntu3.5 (security wins over updates)
	// kernel is at the same version on both sides → no upgrade
	// held-pkg has no candidate → no upgrade
	if got := len(up); got != 2 {
		t.Fatalf("got %d upgrades (%+v), want 2", len(up), up)
	}
	byName := map[string]UpgradeInfo{}
	for _, u := range up {
		byName[u.Name] = u
	}
	if u := byName["bash"]; u.CandidateVersion != "5.2.21-2ubuntu4.1" || u.Security {
		t.Errorf("bash upgrade = %+v, want candidate 5.2.21-2ubuntu4.1, security=false", u)
	}
	if u := byName["openssl"]; u.CandidateVersion != "3.0.13-0ubuntu3.5" || !u.Security {
		t.Errorf("openssl upgrade = %+v, want candidate 3.0.13-0ubuntu3.5, security=true", u)
	}
}

func TestRebootRequired(t *testing.T) {
	r := NewRoot("testdata/root")
	required, pkgs, err := r.RebootRequired()
	if err != nil {
		t.Fatalf("RebootRequired: %v", err)
	}
	if !required {
		t.Errorf("required = false, want true")
	}
	// Deduped + sorted: linux-base, linux-image-6.8.0-45-generic
	want := []string{"linux-base", "linux-image-6.8.0-45-generic"}
	if len(pkgs) != len(want) {
		t.Fatalf("got pkgs %v, want %v", pkgs, want)
	}
	for i := range want {
		if pkgs[i] != want[i] {
			t.Errorf("pkg[%d] = %q, want %q", i, pkgs[i], want[i])
		}
	}
}

func TestRebootRequired_Absent(t *testing.T) {
	r := NewRoot("testdata/empty-root")
	required, pkgs, err := r.RebootRequired()
	if err != nil {
		t.Fatalf("RebootRequired (empty root): %v", err)
	}
	if required {
		t.Errorf("required = true on empty root, want false")
	}
	if len(pkgs) != 0 {
		t.Errorf("pkgs = %v on empty root, want empty", pkgs)
	}
}

func TestAvailablePackages_MissingDir(t *testing.T) {
	r := NewRoot("testdata/empty-root")
	avail, err := r.AvailablePackages()
	if err != nil {
		t.Fatalf("AvailablePackages (empty root): %v", err)
	}
	if len(avail) != 0 {
		t.Errorf("got %d entries on empty root, want 0", len(avail))
	}
}

func TestLastUpdate(t *testing.T) {
	r := NewRoot("testdata/root")
	if _, err := r.LastUpdate(); err != nil {
		t.Fatalf("LastUpdate: %v", err)
	}
}
