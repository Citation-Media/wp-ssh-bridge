package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/klauspost/pgzip"
)

// pseudonymizeTableOrderAwk prints the tables it reads comma-separated, with the lookup
// tables in the order of r first, including each multisite subsite's copy, and without
// those named in d. The rank @people stands for the full table names in a. Within a rank the
// installation's own table comes before a subsite's: a stray wp_2_users must not precede
// wp_users. mysqldump keeps the order of --tables, and the filter needs a lookup before its
// first use.
const pseudonymizeTableOrderAwk = `BEGIN { n = split(r, rank, " "); m = split(d, skip, " "); k = split(a, list, " "); for (i = 1; i <= k; i++) people[list[i]] = 1 } NF { for (i = 1; i <= m; i++) if ($0 ~ ("^" p "([0-9]+_)?" skip[i] "$")) next; place = n + 1; for (i = 1; i <= n; i++) { if (rank[i] == "` + dumpOrderPeople + `") { if ($0 in people) { place = i; break } } else if ($0 ~ ("^" p "([0-9]+_)?" rank[i] "$")) { place = i; break } } if (!(place in out)) out[place] = $0; else if ($0 == p rank[place]) out[place] = $0 "," out[place]; else out[place] = out[place] "," $0 } END { sep = ""; for (i = 1; i <= n + 1; i++) if (i in out) { printf "%s%s", sep, out[i]; sep = "," } }`

// peopleTablesQuery names the tables that look like they hold people's names, by what their
// columns are called. It only decides the export order, so a miss costs a name in free text
// and nothing else. It holds no double quote or dollar sign, because a shell reads it.
const peopleTablesQuery = "SELECT TABLE_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() GROUP BY TABLE_NAME HAVING SUM(LOWER(COLUMN_NAME) REGEXP '(first|last|full|display|customer|contact|client|member|sender|recipient|author|given|family)_?name') > 0 OR SUM(LOWER(COLUMN_NAME) IN ('vorname', 'nachname', 'surname', 'forename')) > 0 OR (SUM(LOWER(COLUMN_NAME) = 'name') > 0 AND SUM(LOWER(COLUMN_NAME) LIKE '%mail%') > 0)"

// pseudonymizeSchemaOnlyAwk prints the tables named in d, comma-separated.
const pseudonymizeSchemaOnlyAwk = `BEGIN { m = split(d, skip, " ") } NF { for (i = 1; i <= m; i++) if ($0 ~ ("^" p "([0-9]+_)?" skip[i] "$")) { printf "%s%s", sep, $0; sep = ","; next } }`

const (
	// dumpStreamMarker ends the text ahead of the compressed stream on stdout.
	dumpStreamMarker = "wp-ssh-dump-stream dumps="
	// exportStatusMarker carries the export's exit status on stderr, because POSIX sh has
	// no pipefail and the pipeline's own status is gzip's.
	exportStatusMarker = "wp-ssh-export-status="
)

// remoteDumpStreamCommands sends the export to stdout, compressed. It writes no file and
// keeps the source's work to the export and the fastest gzip level, since everything else
// happens on the machine that runs the pull.
func remoteDumpStreamCommands(wpExport string, dumpOrder []string, schemaOnly []string) string {
	rows := wpExport + ` --allow-root db export - --tables="$wp_ssh_rows"`
	structure := wpExport + ` --allow-root db export - --no-data --tables="$wp_ssh_structure"`
	list := `printf '%s\n' "$wp_ssh_tables" | tr ',' '\n' | awk -v p="$wp_ssh_prefix" -v d=` + shellQuote(strings.Join(schemaOnly, " "))
	return strings.Join([]string{
		remoteTableSelectionCommands(wpExport),
		`if [ -z "$wp_ssh_prefix" ] || [ -z "$wp_ssh_tables" ]; then echo 'Fatal: cannot pseudonymize the export: the table prefix or table list of the pull source could not be read.' >&2; exit 1; fi;`,
		// Only the order depends on this, so a failure costs nothing but that.
		`wp_ssh_people=$({ ` + wpExport + ` --allow-root --skip-plugins --skip-themes db query "` + peopleTablesQuery + `" --skip-column-names 2>/dev/null || true; } | tr '\n' ' ');`,
		`wp_ssh_rows=$(` + list + ` -v r=` + shellQuote(strings.Join(dumpOrder, " ")) + ` -v a="$wp_ssh_people" ` + shellQuote(pseudonymizeTableOrderAwk) + `);`,
		`wp_ssh_structure=$(` + list + " " + shellQuote(pseudonymizeSchemaOnlyAwk) + `);`,
		`if [ -n "$wp_ssh_structure" ]; then wp_ssh_dumps=2; else wp_ssh_dumps=1; fi;`,
		// A live site keeps priority. nice cannot wrap wp_ssh_wp, which is a shell function.
		`renice -n 10 $$ >/dev/null 2>&1 || true;`,
		`printf '` + dumpStreamMarker + `%s\n' "$wp_ssh_dumps";`,
		`{ wp_ssh_export_status=0; ` + rows + ` || wp_ssh_export_status=$?; if [ "$wp_ssh_export_status" = 0 ] && [ -n "$wp_ssh_structure" ]; then ` + structure + ` || wp_ssh_export_status=$?; fi; printf '\n` + exportStatusMarker + `%s\n' "$wp_ssh_export_status" >&2; } | gzip -1;`,
	}, " ")
}

