package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type Mode string

const (
	ModeTUI     Mode = "tui"
	ModeExact   Mode = "exact"
	ModeNatural Mode = "natural"
)

type Request struct {
	Mode             Mode
	Command          string
	Name             string
	NaturalText      string
	JSON             bool
	AllowExternal    bool
	MaxExternalCalls int
}

// Parse recognizes the deliberately small automation surface. Any other text
// is preserved as one natural-language command for the interpreter.
func Parse(args []string) (Request, error) {
	if len(args) == 0 {
		return Request{Mode: ModeTUI}, nil
	}
	if args[0] == "--json" {
		if len(args) == 1 {
			return Request{}, errors.New("--json requires an exact command")
		}
		req, err := Parse(args[1:])
		if err == nil && req.Mode != ModeExact {
			return Request{}, errors.New("--json is only available for exact commands")
		}
		req.JSON = true
		return req, err
	}
	switch args[0] {
	case "setup":
		return parseNoArgs("setup", args[1:])
	case "auth":
		return parseNamed("auth", args[1:])
	case "version":
		return parseNoArgs("version", args[1:])
	case "sync":
		return parseSync(args[1:])
	case "doctor":
		return parseNoArgs("doctor", args[1:])
	case "schedule":
		return parseSchedule(args[1:])
	case "eval":
		return parseEval(args[1:])
	default:
		return Request{Mode: ModeNatural, NaturalText: strings.Join(args, " ")}, nil
	}
}

func parseNamed(command string, args []string) (Request, error) {
	fs := newFlagSet(command)
	if err := fs.Parse(args); err != nil {
		return Request{}, err
	}
	if fs.NArg() != 1 {
		return Request{}, fmt.Errorf("%s: expected exactly one account name", command)
	}
	return Request{Mode: ModeExact, Command: command, Name: fs.Arg(0)}, nil
}

func parseSync(args []string) (Request, error) {
	fs := newFlagSet("sync")
	jsonOut := fs.Bool("json", false, "write JSON")
	if err := fs.Parse(args); err != nil {
		return Request{}, err
	}
	if fs.NArg() != 0 {
		return Request{}, fmt.Errorf("sync: unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return Request{Mode: ModeExact, Command: "sync", JSON: *jsonOut}, nil
}

func parseNoArgs(command string, args []string) (Request, error) {
	fs := newFlagSet(command)
	jsonOut := fs.Bool("json", false, "write JSON")
	if err := fs.Parse(args); err != nil {
		return Request{}, err
	}
	if fs.NArg() != 0 {
		return Request{}, fmt.Errorf("%s: unexpected arguments: %s", command, strings.Join(fs.Args(), " "))
	}
	return Request{Mode: ModeExact, Command: command, JSON: *jsonOut}, nil
}

func parseSchedule(args []string) (Request, error) {
	if len(args) == 0 || args[0] != "run" {
		return Request{}, errors.New("schedule: expected 'run <name>'")
	}
	fs := newFlagSet("schedule run")
	jsonOut := fs.Bool("json", false, "write JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return Request{}, err
	}
	if fs.NArg() != 1 {
		return Request{}, errors.New("schedule run: expected exactly one schedule name")
	}
	return Request{Mode: ModeExact, Command: "schedule run", Name: fs.Arg(0), JSON: *jsonOut}, nil
}

func parseEval(args []string) (Request, error) {
	if len(args) == 0 || args[0] != "run" {
		return Request{}, errors.New("eval: expected 'run'")
	}
	fs := newFlagSet("eval run")
	jsonOut := fs.Bool("json", false, "write JSON")
	allow := fs.Bool("allow-external", false, "allow capped external calls")
	cap := fs.Int("max-external-calls", 0, "positive external case/call cap")
	if err := fs.Parse(args[1:]); err != nil {
		return Request{}, err
	}
	if fs.NArg() != 0 {
		return Request{}, fmt.Errorf("eval run: unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if *allow && *cap <= 0 {
		return Request{}, errors.New("eval run: --allow-external requires a positive --max-external-calls")
	}
	if !*allow && *cap != 0 {
		return Request{}, errors.New("eval run: --max-external-calls requires --allow-external")
	}
	return Request{Mode: ModeExact, Command: "eval run", JSON: *jsonOut, AllowExternal: *allow, MaxExternalCalls: *cap}, nil
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func (r Request) ExternalCapNotice() string {
	if !r.AllowExternal {
		return "external calls disabled"
	}
	return "external calls enabled; maximum " + strconv.Itoa(r.MaxExternalCalls)
}
