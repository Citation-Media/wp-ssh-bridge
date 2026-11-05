package app

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// sqlCell is one value of a parsed row, held as a span of the statement. Only what a rule
// or the scan asks for gets decoded, because most values of a dump are never looked at.
type sqlCell struct {
	start, end int
	// content is where the characters start, past the quote and any charset marker.
	content   int
	quoted    bool
	escaped   bool
	hex       bool
	null      bool
	literal   []byte
	rewritten bool
}

// parseInsertRow reads one "(…)" of a statement, starting after its opening parenthesis,
// and leaves position after the closing one.
func parseInsertRow(stmt []byte, position int, cells []sqlCell) ([]sqlCell, int, error) {
	length := len(stmt)
	for {
		start := position
		for position < length {
			c := stmt[position]
			if c == '\'' || c == ',' || c == ')' {
				break
			}
			position++
		}
		if position >= length {
			return cells, 0, errors.New("an INSERT statement ends inside a value")
		}
		cell := sqlCell{start: start}
		if stmt[position] == '\'' {
			cell.quoted = true
			cell.content = position + 1
			end, escaped, err := skipSQLString(stmt, position+1)
			if err != nil {
				return cells, 0, err
			}
			position, cell.escaped = end, escaped
		} else if position-start == 4 && bytes.EqualFold(stmt[start:position], []byte("NULL")) {
			cell.null = true
		} else if isHexLiteral(stmt[start:position]) {
			cell.hex = true
			cell.content = start + 2
		}
		cell.end = position
		cells = append(cells, cell)
		if position >= length {
			return cells, 0, errors.New("an INSERT statement has a malformed value list")
		}
		next := stmt[position]
		position++
		if next == ')' {
			return cells, position, nil
		}
		if next != ',' {
			return cells, 0, errors.New("an INSERT statement has a malformed value list")
		}
	}
}

// skipSQLString finds the closing quote of a string whose content starts at position. The
// next quote stays a valid answer until the position passes it, so a long string full of
// backslashes does not search for it again each time.
func skipSQLString(stmt []byte, position int) (int, bool, error) {
	escaped := false
	nextQuote := -1
	for {
		if position >= len(stmt) {
			return 0, false, errors.New("an INSERT statement ends inside a string")
		}
		if nextQuote < position {
			offset := bytes.IndexByte(stmt[position:], '\'')
			if offset < 0 {
				return 0, false, errors.New("an INSERT statement ends inside a string")
			}
			nextQuote = position + offset
		}
		if backslash := bytes.IndexByte(stmt[position:nextQuote], '\\'); backslash >= 0 {
			escaped = true
			position += backslash + 2
			continue
		}
		if nextQuote+1 < len(stmt) && stmt[nextQuote+1] == '\'' {
			escaped = true
			position = nextQuote + 2
			continue
		}
		return nextQuote + 1, escaped, nil
	}
}

func isHexLiteral(literal []byte) bool {
	if len(literal) < 4 || literal[0] != '0' || (literal[1] != 'x' && literal[1] != 'X') || len(literal)%2 != 0 {
		return false
	}
	for _, c := range literal[2:] {
		if hexValue(c) < 0 {
			return false
		}
	}
	return true
}

// cellValue returns the decoded value. Numbers come back as their digits, so ID columns
// need no special case; NULL comes back empty.
func (w *filterWorker) cellValue(cell *sqlCell) []byte {
	switch {
	case cell.null:
		return nil
	case cell.hex:
		decoded, err := hex.AppendDecode(nil, w.stmt[cell.content:cell.end])
		if err != nil {
			return nil
		}
		return decoded
	case !cell.quoted:
		return w.stmt[cell.start:cell.end]
	case !cell.escaped:
		return w.stmt[cell.content : cell.end-1]
	}
	return unescapeSQL(w.stmt[cell.content : cell.end-1])
}

func unescapeSQL(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == '\'' && i+1 < len(raw) && raw[i+1] == '\'' {
			out = append(out, '\'')
			i++
			continue
		}
		if c != '\\' || i+1 >= len(raw) {
			out = append(out, c)
			continue
		}
		i++
		switch raw[i] {
		case '0':
			out = append(out, 0)
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'b':
			out = append(out, 8)
		case 'Z':
			out = append(out, 0x1a)
		case '\\', '\'', '"':
			out = append(out, raw[i])
		default:
			out = append(out, '\\', raw[i])
		}
	}
	return out
}

// setCell gives a cell a new value in the notation it had, so a binary column stays
// binary. A value longer than its column would fail the import in strict mode, so it gets
// cut to fit. A cut serialized value would be broken either way, which a site tolerates.
func (w *filterWorker) setCell(index int, value []byte) {
	cell := &w.cells[index]
	column := &w.layout.columns[index]
	if column.maxLength > 0 && len(value) > column.maxLength {
		value = truncateToCharacters(value, column.maxLength)
	}
	var literal []byte
	if cell.hex && len(value) > 0 {
		literal = append(literal, "0x"...)
		literal = hex.AppendEncode(literal, value)
	} else {
		literal = make([]byte, 0, len(value)+8)
		if cell.quoted {
			literal = append(literal, w.stmt[cell.start:cell.content-1]...)
		}
		literal = append(literal, '\'')
		literal = appendSQLEscaped(literal, value)
		literal = append(literal, '\'')
	}
	cell.literal = literal
	w.changed = true
}