// streamPseudonymizedDump exports the source database and rewrites it while it arrives, so
// the real data exists in the SSH pipe and this process's memory, and in no file on either
// side. The source does nothing but the export.
func (a *App) streamPseudonymizedDump(ctx context.Context, projectRoot string, target RemoteTarget, cfg Config, remoteWP string, wpExport string, mariaDBSetup string, mariaDBCleanup string, localDump string) error {
	spec, err := buildPseudonymizeSpec(cfg)
	if err != nil {
		return err
	}
	if err := os.Remove(localDump); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	remoteCommand := strings.Join([]string{
		"set -eu;",
		fmt.Sprintf("cleanup() { %s; };", mariaDBCleanup),
		"trap cleanup INT TERM HUP EXIT;",
		"cd " + shellQuote(remoteWP) + ";",
		remoteWPCLIPrelude(target),
		mariaDBSetup,
		remoteDumpStreamCommands(wpExport, pseudonymizeDumpOrder(pseudonymizeAdapters()), pseudonymizeSchemaOnlyTables(spec.Tables)),
		mariaDBCleanup + ";",
		"trap - EXIT",
	}, " ")
	disableCoreDumps()

	var preamble string
	var report map[string]pseudonymizeTableReport
	if err := a.runStep("Exporting, pseudonymizing, and downloading the pull source database", "Pull source database downloaded and pseudonymized", func() error {
		var err error
		preamble, report, err = a.filterRemoteDump(ctx, projectRoot, target, remoteCommand, spec, localDump)
		return err
	}); err != nil {
		return err
	}
	a.reportPseudonymization(report)
	return a.recordPullSourceTablePrefix(projectRoot, preamble)
}

// disableCoreDumps keeps a crash from writing the data in memory to disk. It goes for the
// rest of the process: nothing later needs a core dump, and failing to set it changes
// nothing about the pull.
func disableCoreDumps() {
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_CORE, &limit); err == nil {
		limit.Cur = 0
		_ = syscall.Setrlimit(syscall.RLIMIT_CORE, &limit)
	}
}

