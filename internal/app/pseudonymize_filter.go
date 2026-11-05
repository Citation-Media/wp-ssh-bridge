package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// pseudonymizeFilter rewrites a mysqldump stream while it passes through this process, so
// nothing but the result is ever written to disk.
//
// One goroutine reads statements and tracks the table, a pool of workers rewrites them,
// and one writer puts them back in order. Tables other tables look people up in run ahead
// of the rest, and the reader waits for them to finish before it starts the next table,
// so a lookup is always complete by the time it is used.
//
// Any input it cannot parse stops the run, because passing an unparsed row through could
// leak what it holds.
type pseudonymizeFilter struct {
	spec     pseudonymizeSpec
	prefix   string
	dumps    int
	registry *identityRegistry
	names    *nameDictionary
	metas    []*compiledMeta
	rules    map[string]*tableRule
	keepText map[string]struct{}

	table      *compiledTable
	inCreate   bool
	usersSeen  bool
	started    bool
	finished   int
	pending    []byte
	raw        []byte
	inRoutine  bool
	layouts    map[*compiledTable]*tableLayout
	layoutsMu  sync.RWMutex
	headLayout sync.Map

	statements *piiScanner

	report    map[string]*pseudonymizeTableReport
	reportMu  sync.Mutex
	failure   error
	failureMu sync.Mutex
	cancel    context.CancelFunc
	ctx       context.Context

	work    chan *filterJob
	order   chan *filterJob
	running sync.WaitGroup
}

type filterCounters struct {
	rows, dropped, emails, other int
}

// filterJob is one statement, or a batch of the plain lines between statements.
type filterJob struct {
	statement []byte
	table     *compiledTable
	out       []byte
	done      chan struct{}
}

var filterBuffers = sync.Pool{New: func() any { return make([]byte, 0, 64*1024) }}

func newPseudonymizeFilter(spec pseudonymizeSpec, prefix string, dumps int) (*pseudonymizeFilter, error) {
	registry, err := newIdentityRegistry(spec.EmailDomain, spec.KeepUserIDs)
	if err != nil {
		return nil, err
	}
	filter := &pseudonymizeFilter{
		spec:     spec,
		prefix:   prefix,
		dumps:    max(dumps, 1),
		registry: registry,
		names:    newNameDictionary(),
		rules:    make(map[string]*tableRule, len(spec.Tables)),
		report:   map[string]*pseudonymizeTableReport{},
		layouts:  map[*compiledTable]*tableLayout{},
	}
	filter.statements = &piiScanner{registry: registry, emailsOnly: true}
	if len(spec.KeepText) > 0 {
		filter.keepText = make(map[string]struct{}, len(spec.KeepText))
		for _, text := range spec.KeepText {
			filter.keepText[text] = struct{}{}
			filter.names.protect([]byte(text))
		}
		filter.names.publish()
	}
	for name := range spec.Tables {
		rule := spec.Tables[name]
		if rule.Meta != nil {
			compiled, err := compileMetaRule(rule.Meta, len(filter.metas))
			if err != nil {
				return nil, fmt.Errorf("pseudonymization rule for %s: %w", name, err)
			}
			filter.metas = append(filter.metas, compiled)
		}
		filter.rules[name] = &rule
	}
	return filter, nil
}

func (f *pseudonymizeFilter) metaFor(rule *metaRule) *compiledMeta {
	for _, meta := range f.metas {
		if meta.rule == rule {
			return meta
		}
	}
	return nil
}

// run filters input into output and returns the counters per table. On an error, whatever
// reached output must be thrown away.
func (f *pseudonymizeFilter) run(ctx context.Context, input io.Reader, output io.Writer) (map[string]pseudonymizeTableReport, error) {
	f.ctx, f.cancel = context.WithCancel(ctx)
	defer f.cancel()
	workers := max(runtime.GOMAXPROCS(0), 1)
	f.work = make(chan *filterJob, workers)
	f.order = make(chan *filterJob, workers*3)

	var pool sync.WaitGroup
	for range workers {
		pool.Add(1)
		go func() {
			defer pool.Done()
			worker := f.newWorker()
			for job := range f.work {
				worker.process(job)
				f.running.Done()
			}
		}()
	}
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		f.writeInOrder(output)
	}()

	err := f.readStatements(input)
	close(f.work)
	pool.Wait()
	close(f.order)
	<-writerDone
	if failure := f.firstFailure(); failure != nil {
		return nil, failure
	}
	if err != nil {
		return nil, err
	}
	// A cancel at the end of the input stops the writer without failing anything else, and
	// what it left out must not count as a finished dump.
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	if err := writeAll(output, f.marker()); err != nil {
		return nil, err
	}
	return f.finalReport(), nil
}

