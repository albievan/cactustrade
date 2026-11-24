package main

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

// PLAN 6.16 and CLAUDE.md §2/§7: nothing in the web app, the README, the docs or the API
// contract may claim or imply that cactusTrade is profitable. TestNoProfitabilityClaims scans
// those files for a list of banned phrases; a line may only contain one when it is listed in
// wordingAllowed with the reason it is not a claim.

// wordingRoots are the scanned trees and files, relative to the repository root.
var wordingRoots = []string{"web/src", "README.md", "docs", "api/openapi"}

// wordingExts are the text files scanned under the roots.
var wordingExts = map[string]bool{".md": true, ".ts": true, ".tsx": true, ".css": true, ".html": true, ".yaml": true, ".yml": true, ".json": true}

// bannedWording is matched case-insensitively against every line.
var bannedWording = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`profitab\w*`,
	`guarantee\w*`,
	`beat(s|ing)? the market`,
	`outperform\w*`,
	`\bma(ke|kes|king|de) money\b`,
	`winning (strateg\w*|system|formula)`,
	`returns of`,
	`risk[- ]free`,
	`sure[- ]?(fire|thing|bet)`,
	`can(no|')t lose`,
	`never loses?\b`,
	`passive income`,
	`get rich`,
	`consistent (profits?|returns?|gains?)`,
	`high(er)? returns?`,
	`proven (strateg\w*|edge|system)`,
	`money machine`,
	`double your`,
	`steady (income|profits?|gains?)`,
	`edge over the market`,
}, "|"))

// wordingAllow is a line that may contain a banned phrase: in file, a line containing
// contains, for the given reason.
type wordingAllow struct {
	file     string
	contains string
	reason   string
}

var wordingAllowed = []wordingAllow{
	{"README.md", "implies that it is profitable", "the risk warning denies any such claim"},
	{"docs/PLAN.md", "No profitability claims anywhere", "states this rule"},
	{"docs/PLAN.md", "TestNoProfitabilityClaims", "describes this guard and its banned-phrase list"},
	{"docs/ARCHITECTURE.md", "TestNoProfitabilityClaims", "describes this guard"},
}

// wordingFinding is a banned phrase on a line that is not allowed.
type wordingFinding struct {
	file   string
	line   int
	phrase string
	text   string
}

func (f wordingFinding) String() string {
	return fmt.Sprintf("%s:%d: %q in %q", f.file, f.line, f.phrase, strings.TrimSpace(f.text))
}

// scanWording returns every banned phrase in the roots of fsys that no allow entry covers,
// and how often each allow entry was used (by index).
func scanWording(fsys fs.FS, roots []string, allowed []wordingAllow) ([]wordingFinding, []int, error) {
	var out []wordingFinding
	used := make([]int, len(allowed))
	scan := func(name string) error {
		f, err := fsys.Open(name)
		if err != nil {
			return err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		n := 0
	lines:
		for sc.Scan() {
			n++
			text := sc.Text()
			m := bannedWording.FindString(text)
			if m == "" {
				continue
			}
			for i, a := range allowed {
				if a.file == name && strings.Contains(text, a.contains) {
					used[i]++
					continue lines
				}
			}
			out = append(out, wordingFinding{file: name, line: n, phrase: m, text: text})
		}
		return sc.Err()
	}
	for _, root := range roots {
		err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == "dist" {
					return fs.SkipDir
				}
				return nil
			}
			if !wordingExts[path.Ext(p)] {
				return nil
			}
			return scan(p)
		})
		if err != nil {
			return nil, nil, fmt.Errorf("scan %s: %w", root, err)
		}
	}
	return out, used, nil
}

func TestNoProfitabilityClaims(t *testing.T) {
	for _, a := range wordingAllowed {
		if strings.TrimSpace(a.reason) == "" || a.file == "" || a.contains == "" {
			t.Fatalf("allow entry %+v needs a file, a text and a reason", a)
		}
	}
	findings, used, err := scanWording(os.DirFS("../.."), wordingRoots, wordingAllowed)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Errorf("profitability wording (PLAN 6.16; reword it, or allow the line in wordingAllowed with a reason): %s", f)
	}
	for i, n := range used {
		if n == 0 {
			t.Errorf("allow entry %+v matches no line any more: remove it", wordingAllowed[i])
		}
	}
}

func TestNoProfitabilityClaimsCatchesPlantedPhrases(t *testing.T) {
	planted := []string{
		"a profitable strategy",
		"Returns of 20 % a month",
		"Guaranteed gains",
		"It beats the market",
		"make money while you sleep",
		"our winning strategy",
		"risk-free trading",
		"you can't lose",
		"Consistent profits every day",
	}
	for _, phrase := range planted {
		t.Run(phrase, func(t *testing.T) {
			fsys := fstest.MapFS{
				"web/src/pages/Planted.tsx": {Data: []byte("export const x = 1;\nexport const t = \"" + phrase + "\";\n")},
				"README.md":                 {Data: []byte("# cactusTrade\n")},
				"docs/x.md":                 {Data: []byte("nothing to see\n")},
				"api/openapi/a.yaml":        {Data: []byte("openapi: 3.0.3\n")},
			}
			findings, _, err := scanWording(fsys, wordingRoots, wordingAllowed)
			if err != nil {
				t.Fatal(err)
			}
			if len(findings) != 1 || findings[0].file != "web/src/pages/Planted.tsx" || findings[0].line != 2 {
				t.Fatalf("findings = %v, want one on web/src/pages/Planted.tsx:2", findings)
			}
		})
	}
}

func TestNoProfitabilityClaimsAllowsOnlyListedLines(t *testing.T) {
	allowed := []wordingAllow{{"README.md", "implies that it is profitable", "the risk warning"}}
	fsys := fstest.MapFS{
		"README.md":          {Data: []byte("Nothing here implies that it is profitable.\nIt is profitable.\n")},
		"docs/x.md":          {Data: []byte("implies that it is profitable\n")}, // same text, another file
		"web/src/a.ts":       {Data: []byte("\n")},
		"api/openapi/a.yaml": {Data: []byte("\n")},
		"web/src/a.png":      {Data: []byte("profitable\n")}, // not a text file we scan
	}
	findings, used, err := scanWording(fsys, wordingRoots, allowed)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(findings))
	for _, f := range findings {
		got = append(got, fmt.Sprintf("%s:%d", f.file, f.line))
	}
	if strings.Join(got, ",") != "README.md:2,docs/x.md:1" {
		t.Fatalf("findings = %v", got)
	}
	if used[0] != 1 {
		t.Fatalf("used = %v, want the allow entry used once", used)
	}
}
