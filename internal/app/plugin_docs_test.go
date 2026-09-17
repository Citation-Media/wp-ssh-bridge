package app

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The documentation lists the embedded blocked plugins for users, and a list
// that drifts from the binary is worse than no list at all. This keeps the page
// honest: change defaultPluginList without touching the page and the suite
// fails, naming exactly what is missing on either side.
func TestDocumentationListsEveryBlockedPlugin(t *testing.T) {
	t.Parallel()

	page := filepath.Join("..", "..", "docs", "troubleshooting", "blocked-plugins.mdx")
	content, err := os.ReadFile(page)
	if err != nil {
		t.Fatalf("read %s: %v", page, err)
	}

	embedded, err := parsePluginList(strings.NewReader(defaultPluginList), "embedded default plugin list")
	if err != nil {
		t.Fatalf("parse embedded plugin list: %v", err)
	}

	documented := pluginsInFencedBlocks(string(content))

	missing := difference(embedded, documented)
	if len(missing) > 0 {
		t.Errorf("%s does not list these blocked plugins: %s", page, strings.Join(missing, ", "))
	}

	extra := difference(documented, embedded)
	if len(extra) > 0 {
		t.Errorf("%s lists plugins that are not blocked: %s", page, strings.Join(extra, ", "))
	}

	if count := documentedCount(string(content)); count != 0 && count != len(embedded) {
		t.Errorf("%s claims %d slugs, the embedded list has %d", page, count, len(embedded))
	}
}

// pluginsInFencedBlocks collects the slugs from every ```text block on the page.
// The page lays them out in columns, so a block holds several per line.
func pluginsInFencedBlocks(markdown string) []string {
	var found []string
	inBlock := false
	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			// Only the bare ```text blocks hold slugs; titled blocks are examples.
			inBlock = trimmed == "```text"
			continue
		}
		if !inBlock || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		found = append(found, strings.Fields(trimmed)...)
	}
	return found
}

// documentedCount reads the "These N slugs" claim so the prose cannot go stale
// either. Returns 0 when the page does not state a number.
func documentedCount(markdown string) int {
	match := regexp.MustCompile(`These (\d+) slugs`).FindStringSubmatch(markdown)
	if match == nil {
		return 0
	}
	n := 0
	for _, r := range match[1] {
		n = n*10 + int(r-'0')
	}
	return n
}

func difference(want []string, have []string) []string {
	present := make(map[string]bool, len(have))
	for _, item := range have {
		present[item] = true
	}
	var missing []string
	for _, item := range want {
		if !present[item] {
			missing = append(missing, item)
		}
	}
	sort.Strings(missing)
	return missing
}
