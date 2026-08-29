// Command otata installs iOS builds on your phone over your own network.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jonathan-lor/otata/internal/app"
	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/config"
	"github.com/jonathan-lor/otata/internal/transport"
	"github.com/jonathan-lor/otata/internal/version"
)

const usage = `otata installs iOS builds on your phone over your own network

  otata publish [--config Debug] [--scheme S] [--slug NAME] [--artifact PATH] [--builder archive]
  otata list                    what is published
  otata status                  everything, in one call, changing nothing
  otata doctor [--fix]          verify everything; --fix repairs what it can first
  otata forget <slug>           drop one app
  otata serve                   run the file server in the foreground
  otata start | stop | restart  server lifecycle
  otata autostart on|off        run the server under launchd
  otata transport use <name>    tailscale | manual (--base-url, --keep-prefix, --visibility)

Options:
  --json                        machine-readable output, for an agent to parse
  --version                     print the version

'otata <command> --help' shows a command's flags.

Environment: OTATA_ROOT (default ~/.otata), OTATA_PORT, OTATA_PATH, NO_COLOR.
`

func main() { os.Exit(run()) }

func run() int {
	argv := globalFlags(os.Args[1:])
	if len(argv) < 1 {
		fmt.Print(usage)
		return 0
	}
	command := argv[0]
	args := argv[1:]

	switch command {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	case "version", "--version", "-V":
		fmt.Println("otata " + version.String())
		return 0
	}

	// Commands that take no flags still answer --help, and refuse anything else
	switch command {
	case "list", "ls", "status", "serve", "start", "stop", "restart":
		if wantsHelp(args) {
			fmt.Printf("usage: otata %s\n", command)
			return 0
		}
		if len(args) > 0 {
			return cli.EmitError(command, usageError("otata %s takes no arguments (got %q)", command, args[0]))
		}
	case "forget":
		if wantsHelp(args) {
			fmt.Println("usage: otata forget <slug>")
			return 0
		}
		if len(args) != 1 {
			return cli.EmitError(command, usageError("usage: otata forget <slug>"))
		}
	case "autostart":
		if wantsHelp(args) {
			fmt.Println("usage: otata autostart on|off")
			return 0
		}
		if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
			return cli.EmitError(command, usageError("usage: otata autostart on|off"))
		}
	}

	a, err := app.Open()
	if err != nil {
		return cli.EmitError(command, err)
	}

	switch command {
	case "publish":
		return publish(a, args)
	case "list", "ls":
		res, err := a.List()
		if err != nil {
			return cli.EmitError(command, err)
		}
		cli.Emit(command, res)
	case "status":
		return emitStatus(a, command)
	case "doctor":
		return doctor(a, args)
	case "forget":
		res, err := a.Forget(args[0])
		if err != nil {
			return cli.EmitError(command, err)
		}
		cli.Emit(command, res)
	case "serve":
		if err := a.Serve(); err != nil {
			return cli.EmitError(command, err)
		}
	case "start":
		if err := a.StartServer(); err != nil {
			return cli.EmitError(command, err)
		}
		return emitStatus(a, command)
	case "stop":
		if err := a.StopServer(); err != nil {
			return cli.EmitError(command, err)
		}
		return emitStatus(a, command)
	case "restart":
		if err := a.StopServer(); err != nil {
			return cli.EmitError(command, err)
		}
		if err := a.StartServer(); err != nil {
			return cli.EmitError(command, err)
		}
		return emitStatus(a, command)
	case "autostart":
		var err error
		if args[0] == "on" {
			err = a.EnableAutostart()
		} else {
			err = a.DisableAutostart()
		}
		if err != nil {
			return cli.EmitError(command, err)
		}
		return emitStatus(a, command)
	case "transport":
		return transportCmd(a, args)
	default:
		return cli.EmitError(command, usageError("unknown command %q", command).
			WithHint("try 'otata help'"))
	}
	return 0
}

// usageError is a failure in how the command was called, as opposed to what it tried to do.
// cli.EmitError exits 2 for these so a calling agent can tell a typo from a failure without parsing.
func usageError(format string, args ...any) *cli.Failure {
	return cli.Failf(cli.CodeInvalidArgs, format, args...)
}

