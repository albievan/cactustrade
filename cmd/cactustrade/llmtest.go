package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/albievan/cactustrade/internal/adapters/argon2"
	"github.com/albievan/cactustrade/internal/adapters/llm"
	"github.com/albievan/cactustrade/internal/adapters/system"
	"github.com/albievan/cactustrade/internal/app"
)

// llmTestCmd runs the LLM reviewer test of one stored profile from the console, exactly as
// the LLM reviewer page's Test button does (one made-up review, logged as a test call; no
// trade, no broker): `cactustrade llm-test [--profile NAME]`. It prints the verdict or the
// provider's error, never the key.
func llmTestCmd(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("llm-test", flag.ContinueOnError)
	fs.SetOutput(out)
	profile := fs.String("profile", "", "profile name or id (default: the default profile)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(nil, getenv)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer store.Close()
	clock := system.Clock{}
	secrets, _, err := openSecrets(ctx, cfg, store, getenv, clock, log)
	if err != nil {
		return err
	}
	svc, err := app.New(app.Deps{
		Store: store, Secrets: secrets, Hasher: argon2.New(argon2.Default), Random: system.Random{},
		Clock: clock, Log: log, LLM: llm.New(&http.Client{Timeout: time.Minute}),
	}, app.Config{})
	if err != nil {
		return err
	}
	name, res, err := svc.ConsoleTestLLM(ctx, *profile)
	if err != nil {
		return err
	}
	if !res.OK {
		_, err := fmt.Fprintf(out, "%s: no usable answer after %s: %s\n", name, res.Latency.Round(time.Millisecond), res.Error)
		return err
	}
	_, err = fmt.Fprintf(out, "%s: answered %s in %s (model %s, %d in / %d out tokens): %s\n",
		name, res.Verdict.Kind, res.Latency.Round(time.Millisecond), res.Model, res.InputTokens, res.OutputTokens, res.Verdict.Reason)
	return err
}
