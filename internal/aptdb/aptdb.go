// Package aptdb parses dpkg and apt on-disk state read-only. It does no
// shelling out: status, Packages index, and Release files are read directly,
// which keeps the snap's plug surface limited to filesystem-read interfaces
// (system-backup) instead of needing an apt frontend.
package aptdb

import (
	"bufio"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Root points at the filesystem root containing /var/lib/dpkg and /var/lib/apt.
// In the dev/host case this is "/"; inside the strictly-confined snap it is
// "/var/lib/snapd/hostfs" so the system-backup plug's host view is used.
type Root struct{ Path string }

// Default is the system aptdb at /.
var Default = Root{Path: "/"}

// NewRoot returns a Root rooted at the given path.
func NewRoot(path string) Root { return Root{Path: path} }

func (r Root) file(parts ...string) string {
	return filepath.Join(append([]string{r.Path}, parts...)...)
}

// Package is one stanza from /var/lib/dpkg/status.
type Package struct {
	Name         string
	Version      string
	Architecture string
	Status       string
	Source       string
	Section      string
	Priority     string
}

// AvailableEntry is one stanza from an apt Packages index, annotated with the
// Origin/Suite/Component of the index it came from.
type AvailableEntry struct {
	Version      string
	Architecture string
	Origin       string
	Suite        string
	Component    string
	Filename     string // source index file (debug aid)
}

// UpgradeInfo is one installed package whose candidate (highest available)
// version is strictly greater than the installed version.
type UpgradeInfo struct {
	Name             string
	Architecture     string
	InstalledVersion string
	CandidateVersion string
	Origin           string
	Suite            string
	Security         bool
}

// InstalledPackages returns every dpkg stanza whose status contains
// "installed" (i.e. excludes "deinstall ok config-files" etc.).
func (r Root) InstalledPackages() ([]Package, error) {
	f, err := os.Open(r.file("var/lib/dpkg/status"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Package
	err = readStanzas(f, func(s stanza) {
		status := s["Status"]
		if !strings.Contains(status, "installed") || strings.HasPrefix(status, "deinstall") {
			return
		}
		out = append(out, Package{
			Name:         s["Package"],
			Version:      s["Version"],
			Architecture: s["Architecture"],
			Status:       status,
			Source:       s["Source"],
			Section:      s["Section"],
			Priority:     s["Priority"],
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AvailablePackages walks /var/lib/apt/lists and returns every (name, arch)
// candidate keyed by package name. Each candidate is annotated with the
// Origin/Suite of its index's Release file (so we can flag security pockets).
func (r Root) AvailablePackages() (map[string][]AvailableEntry, error) {
	dir := r.file("var/lib/apt/lists")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string][]AvailableEntry{}, nil
		}
		return nil, err
	}

	releases := loadReleaseFiles(dir, entries)
	out := make(map[string][]AvailableEntry, 4096)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !isPackagesIndex(name) {
			continue
		}
		origin, suite, component := lookupRelease(name, releases)
		if err := parsePackagesIndex(filepath.Join(dir, name), origin, suite, component, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UpgradablePackages joins installed × available and returns one entry per
// installed package whose best candidate is strictly newer. Security flag is
// set when the chosen candidate's Suite ends in "-security".
func (r Root) UpgradablePackages() ([]UpgradeInfo, error) {
	installed, err := r.InstalledPackages()
	if err != nil {
		return nil, err
	}
	available, err := r.AvailablePackages()
	if err != nil {
		return nil, err
	}

	var out []UpgradeInfo
	for _, p := range installed {
		cands, ok := available[p.Name]
		if !ok {
			continue
		}
		var best *AvailableEntry
		var bestSecurity bool
		for i := range cands {
			c := &cands[i]
			if c.Architecture != p.Architecture && c.Architecture != "all" {
				continue
			}
			// Skip suites pinned below 500 by apt's defaults. The only
			// well-known case is *-backports (Ubuntu pins them to 100). This
			// keeps the count aligned with `apt list --upgradable`, which is
			// what users actually compare against.
			if strings.HasSuffix(c.Suite, "-backports") {
				continue
			}
			if CompareVersions(c.Version, p.Version) <= 0 {
				continue
			}
			if best == nil || CompareVersions(c.Version, best.Version) > 0 {
				best = c
				bestSecurity = isSecuritySuite(c.Suite)
				continue
			}
			// Same version available in multiple pockets: prefer the security
			// one so the security flag isn't lost.
			if CompareVersions(c.Version, best.Version) == 0 && isSecuritySuite(c.Suite) {
				best = c
				bestSecurity = true
			}
		}
		if best == nil {
			continue
		}
		out = append(out, UpgradeInfo{
			Name:             p.Name,
			Architecture:     p.Architecture,
			InstalledVersion: p.Version,
			CandidateVersion: best.Version,
			Origin:           best.Origin,
			Suite:            best.Suite,
			Security:         bestSecurity,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// LastUpdate returns the most recent successful apt-update timestamp. Falls
// back to the mtime of /var/lib/apt/lists/ when the success stamp is absent.
func (r Root) LastUpdate() (time.Time, error) {
	if st, err := os.Stat(r.file("var/lib/apt/periodic/update-success-stamp")); err == nil {
		return st.ModTime(), nil
	}
	st, err := os.Stat(r.file("var/lib/apt/lists"))
	if err != nil {
		return time.Time{}, err
	}
	return st.ModTime(), nil
}

// RebootRequired returns whether /var/run/reboot-required exists and, if so,
// the list of triggering packages from /var/run/reboot-required.pkgs (deduped).
func (r Root) RebootRequired() (bool, []string, error) {
	_, err := os.Stat(r.file("var/run/reboot-required"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	b, err := os.ReadFile(r.file("var/run/reboot-required.pkgs"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return true, nil, nil
		}
		return true, nil, err
	}
	seen := map[string]bool{}
	var pkgs []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		pkgs = append(pkgs, line)
	}
	sort.Strings(pkgs)
	return true, pkgs, nil
}

// -- internal helpers --------------------------------------------------------

type stanza map[string]string

// readStanzas walks an RFC822-style file (dpkg status, apt Packages indexes)
// stanza-by-stanza. Continuation lines (leading space) are folded back into
// the previous field.
func readStanzas(r io.Reader, emit func(stanza)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	cur := stanza{}
	var lastKey string
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if len(cur) > 0 {
				emit(cur)
				cur = stanza{}
				lastKey = ""
			}
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if lastKey != "" {
				cur[lastKey] += "\n" + strings.TrimSpace(line)
			}
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := line[:colon]
		val := strings.TrimSpace(line[colon+1:])
		cur[key] = val
		lastKey = key
	}
	if len(cur) > 0 {
		emit(cur)
	}
	return sc.Err()
}

// isPackagesIndex picks out compiled apt index files. We accept both
// uncompressed "*_Packages" and gzip "*_Packages.gz". apt also caches lz4
// variants in some configurations — we skip those to stay dependency-free.
func isPackagesIndex(name string) bool {
	switch {
	case strings.HasSuffix(name, "_Packages"):
		return true
	case strings.HasSuffix(name, "_Packages.gz"):
		return true
	}
	return false
}

// parsePackagesIndex reads one apt Packages file (gz or plain) and appends
// every (name, version, arch) into out.
func parsePackagesIndex(path, origin, suite, component string, out map[string][]AvailableEntry) error {
	f, err := os.Open(path) //nolint:gosec // path comes from a fixed directory walk
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	var rdr io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, gerr := gzip.NewReader(f)
		if gerr != nil {
			return gerr
		}
		defer func() { _ = gz.Close() }()
		rdr = gz
	}
	return readStanzas(rdr, func(s stanza) {
		name := s["Package"]
		if name == "" {
			return
		}
		out[name] = append(out[name], AvailableEntry{
			Version:      s["Version"],
			Architecture: s["Architecture"],
			Origin:       origin,
			Suite:        suite,
			Component:    component,
			Filename:     filepath.Base(path),
		})
	})
}

// loadReleaseFiles indexes every *_Release / *_InRelease file in dir by the
// shared prefix used by their sibling Packages files. apt names indexes like
//
//	<URI-encoded>_dists_<suite>_<component>_binary-<arch>_Packages
//
// and the matching release file is
//
//	<URI-encoded>_dists_<suite>_{In,}Release
//
// so we key by the "<URI-encoded>_dists_<suite>" prefix.
func loadReleaseFiles(dir string, entries []os.DirEntry) map[string]releaseInfo {
	out := map[string]releaseInfo{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		var prefix string
		switch {
		case strings.HasSuffix(name, "_InRelease"):
			prefix = strings.TrimSuffix(name, "_InRelease")
		case strings.HasSuffix(name, "_Release"):
			prefix = strings.TrimSuffix(name, "_Release")
		default:
			continue
		}
		info, err := parseReleaseFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		// Prefer InRelease (signed inline) when both are present, but only if
		// we haven't recorded anything yet for this prefix.
		if _, exists := out[prefix]; !exists {
			out[prefix] = info
		}
	}
	return out
}

type releaseInfo struct {
	Origin string
	Suite  string
}

func parseReleaseFile(path string) (releaseInfo, error) {
	f, err := os.Open(path) //nolint:gosec // fixed directory walk
	if err != nil {
		return releaseInfo{}, err
	}
	defer func() { _ = f.Close() }()
	out := releaseInfo{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	// InRelease wraps the Release content in a PGP signed-message envelope:
	//   -----BEGIN PGP SIGNED MESSAGE-----
	//   Hash: SHA512
	//                           <-- blank line: end of PGP header
	//   Origin: Ubuntu
	//   ...
	// We detect the PGP armor and skip until the blank separator before
	// parsing. Plain Release files have no envelope and are parsed directly.
	inEnvelope := false
	pastEnvelopeHeader := false
	for sc.Scan() {
		line := sc.Text()
		if !inEnvelope && !pastEnvelopeHeader && strings.HasPrefix(line, "-----BEGIN PGP") {
			inEnvelope = true
			continue
		}
		if inEnvelope && !pastEnvelopeHeader {
			if line == "" {
				pastEnvelopeHeader = true
			}
			continue
		}
		if line == "" {
			// End of the Release header stanza; everything after is file hashes
			// (potentially megabytes) and a PGP signature.
			break
		}
		if strings.HasPrefix(line, "-----BEGIN PGP SIGNATURE-----") {
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "Origin":
			out.Origin = v
		case "Suite":
			out.Suite = v
		case "Codename":
			if out.Suite == "" {
				out.Suite = v
			}
		}
	}
	return out, sc.Err()
}

// lookupRelease finds the Release file whose prefix matches a Packages file
// name, returning Origin, Suite, and the component segment from the filename.
func lookupRelease(packagesFile string, releases map[string]releaseInfo) (origin, suite, component string) {
	trimmed := strings.TrimSuffix(packagesFile, ".gz")
	trimmed = strings.TrimSuffix(trimmed, "_Packages")
	// trimmed = "<URI>_dists_<suite>_<component>_binary-<arch>"
	// Strip the trailing "_<component>_binary-<arch>" to get the release prefix.
	bIdx := strings.LastIndex(trimmed, "_binary-")
	if bIdx < 0 {
		return "", "", ""
	}
	withoutArch := trimmed[:bIdx]
	cIdx := strings.LastIndexByte(withoutArch, '_')
	if cIdx < 0 {
		return "", "", ""
	}
	component = withoutArch[cIdx+1:]
	prefix := withoutArch[:cIdx]
	info := releases[prefix]
	return info.Origin, info.Suite, component
}

// isSecuritySuite returns true for Ubuntu/Debian security pockets. Ubuntu uses
// "<codename>-security"; Debian uses "<codename>-security" or the
// "<codename>/updates" archive (also surfaced as suite "<codename>-security"
// in modern setups).
func isSecuritySuite(s string) bool {
	return strings.HasSuffix(s, "-security")
}
