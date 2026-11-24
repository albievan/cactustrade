package main

import (
	"bytes"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", "mods", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// writeInputs writes a `go list -m -json all` stream and a build list for the modules.
func writeInputs(t *testing.T, mods []module, build []string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	var b bytes.Buffer
	for _, m := range mods {
		j, err := json.MarshalIndent(m, "", "\t")
		if err != nil {
			t.Fatal(err)
		}
		b.Write(j)
		b.WriteByte('\n')
	}
	mp, bp := filepath.Join(dir, "modules.json"), filepath.Join(dir, "build.txt")
	if err := os.WriteFile(mp, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bp, []byte(strings.Join(build, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return mp, bp
}

// TestGPL3ModuleFails is PLAN 7.7's acceptance test: a GPL-3.0 module in the build fails the
// check, and the failure names the module.
func TestGPL3ModuleFails(t *testing.T) {
	mods := []module{
		{Path: "example.com/main", Main: true, Dir: fixture(t, "mit")},
		{Path: "example.com/fine", Version: "v1.0.0", Dir: fixture(t, "mit")},
		{Path: "example.com/copyleft", Version: "v3.1.0", Dir: fixture(t, "gpl3")},
	}
	mp, bp := writeInputs(t, mods, []string{"example.com/main", "example.com/fine", "example.com/copyleft"})
	var out, errb bytes.Buffer
	if code := run([]string{"-go-modules", mp, "-go-build", bp}, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1\n%s%s", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "module example.com/copyleft@v3.1.0: licence GPL is not allowed") {
		t.Fatalf("stderr: %s", errb.String())
	}
	if strings.Contains(errb.String(), "example.com/fine") || strings.Contains(out.String(), "licences ok") {
		t.Fatalf("out %q err %q", out.String(), errb.String())
	}
}

func TestGoModules(t *testing.T) {
	tests := []struct {
		name    string
		mods    []module
		build   []string
		want    string // in stderr; "" = passes
		allowed int
		skipped int
	}{
		{"allowed", []module{{Path: "a", Main: true, Dir: fixture(t, "mit")}, {Path: "b", Version: "v1", Dir: fixture(t, "mit")}}, []string{"a", "b"}, "", 2, 0},
		{"GPL outside the build is not checked", []module{{Path: "a", Main: true, Dir: fixture(t, "mit")}, {Path: "g", Version: "v1", Dir: fixture(t, "gpl3")}}, []string{"a"}, "", 1, 1},
		{"not downloaded, outside the build", []module{{Path: "a", Main: true, Dir: fixture(t, "mit")}, {Path: "x", Version: "v1"}}, []string{"a"}, "", 1, 1},
		{"not downloaded but built", []module{{Path: "a", Main: true, Dir: fixture(t, "mit")}, {Path: "x", Version: "v1"}}, []string{"a", "x"}, "module x@v1 is built but not downloaded", 1, 0},
		{"no licence file", []module{{Path: "n", Version: "v1", Dir: fixture(t, "none")}}, []string{"n"}, "module n@v1: no licence file", 0, 0},
		{"unrecognised licence file", []module{{Path: "d", Version: "v1", Dir: fixture(t, "docs")}}, []string{"d"}, "LICENSE.docs is not one this check recognises", 0, 0},
		{"reviewed non-code licence file", []module{{Path: "github.com/opencontainers/go-digest", Version: "v1", Dir: fixture(t, "docs")}}, []string{"github.com/opencontainers/go-digest"}, "", 1, 0},
		{"replace directory", []module{{Path: "r", Version: "v1", Replace: &module{Path: "r2", Dir: fixture(t, "gpl3")}, Dir: fixture(t, "mit")}}, []string{"r"}, "licence GPL", 0, 0},
		{"missing directory", []module{{Path: "m", Version: "v1", Dir: filepath.Join(t.TempDir(), "gone")}}, []string{"m"}, "module m@v1:", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mp, bp := writeInputs(t, tt.mods, tt.build)
			rep, err := checkGoFiles(mp, bp)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Join(rep.Problems, "\n")
			if (tt.want == "") != (got == "") || !strings.Contains(got, tt.want) {
				t.Fatalf("problems %q, want %q", got, tt.want)
			}
			if rep.Allowed != tt.allowed || rep.Skipped != tt.skipped {
				t.Fatalf("allowed %d skipped %d, want %d %d", rep.Allowed, rep.Skipped, tt.allowed, tt.skipped)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name, text string
		want       []string
	}{
		{"MIT", "Permission is hereby granted, free of charge, to any person obtaining a copy of this software", []string{licMIT}},
		{"BSD-3", "Redistribution and use in source and binary forms, with or without\nmodification, are permitted provided that ... Neither the name of Google Inc. nor the names", []string{licBSD3}},
		{"BSD-2", "Redistribution and use in source and binary forms, with or without modification, are permitted provided that the following conditions are met", []string{licBSD2}},
		{"ISC", "Permission to use, copy, modify, and/or distribute this software for any purpose with or without fee is hereby granted", []string{licISC}},
		{"Apache", "Apache License\n Version 2.0, January 2004", []string{licApache2}},
		{"MPL names the GNU licences as secondary", "Mozilla Public License Version 2.0\n\"Secondary License\" means either the GNU General Public License, Version 2.0, the GNU Lesser General Public License", []string{licMPL2}},
		{"GPL-3.0", "GNU GENERAL PUBLIC LICENSE\nVersion 3, 29 June 2007", []string{licGPL}},
		{"GPL-2.0 mixed case", "This program is free software; you can redistribute it under the terms of the GNU General Public License", []string{licGPL}},
		{"LGPL", "GNU LESSER GENERAL PUBLIC LICENSE Version 3", []string{licLGPL}},
		{"AGPL", "GNU AFFERO GENERAL PUBLIC LICENSE Version 3", []string{licAGPL}},
		{"Unlicense", "This is free and unencumbered software released into the public domain.", []string{licUnlicense}},
		{"CC0", "Creative Commons Legal Code\n\nCC0 1.0 Universal", []string{licCC0}},
		{"SQLite blessing", "The author disclaims copyright to this source code. In place of a legal notice, here is a blessing", []string{licPublicDomain}},
		{"dual", "Apache License Version 2.0 ... Permission is hereby granted, free of charge, to any person obtaining a copy", []string{licApache2, licMIT}},
		{"proprietary", "Copyright 2026 Example. All rights reserved.", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classify(tt.text); !slices.Equal(got, tt.want) {
				t.Fatalf("classify = %v, want %v", got, tt.want)
			}
		})
	}
	// cactusTrade's own LICENSE is public domain
	b, err := os.ReadFile(filepath.Join("..", "..", "LICENSE"))
	if err != nil {
		t.Fatal(err)
	}
	if got := classify(string(b)); len(got) == 0 || len(disallowed(got)) > 0 {
		t.Fatalf("our LICENSE: %v", got)
	}
}

func TestSPDX(t *testing.T) {
	tests := []struct {
		expr string
		ok   bool
	}{
		{"MIT", true},
		{"Apache-2.0", true},
		{"BSD-3-Clause", true},
		{"ISC", true},
		{"MPL-2.0", true},
		{"Unlicense", true},
		{"CC0-1.0", true},
		{"(MIT OR Apache-2.0)", true},
		{"(MIT OR GPL-3.0-only)", true},
		{"MIT AND ISC", true},
		{"(MIT AND GPL-3.0-only)", false},
		{"GPL-3.0", false},
		{"GPL-3.0-or-later", false},
		{"AGPL-3.0", false},
		{"Unknown", false},
		{"", false},
		{"LicenseRef-Proprietary", false},
		{"Apache-2.0 WITH LLVM-exception", false},
		{"((MIT OR GPL-2.0) AND (BSD-2-Clause OR GPL-2.0))", true},
		{"(MIT) AND (GPL-2.0)", false},
	}
	for _, tt := range tests {
		if got := spdxAllowed(tt.expr); got != tt.ok {
			t.Errorf("spdxAllowed(%q) = %v, want %v", tt.expr, got, tt.ok)
		}
	}
}

func TestPnpm(t *testing.T) {
	dir := t.TempDir()
	in := map[string][]pnpmPackage{
		"MIT":        {{Name: "react", Versions: []string{"18.3.1"}, License: "MIT"}},
		"Apache-2.0": {{Name: "swagger-ui-dist", Versions: []string{"5.29.0"}, License: "Apache-2.0"}},
		"GPL-3.0":    {{Name: "copyleft-widget", Versions: []string{"1.0.0"}, License: "GPL-3.0"}},
		"Unknown":    {{Name: "mystery", Versions: []string{"0.1.0"}}},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "pnpm.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"-pnpm", p, "-list"}, &out, &errb); code != 1 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{`web package copyleft-widget@1.0.0: licence "GPL-3.0" is not allowed`, `web package mystery@0.1.0: licence "Unknown"`} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("missing %q in %s", want, errb.String())
		}
	}
	if !strings.Contains(out.String(), "web: 2 packages allowed") || !strings.Contains(out.String(), "react@18.3.1 MIT") {
		t.Errorf("stdout %s", out.String())
	}

	ok := filepath.Join(dir, "ok.json")
	if err := os.WriteFile(ok, []byte(`{"MIT":[{"name":"react","versions":["18.3.1"],"license":"MIT"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"-pnpm", ok}, &out, &errb); code != 0 || !strings.Contains(out.String(), "licences ok") {
		t.Fatalf("exit %d %s %s", code, out.String(), errb.String())
	}
}

func TestRunArguments(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		nil,
		{"extra"},
		{"-nope"},
		{"-go-modules", empty}, // without -go-build
		{"-go-modules", empty, "-go-build", empty}, // nothing to check
		{"-go-modules", bad, "-go-build", empty},
		{"-go-modules", filepath.Join(dir, "missing"), "-go-build", empty},
		{"-pnpm", bad},
		{"-pnpm", filepath.Join(dir, "missing")},
		{"-pnpm", empty},
	} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != 2 {
			t.Errorf("%v: exit %d, want 2 (%s)", args, code, errb.String())
		}
	}
}

// TestStandardLibraryOnly: the checker itself has no dependency (PLAN 7.7).
func TestStandardLibraryOnly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if first, _, _ := strings.Cut(p, "/"); strings.Contains(first, ".") {
				t.Errorf("%s imports %s: the licence checker uses only the standard library", f, p)
			}
		}
	}
}