func truncateToCharacters(value []byte, limit int) []byte {
	if utf8.RuneCount(value) <= limit {
		return value
	}
	count, i := 0, 0
	for i < len(value) && count < limit {
		_, size := utf8.DecodeRune(value[i:])
		i += size
		count++
	}
	return value[:i]
}

func appendSQLEscaped(dst []byte, value []byte) []byte {
	for _, c := range value {
		switch c {
		case '\\':
			dst = append(dst, '\\', '\\')
		case 0:
			dst = append(dst, '\\', '0')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case 0x1a:
			dst = append(dst, '\\', 'Z')
		case '\'':
			dst = append(dst, '\\', '\'')
		case '"':
			dst = append(dst, '\\', '"')
		default:
			dst = append(dst, c)
		}
	}
	return dst
}

// --- table layout ------------------------------------------------------------------

type columnKind uint8

const (
	columnString columnKind = iota
	columnDate
	columnNumber
	columnOther
)

type tableColumn struct {
	name      string
	kind      columnKind
	maxLength int
	class     piiClass
	// binary columns hold bytes, so an IP address there needs a placeholder in bytes.
	binary bool
	// unique columns must not all receive the same placeholder, or the import would fail.
	unique bool
	// protected columns are spared everything but the email scan.
	protected bool
}

// parseColumnDefinition reads a line of CREATE TABLE. The type decides whether a value
// can hold text at all, which spares the scan every integer and date of the dump, and how
// long a replacement may be.
func parseColumnDefinition(line []byte) (tableColumn, bool) {
	trimmed := bytes.TrimLeft(line, " \t")
	if len(trimmed) == len(line) || len(trimmed) == 0 || trimmed[0] != '`' {
		return tableColumn{}, false
	}
	end := bytes.IndexByte(trimmed[1:], '`')
	if end < 0 {
		return tableColumn{}, false
	}
	column := tableColumn{name: string(trimmed[1 : end+1])}
	definition := bytes.TrimLeft(trimmed[end+2:], " ")
	typeEnd := 0
	for typeEnd < len(definition) && (isASCIILetter(definition[typeEnd]) || definition[typeEnd] >= '0' && definition[typeEnd] <= '9') {
		typeEnd++
	}
	typeName := strings.ToLower(string(definition[:typeEnd]))
	switch typeName {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint", "decimal", "numeric", "float", "double", "real":
		column.kind = columnNumber
	case "bit", "year", "enum", "set", "bool", "boolean", "point", "geometry":
		column.kind = columnOther
	case "date", "datetime", "timestamp", "time":
		column.kind = columnDate
	case "char", "varchar", "binary", "varbinary":
		if typeEnd < len(definition) && definition[typeEnd] == '(' {
			if closing := bytes.IndexByte(definition[typeEnd:], ')'); closing > 0 {
				if size, err := strconv.Atoi(string(definition[typeEnd+1 : typeEnd+closing])); err == nil {
					column.maxLength = size
				}
			}
		}
		column.binary = typeName == "binary" || typeName == "varbinary"
	case "tinytext", "tinyblob":
		column.maxLength = 255
	case "text", "blob":
		column.maxLength = 65535
	case "mediumtext", "mediumblob":
		column.maxLength = 16777215
	}
	return column, true
}

// parseUniqueKeyColumns reads the columns of a UNIQUE KEY line of CREATE TABLE.
func parseUniqueKeyColumns(line []byte) []string {
	trimmed := bytes.TrimLeft(line, " \t")
	if !bytes.HasPrefix(trimmed, []byte("UNIQUE KEY ")) {
		return nil
	}
	open := bytes.IndexByte(trimmed, '(')
	if open < 0 {
		return nil
	}
	names := []string{}
	list := trimmed[open:]
	for {
		start := bytes.IndexByte(list, '`')
		if start < 0 {
			return names
		}
		end := bytes.IndexByte(list[start+1:], '`')
		if end < 0 {
			return names
		}
		names = append(names, string(list[start+1:start+1+end]))
		list = list[start+end+2:]
	}
}

// --- compiled rules ----------------------------------------------------------------

type metaKeyClass uint8

const (
	metaKeyPlain metaKeyClass = iota
	metaKeyDrop
	metaKeySensitive
	metaKeyStrategy
	// metaKeyProtected holds what the site needs as it is, apart from email addresses.
	metaKeyProtected
)

type metaKeyVerdict struct {
	class    metaKeyClass
	strategy string
	// phrase says the value names the site, so it does not count as a person's name.
	phrase bool
}

// compiledMeta holds a meta rule with its patterns ready to run.
type compiledMeta struct {
	id        int
	rule      *metaRule
	dropKeys  map[string]bool
	drop      []*regexp.Regexp
	keep      []*regexp.Regexp
	protect   []*regexp.Regexp
	sensitive *regexp.Regexp
}