// emitStatus reports the state a command left behind. All lifecycle commands
// answer with what `otata status` would say, so the caller sees the
// resulting state instead of an acknowledgment.
func emitStatus(a *app.App, command string) int {
	res, err := a.Status()
	if err != nil {
		return cli.EmitError(command, err)
	}
	cli.Emit(command, res)
	return 0
}

func wantsHelp(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

// parseFlags runs a FlagSet over args.
// --help prints flags to stdout and exits 0,
// and positional arguments are refused because nothing here takes any.
func parseFlags(fs *flag.FlagSet, command, synopsis string, args []string) (exit int, done bool) {
	fs.SetOutput(io.Discard) // we print usage ourselves
	err := fs.Parse(args)
	if err == flag.ErrHelp {
		fmt.Printf("usage: otata %s\n\n", synopsis)
		fs.VisitAll(func(f *flag.Flag) {
			name, usage := flag.UnquoteUsage(f)
			if name != "" {
				name = " " + name
			}
			fmt.Printf("  --%s%s\n        %s\n", f.Name, name, usage)
		})
		return 0, true
	}
	if err != nil {
		// The flag package reports "-config" whichever spelling was typed; the
		// docs and help say "--config", so the error should too.
		return cli.EmitError(command, usageError("%s", strings.Replace(err.Error(), ": -", ": --", 1))), true
	}
	if rest := fs.Args(); len(rest) > 0 {
		return cli.EmitError(command, usageError("otata %s takes no positional arguments (got %q)", command, rest[0])), true
	}
	return 0, false
}

// globalFlags applies the flags that belong to no one command and returns the rest.
// --json is accepted anywhere so callers don't need to know which position a flag parser expects.
func globalFlags(argv []string) []string {
	rest := argv[:0:0]
	for _, arg := range argv {
		if arg == "--json" {
			cli.SetJSON(true)
			continue
		}
		rest = append(rest, arg)
	}
	return rest
}

func publish(a *app.App, args []string) int {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	var opts app.PublishOptions
	fs.StringVar(&opts.Config, "config", "", "build configuration (default "+app.DefaultConfig+")")
	fs.StringVar(&opts.Scheme, "scheme", "", "scheme to build")
	fs.StringVar(&opts.Slug, "slug", "", "publish under this name")
	fs.StringVar(&opts.Artifact, "artifact", "", "publish an already-built .ipa")
	fs.StringVar(&opts.Builder, "builder", "", "build (default, incremental) or archive")
	if exit, done := parseFlags(fs, "publish",
		"publish [--config Debug] [--scheme S] [--slug NAME] [--artifact PATH] [--builder archive]", args); done {
		return exit
	}

	// Progress goes to stderr so it can never corrupt the JSON an agent parses on stdout,
	// and so `otata publish > out` still shows it.
	progress := func(line string) { fmt.Fprintf(os.Stderr, "    %s\n", line) }

	res, err := a.Publish(opts, progress)
	if err != nil {
		return cli.EmitError("publish", err)
	}
	cli.Emit("publish", res)
	return 0
}

func doctor(a *app.App, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fix := fs.Bool("fix", false, "repair what can be repaired before verifying")
	if exit, done := parseFlags(fs, "doctor", "doctor [--fix]", args); done {
		return exit
	}
	res, err := a.Doctor(*fix)
	if err != nil {
		return cli.EmitError("doctor", err)
	}
	if !res.Healthy {
		return cli.EmitFailure("doctor", res, res.Failure())
	}
	cli.Emit("doctor", res)
	return 0
}

const transportSynopsis = "transport use <tailscale|manual> [--base-url URL] [--keep-prefix] [--visibility private|public]"

func transportCmd(a *app.App, args []string) int {
	if wantsHelp(args) && (len(args) < 2 || args[0] != "use" || args[1] == "-h" || args[1] == "--help") {
		fmt.Println("usage: otata " + transportSynopsis)
		return 0
	}
	if len(args) < 2 || args[0] != "use" {
		return cli.EmitError("transport", usageError("usage: otata "+transportSynopsis))
	}
	name := args[1]
	fs := flag.NewFlagSet("transport", flag.ContinueOnError)
	baseURL := fs.String("base-url", "", "base URL your proxy serves (manual only)")
	keepPrefix := fs.Bool("keep-prefix", false,
		"your proxy forwards the base URL's path unchanged instead of stripping it (manual only)")
	visibility := fs.String("visibility", "private", "private or public (manual only)")
	if exit, done := parseFlags(fs, "transport", transportSynopsis, args[2:]); done {
		return exit
	}

	// Everything gets validated before anything is changed.
	var manual *config.Manual
	switch name {
	case "tailscale":
		// Selection is where tailscale proves it can serve. Failing here will name
		// the actual obstacle (logged out, HTTPS certificates disabled), whereas
		// failing at the first publish would surface whatever `tailscale serve` prints.
		if err := transport.NewTailscale(a.Config.ServePath).Verify(); err != nil {
			return cli.EmitError("transport", cli.Failf(cli.CodeTransportDown, "%v", err))
		}
	case "manual":
		if *baseURL == "" {
			return cli.EmitError("transport", usageError("manual transport needs --base-url"))
		}
		if err := transport.ValidateBaseURL(*baseURL); err != nil {
			return cli.EmitError("transport", usageError("%v", err))
		}
		vis, err := transport.ParseVisibility(*visibility)
		if err != nil {
			return cli.EmitError("transport", usageError("%v", err))
		}
		manual = &config.Manual{BaseURL: *baseURL, KeepPrefix: *keepPrefix, Visibility: string(vis)}
	default:
		return cli.EmitError("transport", usageError("unknown transport %q", name).
			WithHint("tailscale or manual"))
	}

	// The server strips whatever prefix the transport forwards, and reads that
	// once at startup, so if this change moves it, the server is restarted
	// below rather than left serving the old contract.
	previousPrefix := a.IncomingPrefix()

	// Tear down the transport being replaced, or its route stays wired to our
	// port after we stop using it. Teardown is scoped to what otata added.
	if previous := a.SelectedTransport(); previous != nil && previous.Name() != name {
		if err := previous.Teardown(); err != nil {
			fmt.Fprintf(os.Stderr, "    warning: could not tear down %s: %v\n", previous.Name(), err)
		}
	}
	a.Config.Transport = name
	if manual != nil {
		a.Config.Manual = manual
	}

	// Persist what was on disk plus this change, so an environment override for
	// this one invocation does not become permanent.
	onDisk, err := config.LoadFile(a.Root)
	if err != nil {
		return cli.EmitError("transport", cli.Failf(cli.CodeInternal, "%v", err))
	}
	onDisk.Transport = a.Config.Transport
	onDisk.Manual = a.Config.Manual
	if err := config.Save(a.Root, onDisk); err != nil {
		return cli.EmitError("transport", cli.Failf(cli.CodeInternal, "%v", err))
	}
	if a.IncomingPrefix() != previousPrefix && a.ServerRunning() {
		if err := a.StopServer(); err != nil {
			return cli.EmitError("transport", err)
		}
		if a.AutostartEnabled() {
			if err := a.StartServer(); err != nil {
				return cli.EmitError("transport", err)
			}
		} else {
			// A foreground `otata serve` was stopped so it cannot keep serving the old contract.
			// Only its own terminal can restart it, and a stopped server is a safe state.
			fmt.Fprintln(os.Stderr, "    the incoming prefix changed; run 'otata serve' again to serve it")
		}
	}
	// Manifests embed the base URL, so switching transports invalidates every published app until something regenerates them.
	if tr, err := a.Transport(); err == nil {
		if baseURL, err := tr.Ensure(a.Config.Port); err == nil {
			if err := a.Reindex(baseURL); err != nil {
				return cli.EmitError("transport",
					cli.Failf(cli.CodeInternal, "transport saved but pages could not be regenerated: %v", err))
			}
		}
	}
	return emitStatus(a, "transport")
}
