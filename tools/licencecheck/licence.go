package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Licence identifiers (SPDX where there is one).
const (
	licMIT          = "MIT"
	licBSD2         = "BSD-2-Clause"
	licBSD3         = "BSD-3-Clause"
	licISC          = "ISC"
	licApache2      = "Apache-2.0"
	licMPL2         = "MPL-2.0"
	licUnlicense    = "Unlicense"
	licCC0          = "CC0-1.0"
	licPublicDomain = "public-domain"
	licGPL          = "GPL"
	licLGPL         = "LGPL"
	licAGPL         = "AGPL"
)

// allowed is cactusTrade's licence list (CLAUDE.md §3).
var allowed = map[string]bool{
	licMIT: true, licBSD2: true, licBSD3: true, licISC: true, licApache2: true, licMPL2: true,
	licUnlicense: true, licCC0: true, licPublicDomain: true,
}

// nonCode lists reviewed licence files that do not cover the module's code (module path /
// file name → why). Everything else in a module's root named like a licence is judged.
var nonCode = map[string]string{
	"github.com/opencontainers/go-digest/LICENSE.docs": "CC BY-SA 4.0 for the specification text; the code is Apache-2.0 (LICENSE)",
	"modernc.org/memory/LICENSE-LOGO":                  "a link to the logo's source; the code is BSD-3-Clause (LICENSE)",
}

// report is the outcome of one check.
type report struct {
	Allowed  int
	Skipped  int
	Problems []string // one line each, naming the module or package
	Listing  []string // "name licence[, licence]" for every allowed one (-list)
}

// module is the part of `go list -m -json` that matters here.
type module struct {
	Path    string
	Version string
	Main    bool
	Dir     string
	Replace *module
}

// readModules decodes the stream of JSON objects `go list -m -json all` writes.
func readModules(r io.Reader) ([]module, error) {
	dec := json.NewDecoder(r)
	var out []module
	for {
		var m module
		err := dec.Decode(&m)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("go modules: %w", err)
		}
		out = append(out, m)
	}
}

// readLines returns the non-empty lines of a file as a set.
func readLines(r io.Reader) (map[string]bool, error) {
	set := map[string]bool{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			set[l] = true
		}
	}
	return set, sc.Err()
}

func checkGoFiles(modulesPath, buildPath string) (report, error) {
	mf, err := os.Open(modulesPath)
	if err != nil {
		return report{}, err
	}
	defer mf.Close()
	mods, err := readModules(mf)
	if err != nil {
		return report{}, err
	}
	bf, err := os.Open(buildPath)
	if err != nil {
		return report{}, err
	}
	defer bf.Close()
	build, err := readLines(bf)
	if err != nil {
		return report{}, err
	}
	if len(mods) == 0 || len(build) == 0 {
		return report{}, errors.New("no Go modules to check (empty input)")
	}
	return checkGo(mods, build), nil
}

// checkGo judges every module of the graph (see the package comment).
func checkGo(mods []module, build map[string]bool) report {
	var rep report
	for _, m := range mods {
		name := m.Path
		if m.Version != "" {
			name += "@" + m.Version
		}
		dir := m.Dir
		if m.Replace != nil && m.Replace.Dir != "" {
			dir = m.Replace.Dir
		}
		if !m.Main && !build[m.Path] {
			// In the module graph (a dependency's own test or tool requirement) but no package
			// of it is compiled into cactusTrade or its tests.
			rep.Skipped++
			continue
		}
		if dir == "" {
			rep.Problems = append(rep.Problems, fmt.Sprintf("module %s is built but not downloaded: run go mod download", name))
			continue
		}
		lics, err := moduleLicences(m.Path, dir)
		if err != nil {
			rep.Problems = append(rep.Problems, fmt.Sprintf("module %s: %v", name, err))
			continue
		}
		if bad := disallowed(lics); len(bad) > 0 {
			rep.Problems = append(rep.Problems, fmt.Sprintf("module %s: licence %s is not allowed", name, strings.Join(bad, ", ")))
			continue
		}
		rep.Allowed++
		rep.Listing = append(rep.Listing, name+" "+strings.Join(lics, ", "))
	}
	return rep
}

// licenceFile reports a file name that holds a licence.
func licenceFile(name string) bool {
	n := strings.ToUpper(name)
	for _, p := range []string{"LICENSE", "LICENCE", "COPYING", "UNLICENSE"} {
		if n == p || strings.HasPrefix(n, p+".") || strings.HasPrefix(n, p+"-") || strings.HasPrefix(n, p+"_") {
			return true
		}
	}
	return false
}

// moduleLicences classifies every licence file in the module's root.
func moduleLicences(path, dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var lics []string
	files := 0
	for _, e := range entries {
		if e.IsDir() || !licenceFile(e.Name()) || nonCode[path+"/"+e.Name()] != "" {
			continue
		}
		files++
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		got := classify(string(b))
		if len(got) == 0 {
			return nil, fmt.Errorf("licence file %s is not one this check recognises", e.Name())
		}
		lics = append(lics, got...)
	}
	if files == 0 {
		return nil, errors.New("no licence file")
	}
	sort.Strings(lics)
	return slices.Compact(lics), nil
}

