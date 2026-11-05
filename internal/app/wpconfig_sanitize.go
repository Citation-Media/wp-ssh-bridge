package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// wpConfigFileMarker and wpConfigMissingMarker separate the file from whatever a login
// script prints ahead of it.
const (
	wpConfigFileMarker    = "wp-ssh-wp-config-file"
	wpConfigMissingMarker = "wp-ssh-wp-config-missing"
)

// pullSanitizedWPConfig takes the source's wp-config.php for a DDEV project. The file holds
// production credentials, so it stays in memory until they are gone: the sync leaves it
// out, and only the sanitized result reaches the disk. A failed or interrupted sync cannot
// leave a password behind.
func (a *App) pullSanitizedWPConfig(ctx context.Context, projectRoot string, cfg Config) error {
	target := cfg.pullTarget()
	remote := shellQuote(trimTrailingSlash(target.RemotePath) + "/wp-config.php")
	command := "if [ -f " + remote + " ]; then printf '\\n" + wpConfigFileMarker + "\\n'; cat " + remote + "; else printf '\\n" + wpConfigMissingMarker + "\\n'; fi"
	return a.runStep("Reading and sanitizing the source wp-config.php", "Sanitized wp-config.php written", func() error {
		stdout, stderr := bytes.Buffer{}, bytes.Buffer{}
		if err := a.runSSHWithWriters(ctx, projectRoot, a.sshArgv(target, sshTarget(target), command), &stdout, &stderr); err != nil {
			return fmt.Errorf("could not read wp-config.php from the pull source: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		contents, found, err := parseWPConfigOutput(stdout.Bytes())
		if err != nil || !found {
			return err
		}
		destination := localWPRoot(projectRoot, cfg)
		if err := os.MkdirAll(destination, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(destination, "wp-config.php"), []byte(sanitizeWPConfigContents(string(contents))), 0o644)
	})
}

func parseWPConfigOutput(output []byte) ([]byte, bool, error) {
	if index := bytes.Index(output, []byte(wpConfigFileMarker+"\n")); index >= 0 {
		return output[index+len(wpConfigFileMarker)+1:], true, nil
	}
	if bytes.Contains(output, []byte(wpConfigMissingMarker)) {
		return nil, false, nil
	}
	return nil, false, errors.New("the pull source did not answer with wp-config.php")
}

// wp-config.php is code, so its secrets are found by reading it the way PHP does, rather
// than by searching lines. This cannot be complete: a secret under a neutral name, or in a
// file it includes, stays.

// skipPHPToken returns the end of a string, heredoc, or comment starting at i, and whether it
// is a comment. Without one, it returns i. Reading these as PHP does keeps a password holding
// a quote or a closing parenthesis from ending a value early.
func skipPHPToken(contents string, i int) (int, bool) {
	rest := contents[i:]
	switch {
	case rest[0] == '\'' || rest[0] == '"':
		for j := i + 1; j < len(contents); j++ {
			if contents[j] == '\\' {
				j++
			} else if contents[j] == rest[0] {
				return j + 1, false
			}
		}
		return len(contents), false
	case strings.HasPrefix(rest, "//"), rest[0] == '#' && !strings.HasPrefix(rest, "#["):
		if newline := strings.IndexByte(rest, '\n'); newline >= 0 {
			return i + newline, true
		}
		return len(contents), true
	case strings.HasPrefix(rest, "/*"):
		if end := strings.Index(rest, "*/"); end >= 0 {
			return i + end + 2, true
		}
		return len(contents), true
	case strings.HasPrefix(rest, "<<<"):
		if opening := phpHeredocStart.FindStringSubmatch(rest); opening != nil {
			position := i + len(opening[0])
			for position < len(contents) {
				line := contents[position:]
				if newline := strings.IndexByte(line, '\n'); newline >= 0 {
					line = line[:newline]
				}
				indent := len(line) - len(strings.TrimLeft(line, " \t"))
				if strings.HasPrefix(line[indent:], opening[1]) {
					return position + indent + len(opening[1]), false
				}
				position += len(line) + 1
			}
			return len(contents), false
		}
	}
	return i, false
}

var (
	phpHeredocStart     = regexp.MustCompile(`^<<<[ \t]*["']?([A-Za-z_][A-Za-z0-9_]*)["']?\r?\n`)
	phpDefineStart      = regexp.MustCompile(`(?i)^define\s*\(\s*['"]([A-Za-z0-9_]+)['"]\s*,`)
	phpConstStart       = regexp.MustCompile(`(?i)^const\s+([A-Za-z0-9_]+)\s*=`)
	phpPutenvStart      = regexp.MustCompile(`(?i)^putenv\s*\(\s*['"]([A-Za-z0-9_]+)=`)
	phpSecretVariable   = regexp.MustCompile(`(?i)^\$[A-Za-z0-9_]*(?:pass|pwd|pw|secret|token|key|auth|credential)[A-Za-z0-9_]*(?:\[[^\]\n]*\])*\s*=\s*`)
	phpSecretArrayKey   = regexp.MustCompile(`(?i)pass|pwd|secret|token|auth|api[-_]?key|access[-_]?key|private[-_]?key|licen[cs]e`)
	phpArrow            = regexp.MustCompile(`^\s*=>\s*`)
	phpScalarLiteral    = regexp.MustCompile(`(?i)^\s*(?:true|false|null|-?[0-9.]+)\s*$`)
	phpCommentedSecret  = regexp.MustCompile(`(?i)define\s*\(\s*['"]([A-Za-z0-9_]+)['"]`)
	wpConfigSecretNames = regexp.MustCompile(`(?i)(?:^|_)(?:KEYS?|TOKEN|SALT|LICEN[CS]E|CREDENTIALS?|PWD|PASSPHRASE|DSN)(?:_ID)?(?:_(?:LIVE|TEST|SANDBOX|PROD|PRODUCTION))?$|^(?:DATABASE|WP_REDIS|REDIS)_URL$|^AS3CF_SETTINGS$`)
)

// wpConfigConstantHoldsSecret knows the constants wp-config.php keeps secrets in, on top of
// what the option scan calls a credential: salts, license keys, and any other *_KEY or *_TOKEN.
func wpConfigConstantHoldsSecret(name string) bool {
	return credentialPattern.MatchString(name) || wpConfigSecretNames.MatchString(name)
}

func wpConfigProvidedByDDEV(name string) bool {
	return name == "WP_DEBUG" || strings.HasPrefix(name, "DB_")
}

// phpValueEnd walks a value, skipping strings and comments, to the terminator at its depth.
func phpValueEnd(contents string, start int, terminator byte) int {
	depth := 0
	for i := start; i < len(contents); {
		if next, _ := skipPHPToken(contents, i); next > i {
			i = next
			continue
		}
		switch c := contents[i]; {
		case c == terminator && depth == 0:
			return i
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			depth--
		}
		i++
	}
	return len(contents)
}

// isWholeStatement says a statement starts on its own line after a finished one, so taking it
// out leaves valid PHP. Anywhere else it may be the body of an if, or part of an expression.
func isWholeStatement(contents string, start int) bool {
	lineStart := strings.LastIndexByte(contents[:start], '\n') + 1
	if strings.TrimSpace(contents[lineStart:start]) != "" {
		return false
	}
	before := strings.TrimRight(contents[:lineStart], " \t\r\n")
	if before == "" {
		return true
	}
	previous := strings.TrimSpace(before[strings.LastIndexByte(before, '\n')+1:])
	if strings.HasPrefix(previous, "//") || strings.HasPrefix(previous, "#") || strings.HasPrefix(previous, "*") || strings.HasPrefix(previous, "/*") {
		return true
	}
	for _, ending := range []string{";", "{", "}", ":", "*/", "<?php", "<?"} {
		if strings.HasSuffix(previous, ending) {
			return true
		}
	}
	return false
}

func wpConfigPlaceholder(name string) string {
	return "'wp-ssh-bridge-local-" + name + "'"
}

// wpConfigSanitizer removes the settings the DDEV settings file provides, and replaces every
// value it recognizes as a secret. A constant stays when it holds a secret, because plugins
// may read it unchecked, and a missing constant is a fatal error in PHP 8.
type wpConfigSanitizer struct {
	contents string
	out      strings.Builder
	last     int
}

func (s *wpConfigSanitizer) run() string {
	contents := s.contents
	for i := 0; i < len(contents); {
		if next, comment := skipPHPToken(contents, i); next > i {
			i = s.token(i, next, comment)
			continue
		}
		next := i
		switch c := contents[i]; {
		case c == '$':
			next = s.variable(i)
		case !s.wordStartsAt(i):
		case c == 'd' || c == 'D':
			next = s.define(i)
		case c == 'c' || c == 'C':
			next = s.constStatement(i)
		case c == 'p' || c == 'P':
			next = s.putenv(i)
		}
		if next > i {
			i = next
		} else {
			i++
		}
	}
	s.out.WriteString(contents[s.last:])
	return s.out.String()
}

func (s *wpConfigSanitizer) replace(start int, end int, text string) {
	s.out.WriteString(s.contents[s.last:start])
	s.out.WriteString(text)
	s.last = end
}

func (s *wpConfigSanitizer) wordStartsAt(i int) bool {
	if i == 0 {
		return true
	}
	c := s.contents[i-1]
	return !(c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z')
}

// token handles a comment or string that ends at next, and returns where to read on.
func (s *wpConfigSanitizer) token(start int, next int, comment bool) int {
	text := s.contents[start:next]
	if comment {
		if commentHoldsSecret(text) {
			marker := "// value removed by wp-ssh-bridge"
			if strings.HasPrefix(text, "/*") {
				marker = "/* value removed by wp-ssh-bridge */"
			}
			s.replace(start, next, marker)
		}
		return next
	}
	if len(text) < 2 || (text[0] != '\'' && text[0] != '"') || !phpSecretArrayKey.MatchString(text[1:len(text)-1]) {
		return next
	}
	arrow := phpArrow.FindString(s.contents[next:])
	if arrow == "" {
		return next
	}
	return s.replaceString(next+len(arrow), "'wp-ssh-bridge-local'", next)
}

// replaceString swaps the string literal at valueStart, and otherwise returns fallback.
func (s *wpConfigSanitizer) replaceString(valueStart int, text string, fallback int) int {
	if valueStart >= len(s.contents) || (s.contents[valueStart] != '\'' && s.contents[valueStart] != '"') {
		return fallback
	}
	end, _ := skipPHPToken(s.contents, valueStart)
	s.replace(valueStart, end, text)
	return end
}

func (s *wpConfigSanitizer) variable(i int) int {
	match := phpSecretVariable.FindString(s.contents[i:])
	if match == "" {
		return i
	}
	return s.replaceString(i+len(match), "'wp-ssh-bridge-local'", i)
}

func (s *wpConfigSanitizer) define(i int) int {
	match := phpDefineStart.FindStringSubmatch(s.contents[i:])
	if match == nil {
		return i
	}
	valueStart := i + len(match[0])
	valueEnd := phpValueEnd(s.contents, valueStart, ')')
	// Without a semicolon it is part of an expression: nothing may be taken out around it.
	statementEnd := valueEnd
	after := min(valueEnd+1, len(s.contents))
	for after < len(s.contents) && (s.contents[after] == ' ' || s.contents[after] == '\t') {
		after++
	}
	if after < len(s.contents) && s.contents[after] == ';' {
		statementEnd = after + 1
	}
	return s.constant(i, match[1], valueStart, valueEnd, statementEnd)
}

func (s *wpConfigSanitizer) constStatement(i int) int {
	match := phpConstStart.FindStringSubmatch(s.contents[i:])
	if match == nil {
		return i
	}
	valueStart := i + len(match[0])
	valueEnd := phpValueEnd(s.contents, valueStart, ';')
	return s.constant(i, match[1], valueStart, valueEnd, min(valueEnd+1, len(s.contents)))
}

// constant treats a define() or const whose value spans valueStart to valueEnd. It returns
// where to read on, which is inside the value if that stays, as it may nest a secret.
func (s *wpConfigSanitizer) constant(start int, name string, valueStart int, valueEnd int, statementEnd int) int {
	contents := s.contents
	switch {
	case wpConfigProvidedByDDEV(name) && statementEnd > valueEnd && isWholeStatement(contents, start):
		to := statementEnd
		for to < len(contents) && (contents[to] == ' ' || contents[to] == '\t') {
			to++
		}
		if to < len(contents) && contents[to] == '\r' {
			to++
		}
		if to < len(contents) && contents[to] == '\n' {
			to++
		}
		s.replace(strings.LastIndexByte(contents[:start], '\n')+1, to, "")
		return to
	case name == "WP_DEBUG":
		s.replace(valueStart, valueEnd, " false")
	case wpConfigProvidedByDDEV(name), wpConfigConstantHoldsSecret(name) && !phpScalarLiteral.MatchString(contents[valueStart:valueEnd]):
		s.replace(valueStart, valueEnd, " "+wpConfigPlaceholder(name))
	default:
		return valueStart
	}
	return statementEnd
}

func (s *wpConfigSanitizer) putenv(i int) int {
	match := phpPutenvStart.FindStringSubmatch(s.contents[i:])
	if match == nil || (!wpConfigConstantHoldsSecret(match[1]) && !strings.HasPrefix(match[1], "DB_")) {
		return i
	}
	end, _ := skipPHPToken(s.contents, i+strings.IndexAny(match[0], `'"`))
	valueStart := i + len(match[0])
	s.replace(valueStart, max(end-1, valueStart), "wp-ssh-bridge-local")
	return end
}

// commentHoldsSecret finds an old value someone commented out rather than deleted.
func commentHoldsSecret(comment string) bool {
	for _, match := range phpCommentedSecret.FindAllStringSubmatch(comment, -1) {
		if name := match[1]; wpConfigProvidedByDDEV(name) && name != "WP_DEBUG" || wpConfigConstantHoldsSecret(name) {
			return true
		}
	}
	return false
}
