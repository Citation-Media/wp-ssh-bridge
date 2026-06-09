package app

import (
	"bytes"
	"strings"
	"testing"
)

func TestUIPrefixWriterLabelsEveryLine(t *testing.T) {
	t.Parallel()
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	ui := NewCLIUI(&stdout, &stderr)

	writer := ui.PrefixedWriter("remote", false)
	if _, err := writer.Write([]byte("first\nsecond")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	flushPrefixed(writer)

	output := stdout.String()
	for _, want := range []string{"remote │ first", "remote │ second"} {
		if !strings.Contains(output, want) {
			t.Fatalf("prefixed output missing %q:\n%s", want, output)
		}
	}
}

func TestRunStepPrintsStartAndSuccess(t *testing.T) {
	t.Parallel()
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	if err := app.runStep("Doing work", "Work done", func() error { return nil }); err != nil {
		t.Fatalf("runStep() error = %v", err)
	}

	output := stdout.String()
	for _, want := range []string{"• Doing work", "✓ Work done"} {
		if !strings.Contains(output, want) {
			t.Fatalf("step output missing %q:\n%s", want, output)
		}
	}
}

func TestDuplicateSummaryWriterSuppressesRepeatedWarnings(t *testing.T) {
	t.Parallel()
	output := bytes.Buffer{}
	writer := newDuplicateSummaryWriter(&output, func(line string) bool {
		return strings.Contains(line, "WPML_Notice")
	}, "Warning: repeated WPML_Notice warnings suppressed")

	if _, err := writer.Write([]byte("Warning: Skipping an uninitialized class \"WPML_Notice\"\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := writer.Write([]byte("Warning: Skipping an uninitialized class \"WPML_Notice\"\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := writer.Write([]byte("Success: Made 1 replacements.\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	text := output.String()
	if count := strings.Count(text, "Skipping an uninitialized class"); count != 1 {
		t.Fatalf("expected one original warning, got %d:\n%s", count, text)
	}
	for _, want := range []string{
		"Success: Made 1 replacements.",
		"Warning: repeated WPML_Notice warnings suppressed (1 repeated warnings suppressed)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
}