// disallowed returns the licences that are not on the list.
func disallowed(lics []string) []string {
	var bad []string
	for _, l := range lics {
		if !allowed[l] {
			bad = append(bad, l)
		}
	}
	return bad
}

// classify names the licences a licence text grants, by their characteristic wording.
func classify(text string) []string {
	t := strings.ToLower(strings.Join(strings.Fields(text), " "))
	has := func(s ...string) bool {
		for _, x := range s {
			if !strings.Contains(t, x) {
				return false
			}
		}
		return true
	}
	var out []string
	mpl := has("mozilla public license") && (has("version 2.0") || has("v. 2.0"))
	if mpl {
		out = append(out, licMPL2)
	}
	// MPL 2.0 names the GNU licences as "Secondary Licenses"; anywhere else they are the
	// licence itself.
	if !mpl {
		switch {
		case has("gnu affero general public license"):
			out = append(out, licAGPL)
		case has("gnu lesser general public license"), has("gnu library general public license"):
			out = append(out, licLGPL)
		case has("gnu general public license"):
			out = append(out, licGPL)
		}
	}
	if has("apache license", "version 2.0") {
		out = append(out, licApache2)
	}
	if has("permission is hereby granted, free of charge, to any person obtaining a copy") {
		out = append(out, licMIT)
	}
	if has("permission to use, copy, modify, and", "distribute this software for any purpose with or without fee is hereby granted") {
		out = append(out, licISC)
	}
	if has("redistribution and use in source and binary forms, with or without modification, are permitted") {
		if has("neither the name") || has("names of its contributors may be used") || has("name of the copyright holder nor") {
			out = append(out, licBSD3)
		} else {
			out = append(out, licBSD2)
		}
	}
	switch {
	case has("this is free and unencumbered software released into the public domain"):
		out = append(out, licUnlicense)
	case has("cc0 1.0 universal"):
		out = append(out, licCC0)
	case has("the author disclaims copyright to this source code"), has("dedicated to the public domain"), has("released into the public domain"):
		out = append(out, licPublicDomain)
	}
	return out
}

// pnpmPackage is one entry of `pnpm licenses list --json`.
type pnpmPackage struct {
	Name     string   `json:"name"`
	Versions []string `json:"versions"`
	License  string   `json:"license"`
}

func checkPnpmFile(path string) (report, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return report{}, err
	}
	var byLicence map[string][]pnpmPackage
	if err := json.Unmarshal(b, &byLicence); err != nil {
		return report{}, fmt.Errorf("pnpm licenses: %w", err)
	}
	if len(byLicence) == 0 {
		return report{}, errors.New("no web packages to check (empty input)")
	}
	return checkPnpm(byLicence), nil
}

// checkPnpm judges every package by its SPDX expression.
func checkPnpm(byLicence map[string][]pnpmPackage) report {
	var rep report
	keys := make([]string, 0, len(byLicence))
	for k := range byLicence {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, p := range byLicence[k] {
			expr := p.License
			if expr == "" {
				expr = k
			}
			if spdxAllowed(expr) {
				rep.Allowed++
				rep.Listing = append(rep.Listing, p.Name+"@"+strings.Join(p.Versions, ",")+" "+expr)
				continue
			}
			rep.Problems = append(rep.Problems, fmt.Sprintf("web package %s@%s: licence %q is not allowed", p.Name, strings.Join(p.Versions, ","), expr))
		}
	}
	return rep
}

// spdxAllowed evaluates an SPDX expression: an OR needs one allowed side, an AND both.
// Anything it does not understand (WITH exceptions, LicenseRef-…, "Unknown") is refused.
func spdxAllowed(expr string) bool {
	e := strings.TrimSpace(expr)
	for strings.HasPrefix(e, "(") && strings.HasSuffix(e, ")") && balanced(e[1:len(e)-1]) {
		e = strings.TrimSpace(e[1 : len(e)-1])
	}
	if parts := splitTop(e, " OR "); len(parts) > 1 {
		return slices.ContainsFunc(parts, spdxAllowed)
	}
	if parts := splitTop(e, " AND "); len(parts) > 1 {
		for _, p := range parts {
			if !spdxAllowed(p) {
				return false
			}
		}
		return true
	}
	return allowed[e]
}

// balanced reports whether the parentheses in s pair up.
func balanced(s string) bool {
	depth := 0
	for _, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

// splitTop splits s at sep outside parentheses.
func splitTop(s, sep string) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 && strings.HasPrefix(s[i:], sep) {
			parts = append(parts, s[start:i])
			start = i + len(sep)
			i += len(sep) - 1
		}
	}
	return append(parts, s[start:])
}