func writeAll(output io.Writer, data []byte) error {
	if _, err := output.Write(data); err != nil {
		return fmt.Errorf("writing the pseudonymized export failed: %w", err)
	}
	return nil
}

// fail records the first failure and stops the pipeline.
func (f *pseudonymizeFilter) fail(err error) {
	f.failureMu.Lock()
	if f.failure == nil {
		f.failure = err
	}
	f.failureMu.Unlock()
	f.cancel()
}

func (f *pseudonymizeFilter) firstFailure() error {
	f.failureMu.Lock()
	defer f.failureMu.Unlock()
	return f.failure
}

func (f *pseudonymizeFilter) writeInOrder(output io.Writer) {
	failed := false
	for job := range f.order {
		<-job.done
		if !failed && f.ctx.Err() == nil {
			if err := writeAll(output, job.out); err != nil {
				failed = true
				f.fail(err)
			}
		}
		if cap(job.out) <= 4<<20 {
			filterBuffers.Put(job.out[:0]) //nolint:staticcheck // the slice header is small next to its buffer
		}
		job.out = nil
	}
}

// --- reading ------------------------------------------------------------------------

const filterReadBuffer = 1 << 20

func (f *pseudonymizeFilter) readStatements(input io.Reader) error {
	reader := bufio.NewReaderSize(input, filterReadBuffer)
	long := []byte(nil)
	f.raw = filterBuffers.Get().([]byte)[:0]
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			long = append(long[:0], line...)
			for errors.Is(err, bufio.ErrBufferFull) {
				line, err = reader.ReadSlice('\n')
				long = append(long, line...)
			}
			line = long
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("reading the database export failed: %w", err)
		}
		if len(line) > 0 {
			if handleErr := f.handleLine(line); handleErr != nil {
				return handleErr
			}
		}
		if err != nil {
			break
		}
		if f.ctx.Err() != nil {
			return f.ctx.Err()
		}
		if len(f.raw) >= 1<<20 {
			if err := f.flushRaw(); err != nil {
				return err
			}
		}
	}
	if err := f.flushRaw(); err != nil {
		return err
	}
	f.drain()
	if len(f.pending) > 0 {
		return errors.New("the database export ends inside an INSERT statement")
	}
	if !f.usersSeen {
		return fmt.Errorf("the export does not contain the %susers table", f.prefix)
	}
	// The exit status of the export is the first check, but a wrapper around wp can
	// swallow it. A my.cnf with skip-comments also ends up here.
	if f.finished < f.dumps {
		return errors.New(`the database export has no closing "-- Dump completed" line, so it may be incomplete; remove skip-comments or compact from the client configuration if it is set`)
	}
	return nil
}