func (m *compiledMeta) classify(key string) metaKeyVerdict {
	if m.dropKeys[key] || matchesAnyPattern(m.drop, key) {
		return metaKeyVerdict{class: metaKeyDrop}
	}
	if matchesAnyPattern(m.keep, key) {
		return metaKeyVerdict{}
	}
	if strategy, ok := m.rule.Keys[key]; ok {
		return metaKeyVerdict{class: metaKeyStrategy, strategy: strategy}
	}
	if m.protectsKey(key) {
		return metaKeyVerdict{class: metaKeyProtected, phrase: stringSet(m.rule.PhraseKeys)[key]}
	}
	if m.sensitive != nil && m.sensitive.MatchString(key) {
		return metaKeyVerdict{class: metaKeySensitive}
	}
	return metaKeyVerdict{}
}

func (m *compiledMeta) protectsKey(key string) bool {
	for _, protected := range m.rule.ProtectKeys {
		if protected == key {
			return true
		}
	}
	return matchesAnyPattern(m.protect, key)
}

func matchesAnyPattern(patterns []*regexp.Regexp, key string) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(key) {
			return true
		}
	}
	return false
}

func compileMetaRule(rule *metaRule, id int) (*compiledMeta, error) {
	compiled := &compiledMeta{id: id, rule: rule, dropKeys: map[string]bool{}}
	for _, key := range rule.DropKeys {
		compiled.dropKeys[key] = true
	}
	var err error
	if compiled.drop, err = compilePatterns(rule.DropPatterns); err != nil {
		return nil, err
	}
	if compiled.keep, err = compilePatterns(rule.KeepPatterns); err != nil {
		return nil, err
	}
	if compiled.protect, err = compilePatterns(rule.ProtectPatterns); err != nil {
		return nil, err
	}
	if rule.SensitivePattern != "" {
		if compiled.sensitive, err = regexp.Compile(rule.SensitivePattern); err != nil {
			return nil, err
		}
	}
	return compiled, nil
}

func compilePatterns(patterns []string) ([]*regexp.Regexp, error) {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		value, err := regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, value)
	}
	return compiled, nil
}

// compiledTable is one table's rule for the whole run. Anything about column positions
// lives in tableLayout, since a statement may name its own column list.
type compiledTable struct {
	name    string
	rule    *tableRule
	meta    *compiledMeta
	site    string
	columns []tableColumn
	// protected names the columns and keys a site needs as they are.
	protected map[string]bool
}

func (t *compiledTable) scoped(name string) string {
	return t.site + "|" + name
}

type columnStrategy struct {
	index    int
	strategy string
}

type boundCondition struct {
	column int
	values map[string]bool
	ops    []columnStrategy
}

type boundCollect struct {
	linkMap string
	id      int
	where   *boundCondition
	store   bool
}

// autoNamePair is a first and a last name column that belong together. A table can hold
// several people's, such as billing_first_name and shipping_first_name.
type autoNamePair struct {
	scope       string
	first, last int
}

// tableLayout binds a rule to the column positions of one statement form.
type tableLayout struct {
	table   *compiledTable
	columns []tableColumn
	index   map[string]int

	ops        []columnStrategy
	conditions []boundCondition
	collects   []boundCollect

	userColumn, emailColumn, linkColumn, rowColumn int
	linkMap                                        string

	metaKey, metaValue, metaObject int

	registerID, registerEmail, registerLogin int
	firstName, lastName                      int
	fullNames                                []int
	// nameSkip spots the rows whose name column holds no person.
	nameSkip *boundCondition
	// phrases are columns that name the site's own things rather than people.
	phrases []int

	// autoColumns have no rule, but a name that promises personal data.
	autoColumns []int
	// autoNamePairs and autoFullNames are the automatic columns that hold a name.
	autoNamePairs []autoNamePair
	autoFullNames []int
	// autoObject ties the rows of a key/value pair table to one record.
	autoObject int
	// autoEmail is the column an unknown table names a person by.
	autoEmail int
	// autoKey and autoValue form a key/value pair in a table without a rule.
	autoKey, autoValue int
	// scanColumns can hold text.
	scanColumns []int
}

func (l *tableLayout) find(name string) int {
	if index, ok := l.index[name]; ok {
		return index
	}
	return -1
}

