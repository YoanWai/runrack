package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/YoanWai/runrack/internal/collect"
	"github.com/YoanWai/runrack/internal/provider/github"
	"github.com/YoanWai/runrack/internal/target"
	"github.com/YoanWai/runrack/internal/ui"
)

var (
	version = "dev"
	commit  = "none"
)

type options struct {
	noAPI      bool
	color      string
	glyphs     string
	containers string
	timeout    time.Duration
	demo       bool
	jsonOut    bool
	once       bool
	width      int
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	command := ""
	if len(args) > 0 && (args[0] == "doctor" || args[0] == "version") {
		command, args = args[0], args[1:]
	}
	opts, err := parseFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	switch command {
	case "version":
		fmt.Fprintf(stdout, "runrack %s (%s)\n", version, commit)
		return 0
	case "doctor":
		return doctor(opts, stdout)
	}
	if opts.jsonOut {
		return printJSON(opts, stdout, stderr)
	}
	if opts.once {
		return printOnce(opts, stdout, stderr)
	}
	return runTUI(opts, stderr)
}

func parseFlags(args []string, stderr io.Writer) (options, error) {
	var opts options
	fs := flag.NewFlagSet("runrack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&opts.noAPI, "no-api", false, "never call the GitHub API, even with a token")
	fs.StringVar(&opts.color, "color", "auto", "auto|none|16|256|truecolor")
	fs.StringVar(&opts.glyphs, "glyphs", "auto", "auto|unicode|console|ascii")
	fs.StringVar(&opts.containers, "containers", "*", "only watch Incus containers matching this glob")
	fs.DurationVar(&opts.timeout, "timeout", 10*time.Second, "time limit for --json, --once and doctor")
	fs.BoolVar(&opts.demo, "demo", false, "show a made-up rack of runners")
	fs.BoolVar(&opts.jsonOut, "json", false, "print one snapshot as JSON and exit")
	fs.BoolVar(&opts.once, "once", false, "print the board once and exit")
	fs.IntVar(&opts.width, "width", 0, "board width for --once (default: terminal width or 120)")
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: runrack [doctor|version] [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if _, ok := ui.ParseColor(opts.color); !ok {
		fmt.Fprintf(stderr, "runrack: --color must be auto, none, 16, 256 or truecolor\n")
		return opts, errors.New("bad --color")
	}
	if _, ok := ui.ParseGlyphs(opts.glyphs); !ok {
		fmt.Fprintf(stderr, "runrack: --glyphs must be auto, unicode, console or ascii\n")
		return opts, errors.New("bad --glyphs")
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "runrack: unexpected argument %q\n", fs.Arg(0))
		return opts, errors.New("unexpected argument")
	}
	return opts, nil
}

func newEngine(opts options) (*collect.Engine, string) {
	client, source := apiClient(opts)
	return collect.New(collect.Options{ProcRoot: "/proc", ContainerGlob: opts.containers, API: client}), source
}

func apiClient(opts options) (*github.Client, string) {
	if opts.noAPI {
		return nil, ""
	}
	token, source := github.TokenSource(os.Getenv, func() string {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return target.GHToken(ctx)
	})
	if token == "" {
		return nil, ""
	}
	return github.NewClient(github.DefaultAPI, token), source
}

func printJSON(opts options, stdout, stderr io.Writer) int {
	snap, err := collectOnce(opts)
	if err != nil {
		fmt.Fprintln(stderr, "runrack:", err)
		return 1
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(snap); err != nil {
		fmt.Fprintln(stderr, "runrack:", err)
		return 1
	}
	return 0
}