// filterRemoteDump runs the remote command and pipes its stdout through the filter into
// localDump. The file only appears under its final name once everything has succeeded.
func (a *App) filterRemoteDump(ctx context.Context, projectRoot string, target RemoteTarget, remoteCommand string, spec pseudonymizeSpec, localDump string) (preamble string, report map[string]pseudonymizeTableReport, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	args := a.sshArgv(target, sshTarget(target), remoteCommand)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = projectRoot
	cmd.WaitDelay = sshWaitDelay
	stderr := bytes.Buffer{}
	filteredStderr := newDuplicateSummaryWriter(&stderr, isRepeatedWarningLine, "Warning: repeated similar warnings suppressed")
	cmd.Stderr = filteredStderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", nil, err
	}
	partial := localDump + ".partial"
	_ = os.Remove(partial)
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", nil, err
	}
	defer func() {
		_ = file.Close()
		if err != nil {
			_ = os.Remove(partial)
		}
	}()
	if err := cmd.Start(); err != nil {
		return "", nil, err
	}

	preamble, report, filterErr := filterDumpStream(ctx, stdout, file, spec)
	if filterErr != nil {
		// Stops the export on the source too, rather than let it run to the end.
		cancel()
	}
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	flushPrefixed(filteredStderr)
	remoteOutput, exportStatus := splitExportStatus(stderr.String())

	// A failed export is the cause of the filter's complaint about a cut-off stream, so
	// it comes first, with what the source said.
	switch {
	case exportStatus != "" && exportStatus != "0":
		a.writeCapturedOutput("remote", remoteOutput, true)
		return preamble, nil, fmt.Errorf("the database export failed on the source with status %s, so nothing was kept", exportStatus)
	case filterErr != nil:
		a.writeCapturedOutput("remote", remoteOutput, true)
		return preamble, nil, fmt.Errorf("pseudonymizing the export failed: %w", filterErr)
	case waitErr != nil:
		a.writeCapturedOutput("remote", remoteOutput, true)
		return preamble, nil, fmt.Errorf("the connection to the pull source failed during the export: %w", waitErr)
	case exportStatus == "":
		// The status is printed in every case the shell survives, so its absence means
		// the export did not finish the way the exit status claims.
		a.writeCapturedOutput("remote", remoteOutput, true)
		return preamble, nil, errors.New("the export did not report its status, so nothing was kept")
	}
	if err = file.Sync(); err != nil {
		return preamble, nil, err
	}
	if err = file.Close(); err != nil {
		return preamble, nil, err
	}
	if err = os.Rename(partial, localDump); err != nil {
		return preamble, nil, err
	}
	return preamble, report, nil
}

// splitExportStatus takes the status line out of what the source printed to stderr, so a
// successful export shows nothing.
func splitExportStatus(stderr string) (string, string) {
	status := ""
	lines := make([]string, 0, 4)
	for _, line := range strings.Split(stderr, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), exportStatusMarker); ok {
			status = value
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), status
}

const dumpPreambleLimit = 1 << 20

// filterDumpStream reads the plain text ahead of the stream, which holds the table prefix
// and anything a login script printed, then filters the compressed rest.
func filterDumpStream(ctx context.Context, stdout io.Reader, destination io.Writer, spec pseudonymizeSpec) (string, map[string]pseudonymizeTableReport, error) {
	reader := bufio.NewReaderSize(stdout, 1<<20)
	preamble := strings.Builder{}
	dumps := 0
	for dumps == 0 {
		line, err := reader.ReadString('\n')
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), dumpStreamMarker); ok {
			dumps, _ = strconv.Atoi(value)
			if dumps < 1 {
				return preamble.String(), nil, errors.New("the source announced no export")
			}
			break
		}
		preamble.WriteString(line)
		if err != nil {
			return preamble.String(), nil, errors.New("the source stopped before the export started")
		}
		if preamble.Len() > dumpPreambleLimit {
			return preamble.String(), nil, errors.New("the source printed too much before the export started")
		}
	}
	prefix := pulledTablePrefix(preamble.String())
	if prefix == "" {
		return preamble.String(), nil, errors.New("the table prefix of the pull source could not be read")
	}
	filter, err := newPseudonymizeFilter(spec, prefix, dumps)
	if err != nil {
		return preamble.String(), nil, err
	}
	decompressor, err := pgzip.NewReader(reader)
	if err != nil {
		return preamble.String(), nil, fmt.Errorf("the source sent no compressed export: %w", err)
	}
	// Anything after the one gzip stream is a logout script's chatter, not more export.
	decompressor.Multistream(false)
	buffered := bufio.NewWriterSize(destination, 1<<20)
	compressor, err := pgzip.NewWriterLevel(buffered, pgzip.BestSpeed)
	if err != nil {
		return preamble.String(), nil, err
	}
	report, err := filter.run(ctx, decompressor, compressor)
	if err != nil {
		return preamble.String(), nil, err
	}
	if err := compressor.Close(); err != nil {
		return preamble.String(), nil, fmt.Errorf("writing the pseudonymized export failed: %w", err)
	}
	if err := buffered.Flush(); err != nil {
		return preamble.String(), nil, fmt.Errorf("writing the pseudonymized export failed: %w", err)
	}
	return preamble.String(), report, nil
}

func pulledTablePrefix(preamble string) string {
	prefix := ""
	for _, line := range strings.Split(preamble, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), tablePrefixMarker); ok {
			prefix = parseTablePrefix(value)
		}
	}
	return prefix
}
