package tools

import (
	"strings"
	"testing"
)

func TestListUnitsArgs(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		args, err := listUnitsArgs(listUnitsIn{})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "list-units") || !strings.Contains(joined, "--output=json") {
			t.Errorf("unexpected argv: %q", joined)
		}
		if strings.Contains(joined, "--state") || strings.Contains(joined, "--type") {
			t.Errorf("unexpected filter in defaults: %q", joined)
		}
	})
	t.Run("with filters", func(t *testing.T) {
		args, err := listUnitsArgs(listUnitsIn{State: "failed", Type: "service"})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--state=failed") || !strings.Contains(joined, "--type=service") {
			t.Errorf("missing filters: %q", joined)
		}
	})
	t.Run("rejects bad state", func(t *testing.T) {
		if _, err := listUnitsArgs(listUnitsIn{State: "panic"}); err == nil {
			t.Fatal("expected error for unknown state")
		}
	})
	t.Run("rejects bad type", func(t *testing.T) {
		if _, err := listUnitsArgs(listUnitsIn{Type: "container"}); err == nil {
			t.Fatal("expected error for unknown type")
		}
	})
}

func TestParseSystemctlShow(t *testing.T) {
	input := "Id=NetworkManager.service\n" +
		"LoadState=loaded\n" +
		"ActiveState=active\n" +
		"SubState=running\n" +
		"FragmentPath=/lib/systemd/system/NetworkManager.service\n" +
		"Environment=LANG=C.UTF-8\n" + // value containing '='
		"\n"
	out := parseSystemctlShow(input)
	if out["Id"] != "NetworkManager.service" {
		t.Errorf("Id = %q", out["Id"])
	}
	if out["ActiveState"] != "active" || out["SubState"] != "running" {
		t.Errorf("active/sub = %q/%q", out["ActiveState"], out["SubState"])
	}
	if out["Environment"] != "LANG=C.UTF-8" {
		t.Errorf("Environment should preserve embedded '=': got %q", out["Environment"])
	}
}
