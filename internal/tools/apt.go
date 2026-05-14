package tools

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pkgNameRE permits the substring filter we accept on apt tool inputs. The
// dpkg package name grammar (a-z 0-9 + - .) plus uppercase for case-insensitive
// matching. We never compile this as a user regex — it's a substring filter —
// but the screen rejects shell metacharacters and control bytes up front.
var pkgNameRE = regexp.MustCompile(`^[a-zA-Z0-9._+\-]{1,64}$`)

// -------- apt_update_status -------------------------------------------------

type aptUpdateStatusIn struct{}

type aptUpdateStatusOut struct {
	UpdatesPending         int       `json:"updates_pending"`
	SecurityUpdatesPending int       `json:"security_updates_pending"`
	RebootRequired         bool      `json:"reboot_required"`
	RebootRequiredPackages []string  `json:"reboot_required_packages,omitempty"`
	LastAptUpdate          time.Time `json:"last_apt_update"`
	LastAptUpdateAgeSec    int64     `json:"last_apt_update_age_sec"`
}

// -------- list_upgradable_packages -----------------------------------------

type listUpgradableIn struct {
	SecurityOnly bool   `json:"security_only,omitempty" jsonschema:"only return packages with security updates"`
	NamePattern  string `json:"name_pattern,omitempty"  jsonschema:"optional case-insensitive substring filter on package name (1-64 chars, [a-zA-Z0-9._+-])"`
	Limit        int    `json:"limit,omitempty"         jsonschema:"max packages to return (default 500, max 2000)"`
}

type upgradablePackage struct {
	Name             string `json:"name"`
	Architecture     string `json:"architecture"`
	InstalledVersion string `json:"installed_version"`
	CandidateVersion string `json:"candidate_version"`
	Origin           string `json:"origin,omitempty"`
	Suite            string `json:"suite,omitempty"`
	Security         bool   `json:"security"`
}

type listUpgradableOut struct {
	Count    int                 `json:"count"`
	Packages []upgradablePackage `json:"packages"`
}

// -------- list_installed_packages -------------------------------------------

type listInstalledIn struct {
	NamePattern string `json:"name_pattern,omitempty" jsonschema:"optional case-insensitive substring filter on package name (1-64 chars, [a-zA-Z0-9._+-])"`
	Limit       int    `json:"limit,omitempty"        jsonschema:"max packages to return (default 1000, max 5000)"`
}

type installedPackage struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	Status       string `json:"status"`
	Source       string `json:"source,omitempty"`
	Section      string `json:"section,omitempty"`
}

type listInstalledOut struct {
	Count    int                `json:"count"`
	Packages []installedPackage `json:"packages"`
}

// ---------------------------------------------------------------------------

