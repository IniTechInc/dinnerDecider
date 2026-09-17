// Command dinnerdecider is the Linux/IoT service: photograph your fridge, a
// local Gemma 4 E4B identifies what is in it, and the result drives inventory,
// recipes and a shopping list. Nothing leaves the box.
//
// It is one static Go binary with the web UI embedded in it. Inference is the
// one exception: llama.cpp is not compiled in, it is an official prebuilt
// llama-server supervised as a child process and reached over loopback HTTP.
// That is what keeps this binary cgo-free and trivially cross-compiled for a
// Raspberry Pi, a Jetson or an x86 box alike.
//
// Usage:
//
//	dinnerdecider [flags]              run the service
//	dinnerdecider token show           print the pairing token
//	dinnerdecider token rotate         issue a new one, unpairing every browser
//	dinnerdecider token forget         delete it
//	dinnerdecider version              print build information
//
// Configuration comes from four layers, lowest first: built-in defaults, the
// config file, DD_-prefixed environment variables, then flags. Run with -h for
// the full list.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/IniTechInc/dinnerDecider/linux/internal/api"
	"github.com/IniTechInc/dinnerDecider/linux/internal/auth"
	"github.com/IniTechInc/dinnerDecider/linux/internal/config"
)

// Build information, set at link time:
//
//	go build -ldflags "-X main.version=$(git describe --tags) -X main.commit=$(git rev-parse --short HEAD) -X main.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
//
// All three are deliberately plain strings with harmless defaults: a developer
// build that nobody stamped must still run, and GET /api/status renders an
// empty version as "dev" rather than as a blank field.
var (
	version   = ""
	commit    = ""
	buildDate = ""
)

func buildInfo() api.BuildInfo {
	return api.BuildInfo{Version: version, Commit: commit, BuildDate: buildDate}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main with the process boundary removed, so the tests can drive the
// whole program without spawning it.
func run(args []string, stdout, stderr io.Writer) int {
	cfg, positional, err := config.Load(config.Source{Args: args, Output: stderr})
	switch {
	case errors.Is(err, flag.ErrHelp):
		// -h is a request that was satisfied, not a failure.
		return 0
	case err != nil:
		fmt.Fprintf(stderr, "dinnerdecider: %v\n", err)
		return 2
	}

	logger := log.New(stderr, "", log.LstdFlags)

	if len(positional) > 0 {
		return runSubcommand(cfg, positional, stdout, stderr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := serve(ctx, cfg, logger.Printf); err != nil {
		fmt.Fprintf(stderr, "dinnerdecider: %v\n", err)
		return 1
	}
	return 0
}

// serve builds and runs the service.
func serve(ctx context.Context, cfg config.Config, logf func(string, ...any)) error {
	if cfg.ConfigPath != "" {
		logf("[service] configuration file: %s", cfg.ConfigPath)
	}
	logf("[service] state directory: %s", cfg.StateDir)

	svc, err := buildService(ctx, cfg, logf)
	if err != nil {
		return err
	}
	return svc.run(ctx)
}

// runSubcommand dispatches the non-serving verbs.
func runSubcommand(cfg config.Config, args []string, stdout, stderr io.Writer) int {
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, versionLine())
		return 0

	case "token":
		return runToken(cfg, args[1:], stdout, stderr)

	case "help":
		// `dinnerdecider help` is the same request as -h, and config.Load has
		// already printed the flag list for that spelling. Point at it.
		fmt.Fprintln(stdout, "usage: dinnerdecider [flags] | dinnerdecider token <show|rotate|forget> | dinnerdecider version")
		fmt.Fprintln(stdout, "run `dinnerdecider -h` for the full flag list")
		return 0

	default:
		fmt.Fprintf(stderr, "dinnerdecider: unknown command %q\n", args[0])
		fmt.Fprintln(stderr, "usage: dinnerdecider [flags] | dinnerdecider token <show|rotate|forget> | dinnerdecider version")
		return 2
	}
}

func versionLine() string {
	v := version
	if v == "" {
		v = "dev"
	}
	line := "dinnerdecider " + v
	if commit != "" {
		line += " (" + commit + ")"
	}
	if buildDate != "" {
		line += " built " + buildDate
	}
	return line
}

// runToken implements the pairing-token verbs.
//
// These exist because the token is the only credential, it is generated on
// first boot, and a headless box gives an operator nowhere to read it from. The
// state directory is the one thing they need to already know, and it is the
// same flag the service takes.
func runToken(cfg config.Config, args []string, stdout, stderr io.Writer) int {
	store := auth.NewTokenStore(cfg.StateDir)

	verb := "show"
	if len(args) > 0 {
		verb = args[0]
	}

	switch verb {
	case "show":
		token, err := store.Show()
		if err != nil {
			fmt.Fprintf(stderr, "dinnerdecider: no pairing token in %s: %v\n", store.Dir(), err)
			fmt.Fprintln(stderr, "one is created the first time the service starts in token mode")
			return 1
		}
		fmt.Fprintln(stdout, token)
		return 0

	case "rotate":
		token, err := store.Rotate()
		if err != nil {
			fmt.Fprintf(stderr, "dinnerdecider: could not rotate the pairing token: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, token)
		// Rotation silently unpairs every browser. An operator who did it by
		// accident should find out here, not from a support call.
		fmt.Fprintln(stderr, "every previously paired browser must pair again with this token")
		return 0

	case "forget":
		if err := store.Forget(); err != nil {
			fmt.Fprintf(stderr, "dinnerdecider: could not delete the pairing token: %v\n", err)
			return 1
		}
		fmt.Fprintln(stderr, "pairing token deleted; a new one is generated on the next start")
		return 0

	default:
		fmt.Fprintf(stderr, "dinnerdecider: unknown token command %q\n", verb)
		fmt.Fprintln(stderr, "usage: dinnerdecider token <show|rotate|forget>")
		return 2
	}
}
