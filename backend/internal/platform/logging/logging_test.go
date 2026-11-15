package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	l, err := New(&buf, "warn", "json")
	if err != nil {
		t.Fatal(err)
	}
	l.Info("hidden")
	l.Warn("shown")
	out := buf.String()
	if strings.Contains(out, "hidden") || !strings.Contains(out, "shown") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestNewRejectsBadInput(t *testing.T) {
	if _, err := New(&bytes.Buffer{}, "loud", "json"); err == nil {
		t.Error("expected error for bad level")
	}
	if _, err := New(&bytes.Buffer{}, "info", "xml"); err == nil {
		t.Error("expected error for bad format")
	}
}
