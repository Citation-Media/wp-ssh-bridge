package app

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pterm/pterm"
	"golang.org/x/term"
)

// CLIUI centralizes terminal styling so command output stays readable in logs.
type CLIUI struct {
	out   io.Writer
	err   io.Writer
	color bool
}

// NewCLIUI creates the output helper used by command handlers and subprocesses.
func NewCLIUI(stdout io.Writer, stderr io.Writer) *CLIUI {
	return &CLIUI{
		out:   stdout,
		err:   stderr,
		color: isTerminal(stdout),
	}
}

func isTerminal(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func (ui *CLIUI) Info(format string, args ...any) {
	fmt.Fprintln(ui.out, ui.render("•", pterm.FgCyan, fmt.Sprintf(format, args...)))
}

func (ui *CLIUI) Success(format string, args ...any) {
	fmt.Fprintln(ui.out, ui.render("✓", pterm.FgGreen, fmt.Sprintf(format, args...)))
}

func (ui *CLIUI) Warning(format string, args ...any) {
	fmt.Fprintln(ui.err, ui.render("!", pterm.FgYellow, fmt.Sprintf(format, args...)))
}

func (ui *CLIUI) Error(format string, args ...any) {
	fmt.Fprintln(ui.err, ui.render("✗", pterm.FgRed, fmt.Sprintf(format, args...)))
}

func (ui *CLIUI) Step(message string) *UIStep {
	return &UIStep{ui: ui, message: message}
}

func (ui *CLIUI) PrefixedWriter(label string, stderr bool) io.Writer {
	writer := ui.out
	if stderr {
		writer = ui.err
	}
	return &prefixWriter{
		writer: writer,
		prefix: ui.prefix(label, stderr),
	}
}

func (ui *CLIUI) render(symbol string, color pterm.Color, message string) string {
	if !ui.color {
		return symbol + " " + message
	}
	return color.Sprint(symbol) + " " + message
}

func (ui *CLIUI) prefix(label string, stderr bool) string {
	text := fmt.Sprintf("%-6s │ ", label)
	if !ui.color {
		return text
	}
	color := pterm.FgGray
	if stderr {
		color = pterm.FgYellow
	}
	if label == "remote" {
		color = pterm.FgMagenta
	}
	if label == "rsync" {
		color = pterm.FgBlue
	}
	return color.Sprint(text)
}

// UIStep prints a final success or failure line for a previously-started step.
type UIStep struct {
	ui      *CLIUI
	message string
}

func (step *UIStep) Done(message string) {
	if message == "" {
		message = step.message
	}
	step.ui.Success("%s", message)
}

func (step *UIStep) Failed(err error) {
	step.ui.Error("%s failed: %s", step.message, err)
}

type commandOutputError struct {
	err    error
	stderr string
}

func (err commandOutputError) Error() string {
	details := strings.TrimSpace(err.stderr)
	if details == "" {
		return err.err.Error()
	}
	return err.err.Error() + ": " + compactWhitespace(details)
}

func (err commandOutputError) Unwrap() error {
	return err.err
}

func compactWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// prefixWriter prefixes every complete subprocess output line with its source.
type prefixWriter struct {
	writer io.Writer
	prefix string
	buffer bytes.Buffer
}

func (w *prefixWriter) Write(data []byte) (int, error) {
	total := len(data)
	for len(data) > 0 {
		index := bytes.IndexByte(data, '\n')
		if index == -1 {
			_, _ = w.buffer.Write(data)
			return total, nil
		}

		_, _ = w.buffer.Write(data[:index])
		if err := w.flushLine(); err != nil {
			return 0, err
		}
		data = data[index+1:]
	}
	return total, nil
}

func (w *prefixWriter) Flush() error {
	if w.buffer.Len() == 0 {
		return nil
	}
	return w.flushLine()
}

func (w *prefixWriter) flushLine() error {
	line := strings.TrimRight(w.buffer.String(), "\r")
	w.buffer.Reset()
	if line == "" {
		_, err := fmt.Fprintln(w.writer)
		return err
	}
	_, err := fmt.Fprintln(w.writer, w.prefix+line)
	return err
}

// duplicateSummaryWriter prints the first matching line and summarizes repeats on flush.
type duplicateSummaryWriter struct {
	writer      io.Writer
	match       func(string) bool
	summaryText string
	seen        map[string]bool
	suppressed  int
	buffer      bytes.Buffer
}

func newDuplicateSummaryWriter(writer io.Writer, match func(string) bool, summaryText string) *duplicateSummaryWriter {
	return &duplicateSummaryWriter{
		writer:      writer,
		match:       match,
		summaryText: summaryText,
		seen:        map[string]bool{},
	}
}

func (w *duplicateSummaryWriter) Write(data []byte) (int, error) {
	total := len(data)
	for len(data) > 0 {
		index := bytes.IndexByte(data, '\n')
		if index == -1 {
			_, _ = w.buffer.Write(data)
			return total, nil
		}

		_, _ = w.buffer.Write(data[:index])
		if err := w.flushLine(); err != nil {
			return 0, err
		}
		data = data[index+1:]
	}
	return total, nil
}

func (w *duplicateSummaryWriter) Flush() error {
	if w.buffer.Len() > 0 {
		if err := w.flushLine(); err != nil {
			return err
		}
	}
	if w.suppressed > 0 {
		if _, err := fmt.Fprintf(w.writer, "%s (%d repeated warnings suppressed)\n", w.summaryText, w.suppressed); err != nil {
			return err
		}
	}
	if flusher, ok := w.writer.(interface{ Flush() error }); ok {
		return flusher.Flush()
	}
	return nil
}

func (w *duplicateSummaryWriter) flushLine() error {
	line := strings.TrimRight(w.buffer.String(), "\r")
	w.buffer.Reset()
	if line == "" || !w.match(line) {
		_, err := fmt.Fprintln(w.writer, line)
		return err
	}
	if w.seen[line] {
		w.suppressed++
		return nil
	}
	w.seen[line] = true
	_, err := fmt.Fprintln(w.writer, line)
	return err
}