// flushRaw hands on the plain lines collected since the last statement, to keep the order.
func (f *pseudonymizeFilter) flushRaw() error {
	if len(f.raw) == 0 {
		return nil
	}
	job := &filterJob{out: f.raw, done: closedChannel}
	f.raw = filterBuffers.Get().([]byte)[:0]
	select {
	case f.order <- job:
		return nil
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}

var closedChannel = func() chan struct{} {
	channel := make(chan struct{})
	close(channel)
	return channel
}()

var dumpStartPrefixes = [][]byte{[]byte("--"), []byte("/*"), []byte("#"), []byte("SET "), []byte("USE "), []byte("DROP "), []byte("CREATE "), []byte("LOCK "), []byte("INSERT ")}

func looksLikeDumpStart(line []byte) bool {
	for _, prefix := range dumpStartPrefixes {
		if bytes.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

var insertOrReplace = regexp.MustCompile(`(?i)^(?:INSERT|REPLACE)\b`)

// handleLine classifies one physical line.
func (f *pseudonymizeFilter) handleLine(line []byte) error {
	if !f.started {
		// WP-CLI runs PHP, whose notices land on stdout ahead of the dump.
		if !looksLikeDumpStart(line) {
			return nil
		}
		f.started = true
	}
	if f.finished >= f.dumps {
		return nil
	}
	if len(f.pending) > 0 {
		return f.continueInsert(line)
	}
	// Trigger bodies sit between DELIMITER lines and hold code, not rows, even when a line
	// of that code reads like an INSERT.
	if bytes.HasPrefix(line, []byte("DELIMITER ")) {
		f.inRoutine = !bytes.HasPrefix(line, []byte("DELIMITER ;")) || bytes.HasPrefix(line, []byte("DELIMITER ;;"))
		f.raw = append(f.raw, line...)
		return nil
	}
	if f.inRoutine {
		f.raw = append(f.raw, f.scanStatementLine(line)...)
		return nil
	}
	if bytes.HasPrefix(line, []byte("-- Dump completed")) {
		f.finished++
		// A second export of the table structures follows, with its own notices ahead.
		f.started = false
		f.raw = append(f.raw, line...)
		return nil
	}
	if bytes.HasPrefix(line, []byte("INSERT INTO ")) {
		return f.continueInsert(line)
	}
	// A client configured with insert-ignore or replace writes rows this filter would
	// otherwise pass through as ordinary statements.
	if insertOrReplace.Match(line) {
		return fmt.Errorf("the export uses a statement form the filter does not understand: %s", statementFormOf(line))
	}
	if err := f.trackSchema(line); err != nil {
		return err
	}
	f.raw = append(f.raw, f.scanStatementLine(line)...)
	return nil
}

// statementFormOf cuts a line ahead of its values, so an error message cannot hold a row.
func statementFormOf(line []byte) string {
	form := string(line)
	if index := strings.Index(form, " VALUES"); index >= 0 {
		form = form[:index]
	}
	return strings.TrimSpace(form[:min(len(form), 60)])
}

// scanStatementLine covers SQL outside of rows. A column comment or a literal in a trigger
// can name someone as well, and no length depends on these lines.
func (f *pseudonymizeFilter) scanStatementLine(line []byte) []byte {
	if bytes.IndexByte(line, '@') < 0 && bytes.IndexByte(line, '%') < 0 && bytes.IndexByte(line, '&') < 0 && bytes.IndexByte(line, '\\') < 0 && bytes.IndexByte(line, '(') < 0 && bytes.IndexByte(line, '[') < 0 {
		return line
	}
	out := f.statements.scan(line)
	if replaced := f.statements.emails; replaced > 0 && f.table != nil {
		f.addCounters(f.table, filterCounters{emails: replaced})
	}
	f.statements.emails = 0
	return out
}

func (f *pseudonymizeFilter) trackSchema(line []byte) error {
	if bytes.HasPrefix(line, []byte("CREATE TABLE ")) {
		name := createTableName(line)
		if name == "" {
			return nil
		}
		return f.enterTable(name)
	}
	if !f.inCreate {
		return nil
	}
	if line[0] == ')' {
		f.inCreate = false
		return nil
	}
	if column, ok := parseColumnDefinition(line); ok {
		f.table.columns = append(f.table.columns, column)
		return nil
	}
	for _, name := range parseUniqueKeyColumns(line) {
		for i := range f.table.columns {
			if f.table.columns[i].name == name {
				f.table.columns[i].unique = true
			}
		}
	}
	return nil
}

func createTableName(line []byte) string {
	rest := bytes.TrimPrefix(line, []byte("CREATE TABLE "))
	rest = bytes.TrimPrefix(rest, []byte("IF NOT EXISTS "))
	if len(rest) == 0 || rest[0] != '`' {
		return ""
	}
	end := bytes.IndexByte(rest[1:], '`')
	if end < 0 {
		return ""
	}
	return string(rest[1 : end+1])
}

// enterTable is the boundary the lookups rely on: the previous table's rows are all done
// before the next one's first row starts. Any table can add names, and waiting for the few
// statements still running costs next to nothing.
func (f *pseudonymizeFilter) enterTable(name string) error {
	f.drain()
	f.names.publish()
	f.table = f.compileTable(name)
	f.inCreate = true
	if name == f.prefix+"users" {
		f.usersSeen = true
	} else if !f.usersSeen {
		// Every other table resolves people through the users table.
		return fmt.Errorf("the export must start with %susers, but it starts with %s", f.prefix, name)
	}
	return nil
}

// drain waits for every statement handed out so far.
func (f *pseudonymizeFilter) drain() {
	f.running.Wait()
}

func (f *pseudonymizeFilter) compileTable(name string) *compiledTable {
	table := &compiledTable{name: name}
	if !strings.HasPrefix(name, f.prefix) {
		return table
	}
	relative := name[len(f.prefix):]
	site := ""
	if digits := leadingDigits(relative); digits > 0 && digits < len(relative)-1 && relative[digits] == '_' {
		site = relative[:digits]
		relative = relative[digits+1:]
	}
	table.protected = f.spec.Protected[relative]
	rule := f.rules[relative]
	if rule == nil || (site != "" && rule.Global) {
		return table
	}
	table.rule = rule
	table.site = site
	if rule.Meta != nil {
		table.meta = f.metaFor(rule.Meta)
	}
	return table
}

func leadingDigits(value string) int {
	i := 0
	for i < len(value) && value[i] >= '0' && value[i] <= '9' {
		i++
	}
	return i
}

// continueInsert joins the physical lines of a statement. MariaDB's client puts each row on
// its own line and MySQL's keeps them on one. A line break only ever falls between rows,
// since strings escape theirs, so a statement is complete at the first line ending in a
// semicolon.
func (f *pseudonymizeFilter) continueInsert(line []byte) error {
	line = bytes.TrimRight(line, "\r\n")
	f.pending = append(f.pending, line...)
	if len(line) == 0 || line[len(line)-1] != ';' {
		return nil
	}
	statement := f.pending
	f.pending = filterBuffers.Get().([]byte)[:0]
	if err := f.flushRaw(); err != nil {
		return err
	}
	return f.dispatch(statement)
}

func (f *pseudonymizeFilter) dispatch(statement []byte) error {
	if f.table == nil {
		return errors.New("an INSERT statement has no CREATE TABLE before it")
	}
	expected := len("INSERT INTO `") + len(f.table.name)
	if len(statement) <= expected || !bytes.HasPrefix(statement, []byte("INSERT INTO `")) ||
		string(statement[len("INSERT INTO `"):expected]) != f.table.name || statement[expected] != '`' {
		return errors.New("an INSERT statement has no CREATE TABLE before it")
	}
	if f.table.rule != nil && f.table.rule.RegisterUsers {
		// Nothing may read the user maps while this writes them.
		f.drain()
		job := &filterJob{statement: statement, table: f.table, done: make(chan struct{})}
		f.running.Add(1)
		f.newWorker().process(job)
		f.running.Done()
		select {
		case f.order <- job:
			return nil
		case <-f.ctx.Done():
			return f.ctx.Err()
		}
	}
	job := &filterJob{statement: statement, table: f.table, done: make(chan struct{})}
	select {
	case f.order <- job:
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
	f.running.Add(1)
	select {
	case f.work <- job:
		return nil
	case <-f.ctx.Done():
		f.running.Done()
		close(job.done)
		return f.ctx.Err()
	}
}

// --- workers -----------------------------------------------------------------------

// filterWorker holds what one goroutine needs per statement and per row.
type filterWorker struct {
	filter     *pseudonymizeFilter
	scanner    *piiScanner
	values     *valueScanner
	metaCaches map[int]map[string]metaKeyVerdict

	stmt    []byte
	layout  *tableLayout
	cells   []sqlCell
	changed bool

	rowIdentity   personIdentity
	identityKnown bool
	// protectedCell is the value this row's key protects, or -1.
	protectedCell int
}

func (f *pseudonymizeFilter) newWorker() *filterWorker {
	scanner := &piiScanner{registry: f.registry, names: f.names, keep: f.keepText, buffer: make([]byte, 0, 128)}
	return &filterWorker{
		filter:     f,
		scanner:    scanner,
		values:     &valueScanner{scanner: scanner, keys: map[string]piiClass{}},
		metaCaches: map[int]map[string]metaKeyVerdict{},
		cells:      make([]sqlCell, 0, 32),
	}
}

func (w *filterWorker) process(job *filterJob) {
	defer close(job.done)
	defer func() {
		// A panic message may quote the value at hand, so this one states the fact only.
		if recovered := recover(); recovered != nil {
			w.filter.fail(errors.New("the filter hit an internal error on a statement"))
		}
	}()
	if w.filter.ctx.Err() != nil {
		return
	}
	if err := w.processStatement(job); err != nil {
		w.filter.fail(err)
	}
}

func (w *filterWorker) processStatement(job *filterJob) error {
	table := job.table
	statement := job.statement
	counters := filterCounters{}
	defer func() {
		w.filter.addCounters(table, counters)
		if cap(statement) <= 4<<20 {
			filterBuffers.Put(statement[:0]) //nolint:staticcheck // the slice header is small next to its buffer
		}
	}()

	if table.rule != nil && table.rule.Drop {
		counters.rows = bytes.Count(statement, []byte("),(")) + 1
		counters.dropped = counters.rows
		job.out = filterBuffers.Get().([]byte)[:0]
		return nil
	}

	valuesAt := bytes.Index(statement, []byte(" VALUES"))
	if valuesAt < 0 {
		return errors.New("an INSERT statement has no VALUES clause")
	}
	layout, err := w.filter.layoutFor(table, statement[:valuesAt])
	if err != nil {
		return err
	}
	// A table of numbers and dates has nothing to look at, and a dump has many.
	if table.rule == nil && len(layout.scanColumns) == 0 && len(layout.autoColumns) == 0 {
		counters.rows = bytes.Count(statement, []byte("),(")) + 1
		job.out = append(append(filterBuffers.Get().([]byte)[:0], statement...), '\n')
		return nil
	}

	position := valuesAt + len(" VALUES")
	for position < len(statement) && statement[position] == ' ' {
		position++
	}
	out := filterBuffers.Get().([]byte)[:0]
	out = append(out, statement[:valuesAt]...)
	out = append(out, " VALUES "...)
	headLength := len(out)
	statementChanged := false
	w.stmt = statement
	w.layout = layout
	for {
		if position >= len(statement) || statement[position] != '(' {
			return errors.New("an INSERT statement has a malformed row")
		}
		rowStart := position
		position++
		w.cells, position, err = parseInsertRow(statement, position, w.cells[:0])
		if err != nil {
			return err
		}
		if len(w.cells) != len(layout.columns) {
			return fmt.Errorf("a row of %s has %d values for %d columns", table.name, len(w.cells), len(layout.columns))
		}
		counters.rows++
		w.changed = false
		w.identityKnown = false
		w.protectedCell = -1
		w.scanner.rowNames = w.scanner.rowNames[:0]
		keep, err := w.applyRule()
		if err != nil {
			return err
		}
		if !keep {
			counters.dropped++
			statementChanged = true
		} else {
			w.scanRow()
			counters.other += w.scanner.other
			counters.emails += w.scanner.emails
			w.scanner.other, w.scanner.emails = 0, 0
			if len(out) > headLength {
				out = append(out, ',')
			}
			if w.changed {
				statementChanged = true
				out = w.appendRow(out, rowStart, position)
			} else {
				out = append(out, statement[rowStart:position]...)
			}
		}
		if position >= len(statement) {
			return errors.New("an INSERT statement does not end its row list")
		}
		next := statement[position]
		position++
		if next == ';' {
			break
		}
		if next != ',' {
			return errors.New("an INSERT statement does not end its row list")
		}
	}
	if len(out) == headLength {
		// Every row left out: an INSERT without values would fail the import.
		job.out = out[:0]
		return nil
	}
	if statementChanged {
		out = append(out, ';', '\n')
	} else {
		out = append(append(out[:0], statement...), '\n')
	}
	job.out = out
	return nil
}

// scanRow runs what no rule covered through the column heuristics and the text scan. A
// kept account keeps its data, apart from other people's addresses in it.
func (w *filterWorker) scanRow() {
	// A meta row without a rewrite never asked who it belongs to, so ask if the answer counts.
	if !w.identityKnown && len(w.filter.spec.KeepUserIDs) > 0 && w.layout.userColumn >= 0 {
		w.identity()
	}
	kept := w.identityKnown && w.rowIdentity.kept
	w.scanner.emailsOnly = kept
	if !kept {
		w.applyAutomatic()
	}
	for _, index := range w.layout.scanColumns {
		cell := &w.cells[index]
		if cell.rewritten || cell.null || cell.end-cell.start < 3 || (!cell.quoted && !cell.hex) {
			continue
		}
		// Whatever else a site needs as it is, an email address never stays.
		w.scanner.emailsOnly = kept || w.layout.columns[index].protected || index == w.protectedCell
		value := w.cellValue(cell)
		scanned := w.values.scanValue(value, 0)
		if len(scanned) != len(value) || !bytes.Equal(scanned, value) {
			w.setCell(index, scanned)
		}
	}
	w.scanner.emailsOnly = false
}

func (w *filterWorker) appendRow(out []byte, rowStart int, rowEnd int) []byte {
	last := rowStart
	for i := range w.cells {
		cell := &w.cells[i]
		if cell.literal != nil {
			out = append(out, w.stmt[last:cell.start]...)
			out = append(out, cell.literal...)
			last = cell.end
		}
	}
	return append(out, w.stmt[last:rowEnd]...)
}

func (f *pseudonymizeFilter) addCounters(table *compiledTable, counters filterCounters) {
	f.reportMu.Lock()
	defer f.reportMu.Unlock()
	report := f.report[table.name]
	if report == nil {
		report = &pseudonymizeTableReport{Missing: table.missingColumns()}
		if table.rule != nil {
			report.Rule = 1
		}
		f.report[table.name] = report
	}
	report.Rows += counters.rows
	report.Dropped += counters.dropped
	report.Emails += counters.emails
	report.Other += counters.other
}

func (f *pseudonymizeFilter) finalReport() map[string]pseudonymizeTableReport {
	report := make(map[string]pseudonymizeTableReport, len(f.report))
	for table, counters := range f.report {
		report[table] = *counters
	}
	return report
}

// layoutFor returns the column positions for a statement's head. Generated columns and a
// client configured for complete inserts both make mysqldump name the columns in the
// statement, which then wins over the table definition.
func (f *pseudonymizeFilter) layoutFor(table *compiledTable, head []byte) (*tableLayout, error) {
	if bytes.IndexByte(head, '(') < 0 {
		f.layoutsMu.RLock()
		layout := f.layouts[table]
		f.layoutsMu.RUnlock()
		if layout != nil {
			return layout, nil
		}
		f.layoutsMu.Lock()
		defer f.layoutsMu.Unlock()
		if layout := f.layouts[table]; layout != nil {
			return layout, nil
		}
		layout, err := newTableLayout(table, append([]tableColumn(nil), table.columns...))
		if err != nil {
			return nil, err
		}
		f.layouts[table] = layout
		return layout, nil
	}
	key := table.name + string(head)
	if cached, ok := f.headLayout.Load(key); ok {
		return cached.(*tableLayout), nil
	}
	known := make(map[string]tableColumn, len(table.columns))
	for _, column := range table.columns {
		known[column.name] = column
	}
	columns := []tableColumn{}
	list := head[bytes.IndexByte(head, '(')+1:]
	for {
		start := bytes.IndexByte(list, '`')
		if start < 0 {
			break
		}
		end := bytes.IndexByte(list[start+1:], '`')
		if end < 0 {
			break
		}
		name := string(list[start+1 : start+1+end])
		column, ok := known[name]
		if !ok {
			column = tableColumn{name: name}
		}
		columns = append(columns, column)
		list = list[start+end+2:]
	}
	layout, err := newTableLayout(table, columns)
	if err != nil {
		return nil, err
	}
	f.headLayout.Store(key, layout)
	return layout, nil
}

// marker tells later commands that this database holds pseudonyms. A push reads it and
// stops rather than overwriting real customers.
func (f *pseudonymizeFilter) marker() []byte {
	table := "`" + f.prefix + "options`"
	date := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return []byte("INSERT INTO " + table + " (`option_name`, `option_value`, `autoload`) VALUES ('" + pseudonymizedOption + "', '" + date + "', 'no') " +
		"ON DUPLICATE KEY UPDATE `option_value` = VALUES(`option_value`);\n")
}