func sortedStrategies(strategies map[string]string) []string {
	names := make([]string, 0, len(strategies))
	for name := range strategies {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func newTableLayout(table *compiledTable, columns []tableColumn) (*tableLayout, error) {
	layout := &tableLayout{
		table: table, columns: columns, index: make(map[string]int, len(columns)),
		userColumn: -1, emailColumn: -1, linkColumn: -1, rowColumn: 0,
		metaKey: -1, metaValue: -1, metaObject: -1,
		registerID: -1, registerEmail: -1, registerLogin: -1,
		firstName: -1, lastName: -1, autoEmail: -1, autoKey: -1, autoValue: -1,
		autoObject: -1,
	}
	for i := range columns {
		layout.index[columns[i].name] = i
		columns[i].protected = table.protected[columns[i].name]
	}
	claimed := map[int]bool{}
	bindOps := func(strategies map[string]string) []columnStrategy {
		ops := make([]columnStrategy, 0, len(strategies))
		for _, name := range sortedStrategies(strategies) {
			if index := layout.find(name); index >= 0 {
				ops = append(ops, columnStrategy{index: index, strategy: strategies[name]})
				claimed[index] = true
			}
		}
		return ops
	}
	if rule := table.rule; rule != nil {
		layout.ops = bindOps(rule.Columns)
		for _, op := range layout.ops {
			switch op.strategy {
			case strategyFirstName:
				layout.firstName = op.index
			case strategyLastName:
				layout.lastName = op.index
			case strategyFullName:
				layout.fullNames = append(layout.fullNames, op.index)
			}
		}
		if skip := rule.NameSkip; skip != nil {
			layout.nameSkip = &boundCondition{column: layout.find(skip.Column), values: stringSet(skip.In)}
		}
		for _, name := range rule.Phrases {
			if index := layout.find(name); index >= 0 {
				layout.phrases = append(layout.phrases, index)
			}
		}
		for i := range rule.Conditions {
			condition := &rule.Conditions[i]
			layout.conditions = append(layout.conditions, boundCondition{
				column: layout.find(condition.Column),
				values: stringSet(condition.In),
				ops:    bindOps(condition.Columns),
			})
		}
		for i := range rule.Collect {
			collect := &rule.Collect[i]
			id := layout.find(collect.IDColumn)
			if id < 0 {
				return nil, fmt.Errorf("%s has no column %s, which later tables look people up by", table.name, collect.IDColumn)
			}
			bound := boundCollect{linkMap: table.scoped(collect.Map), id: id, store: collect.StoreIdentity}
			if collect.Where != nil {
				bound.where = &boundCondition{column: layout.find(collect.Where.Column), values: stringSet(collect.Where.In)}
			}
			layout.collects = append(layout.collects, bound)
		}
		if identity := rule.Identity; identity != nil {
			layout.userColumn = layout.find(identity.UserColumn)
			layout.emailColumn = layout.find(identity.EmailColumn)
			layout.linkColumn = layout.find(identity.LinkColumn)
			if identity.LinkMap != "" {
				layout.linkMap = table.scoped(identity.LinkMap)
			}
			if identity.RowColumn != "" {
				if index := layout.find(identity.RowColumn); index >= 0 {
					layout.rowColumn = index
				}
			}
		}
		if rule.RegisterUsers {
			layout.registerID = layout.find("ID")
			layout.registerEmail = layout.find("user_email")
			layout.registerLogin = layout.find("user_login")
			if layout.registerID < 0 || layout.registerEmail < 0 || layout.registerLogin < 0 {
				return nil, fmt.Errorf("%s lacks the columns ID, user_email, or user_login", table.name)
			}
		}
		if meta := rule.Meta; meta != nil {
			layout.metaKey = layout.find(meta.KeyColumn)
			layout.metaValue = layout.find(meta.ValueColumn)
			layout.metaObject = layout.find(meta.ObjectColumn)
			if layout.metaKey < 0 || layout.metaValue < 0 {
				return nil, fmt.Errorf("%s lacks the columns %s or %s", table.name, meta.KeyColumn, meta.ValueColumn)
			}
			claimed[layout.metaKey] = true
			claimed[layout.metaValue] = true
			// A key is code's handle on the value, so it stays, apart from an address in it.
			columns[layout.metaKey].protected = true
		}
	}
	layout.bindAutomatic(claimed)
	if layout.autoKey >= 0 {
		columns[layout.autoKey].protected = true
	}
	for i := range columns {
		if columns[i].kind == columnString {
			layout.scanColumns = append(layout.scanColumns, i)
		}
	}
	return layout, nil
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

// isEmailColumn spots a column holding an address, by a name that says nothing more. An
// email_id or email_template only describes an email, and turns no neighbor into a person.
func isEmailColumn(lower string) bool {
	tokens := splitKeyTokens(lower)
	if len(tokens) == 0 {
		return false
	}
	last := tokens[len(tokens)-1]
	if piiKeyTails[last] || last == "template" || last == "sent" || last == "subject" || last == "body" || last == "content" {
		return false
	}
	if strings.Contains(lower, "email") || last == "mail" {
		return true
	}
	return last == "address" && len(tokens) >= 2 && tokens[len(tokens)-2] == "mail"
}

// personColumnNames only mean a person next to an email column: on its own, "name" is as
// likely a product, a menu, or a plugin.
var personColumnNames = map[string]bool{
	"name": true, "author": true, "customer": true, "contact": true, "sender": true, "recipient": true,
	"subscriber": true, "person": true, "member": true, "owner": true,
}

var keyColumnNames = []string{"meta_key", "field_key", "field_name", "attribute", "key", "name"}
var valueColumnNames = []string{"meta_value", "field_value", "value"}

func (l *tableLayout) autoNameColumn(name string) *autoNamePair {
	scope := nameKeyScope([]byte(name))
	for i := range l.autoNamePairs {
		if l.autoNamePairs[i].scope == scope {
			return &l.autoNamePairs[i]
		}
	}
	l.autoNamePairs = append(l.autoNamePairs, autoNamePair{scope: scope, first: -1, last: -1})
	return &l.autoNamePairs[len(l.autoNamePairs)-1]
}

// keyValueRecordColumns are what a form plugin calls the entry its rows belong to.
var keyValueRecordColumns = []string{"entry_id", "submission_id", "lead_id", "form_entry_id", "object_id", "parent_id", "post_id", "user_id", "customer_id", "order_id"}

// bindAutomatic covers what no rule names, by what the columns are called: a plugin's
// phone column, or the key/value pairs a form plugin stores its entries in.
func (l *tableLayout) bindAutomatic(claimed map[int]bool) {
	hasEmail := false
	for i := range l.columns {
		column := &l.columns[i]
		column.class = piiNone
		if claimed[i] || column.kind == columnOther {
			continue
		}
		lower := strings.ToLower(column.name)
		// Keeping an address column is no reason to take its neighbors for something else.
		if isEmailColumn(lower) {
			hasEmail = true
			if l.autoEmail < 0 {
				l.autoEmail = i
			}
			continue
		}
		if column.protected {
			continue
		}
		class := classifyKey(column.name)
		if class == piiNone || class == piiVersion {
			continue
		}
		if column.kind == columnDate && class != piiBirthdate {
			continue
		}
		// A number under such a name is the value itself, unless it merely points to a
		// record: a company or city column of integers is an ID.
		if column.kind == columnNumber && class != piiPhone && class != piiPostcode && class != piiBirthdate && class != piiIP && class != piiIdentifier {
			continue
		}
		if column.kind == columnNumber && strings.HasSuffix(lower, "_id") {
			continue
		}
		column.class = class
		l.autoColumns = append(l.autoColumns, i)
		switch class {
		case piiFirstName:
			l.autoNameColumn(column.name).first = i
		case piiLastName:
			l.autoNameColumn(column.name).last = i
		case piiFullName:
			l.autoFullNames = append(l.autoFullNames, i)
		}
	}
	if hasEmail {
		for i := range l.columns {
			column := &l.columns[i]
			if !claimed[i] && !column.protected && column.kind == columnString && column.class == piiNone && personColumnNames[strings.ToLower(column.name)] {
				column.class = piiFullName
				l.autoColumns = append(l.autoColumns, i)
				l.autoFullNames = append(l.autoFullNames, i)
			}
		}
	}
	if l.table.rule == nil {
		for _, key := range keyColumnNames {
			index := l.find(key)
			if index < 0 {
				continue
			}
			for _, value := range valueColumnNames {
				if other := l.find(value); other >= 0 && other != index {
					l.autoKey, l.autoValue = index, other
					for _, name := range keyValueRecordColumns {
						if record := l.find(name); record >= 0 {
							l.autoObject = record
							break
						}
					}
					return
				}
			}
		}
	}
}

// --- applying a rule to a row ------------------------------------------------------

func (w *filterWorker) cellIsEmpty(index int) bool {
	cell := &w.cells[index]
	if cell.null {
		return true
	}
	return cell.quoted && cell.end-1 == cell.content
}

func (w *filterWorker) value(index int) []byte {
	return w.cellValue(&w.cells[index])
}

func parseIdentifier(value []byte) uint64 {
	id := uint64(0)
	for _, c := range value {
		if c < '0' || c > '9' {
			return fnvIdentifier(value)
		}
		id = id*10 + uint64(c-'0')
	}
	return id
}

// fnvIdentifier maps the rare non-numeric ID to a number, for the compact link maps.
func fnvIdentifier(value []byte) uint64 {
	hash := uint64(14695981039346656037)
	for _, c := range value {
		hash ^= uint64(c)
		hash *= 1099511628211
	}
	return hash | identityRefKept
}

func (w *filterWorker) identity() personIdentity {
	if !w.identityKnown {
		w.rowIdentity = w.resolveIdentity()
		w.identityKnown = true
	}
	return w.rowIdentity
}

func (w *filterWorker) resolveIdentity() personIdentity {
	layout := w.layout
	if layout.userColumn >= 0 {
		id := parseIdentifier(w.value(layout.userColumn))
		if id > 0 && w.filter.registry.isUser(id) {
			return w.filter.registry.forUser(id)
		}
	}
	if layout.emailColumn >= 0 {
		if email := w.value(layout.emailColumn); len(email) > 0 {
			return w.filter.registry.forEmail(string(email))
		}
	}
	if layout.linkColumn >= 0 && layout.linkMap != "" {
		if linked, ok := w.filter.registry.recall(layout.linkMap, parseIdentifier(w.value(layout.linkColumn))); ok {
			return linked
		}
	}
	return w.filter.registry.forRow(layout.table.scoped(layout.table.ruleName()) + ":" + string(w.value(layout.rowColumn)))
}

func (t *compiledTable) ruleName() string {
	if t.rule == nil {
		return t.name
	}
	return t.rule.Name
}

// applyRule runs the table's rule over the current row and reports whether to keep it.
func (w *filterWorker) applyRule() (bool, error) {
	layout := w.layout
	rule := layout.table.rule
	if rule == nil {
		return true, nil
	}
	if rule.RegisterUsers {
		w.filter.registry.registerUser(
			parseIdentifier(w.value(layout.registerID)),
			string(w.value(layout.registerEmail)),
			string(w.value(layout.registerLogin)),
		)
	}
	var verdict metaKeyVerdict
	if layout.table.meta != nil {
		verdict = w.metaVerdict()
		if verdict.class == metaKeyDrop && !w.identity().kept {
			return false, nil
		}
		if verdict.class == metaKeyProtected {
			w.protectedCell = layout.metaValue
			if verdict.phrase && !w.cellIsEmpty(layout.metaValue) {
				w.filter.names.protect(w.value(layout.metaValue))
			}
		}
	}
	w.collectRow()
	w.collectNames()
	for _, index := range layout.phrases {
		if !w.cellIsEmpty(index) {
			w.filter.names.protect(w.value(index))
		}
	}
	for _, op := range layout.ops {
		if err := w.rewrite(op.index, op.strategy); err != nil {
			return false, err
		}
	}
	for i := range layout.conditions {
		condition := &layout.conditions[i]
		if w.conditionMatches(condition) {
			for _, op := range condition.ops {
				if err := w.rewrite(op.index, op.strategy); err != nil {
					return false, err
				}
			}
		}
	}
	if layout.table.meta != nil {
		return true, w.rewriteMeta(verdict)
	}
	return true, nil
}

func (w *filterWorker) conditionMatches(condition *boundCondition) bool {
	return condition.column >= 0 && condition.values[string(w.value(condition.column))]
}

func (w *filterWorker) metaVerdict() metaKeyVerdict {
	meta := w.layout.table.meta
	cache := w.metaCaches[meta.id]
	if cache == nil {
		cache = map[string]metaKeyVerdict{}
		w.metaCaches[meta.id] = cache
	}
	key := w.value(w.layout.metaKey)
	verdict, ok := cache[string(key)]
	if !ok {
		verdict = meta.classify(string(key))
		cache[string(key)] = verdict
	}
	return verdict
}

func (w *filterWorker) collectRow() {
	layout := w.layout
	for i := range layout.collects {
		collect := &layout.collects[i]
		if collect.where != nil && !w.conditionMatches(collect.where) {
			continue
		}
		id := parseIdentifier(w.value(collect.id))
		if collect.store {
			w.filter.registry.remember(collect.linkMap, id, w.identity())
		} else {
			w.filter.registry.mark(collect.linkMap, id)
		}
	}
}

// collectNames remembers the row's real name before it gets replaced, so that the same
// name in free text elsewhere can be replaced as well.
func (w *filterWorker) collectNames() {
	layout := w.layout
	if layout.nameSkip != nil && w.conditionMatches(layout.nameSkip) {
		return
	}
	if layout.firstName >= 0 && layout.lastName >= 0 && !w.cellIsEmpty(layout.firstName) && !w.cellIsEmpty(layout.lastName) {
		w.noteName(w.joinName(layout.firstName, layout.lastName), w.identity())
	}
	for _, index := range layout.fullNames {
		if !w.cellIsEmpty(index) {
			w.noteName(w.value(index), w.identity())
		}
	}
}

func (w *filterWorker) joinName(first int, last int) []byte {
	return append(append(append([]byte(nil), w.value(first)...), ' '), w.value(last)...)
}

// noteName makes a name known to later tables, and to the rest of this row at once.
func (w *filterWorker) noteName(name []byte, identity personIdentity) {
	if identity.kept {
		return
	}
	names := w.filter.names
	key, prefixes, ok := names.nameKey(name)
	if !ok || names.isProtected(key) {
		return
	}
	names.record(key, prefixes, identity)
	w.scanner.rowNames = append(w.scanner.rowNames, rowName{key: key, ref: identity.ref(), namePrefixes: prefixes})
}

// collectAutomaticNames does for a table without a rule what collectNames does with one, so
// the people of a plugin's own tables count too.
func (w *filterWorker) collectAutomaticNames() {
	layout := w.layout
	for i := range layout.autoNamePairs {
		pair := &layout.autoNamePairs[i]
		if pair.first >= 0 && pair.last >= 0 && !w.cellIsEmpty(pair.first) && !w.cellIsEmpty(pair.last) {
			full := w.joinName(pair.first, pair.last)
			w.noteName(full, w.automaticIdentity(full))
		}
	}
	for _, index := range layout.autoFullNames {
		if !w.cellIsEmpty(index) {
			w.noteName(w.value(index), w.automaticIdentity(w.value(index)))
		}
	}
}

// automaticIdentity gives a name in free text the pseudonym the name's own column gets.
func (w *filterWorker) automaticIdentity(name []byte) personIdentity {
	if email := w.layout.autoEmail; email >= 0 && !w.cellIsEmpty(email) {
		return w.filter.registry.forEmail(string(w.value(email)))
	}
	return w.filter.registry.forRow("name:" + strings.ToLower(string(name)))
}

// collectKeyValueName handles a form plugin's rows, which hold one field each.
func (w *filterWorker) collectKeyValueName(class piiClass, key []byte, value []byte) {
	layout := w.layout
	switch class {
	case piiFullName:
		w.noteName(value, w.automaticIdentity(value))
	case piiFirstName, piiLastName:
		if layout.autoObject >= 0 {
			object := layout.table.site + "|" + layout.table.name + "|" + string(w.value(layout.autoObject))
			w.filter.names.addPart(object+"|"+nameKeyScope(key), value, class == piiFirstName, w.filter.registry.forRow("form:"+object))
		}
	}
}

// nameKeyScope keeps billing_first_name and shipping_last_name from forming one person, since
// a gift order holds two.
func nameKeyScope(key []byte) string {
	scope := []string{}
	for _, token := range splitKeyTokens(string(key)) {
		switch class := piiKeyNames[token]; {
		case class == piiFirstName, class == piiLastName, token == "first", token == "last", token == "name", token == "given", token == "family":
		default:
			scope = append(scope, token)
		}
	}
	return strings.Join(scope, "_")
}

func (w *filterWorker) rewrite(index int, strategy string) error {
	if w.cellIsEmpty(index) || w.cells[index].rewritten {
		return nil
	}
	identity := w.identity()
	if identity.kept {
		return nil
	}
	replacement, err := w.applyStrategy(strategy, w.value(index), identity)
	if err != nil {
		return err
	}
	if isSharedPlaceholder(strategy) {
		replacement = w.distinctIfUnique(index, replacement)
	}
	w.setCell(index, replacement)
	w.cells[index].rewritten = true
	w.scanner.other++
	return nil
}

func isSharedPlaceholder(strategy string) bool {
	switch strategy {
	case strategyStreet, strategyCity, strategyPostcode, strategyCompany, strategyPhone, strategyIP, strategyUserAgent, strategyText:
		return true
	}
	return false
}

// distinctIfUnique swaps a placeholder many rows share for one derived from the value, in
// a column with a unique index. The original values were distinct, so these are too.
func (w *filterWorker) distinctIfUnique(index int, replacement []byte) []byte {
	if w.layout.columns[index].unique {
		return []byte(w.filter.registry.token(string(w.value(index))))
	}
	return replacement
}

func (w *filterWorker) applyStrategy(strategy string, value []byte, identity personIdentity) ([]byte, error) {
	if constant, ok := strings.CutPrefix(strategy, "constant:"); ok {
		return []byte(constant), nil
	}
	registry := w.filter.registry
	switch strategy {
	case strategyEmail:
		return registry.appendEmail(nil, identity), nil
	case strategyFirstName:
		return identity.appendFirstName(nil), nil
	case strategyLastName:
		return identity.appendLastName(nil), nil
	case strategyFullName:
		return identity.appendDisplayName(nil), nil
	case strategyLogin:
		return identity.appendSlug(nil), nil
	case strategyStreet:
		return []byte(placeholderStreet), nil
	case strategyCity:
		return []byte(placeholderCity), nil
	case strategyPostcode:
		return []byte(placeholderPostcode), nil
	case strategyCompany:
		return []byte(placeholderCompany), nil
	case strategyPhone:
		return []byte(placeholderPhone), nil
	case strategyIP:
		if bytes.IndexByte(value, ':') >= 0 {
			return []byte(placeholderIPv6), nil
		}
		return []byte(placeholderIPv4), nil
	case strategyUserAgent:
		return []byte(placeholderUserAgent), nil
	case strategyText:
		return []byte(placeholderText), nil
	case strategyToken:
		return []byte(registry.token(string(value))), nil
	case strategyPassword:
		return []byte("!pseudonymized"), nil
	case strategyLogins:
		return w.renameLogins(value)
	case strategyBlank:
		return []byte{}, nil
	}
	return nil, fmt.Errorf("unknown strategy %s", strategy)
}

// renameLogins follows the rename of the users table in the serialized list of super
// admins, which only stays valid when every login still resolves.
func (w *filterWorker) renameLogins(value []byte) ([]byte, error) {
	walker := serializedLoginWalker{registry: w.filter.registry, data: value}
	renamed, err := walker.rename()
	if err != nil {
		return nil, errors.New("a list of logins is not valid serialized data")
	}
	return renamed, nil
}

type serializedLoginWalker struct {
	registry *identityRegistry
	data     []byte
}

// rename rewrites every string of a flat list. Super admins never nest.
func (s *serializedLoginWalker) rename() ([]byte, error) {
	out := make([]byte, 0, len(s.data)+32)
	position := 0
	for position < len(s.data) {
		index := bytes.Index(s.data[position:], []byte(`s:`))
		if index < 0 {
			break
		}
		start := position + index
		walker := serializedWalker{data: s.data, pos: start + 2}
		bodyStart, bodyEnd, err := walker.quoted()
		if err != nil {
			return nil, err
		}
		out = append(out, s.data[position:start]...)
		login := s.data[bodyStart:bodyEnd]
		renamed := login
		if identity, ok := s.registry.forLogin(string(login)); ok && !identity.kept {
			renamed = identity.appendSlug(nil)
		}
		out = append(out, "s:"...)
		out = strconv.AppendInt(out, int64(len(renamed)), 10)
		out = append(out, ':', '"')
		out = append(out, renamed...)
		out = append(out, '"')
		position = bodyEnd + 1
	}
	return append(out, s.data[position:]...), nil
}

func (w *filterWorker) rewriteMeta(verdict metaKeyVerdict) error {
	layout := w.layout
	strategy := verdict.strategy
	switch verdict.class {
	case metaKeyPlain, metaKeyDrop, metaKeyProtected:
		return nil
	case metaKeySensitive:
		// woocommerce_registration_generate_password = yes is a setting, not a password.
		if isBooleanLike(w.value(layout.metaValue)) {
			return nil
		}
		only := layout.table.meta.rule.SensitiveOnlyInMap
		if only != "" && (layout.metaObject < 0 || !w.filter.registry.isMarked(layout.table.scoped(only), parseIdentifier(w.value(layout.metaObject)))) {
			return nil
		}
		strategy = strategyBlank
	}
	// A meta row naming an address belongs to that address, whoever the object is.
	if strategy == strategyEmail && !w.cellIsEmpty(layout.metaValue) {
		w.rowIdentity = w.filter.registry.forEmail(string(w.value(layout.metaValue)))
		w.identityKnown = true
	}
	if (strategy == strategyFirstName || strategy == strategyLastName) && layout.metaObject >= 0 && !w.cellIsEmpty(layout.metaValue) {
		w.filter.names.addPart(
			layout.table.site+"|"+layout.table.ruleName()+"|"+string(w.value(layout.metaObject))+"|"+nameKeyScope(w.value(layout.metaKey)),
			w.value(layout.metaValue), strategy == strategyFirstName, w.identity(),
		)
	}
	return w.rewrite(layout.metaValue, strategy)
}

// applyAutomatic replaces what a column's name promises, in tables and columns no rule
// describes. A person in such a row gets their pseudonym through the row's email column.
func (w *filterWorker) applyAutomatic() {
	layout := w.layout
	w.collectAutomaticNames()
	for _, index := range layout.autoColumns {
		if w.cellIsEmpty(index) || w.cells[index].rewritten {
			continue
		}
		value := w.value(index)
		class := layout.columns[index].class
		var replacement []byte
		ok := false
		if layout.columns[index].kind == columnNumber {
			// Short numbers are flags and counts rather than values.
			if len(value) >= 3 {
				w.cells[index].literal = []byte("0")
				w.cells[index].rewritten = true
				w.changed = true
				w.scanner.other++
			}
			continue
		}
		if layout.columns[index].binary && class == piiIP {
			w.setCell(index, packedIPPlaceholder(len(value)))
			w.cells[index].rewritten = true
			w.scanner.other++
			continue
		}
		if layout.autoEmail >= 0 && !w.cellIsEmpty(layout.autoEmail) && class >= piiFirstName && class <= piiLogin {
			identity := w.filter.registry.forEmail(string(w.value(layout.autoEmail)))
			switch class {
			case piiFirstName:
				replacement = identity.appendFirstName(nil)
			case piiLastName:
				replacement = identity.appendLastName(nil)
			case piiFullName:
				replacement = identity.appendDisplayName(nil)
			case piiLogin:
				replacement = identity.appendSlug(nil)
			}
			ok = true
		} else {
			replacement, ok = w.values.redactClass(class, value)
			if ok {
				replacement = w.distinctIfUnique(index, replacement)
			}
		}
		if ok {
			w.setCell(index, replacement)
			w.cells[index].rewritten = true
			w.scanner.other++
		}
	}
	if layout.autoKey >= 0 && !w.cellIsEmpty(layout.autoKey) && !w.cellIsEmpty(layout.autoValue) {
		key := w.value(layout.autoKey)
		if layout.table.protected[string(key)] {
			w.protectedCell = layout.autoValue
			return
		}
		class := w.values.classify(key)
		w.collectKeyValueName(class, key, w.value(layout.autoValue))
		if replacement, ok := w.values.redactClass(class, w.value(layout.autoValue)); ok {
			w.setCell(layout.autoValue, replacement)
			w.cells[layout.autoValue].rewritten = true
			w.scanner.other++
		}
	}
}

// packedIPPlaceholder is 192.0.2.1 as bytes, in the width the column uses.
func packedIPPlaceholder(width int) []byte {
	address := netip.AddrFrom4([4]byte{192, 0, 2, 1})
	if width == 4 {
		bytes4 := address.As4()
		return bytes4[:]
	}
	if width == 16 {
		bytes16 := address.As16()
		return bytes16[:]
	}
	return make([]byte, width)
}

// missingColumns lists what a rule names but the table lacks. Without this a typo in a
// project rule, or a plugin renaming a column, would silently leave the data in place.
func (t *compiledTable) missingColumns() []string {
	if t.rule == nil {
		return nil
	}
	have := make(map[string]bool, len(t.columns))
	for i := range t.columns {
		have[t.columns[i].name] = true
	}
	missing := []string{}
	note := func(strategies map[string]string) {
		for _, name := range sortedStrategies(strategies) {
			if !have[name] {
				missing = append(missing, name)
			}
		}
	}
	note(t.rule.Columns)
	for i := range t.rule.Conditions {
		note(t.rule.Conditions[i].Columns)
	}
	return missing
}
