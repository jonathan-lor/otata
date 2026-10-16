// Command otata installs iOS and Android builds on your phone over your own network.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jonathan-lor/otata/internal/app"
	"github.com/jonathan-lor/otata/internal/artifact"
	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/version"
)

const usage = `otata installs iOS and Android builds on your phone over your own network

  otata publish --platform ios [--config Debug] [--scheme S] [--slug NAME] [--builder archive]
  otata publish --platform android [--config Debug] [--module M] [--flavor F] [--slug NAME]
  otata publish --artifact PATH [--slug NAME]
                                publish an already-built payload
  otata list                    what is published
  otata status                  the status of everything
  otata doctor [--fix]          verify everything; --fix repairs what it can first
  otata forget <slug>           drop a specified app
  otata serve                   run the file server in the foreground
  otata start | stop | restart  server lifecycle
  otata autostart on|off        run the server at login (launchd or systemd --user)
  otata transport use <name>    tailscale (default) | manual
  otata transport login         enroll otata in your tailnet or retry startup
  otata version                 print the version
  otata help                    this summary

Options:
  --json                        machine-readable output

'otata <command> --help' shows a command's flags.

Environment: OTATA_ROOT (default ~/.otata), OTATA_PORT, NO_COLOR.
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

// usageError is a failure in how the command was called, which exits 2.
func usageError(format string, args ...any) *cli.Failure {
	return cli.Failf(cli.CodeInvalidArgs, format, args...)
}

// emitStatus reports the status a command left behind.
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

// parseFlags parses args. --help prints the flags, and positional arguments
// are refused.
func parseFlags(fs *flag.FlagSet, command, summary string, args []string) (exit int, done bool) {
	fs.SetOutput(io.Discard) // we print usage ourselves
	err := fs.Parse(args)
	if err == flag.ErrHelp {
		fmt.Printf("usage: otata %s\n\n", summary)
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
		// The flag package says -config whatever was typed. Match the docs' --config.
		return cli.EmitError(command, usageError("%s", strings.Replace(err.Error(), ": -", ": --", 1))), true
	}
	if rest := fs.Args(); len(rest) > 0 {
		return cli.EmitError(command, usageError("otata %s takes no positional arguments (got %q)", command, rest[0])), true
	}
	return 0, false
}

// globalFlags pulls out --json, which is accepted anywhere, and returns the rest.
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

// progress prints a command's progress to stderr, keeping stdout clean for JSON.
func progress(line string) { fmt.Fprintf(os.Stderr, "    %s\n", line) }

const publishSummary = "publish --platform ios [--config Debug] [--scheme S] [--slug NAME] [--builder archive]\n" +
	"       otata publish --platform android [--config Debug] [--module M] [--flavor F] [--slug NAME]\n" +
	"       otata publish --artifact PATH [--slug NAME]"

func publish(a *app.App, args []string) int {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	var opts app.PublishOptions
	var platform string
	fs.StringVar(&platform, "platform", "", "what to build for: ios or android (required; --artifact reads it off the file)")
	fs.StringVar(&opts.Config, "config", "", "build configuration (default "+app.DefaultConfig+")")
	fs.StringVar(&opts.Scheme, "scheme", "", "Xcode scheme to build (iOS)")
	fs.StringVar(&opts.Module, "module", "", "Gradle module to build, as :app (Android)")
	fs.StringVar(&opts.Flavor, "flavor", "", "Gradle product flavor to build (Android)")
	fs.StringVar(&opts.Slug, "slug", "", "publish under this name")
	fs.StringVar(&opts.Artifact, "artifact", "", "publish an already-built .ipa or .apk; its platform is the file's")
	fs.StringVar(&opts.Builder, "builder", "", "build (default, incremental) or archive")
	if exit, done := parseFlags(fs, "publish", publishSummary, args); done {
		return exit
	}
	// Publish validates the platform, for every caller.
	opts.Platform = artifact.Platform(platform)

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

const transportSummary = "transport use <tailscale|manual> [--hostname NAME] [--base-url URL] [--keep-prefix] | transport login"

func transportCmd(a *app.App, args []string) int {
	if len(args) == 1 && args[0] == "login" {
		if err := a.LoginTransport(); err != nil {
			return cli.EmitError("transport", err)
		}
		res, err := a.Status()
		if err != nil {
			return cli.EmitError("transport", err)
		}
		cli.Emit("transport", app.TransportLoginResult{StatusResult: res})
		return 0
	}
	if wantsHelp(args) && (len(args) < 2 || args[0] != "use" || args[1] == "-h" || args[1] == "--help") {
		fmt.Println("usage: otata " + transportSummary)
		return 0
	}
	if len(args) < 2 || args[0] != "use" {
		return cli.EmitError("transport", usageError("usage: otata "+transportSummary))
	}
	name := args[1]
	fs := flag.NewFlagSet("transport", flag.ContinueOnError)
	baseURL := fs.String("base-url", "", "base URL your proxy serves (manual only)")
	hostname := fs.String("hostname", "", "otata device hostname (tailscale only; defaults to otata)")
	keepPrefix := fs.Bool("keep-prefix", false,
		"your proxy forwards the base URL's path unchanged instead of stripping it (manual only)")
	if exit, done := parseFlags(fs, "transport", transportSummary, args[2:]); done {
		return exit
	}

	sel := app.TransportSelection{Name: name, BaseURL: *baseURL, KeepPrefix: *keepPrefix, Hostname: *hostname}
	if err := a.UseTransport(sel, progress); err != nil {
		return cli.EmitError("transport", err)
	}
	return emitStatus(a, "transport")
}
