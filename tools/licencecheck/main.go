// Command licencecheck fails when a dependency's licence is not on cactusTrade's allowed list
// (CLAUDE.md §3, PLAN 7.7): MIT, BSD-2-Clause, BSD-3-Clause, ISC, Apache-2.0, MPL-2.0 and
// public domain (Unlicense, CC0, the SQLite blessing). It uses only the standard library.
//
//	go list -m -json all >modules.json
//	go list -deps -test -tags integration -f '{{with .Module}}{{.Path}}{{end}}' ./... >build.txt
//	pnpm -C web licenses list --prod --json >pnpm.json
//	go run ./tools/licencecheck -go-modules modules.json -go-build build.txt -pnpm pnpm.json
//
// Go modules are judged by the licence files in their root (LICENSE, LICENCE, COPYING,
// UNLICENSE…), read from the module cache. Every module a package of which is built or
// tested here (build.txt, plus the main module) is judged; the rest of the module graph
// (dependencies' own tests and tools, never compiled into cactusTrade) is counted as skipped,
// so the result does not depend on what happens to be in the module cache. npm packages are
// judged by their SPDX expression.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("licencecheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	goModules := fs.String("go-modules", "", "output of `go list -m -json all`")
	goBuild := fs.String("go-build", "", "module paths built or tested here, one per line")
	pnpm := fs.String("pnpm", "", "output of `pnpm licenses list --prod --json`")
	list := fs.Bool("list", false, "also print every allowed dependency with its licence")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 || (*goModules == "" && *pnpm == "") || (*goModules != "") != (*goBuild != "") {
		fmt.Fprintln(stderr, "usage: licencecheck [-go-modules FILE -go-build FILE] [-pnpm FILE]")
		return 2
	}
	var problems []string
	if *goModules != "" {
		rep, err := checkGoFiles(*goModules, *goBuild)
		if err != nil {
			fmt.Fprintln(stderr, "licencecheck:", err)
			return 2
		}
		fmt.Fprintf(stdout, "go: %d modules allowed, %d in the module graph but outside the build (not checked)\n", rep.Allowed, rep.Skipped)
		printList(stdout, *list, rep.Listing)
		problems = append(problems, rep.Problems...)
	}
	if *pnpm != "" {
		rep, err := checkPnpmFile(*pnpm)
		if err != nil {
			fmt.Fprintln(stderr, "licencecheck:", err)
			return 2
		}
		fmt.Fprintf(stdout, "web: %d packages allowed\n", rep.Allowed)
		printList(stdout, *list, rep.Listing)
		problems = append(problems, rep.Problems...)
	}
	for _, p := range problems {
		fmt.Fprintln(stderr, "licencecheck:", p)
	}
	if len(problems) > 0 {
		fmt.Fprintf(stderr, "licencecheck: %d dependency licence problem(s)\n", len(problems))
		return 1
	}
	fmt.Fprintln(stdout, "licences ok")
	return 0
}

func printList(w io.Writer, on bool, lines []string) {
	if !on {
		return
	}
	for _, l := range lines {
		fmt.Fprintln(w, "  "+l)
	}
}
