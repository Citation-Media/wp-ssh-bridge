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
