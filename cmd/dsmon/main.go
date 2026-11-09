package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"dsmon/internal/config"
	"dsmon/internal/ipc"
	"dsmon/internal/store"
	"dsmon/internal/tui"
	"dsmon/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}

	switch args[0] {
	case "serve":
		return runServe(args[1:], stdout, stderr)
	case "tui":
		return runTUI(args[1:], stdout, stderr)
	case "settings":
		return runSettings(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "import":
		return runImport(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "dsmon", version.Current)
		return 0
	case "help", "--help", "-h":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		usage(stderr)
		return 2
	}
}

func runTUI(args []string, stdout, stderr io.Writer) int {
	ascii := false
	for _, arg := range args {
		switch arg {
		case "help", "--help", "-h":
			tuiUsage(stdout)
			return 0
		case "--ascii":
			ascii = true
		default:
			fmt.Fprintf(stderr, "dsmon tui: unknown option %q\n\n", arg)
			tuiUsage(stderr)
			return 2
		}
	}
	client := ipc.NewClient(config.RuntimeSocketPath())
	if err := tui.Run(client, ascii); err != nil {
		fmt.Fprintf(stderr, "dsmon tui: %v\n", err)
		return 1
	}
	return 0
}

func runSettings(args []string, stdout, stderr io.Writer) int {
	ascii := false
	for _, arg := range args {
		switch arg {
		case "help", "--help", "-h":
			settingsUsage(stdout)
			return 0
		case "--ascii":
			ascii = true
		default:
			fmt.Fprintf(stderr, "dsmon settings: unknown option %q\n\n", arg)
			settingsUsage(stderr)
			return 2
		}
	}
	client := ipc.NewClient(config.RuntimeSocketPath())
	if err := tui.RunSettings(client, ascii); err != nil {
		fmt.Fprintf(stderr, "dsmon settings: %v\n", err)
		return 1
	}
	return 0
}

func runImport(args []string, stdout, stderr io.Writer) int {
	targetPath := config.DatabasePath()
	sourcePath := ""
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--into":
			if index+1 >= len(args) {
				fmt.Fprintln(stderr, "dsmon import: --into requires a database path")
				importUsage(stderr)
				return 2
			}
			index++
			targetPath = args[index]
		case arg == "help" || arg == "--help" || arg == "-h":
			importUsage(stdout)
			return 0
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "dsmon import: unknown option %q\n\n", arg)
			importUsage(stderr)
			return 2
		case sourcePath == "":
			sourcePath = arg
		default:
			fmt.Fprintln(stderr, "dsmon import: expected exactly one source database")
			importUsage(stderr)
			return 2
		}
	}
	if sourcePath == "" {
		fmt.Fprintln(stderr, "dsmon import: missing source database")
		importUsage(stderr)
		return 2
	}

	result, err := store.ImportDatabase(context.Background(), targetPath, sourcePath)
	if err != nil {
		fmt.Fprintf(stderr, "dsmon import: %v\n", err)
		return 1
	}
	if result.Copied {
		fmt.Fprintf(stdout, "imported %s -> %s: copied %d rows, %d pricing revisions, %d balance samples\n",
			result.Source, result.Target, result.Inserted, result.Revisions, result.BalanceSamples)
	} else {
		fmt.Fprintf(stdout, "imported %s -> %s: inserted %d, skipped %d, %d pricing revisions, %d balance samples\n",
			result.Source, result.Target, result.Inserted, result.Skipped, result.Revisions, result.BalanceSamples)
	}
	return 0
}

func notImplemented(name string, stderr io.Writer) int {
	fmt.Fprintf(stderr, "dsmon %s: not implemented yet (see docs/PLAN.md)\n", name)
	return 1
}

func usage(writer io.Writer) {
	fmt.Fprint(writer, `dsmon - local API proxy and usage dashboard

usage:
  dsmon serve              run the proxy daemon (systemd --user)
  dsmon tui                open the btop-style dashboard
  dsmon settings           edit settings (bound to a Hyprland key)
  dsmon status [--json]    print daemon status
  dsmon import [--into <path>] <usage.db>
                           import a macOS DS-mon database
  dsmon version            print version
`)
}

func importUsage(writer io.Writer) {
	fmt.Fprint(writer, `usage: dsmon import [--into <path>] <usage.db>

Without --into the database is imported into the XDG data path
(~/.local/share/dsmon/usage.db). A missing target is copied; an existing
target is merged with timestamp/token deduplication.
`)
}

func serveUsage(writer io.Writer) {
	fmt.Fprint(writer, `usage: dsmon serve

Settings come from ~/.config/dsmon/config.toml (edit with 'dsmon settings').
The API key falls back to DSMON_API_KEY / DEEPSEEK_API_KEY, then the encrypted
config value, then the systemd credential directory. Keys are never logged or
written as plaintext.
`)
}

func tuiUsage(writer io.Writer) {
	fmt.Fprint(writer, `usage: dsmon tui [--ascii]

The dashboard talks to the running daemon over the local Unix socket.
--ascii forces the block-character fallback instead of Braille curves.
NO_COLOR=1 also enables the plain-text fallback.
`)
}

func settingsUsage(writer io.Writer) {
	fmt.Fprint(writer, `usage: dsmon settings [--ascii]

Edit the daemon configuration. The API key is encrypted with AES-GCM and is
never shown in plaintext. ctrl+s writes atomically, then asks the running daemon
to reload; if the daemon is offline the change applies on next start.

--ascii uses the same plain-text fallback as the dashboard.
`)
}
