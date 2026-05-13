package tools

import (
	"strings"
	"testing"
)

func TestJournalArgs(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		args, err := journalArgs(readJournalIn{})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-n 100") {
			t.Errorf("missing default line count: %q", joined)
		}
	})
	t.Run("rejects bad unit", func(t *testing.T) {
		_, err := journalArgs(readJournalIn{Unit: "bad name; rm -rf /"})
		if err == nil {
			t.Fatal("expected error for malicious unit")
		}
	})
	t.Run("rejects bad priority", func(t *testing.T) {
		_, err := journalArgs(readJournalIn{Priority: "panic"})
		if err == nil {
			t.Fatal("expected error for unknown priority")
		}
	})
	t.Run("accepts valid input", func(t *testing.T) {
		args, err := journalArgs(readJournalIn{Unit: "snapd.service", Priority: "err", Lines: 50})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--unit snapd.service") || !strings.Contains(joined, "-p err") {
			t.Errorf("missing parts: %q", joined)
		}
	})
	t.Run("rejects huge line count", func(t *testing.T) {
		_, err := journalArgs(readJournalIn{Lines: 999999})
		if err == nil {
			t.Fatal("expected error for excessive lines")
		}
	})
}

func TestTailLines(t *testing.T) {
	in := "a\nb\nc\nd\ne\n"
	if got := tailLines(in, 2); got != "d\ne\n" {
		t.Errorf("tailLines(in, 2) = %q", got)
	}
	if got := tailLines(in, 99); got != in {
		t.Errorf("tailLines(in, 99) = %q (want full input)", got)
	}
}

func TestSSArgs(t *testing.T) {
	t.Run("listening tcp only", func(t *testing.T) {
		args, err := ssArgs(listSocketsIn{Listening: true, Protocols: []string{"tcp"}})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-l") || !strings.Contains(joined, "-t") {
			t.Errorf("missing flags: %q", joined)
		}
	})
	t.Run("rejects unknown protocol", func(t *testing.T) {
		_, err := ssArgs(listSocketsIn{Protocols: []string{"sctp"}})
		if err == nil {
			t.Fatal("expected error for unknown protocol")
		}
	})
}