func registerApt(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "apt_update_status",
		Description: "One-call answer to \"is this host up to date?\". Returns counts of " +
			"pending apt updates (total and security), whether a reboot is required, the " +
			"packages that triggered the reboot flag, and the timestamp of the last " +
			"successful apt-update. Reads /var/lib/dpkg/status and /var/lib/apt/lists/ " +
			"directly — inside the snap this requires the system-backup interface to be " +
			"connected (`snap connect fleetmind:system-backup`).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ aptUpdateStatusIn) (*mcp.CallToolResult, aptUpdateStatusOut, error) {
		upgrades, err := d.AptDB.UpgradablePackages()
		if err != nil {
			return nil, aptUpdateStatusOut{}, fmt.Errorf("upgradable: %w", err)
		}
		security := 0
		for _, u := range upgrades {
			if u.Security {
				security++
			}
		}
		required, rebootPkgs, err := d.AptDB.RebootRequired()
		if err != nil {
			return nil, aptUpdateStatusOut{}, fmt.Errorf("reboot-required: %w", err)
		}
		last, err := d.AptDB.LastUpdate()
		if err != nil {
			return nil, aptUpdateStatusOut{}, fmt.Errorf("last-update: %w", err)
		}
		out := aptUpdateStatusOut{
			UpdatesPending:         len(upgrades),
			SecurityUpdatesPending: security,
			RebootRequired:         required,
			RebootRequiredPackages: rebootPkgs,
			LastAptUpdate:          last,
			LastAptUpdateAgeSec:    int64(time.Since(last).Seconds()),
		}
		rebootStr := "no"
		if required {
			rebootStr = "yes"
		}
		return textResult("%d updates pending (%d security), reboot required: %s",
			out.UpdatesPending, out.SecurityUpdatesPending, rebootStr), out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_upgradable_packages",
		Description: "Packages with an available upgrade. Each entry includes installed " +
			"and candidate versions, the source pocket (e.g. noble-security, noble-updates) " +
			"and a security flag derived from the suite name. Filter to security-only or " +
			"by name substring.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in listUpgradableIn) (*mcp.CallToolResult, listUpgradableOut, error) {
		limit, err := boundedLimit(in.Limit, 500, 2000)
		if err != nil {
			return nil, listUpgradableOut{}, err
		}
		if in.NamePattern != "" && !pkgNameRE.MatchString(in.NamePattern) {
			return nil, listUpgradableOut{}, fmt.Errorf("invalid name_pattern %q", in.NamePattern)
		}
		upgrades, err := d.AptDB.UpgradablePackages()
		if err != nil {
			return nil, listUpgradableOut{}, fmt.Errorf("upgradable: %w", err)
		}
		needle := strings.ToLower(in.NamePattern)
		out := listUpgradableOut{Packages: make([]upgradablePackage, 0, len(upgrades))}
		for _, u := range upgrades {
			if in.SecurityOnly && !u.Security {
				continue
			}
			if needle != "" && !strings.Contains(strings.ToLower(u.Name), needle) {
				continue
			}
			out.Packages = append(out.Packages, upgradablePackage{
				Name:             u.Name,
				Architecture:     u.Architecture,
				InstalledVersion: u.InstalledVersion,
				CandidateVersion: u.CandidateVersion,
				Origin:           u.Origin,
				Suite:            u.Suite,
				Security:         u.Security,
			})
		}
		sort.SliceStable(out.Packages, func(i, j int) bool { return out.Packages[i].Name < out.Packages[j].Name })
		if len(out.Packages) > limit {
			out.Packages = out.Packages[:limit]
		}
		out.Count = len(out.Packages)
		return textResult("%d upgradable package(s)", out.Count), out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_installed_packages",
		Description: "Installed-package inventory parsed from /var/lib/dpkg/status. " +
			"Entries with \"deinstall\" status (residual config-files) are excluded. Filter " +
			"by name substring; limit defaults to 1000 entries to keep results bounded.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in listInstalledIn) (*mcp.CallToolResult, listInstalledOut, error) {
		limit, err := boundedLimit(in.Limit, 1000, 5000)
		if err != nil {
			return nil, listInstalledOut{}, err
		}
		if in.NamePattern != "" && !pkgNameRE.MatchString(in.NamePattern) {
			return nil, listInstalledOut{}, fmt.Errorf("invalid name_pattern %q", in.NamePattern)
		}
		pkgs, err := d.AptDB.InstalledPackages()
		if err != nil {
			return nil, listInstalledOut{}, fmt.Errorf("installed: %w", err)
		}
		needle := strings.ToLower(in.NamePattern)
		out := listInstalledOut{Packages: make([]installedPackage, 0, len(pkgs))}
		for _, p := range pkgs {
			if needle != "" && !strings.Contains(strings.ToLower(p.Name), needle) {
				continue
			}
			out.Packages = append(out.Packages, installedPackage{
				Name:         p.Name,
				Version:      p.Version,
				Architecture: p.Architecture,
				Status:       p.Status,
				Source:       p.Source,
				Section:      p.Section,
			})
		}
		sort.SliceStable(out.Packages, func(i, j int) bool { return out.Packages[i].Name < out.Packages[j].Name })
		if len(out.Packages) > limit {
			out.Packages = out.Packages[:limit]
		}
		out.Count = len(out.Packages)
		return textResult("%d installed package(s)", out.Count), out, nil
	})
}

// boundedLimit returns in if 0<in<=max, the default when in==0, or an error
// when in is negative or above max.
func boundedLimit(in, def, max int) (int, error) {
	if in == 0 {
		return def, nil
	}
	if in < 0 {
		return 0, fmt.Errorf("limit must be > 0, got %d", in)
	}
	if in > max {
		return 0, fmt.Errorf("limit must be <= %d, got %d", max, in)
	}
	return in, nil
}
