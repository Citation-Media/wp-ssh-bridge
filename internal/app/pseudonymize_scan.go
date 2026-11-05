package app

import (
	"bytes"
	"hash/maphash"
	"html"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Placeholders for values found by their shape. Each is a value the scanner itself does
// not match again, and has the format software expects of the original, so a form or an
// IP check on a development copy keeps working.
const (
	placeholderPhone      = "+00 000 0000000"
	placeholderIPv4       = "192.0.2.1"
	placeholderIPv6       = "2001:db8::1"
	placeholderIPv4Mapped = "::ffff:192.0.2.1"
	placeholderIBAN       = "XX00REDACTED"
	placeholderCard       = "0000-0000-0000-0000"
	placeholderBirthdate  = "1970-01-01"
	placeholderStreet     = "Example Street 1"
	placeholderCity       = "Example City"
	placeholderPostcode   = "12345"
	placeholderCompany    = "Example Company"
	placeholderUserAgent  = "Pseudonymized"
	placeholderText       = "Pseudonymized text."
	placeholderRedacted   = "[redacted]"
)

// Byte classes for the scanner's main loop. Almost every byte is class 0, so scanning
// costs one table lookup per byte until something looks like personal data.
const (
	scanClassNone uint8 = iota
	scanClassAt
	scanClassMarker
	scanClassParen
	scanClassDigit
	scanClassPlus
	scanClassUpper
	scanClassColon
	scanClassLatinCapital
	// scanClassLower only exists while names are known, see scanClassesWithNames.
	scanClassLower
)

var (
	scanClasses = buildScanClasses()
	// scanClassesWithNames adds the lower-case letters, because a name in free text can be
	// all lower case. A text scanned while no names are known never pays for that.
	scanClassesWithNames = buildScanClassesWithNames()
	localBytes           = buildByteSet("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._+-%&;#\\'", true)
	localPlain           = buildByteSet("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._+-", true)
	labelPlain           = buildByteSet("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-", true)
	asciiAlnum           = buildByteSet("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", false)
	wordOrHighSet        = buildByteSet("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", true)
)

func buildScanClasses() [256]uint8 {
	var classes [256]uint8
	classes['@'] = scanClassAt
	classes['%'] = scanClassMarker
	classes['&'] = scanClassMarker
	classes['\\'] = scanClassMarker
	classes['['] = scanClassMarker
	classes['('] = scanClassParen
	classes['+'] = scanClassPlus
	classes[':'] = scanClassColon
	for c := 0xc3; c <= 0xf4; c++ {
		classes[c] = scanClassLatinCapital
	}
	for c := '0'; c <= '9'; c++ {
		classes[c] = scanClassDigit
	}
	for c := 'A'; c <= 'Z'; c++ {
		classes[c] = scanClassUpper
	}
	return classes
}

func buildScanClassesWithNames() [256]uint8 {
	classes := buildScanClasses()
	for c := 'a'; c <= 'z'; c++ {
		classes[c] = scanClassLower
	}
	return classes
}

func buildByteSet(chars string, high bool) [256]bool {
	var set [256]bool
	for i := 0; i < len(chars); i++ {
		set[chars[i]] = true
	}
	if high {
		for c := 0x80; c <= 0xff; c++ {
			set[c] = true
		}
	}
	return set
}

// piiScanner replaces personal data in free text wherever it recognizes it by shape:
// email addresses, phone numbers, IP addresses, IBANs, card numbers, and the full names
// of people the rules already met.
//
// One instance belongs to one worker. A hand-written scanner rather than regular
// expressions, because this runs over every string of every table, and because Go's
// regexp package has neither the lookbehind nor the speed this needs.
type piiScanner struct {
	registry *identityRegistry
	names    *nameDictionary

	// emailsOnly applies to a kept account's rows and to SQL outside of rows.
	emailsOnly bool
	// skipNumbers applies under keys such as db_version, where 8.6.1.0 is not an address.
	skipNumbers bool
	// keep holds the exact values a project protects with pseudonymize_keep_text.
	keep map[string]struct{}
	// rowNames holds the names of the row at hand, which the dictionary only learns at the
	// end of the table: a message signed with the name of the person who sent it.
	rowNames []rowName

	emails int
	other  int
	buffer []byte
}

type rowName struct {
	key, ref uint64
	namePrefixes
}

type scanMatch struct {
	start, end  int
	replacement []byte
}

// scan returns text itself when nothing matched, so a clean value costs no allocation.
func (s *piiScanner) scan(text []byte) []byte {
	var out []byte
	copied, i, n := 0, 0, len(text)
	classes := &scanClasses
	if !s.emailsOnly && s.namesKnown() {
		classes = &scanClassesWithNames
	}
	for i < n {
		class := classes[text[i]]
		if class == scanClassNone {
			i++
			continue
		}
		if class == scanClassLower && !s.nameCanStartAt(text, i) {
			// The rest of the word cannot start anything, and a backslash may have made its
			// first letter look like a word of its own.
			if i > 0 && text[i-1] == '\\' {
				i++
				continue
			}
			for i++; i < n && classes[text[i]] == scanClassLower; i++ {
			}
			continue
		}
		match, skipTo := s.matchAt(text, i, copied, class)
		if match.end > 0 {
			if _, kept := s.keep[string(text[match.start:match.end])]; kept {
				s.other--
				i = match.end
				continue
			}
			if out == nil {
				out = make([]byte, 0, n+16)
			}
			out = append(out, text[copied:match.start]...)
			out = append(out, match.replacement...)
			copied = match.end
			i = match.end
			continue
		}
		if skipTo > i {
			i = skipTo
		} else {
			i++
		}
	}
	if out == nil {
		return text
	}
	return append(out, text[copied:]...)
}

func (s *piiScanner) namesKnown() bool {
	return s.names != nil && (len(s.names.entries) > 0 || len(s.rowNames) > 0)
}

func (s *piiScanner) nameCanStartAt(text []byte, i int) bool {
	return wordStartsAt(text, i) && s.mayStartName(firstWordHash(text, i))
}

func (s *piiScanner) mayStartName(first uint32) bool {
	if s.names.hasPrefix(first) {
		return true
	}
	for i := range s.rowNames {
		if s.rowNames[i].first == first {
			return true
		}
	}
	return false
}

func (s *piiScanner) mayContinueName(firstTwo uint32) bool {
	if s.names.hasPrefix(firstTwo) {
		return true
	}
	for i := range s.rowNames {
		if s.rowNames[i].firstTwo == firstTwo {
			return true
		}
	}
	return false
}

// matchAt dispatches on the byte class. A zero match means none; skipTo lets the caller
// jump past bytes it would only examine again.
func (s *piiScanner) matchAt(text []byte, i int, copied int, class uint8) (scanMatch, int) {
	switch class {
	case scanClassAt, scanClassMarker:
		return s.matchEmail(text, i, copied)
	case scanClassParen:
		if match, skipTo := s.matchEmail(text, i, copied); match.end > 0 || skipTo > i {
			return match, skipTo
		}
		if s.emailsOnly || s.skipNumbers {
			return scanMatch{}, 0
		}
		return s.matchParenPhone(text, i, copied)
	case scanClassDigit:
		if s.emailsOnly || s.skipNumbers {
			return scanMatch{}, 0
		}
		return s.matchNumber(text, i, copied)
	case scanClassPlus:
		if s.emailsOnly || s.skipNumbers {
			return scanMatch{}, 0
		}
		return s.matchInternationalPhone(text, i, copied)
	case scanClassUpper, scanClassLatinCapital:
		if s.emailsOnly {
			return scanMatch{}, 0
		}
		if !wordStartsAt(text, i) {
			return scanMatch{}, 0
		}
		if class == scanClassUpper {
			if match, skipTo := s.matchIBAN(text, i, copied); match.end > 0 {
				return match, skipTo
			}
		}
		if !s.namesKnown() || !s.mayStartName(firstWordHash(text, i)) {
			return scanMatch{}, 0
		}
		return s.matchName(text, i, copied)
	case scanClassLower:
		// The scan loop has asked mayStartName already, for lower case.
		return s.matchName(text, i, copied)
	case scanClassColon:
		if s.emailsOnly || s.skipNumbers {
			return scanMatch{}, 0
		}
		return s.matchIPv6(text, i, copied)
	}
	return scanMatch{}, 0
}

func (s *piiScanner) otherMatch(start int, end int, replacement string) scanMatch {
	s.other++
	s.buffer = append(s.buffer[:0], replacement...)
	return scanMatch{start: start, end: end, replacement: s.buffer}
}

// --- email addresses ---------------------------------------------------------------

// atMarkerAt recognizes how stored values spell the @: plainly, JSON and URL encoded, once
// or twice, as HTML entities, regex escaped, and the spellings people use against spam
// harvesters. It returns where the name part ends and the domain part starts.
func atMarkerAt(text []byte, i int) (localEnd int, domainStart int, ok bool) {
	rest := text[i:]
	switch text[i] {
	case '@':
		if i > 0 && text[i-1] == '\\' {
			return i - 1, i + 1, true
		}
		return i, i + 1, true
	case '%':
		if bytes.HasPrefix(rest, []byte("%40")) {
			return i, i + 3, true
		}
		if bytes.HasPrefix(rest, []byte("%2540")) {
			return i, i + 5, true
		}
	case '\\':
		if bytes.HasPrefix(rest, []byte("\\u0040")) {
			return i, i + 6, true
		}
	case '&':
		if bytes.HasPrefix(rest, []byte("&commat;")) {
			return i, i + 8, true
		}
		if n := numericEntityLen(rest, 64); n > 0 {
			return i, i + n, true
		}
	case '(', '[':
		if len(rest) >= 4 && (rest[1] == 'a' || rest[1] == 'A') && (rest[2] == 't' || rest[2] == 'T') && (rest[3] == ')' || rest[3] == ']') {
			localEnd = i
			for localEnd > 0 && isSpace(text[localEnd-1]) {
				localEnd--
			}
			domainStart = i + 4
			for domainStart < len(text) && isSpace(text[domainStart]) {
				domainStart++
			}
			return localEnd, domainStart, true
		}
	}
	return 0, 0, false
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t'
}

// numericEntityLen returns the length of an HTML entity for the code point, in decimal or
// hex and with any padding, or 0.
func numericEntityLen(text []byte, code int) int {
	if len(text) < 4 || text[0] != '&' || text[1] != '#' {
		return 0
	}
	i, base := 2, 10
	if text[i] == 'x' || text[i] == 'X' {
		i++
		base = 16
	}
	start, value := i, 0
	for i < len(text) && i-start < 8 {
		digit := hexValue(text[i])
		if digit < 0 || digit >= base {
			break
		}
		value = value*base + digit
		i++
	}
	if i == start || i >= len(text) || text[i] != ';' || value != code {
		return 0
	}
	return i + 1
}

func hexValue(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

func (s *piiScanner) matchEmail(text []byte, i int, copied int) (scanMatch, int) {
	localEnd, domainStart, ok := atMarkerAt(text, i)
	if !ok || localEnd <= copied || domainStart >= len(text) {
		return scanMatch{}, 0
	}
	end, ok := parseEmailDomain(text, domainStart)
	if !ok {
		return scanMatch{}, 0
	}
	start, ok := findLocalStart(text, localEnd, copied)
	if !ok {
		return scanMatch{}, 0
	}
	address := plainAddress(text[start:end])
	if isFileNameAddress(address) || s.registry.isOwnAddress(address) {
		return scanMatch{}, end
	}
	s.emails++
	s.buffer = s.registry.appendEmail(s.buffer[:0], s.registry.forEmail(address))
	return scanMatch{start: start, end: end, replacement: s.buffer}, end
}

// emailLocalLimit bounds the walk back from an @, so a long run of letters ahead of one
// cannot make a large blob quadratic. RFC 5321 allows 64 bytes, and escapes take up to
// six bytes for each of them.
const emailLocalLimit = 384

// findLocalStart finds where the name part before an @ begins. It walks back over every
// byte a name could hold, then parses forward, restarting after whatever turns out not to
// belong: in "&lt;anna@", the &lt; wraps the address rather than being part of it.
func findLocalStart(text []byte, end int, lower int) (int, bool) {
	floor := max(lower, end-emailLocalLimit)
	start := end
	for start > floor && localBytes[text[start-1]] {
		start--
	}
	for start < end {
		position := start
		for position < end {
			width := localUnitLen(text, position)
			if width == 0 {
				break
			}
			position += width
		}
		if position >= end {
			return start, true
		}
		if width := asciiEscapeLen(text, position); width > 0 {
			start = position + width
		} else {
			start = position + 1
		}
	}
	return 0, false
}

func localUnitLen(text []byte, i int) int {
	c := text[i]
	if localPlain[c] {
		return 1
	}
	switch c {
	case '%':
		if i+2 < len(text) && text[i+1] == '2' && (text[i+2] == 'B' || text[i+2] == 'b') {
			return 3
		}
	case '\\':
		if i+5 < len(text) && text[i+1] == 'u' && text[i+2] == '0' && text[i+3] == '0' && text[i+4] == '2' && text[i+5] == '7' && precededByLetter(text, i) {
			return 6
		}
	case '\'':
		if precededByLetter(text, i) && i+1 < len(text) && isASCIILetter(text[i+1]) {
			return 1
		}
		return 0
	case '&':
		if i > 0 && asciiAlnum[text[i-1]] {
			if n := numericEntityLen(text[i:], 39); n > 0 && precededByLetter(text, i) {
				return n
			}
			return namedEntityLen(text, i)
		}
		return 0
	}
	return escapedHighLen(text, i)
}

func precededByLetter(text []byte, i int) bool {
	return i > 0 && isASCIILetter(text[i-1])
}

func isASCIILetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// escapedHighLen recognizes an escape of a character outside ASCII. Only those count as
// part of a name: an escaped < or a quote wraps an address, and swallowing it would
// attribute the address to a stranger.
func escapedHighLen(text []byte, i int) int {
	switch text[i] {
	case '\\':
		if i+5 < len(text) && text[i+1] == 'u' && hexValue(text[i+2]) >= 0 && hexValue(text[i+3]) >= 0 && hexValue(text[i+4]) >= 0 && hexValue(text[i+5]) >= 0 {
			value := hexValue(text[i+2])<<12 | hexValue(text[i+3])<<8 | hexValue(text[i+4])<<4 | hexValue(text[i+5])
			if value >= 0x80 {
				return 6
			}
		}
	case '%':
		if i+2 < len(text) && hexValue(text[i+1]) >= 8 && hexValue(text[i+2]) >= 0 {
			return 3
		}
	}
	return 0
}

// asciiEscapeLen recognizes an escaped ASCII character ahead of a name, as in \u003canna,
// \nanna or %3Canna, which would otherwise read as the name u003canna or nanna.
func asciiEscapeLen(text []byte, i int) int {
	switch text[i] {
	case '\\':
		if i+1 < len(text) && strings.IndexByte("nrtbf/\"\\", text[i+1]) >= 0 {
			return 2
		}
		if i+5 < len(text) && text[i+1] == 'u' && text[i+2] == '0' && text[i+3] == '0' && hexValue(text[i+4]) >= 0 && hexValue(text[i+4]) < 8 && hexValue(text[i+5]) >= 0 {
			return 6
		}
	case '%':
		if i+2 < len(text) && hexValue(text[i+1]) >= 0 && hexValue(text[i+1]) < 8 && hexValue(text[i+2]) >= 0 {
			return 3
		}
	}
	return 0
}

// namedEntityLen recognizes an entity such as &uuml;. The markup entities are excluded,
// since they wrap an address rather than spell one, and so are the two separators.
func namedEntityLen(text []byte, i int) int {
	if text[i] != '&' {
		return 0
	}
	j := i + 1
	for j < len(text) && j-i <= 16 && asciiAlnum[text[j]] {
		j++
	}
	if j == i+1 || j >= len(text) || text[j] != ';' || !isASCIILetter(text[i+1]) {
		return 0
	}
	switch string(text[i+1 : j]) {
	case "lt", "gt", "amp", "quot", "apos", "nbsp", "commat", "period":
		return 0
	}
	return j + 1 - i
}

func labelUnitLen(text []byte, i int) int {
	if labelPlain[text[i]] {
		return 1
	}
	if n := escapedHighLen(text, i); n > 0 {
		return n
	}
	if text[i] == '&' {
		return namedEntityLen(text, i)
	}
	return 0
}

// dotLen recognizes how stored values spell the dot of a domain.
func dotLen(text []byte, i int) int {
	if i >= len(text) {
		return 0
	}
	rest := text[i:]
	switch text[i] {
	case '.':
		return 1
	case '%':
		if len(rest) >= 3 && rest[1] == '2' && (rest[2] == 'E' || rest[2] == 'e') {
			return 3
		}
	case '\\':
		if bytes.HasPrefix(rest, []byte("\\u002e")) || bytes.HasPrefix(rest, []byte("\\u002E")) {
			return 6
		}
	case '&':
		if bytes.HasPrefix(rest, []byte("&period;")) {
			return 8
		}
		return numericEntityLen(rest, 46)
	case ' ', '\t', '(', '[':
		j := i
		for j < len(text) && isSpace(text[j]) {
			j++
		}
		if j+4 < len(text) && (text[j] == '(' || text[j] == '[') && (text[j+1] == 'd' || text[j+1] == 'D') && (text[j+2] == 'o' || text[j+2] == 'O') && (text[j+3] == 't' || text[j+3] == 'T') && (text[j+4] == ')' || text[j+4] == ']') {
			j += 5
			for j < len(text) && isSpace(text[j]) {
				j++
			}
			return j - i
		}
	}
	return 0
}

// parseEmailDomain returns the end of the domain starting at i. A domain needs a dot,
// because swiper@latest in a script URL has the same shape as anna@intranet.
func parseEmailDomain(text []byte, i int) (int, bool) {
	if text[i] == '[' {
		j := i + 1
		for j < len(text) && j-i < 48 && (hexValue(text[j]) >= 0 || text[j] == ':' || text[j] == '.') {
			j++
		}
		if j > i+1 && j < len(text) && text[j] == ']' {
			return j + 1, true
		}
		return 0, false
	}
	var labels [16]int
	count := 0
	position := i
	for count < len(labels) {
		end := position
		for end < len(text) {
			width := labelUnitLen(text, end)
			if width == 0 {
				break
			}
			end += width
		}
		if end == position {
			break
		}
		labels[count] = position
		count++
		width := dotLen(text, end)
		if width == 0 {
			break
		}
		position = end + width
	}
	for candidate := count - 1; candidate >= 1; candidate-- {
		if end, ok := topLevelDomainEnd(text, labels[candidate]); ok {
			// Whatever is stuck to it goes too, rather than trail after the pseudonym.
			for end < len(text) && asciiAlnum[text[end]] {
				end++
			}
			return end, true
		}
	}
	return 0, false
}

func topLevelDomainEnd(text []byte, i int) (int, bool) {
	if len(text) >= i+4 && (text[i] == 'x' || text[i] == 'X') && (text[i+1] == 'n' || text[i+1] == 'N') && text[i+2] == '-' && text[i+3] == '-' {
		j := i + 4
		for j < len(text) && (asciiAlnum[text[j]] || text[j] == '-') {
			j++
		}
		return j, j > i+4
	}
	j, units := i, 0
	for j < len(text) {
		if isASCIILetter(text[j]) || text[j] >= 0x80 {
			j++
			units++
			continue
		}
		width := escapedHighLen(text, j)
		if width == 0 {
			break
		}
		j += width
		units++
	}
	return j, units >= 2
}

var (
	densityInFileName = regexp.MustCompile(`@\d(?:\.\d)?x\.[A-Za-z0-9]+$`)

	spelledAt  = regexp.MustCompile(`\\?@|%(?:25)?40|\\u0040|&#0*64;|&#[xX]0*40;|&commat;|\s*[\[(](?i:at)[\])]\s*`)
	spelledDot = regexp.MustCompile(`%2[Ee]|\\u002[Ee]|&#0*46;|&#[xX]0*2[Ee];|&period;|\s*[\[(](?i:dot)[\])]\s*`)
	jsonEscape = regexp.MustCompile(`\\u[0-9a-fA-F]{4}`)
)

// plainAddress is the address as a person would write it, so every spelling of it
// resolves to one identity. Most addresses need nothing done.
func plainAddress(match []byte) string {
	if !bytes.ContainsAny(match, `%&\([ `) {
		return string(match)
	}
	address := spelledAt.ReplaceAllString(string(match), "@")
	address = spelledDot.ReplaceAllString(address, ".")
	address = jsonEscape.ReplaceAllStringFunc(address, func(escape string) string {
		value, err := strconv.ParseUint(escape[2:], 16, 32)
		if err != nil {
			return escape
		}
		return string(rune(value))
	})
	if decoded, err := url.PathUnescape(address); err == nil {
		address = decoded
	}
	return html.UnescapeString(address)
}

// isFileNameAddress leaves names like logo@2x.png alone. A top-level domain is never a
// file ending, so one dot after the @ settles it. A file name with more dots only counts
// when a version or density follows the @, because anna@real.test.png is an address in a
// file name, unlike jquery@3.6.0.min.js.
func isFileNameAddress(address string) bool {
	dot := strings.LastIndexByte(address, '.')
	if dot < 0 {
		return false
	}
	// A density such as @2x is no domain, whatever the file's type: video@2x.mp4.
	if densityInFileName.MatchString(address) {
		return true
	}
	switch strings.ToLower(address[dot+1:]) {
	case "png", "jpg", "jpeg", "gif", "svg", "webp", "avif", "ico", "bmp", "css", "js", "mjs", "map", "woff", "woff2", "ttf", "eot", "pdf":
	default:
		return false
	}
	domain := address[strings.LastIndexByte(address, '@')+1:]
	return strings.Count(domain, ".") == 1 || domain[0] >= '0' && domain[0] <= '9'
}

// --- numbers: phones, IP addresses, cards ------------------------------------------

// followsLetterEscape spots the n of a literal backslash-n ahead of position i, which only
// looks like the end of a word. Text can hold these outside of valid JSON too.
func followsLetterEscape(text []byte, i int) bool {
	return i >= 2 && text[i-2] == '\\' && strings.IndexByte("nrtbf", text[i-1]) >= 0
}

// wordStartsAt also reads a multi-byte character ahead of i, so a typographic quotation mark
// or a dash separates words, and a letter does not.
func wordStartsAt(text []byte, i int) bool {
	if i == 0 {
		return true
	}
	if previous := text[i-1]; previous >= utf8.RuneSelf {
		r, size := utf8.DecodeLastRune(text[:i])
		if r == utf8.RuneError && size <= 1 {
			return false
		}
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.IsMark(r)
	}
	return !wordOrHighSet[text[i-1]] || followsLetterEscape(text, i)
}

// numberBoundaryBefore rejects a digit that continues something else: a word, a decimal,
// a slug, or a hex color.
func numberBoundaryBefore(text []byte, i int) bool {
	if i == 0 || followsLetterEscape(text, i) {
		return true
	}
	c := text[i-1]
	return !wordOrHighSet[c] && c != '.' && c != '_' && c != '-' && c != '#' && c != '$'
}

func numberBoundaryAfter(text []byte, i int) bool {
	if i >= len(text) {
		return true
	}
	c := text[i]
	if wordOrHighSet[c] || c == '_' {
		return false
	}
	return !(c == '.' && i+1 < len(text) && text[i+1] >= '0' && text[i+1] <= '9')
}

// numberGroups is a run of digit groups joined by the separators phone numbers use.
type numberGroups struct {
	ends   [8]int
	digits [8]int
	count  int
	// which separators the whole run holds
	hasDot, hasSlash, hasParen bool
	firstSeparator             byte
}

const numberWalkLimit = 40

func walkNumberGroups(text []byte, i int) numberGroups {
	var groups numberGroups
	position, digits := i, 0
	for groups.count < len(groups.ends) && position-i < numberWalkLimit {
		start := position
		for position < len(text) && text[position] >= '0' && text[position] <= '9' {
			position++
		}
		if position == start {
			break
		}
		digits += position - start
		groups.ends[groups.count] = position
		groups.digits[groups.count] = digits
		groups.count++
		separator := position
		for separator < len(text) && separator-position < 4 && isPhoneSeparator(text[separator]) {
			separator++
		}
		if separator == position || separator >= len(text) || text[separator] < '0' || text[separator] > '9' {
			break
		}
		if groups.count == 1 {
			groups.firstSeparator = text[position]
		}
		for _, c := range text[position:separator] {
			switch c {
			case '.':
				groups.hasDot = true
			case '/':
				groups.hasSlash = true
			case '(', ')':
				groups.hasParen = true
			}
		}
		position = separator
	}
	return groups
}

func isPhoneSeparator(c byte) bool {
	return c == ' ' || c == '-' || c == '/' || c == '.' || c == '(' || c == ')'
}

// longestWithin picks the longest prefix of the groups with a digit count in range that
// ends at a clean boundary, since a phone number can run into the next number in a text.
func (g numberGroups) longestWithin(text []byte, low int, high int) (int, bool) {
	for k := g.count - 1; k >= 0; k-- {
		if g.digits[k] >= low && g.digits[k] <= high && numberBoundaryAfter(text, g.ends[k]) {
			return g.ends[k], true
		}
	}
	return 0, false
}

func (s *piiScanner) matchInternationalPhone(text []byte, i int, copied int) (scanMatch, int) {
	if i+1 >= len(text) || text[i+1] < '1' || text[i+1] > '9' || i < copied {
		return scanMatch{}, 0
	}
	if !wordStartsAt(text, i) {
		return scanMatch{}, 0
	}
	groups := walkNumberGroups(text, i+1)
	if end, ok := groups.longestWithin(text, 8, 15); ok {
		return s.otherMatch(i, end, placeholderPhone), end
	}
	return scanMatch{}, 0
}

// matchParenPhone handles an area code in parentheses, as in (030) 1234567.
func (s *piiScanner) matchParenPhone(text []byte, i int, copied int) (scanMatch, int) {
	if i < copied || i+1 >= len(text) || text[i+1] < '0' || text[i+1] > '9' {
		return scanMatch{}, 0
	}
	if !wordStartsAt(text, i) {
		return scanMatch{}, 0
	}
	groups := walkNumberGroups(text, i+1)
	if groups.count < 2 || !groups.hasParen {
		return scanMatch{}, 0
	}
	if end, ok := groups.longestWithin(text, 8, 15); ok && groups.digits[0] >= 2 && groups.digits[0] <= 6 {
		return s.otherMatch(i, end, placeholderPhone), end
	}
	return scanMatch{}, 0
}

// phoneLabels are the words that make any number after them a phone number.
var phoneLabels = map[string]bool{
	"tel": true, "telefon": true, "telephone": true, "phone": true, "mobile": true, "mobil": true,
	"handy": true, "fax": true, "fon": true, "whatsapp": true, "rufnummer": true, "telefonnummer": true,
	"mobilnummer": true, "cell": true, "call": true,
}

// wordBefore returns the lower-case word ahead of position i, skipping the punctuation
// that follows a label, as in "Tel.: 123" or tel:123.
func wordBefore(text []byte, i int, buffer *[16]byte) string {
	j := i
	for j > 0 && i-j < 6 && strings.IndexByte(" :.=/\"'+()>\t", text[j-1]) >= 0 {
		j--
	}
	end := j
	for j > 0 && end-j < len(buffer) && isASCIILetter(text[j-1]) {
		j--
	}
	if j > 0 && wordOrHighSet[text[j-1]] || j == end {
		return ""
	}
	for k := j; k < end; k++ {
		c := text[k]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		buffer[k-j] = c
	}
	return string(buffer[:end-j])
}

// versionLabels make a dotted number after them a version rather than an IP address.
var versionLabels = map[string]bool{
	"version": true, "ver": true, "v": true, "wordpress": true, "php": true, "mysql": true,
	"mariadb": true, "build": true, "release": true, "woocommerce": true,
}

// matchNumber sorts a number that starts at i into what it may be. Short numbers leave
// after the first group, so the many IDs and years in a dump cost next to nothing.
func (s *piiScanner) matchNumber(text []byte, i int, copied int) (scanMatch, int) {
	if i < copied {
		return scanMatch{}, 0
	}
	if ipBoundaryBefore(text, i) {
		if match, ok := s.matchIPv4(text, i); ok {
			return match, match.end
		}
	}
	if !numberBoundaryBefore(text, i) {
		return scanMatch{}, 0
	}
	groups := walkNumberGroups(text, i)
	firstEnd := groups.ends[0]
	if groups.digits[groups.count-1] < 6 {
		return scanMatch{}, firstEnd
	}
	if end, ok := matchCard(text, i, groups); ok {
		return s.otherMatch(i, end, placeholderCard), end
	}
	var label [16]byte
	if phoneLabels[wordBefore(text, i, &label)] {
		if end, ok := groups.longestWithin(text, 6, 15); ok {
			return s.otherMatch(i, end, placeholderPhone), end
		}
	}
	if end, ok := matchBareNumberPhone(text, i, groups); ok {
		return s.otherMatch(i, end, placeholderPhone), end
	}
	return scanMatch{}, firstEnd
}

// matchBareNumberPhone accepts a number without a label only in a shape that dates, IDs,
// and prices do not have: an international 00 prefix, a national 0 prefix with its area
// code set apart, or the three groups of a North American number.
func matchBareNumberPhone(text []byte, i int, groups numberGroups) (int, bool) {
	first := groups.digits[0]
	if text[i] == '0' && first >= 3 {
		if text[i+1] == '0' && text[i+2] >= '1' && text[i+2] <= '9' && groups.count >= 2 {
			return groups.longestWithin(text, 10, 15)
		}
		if text[i+1] != '0' && first <= 6 && groups.count >= 2 && groups.firstSeparator != '.' && !groups.hasDot {
			return groups.longestWithin(text, 8, 13)
		}
		return 0, false
	}
	if groups.count == 3 && first == 3 && groups.digits[1] == 6 && groups.digits[2] == 10 && text[i] >= '2' && !groups.hasDot && !groups.hasSlash && !groups.hasParen && numberBoundaryAfter(text, groups.ends[2]) {
		return groups.ends[2], true
	}
	return 0, false
}

// matchCard checks the Luhn digit and the range of real card numbers. The check rules out
// nine of ten long numbers that merely look like one.
func matchCard(text []byte, i int, groups numberGroups) (int, bool) {
	if text[i] < '3' || text[i] > '6' || groups.hasDot || groups.hasSlash || groups.hasParen {
		return 0, false
	}
	for k := groups.count - 1; k >= 0; k-- {
		// Fewer than 14 digits is a product's EAN far more often than an old Visa card.
		if groups.digits[k] < 14 || groups.digits[k] > 19 || !numberBoundaryAfter(text, groups.ends[k]) {
			continue
		}
		if luhnValid(text[i:groups.ends[k]]) {
			return groups.ends[k], true
		}
	}
	return 0, false
}

func luhnValid(number []byte) bool {
	sum, double := 0, false
	for i := len(number) - 1; i >= 0; i-- {
		c := number[i]
		if c < '0' || c > '9' {
			continue
		}
		digit := int(c - '0')
		if double {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
		double = !double
	}
	return sum%10 == 0
}

// matchIPv4 replaces public addresses only. Private, loopback, and documentation ranges
// identify nobody, and a database host in a setting should keep working.
func (s *piiScanner) matchIPv4(text []byte, i int) (scanMatch, bool) {
	var octets [4]int
	position := i
	for k := range octets {
		start := position
		value := 0
		for position < len(text) && position-start < 3 && text[position] >= '0' && text[position] <= '9' {
			value = value*10 + int(text[position]-'0')
			position++
		}
		if position == start || value > 255 || (text[start] == '0' && position-start > 1) {
			return scanMatch{}, false
		}
		octets[k] = value
		if k < 3 {
			if position >= len(text) || text[position] != '.' {
				return scanMatch{}, false
			}
			position++
		}
	}
	if !ipBoundaryAfter(text, position) || !isPublicIPv4(octets) {
		return scanMatch{}, false
	}
	if looksLikeVersion(text, i, octets) {
		return scanMatch{}, false
	}
	return s.otherMatch(i, position, placeholderIPv4), true
}

// ipBoundaryBefore is looser than for other numbers, because four octets are unmistakable
// enough for keys such as limit_93.184.216.34 or visitor-93.184.216.34.
func ipBoundaryBefore(text []byte, i int) bool {
	if i == 0 || followsLetterEscape(text, i) {
		return true
	}
	c := text[i-1]
	return !wordOrHighSet[c] && c != '.'
}

func ipBoundaryAfter(text []byte, i int) bool {
	if i >= len(text) {
		return true
	}
	if wordOrHighSet[text[i]] {
		return false
	}
	return !(text[i] == '.' && i+1 < len(text) && text[i+1] >= '0' && text[i+1] <= '9')
}

func isPublicIPv4(o [4]int) bool {
	switch {
	case o[0] == 0, o[0] == 10, o[0] == 127, o[0] >= 224:
		return false
	case o[0] == 172 && o[1] >= 16 && o[1] <= 31:
		return false
	case o[0] == 192 && o[1] == 168:
		return false
	case o[0] == 169 && o[1] == 254:
		return false
	case o[0] == 192 && o[1] == 0 && o[2] == 2:
		return false
	}
	return true
}

// looksLikeVersion spares 8.6.1.0 and Chrome/120.0.0.0: replacing a version can trigger a
// plugin's database migration on the development copy.
func looksLikeVersion(text []byte, i int, o [4]int) bool {
	if o[1] < 10 && o[2] < 10 && o[3] < 10 && o[0] < 100 {
		return true
	}
	if i > 1 && text[i-1] == '/' && isProductBefore(text, i-1) {
		return true
	}
	var label [16]byte
	return versionLabels[wordBefore(text, i, &label)]
}

// isProductBefore says whether a plain word ends at i, as in Chrome/120.0.0.0. A path such
// as example.test/93.184.216.34 does not count, nor does a slash on its own.
func isProductBefore(text []byte, i int) bool {
	j := i
	for j > 0 && i-j < 32 && isASCIILetter(text[j-1]) {
		j--
	}
	return j < i && (j == 0 || text[j-1] != '.')
}

func isHexOrColon(c byte) bool {
	return c == ':' || hexValue(c) >= 0
}

func (s *piiScanner) matchIPv6(text []byte, i int, copied int) (scanMatch, int) {
	low := i
	for low > copied && i-low < 39 && isHexOrColon(text[low-1]) {
		low--
	}
	high := i
	// The dots are for an IPv4 address in IPv6 form, as in ::ffff:93.184.216.34.
	for high < len(text) && high-i < 45 && (isHexOrColon(text[high]) || text[high] == '.') {
		high++
	}
	for high > i && text[high-1] == '.' {
		high--
	}
	if low > 0 && wordOrHighSet[text[low-1]] || high < len(text) && wordOrHighSet[text[high]] {
		return scanMatch{}, 0
	}
	token := text[low:high]
	colons := bytes.Count(token, []byte(":"))
	if colons < 2 || (colons != 7 && !bytes.Contains(token, []byte("::"))) || len(token) < 7 || !containsDigits(token, 1) {
		return scanMatch{}, 0
	}
	address, err := netip.ParseAddr(string(token))
	if err == nil && address.Is4In6() {
		unmapped := address.Unmap().As4()
		if !isPublicIPv4([4]int{int(unmapped[0]), int(unmapped[1]), int(unmapped[2]), int(unmapped[3])}) {
			return scanMatch{}, high
		}
		return s.otherMatch(low, high, placeholderIPv4Mapped), high
	}
	if err != nil || !address.Is6() || address.IsPrivate() || address.IsLoopback() || address.IsUnspecified() || address.IsLinkLocalUnicast() || address.IsMulticast() || bytes.HasPrefix(token, []byte("2001:db8:")) {
		return scanMatch{}, high
	}
	return s.otherMatch(low, high, placeholderIPv6), high
}

// --- IBAN --------------------------------------------------------------------------

// matchIBAN trusts the mod-97 check digits, which make a false positive unlikely enough
// that any text with the right shape can be replaced.
func (s *piiScanner) matchIBAN(text []byte, i int, copied int) (scanMatch, int) {
	if i < copied || i+15 > len(text) || text[i+1] < 'A' || text[i+1] > 'Z' || text[i+2] < '0' || text[i+2] > '9' || text[i+3] < '0' || text[i+3] > '9' {
		return scanMatch{}, 0
	}
	position := i
	best := 0
	characters := 0
	for position < len(text) && characters < 34 {
		c := text[position]
		switch {
		case c >= 'A' && c <= 'Z' || c >= '0' && c <= '9':
			characters++
			position++
			if characters >= 15 && (position == len(text) || !asciiAlnum[text[position]]) && ibanValid(text[i:position]) {
				best = position
			}
			continue
		case c == ' ' && position+1 < len(text) && asciiAlnum[text[position+1]]:
			position++
			continue
		}
		break
	}
	if best == 0 {
		return scanMatch{}, 0
	}
	return s.otherMatch(i, best, placeholderIBAN), best
}

func ibanValid(iban []byte) bool {
	remainder := 0
	process := func(c byte) {
		if c >= '0' && c <= '9' {
			remainder = (remainder*10 + int(c-'0')) % 97
			return
		}
		value := int(c-'A') + 10
		remainder = (remainder*100 + value) % 97
	}
	stripped := make([]byte, 0, 34)
	for _, c := range iban {
		if c != ' ' {
			stripped = append(stripped, c)
		}
	}
	for _, c := range stripped[4:] {
		process(c)
	}
	for _, c := range stripped[:4] {
		process(c)
	}
	return remainder == 1
}

// --- known names -------------------------------------------------------------------

// matchName looks up the words starting at i in the names the rules met in earlier rows.
// It takes the longest match, so "Anna Maria Real" wins over "Anna Maria".
func (s *piiScanner) matchName(text []byte, i int, copied int) (scanMatch, int) {
	if i < copied {
		return scanMatch{}, 0
	}
	// One byte beyond the longest stored name, so a name of exactly that length still fits.
	var buffer [nameMaxBytes + 1]byte
	length, words, position := 0, 0, i
	bestEnd, bestRef := 0, uint64(0)
	for words < nameMaxWords {
		wordStart := position
		for position < len(text) {
			if c := text[position]; c < utf8.RuneSelf {
				if !nameBytes[c] {
					break
				}
				if length == len(buffer) {
					return s.nameResult(i, bestEnd, bestRef)
				}
				if c >= 'A' && c <= 'Z' {
					c += 32
				}
				buffer[length] = c
				length++
				position++
				continue
			}
			folded, width := nameCharAt(text, position)
			if width == 0 {
				break
			}
			if length+width > len(buffer) {
				return s.nameResult(i, bestEnd, bestRef)
			}
			copy(buffer[length:], folded[:width])
			length += width
			position += width
		}
		if position == wordStart {
			break
		}
		words++
		gap := 0
		if position < len(text) {
			gap = nameGapLen(text, position)
		}
		if words >= 2 {
			// A name ends a sentence, or takes a possessive: Anna Real's.
			key := nameEndTrimmed(buffer[:length])
			if words == 2 && !s.mayContinueName(normalizedPrefixHash(buffer[:key])) {
				break
			}
			if s.nameMayEndAt(text, position, gap) {
				if ref, ok := s.lookupName(maphash.Bytes(s.names.seed, buffer[:key])); ok {
					bestEnd, bestRef = position-(length-key), ref
				}
			}
		}
		if gap == 0 || length >= len(buffer) {
			break
		}
		buffer[length] = ' '
		length++
		position += gap
	}
	return s.nameResult(i, bestEnd, bestRef)
}

// nameMayEndAt rejects a number glued to the last word, and what is really the start of an
// email address: replacing the name part of "Anna Real@corp.test" would keep the domain.
func (s *piiScanner) nameMayEndAt(text []byte, position int, gap int) bool {
	if position == len(text) {
		return true
	}
	if c := text[position]; c >= '0' && c <= '9' {
		return false
	}
	if next := position + gap; next < len(text) {
		if _, _, isAddress := atMarkerAt(text, next); isAddress {
			return false
		}
	}
	return true
}

func (s *piiScanner) nameResult(i int, bestEnd int, bestRef uint64) (scanMatch, int) {
	if bestEnd == 0 {
		return scanMatch{}, 0
	}
	s.other++
	s.buffer = identityFromRef(bestRef).appendDisplayName(s.buffer[:0])
	return scanMatch{start: i, end: bestEnd, replacement: s.buffer}, bestEnd
}

func (s *piiScanner) lookupName(key uint64) (uint64, bool) {
	if ref, ok := s.names.entries[key]; ok {
		return ref, true
	}
	for i := range s.rowNames {
		if s.rowNames[i].key == key {
			return s.rowNames[i].ref, true
		}
	}
	return 0, false
}
