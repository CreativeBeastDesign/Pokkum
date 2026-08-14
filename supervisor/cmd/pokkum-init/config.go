package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// The image runtime contract, mirrored from internal/ports/packager.go.
//
// These literals are deliberately duplicated rather than imported. pokkum-init
// is embedded into the pokkum CLI with go:embed, so every byte of it ships
// inside another binary; importing internal/ports would drag
// go-containerregistry's v1 types (and their transitive dependencies) into a
// program whose entire job is to fork one child and count signals. The
// constants below must stay in lockstep with ports.EnvPort,
// ports.EnvProbePort, ports.EnvShutdownTimeout, ports.DefaultPort,
// ports.DefaultProbePort and ports.DefaultShutdownTimeout.
const (
	envPort            = "PORT"
	envProbePort       = "POKKUM_PROBE_PORT"
	envShutdownTimeout = "POKKUM_SHUTDOWN_TIMEOUT"
	envLogLevel        = "POKKUM_LOG_LEVEL"
	envRequiredEnv     = "POKKUM_REQUIRED_ENV"

	defaultPort      = 3000
	defaultProbePort = 8081

	// defaultShutdownTimeout mirrors adapter-node's SHUTDOWN_TIMEOUT. The
	// SvelteKit server's SIGTERM handler calls server.stop(true) but never
	// process.exit(), so this window is what stands between a well-behaved
	// drain and a container that hangs until the runtime's own timeout.
	defaultShutdownTimeout = 30 * time.Second
)

// version is stamped at link time with -ldflags "-X main.version=...". It is
// reported by -version and is what ports.SupervisorProvider.Version describes.
var version = "dev"

// errVersionRequested is returned by parseConfig when -version was handled and
// the process should exit successfully without starting anything.
var errVersionRequested = errors.New("version requested")

// Config is the fully resolved supervisor configuration. Every field is
// populated; there are no zero-means-default fields left by the time
// parseConfig returns.
type Config struct {
	// Command is the child argv. Command[0] is resolved against PATH if it is
	// not already a path. It is never empty.
	Command []string

	// Port is written to the child's PORT environment variable, overriding any
	// inherited value, so the Bun server binds where the image config, the
	// probe server and the generated Kubernetes manifests all expect.
	Port int

	// ProbePort is where the probe server listens. The supervisor does not
	// serve probes itself; it parses and carries the value so that the probe
	// server can be attached without re-deriving configuration. See the W8 seam
	// in main.go.
	ProbePort int

	// ShutdownTimeout is how long the child's process group gets between the
	// forwarded termination signal and SIGKILL.
	ShutdownTimeout time.Duration

	// LogLevel is the minimum level for the stderr text handler.
	LogLevel slog.Level

	// RequireEnv lists environment variable keys required to be present at runtime.
	RequireEnv []string
}

// parseConfig resolves configuration from the environment and then applies flag
// overrides, giving the precedence the project requires: flag > env > default.
//
// getenv is injected rather than read through os.Getenv so that configuration
// can be tested without mutating process state.
//
// Environment parsing never fails. An unparseable POKKUM_SHUTDOWN_TIMEOUT in a
// production image must not stop the container from starting; it degrades to
// the default and records a warning. The returned warnings are emitted by the
// caller once the logger exists, because the log level itself comes from this
// function. Flags are the opposite: they are typed by a human at a terminal, so
// a bad flag is an error.
func parseConfig(args []string, getenv func(string) string, out io.Writer) (Config, []string, error) {
	cfg := Config{
		Port:            defaultPort,
		ProbePort:       defaultProbePort,
		ShutdownTimeout: defaultShutdownTimeout,
		LogLevel:        slog.LevelInfo,
	}
	var warnings []string
	warnf := func(format string, a ...any) {
		warnings = append(warnings, fmt.Sprintf(format, a...))
	}

	if raw := getenv(envLogLevel); raw != "" {
		if lvl, err := parseLevel(raw); err != nil {
			warnf("ignoring invalid %s=%q, using %s", envLogLevel, raw, cfg.LogLevel)
		} else {
			cfg.LogLevel = lvl
		}
	}
	cfg.Port = envPortValue(getenv, envPort, cfg.Port, warnf)
	cfg.ProbePort = envPortValue(getenv, envProbePort, cfg.ProbePort, warnf)
	if raw := getenv(envShutdownTimeout); raw != "" {
		switch d, err := time.ParseDuration(raw); {
		case err != nil:
			warnf("ignoring unparseable %s=%q, using %s", envShutdownTimeout, raw, cfg.ShutdownTimeout)
		case d <= 0:
			warnf("ignoring non-positive %s=%q, using %s", envShutdownTimeout, raw, cfg.ShutdownTimeout)
		default:
			cfg.ShutdownTimeout = d
		}
	}
	if raw := getenv(envRequiredEnv); raw != "" {
		for _, key := range strings.Split(raw, ",") {
			if k := strings.TrimSpace(key); k != "" {
				cfg.RequireEnv = append(cfg.RequireEnv, k)
			}
		}
	}

	flagArgs, childArgs, sawSeparator := splitArgs(args)

	fs := flag.NewFlagSet("pokkum-init", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {
		fmt.Fprint(out, "usage: pokkum-init [flags] -- command [args...]\n\n"+
			"Supervises one child process as PID 1: forwards signals to its process\n"+
			"group, reaps orphans, and escalates to SIGKILL when the child overstays\n"+
			"the shutdown deadline.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	var (
		flagLevel      = fs.String("log-level", cfg.LogLevel.String(), "log level: debug, info, warn or error (env "+envLogLevel+")")
		flagPort       = fs.Int("port", cfg.Port, "port written to the child's PORT variable (env "+envPort+")")
		flagProbePort  = fs.Int("probe-port", cfg.ProbePort, "port for the liveness and readiness endpoints (env "+envProbePort+")")
		flagTimeout    = fs.Duration("shutdown-timeout", cfg.ShutdownTimeout, "grace period before SIGKILL (env "+envShutdownTimeout+")")
		flagRequireEnv = fs.String("require-env", strings.Join(cfg.RequireEnv, ","), "comma-separated list of required env vars (env "+envRequiredEnv+")")
		flagVersion    = fs.Bool("version", false, "print the supervisor version and exit")
	)
	if err := fs.Parse(flagArgs); err != nil {
		return cfg, warnings, err
	}
	if *flagVersion {
		fmt.Fprintf(out, "pokkum-init %s\n", version)
		return cfg, warnings, errVersionRequested
	}

	lvl, err := parseLevel(*flagLevel)
	if err != nil {
		return cfg, warnings, fmt.Errorf("invalid -log-level %q", *flagLevel)
	}
	cfg.LogLevel = lvl
	cfg.Port = *flagPort
	cfg.ProbePort = *flagProbePort
	cfg.ShutdownTimeout = *flagTimeout

	if *flagRequireEnv != "" {
		var reqs []string
		for _, key := range strings.Split(*flagRequireEnv, ",") {
			if k := strings.TrimSpace(key); k != "" {
				reqs = append(reqs, k)
			}
		}
		cfg.RequireEnv = reqs
	}

	// When "--" is present it is authoritative: everything before it is ours,
	// everything after it is the child's, and a child argument that looks like
	// a supervisor flag cannot be misread. Without it we fall back to the
	// residual arguments so that `pokkum-init /app/server` still works when a
	// human is debugging a container by hand.
	if !sawSeparator {
		childArgs = fs.Args()
	} else if fs.NArg() > 0 {
		return cfg, warnings, fmt.Errorf("unexpected argument %q before --", fs.Arg(0))
	}
	cfg.Command = childArgs

	if err := cfg.validate(getenv); err != nil {
		return cfg, warnings, err
	}
	if cfg.Port == cfg.ProbePort {
		warnf("application port and probe port are both %d; the probe server will fail to bind", cfg.Port)
	}
	return cfg, warnings, nil
}

func (c Config) validate(getenv func(string) string) error {
	if len(c.Command) == 0 {
		return errors.New("no command given; expected: pokkum-init [flags] -- command [args...]")
	}
	if c.Command[0] == "" {
		return errors.New("empty command")
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("application port %d out of range", c.Port)
	}
	if c.ProbePort < 1 || c.ProbePort > 65535 {
		return fmt.Errorf("probe port %d out of range", c.ProbePort)
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("shutdown timeout %s must be positive", c.ShutdownTimeout)
	}
	if len(c.RequireEnv) > 0 && getenv != nil {
		var missing []string
		for _, key := range c.RequireEnv {
			if getenv(key) == "" {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("runtime env contract violation: missing required environment variable(s): %s", strings.Join(missing, ", "))
		}
	}
	return nil
}

// splitArgs divides argv at the first bare "--". flagArgs is everything before
// it and childArgs everything after; a "--" inside childArgs is left alone
// because it belongs to the child.
func splitArgs(args []string) (flagArgs, childArgs []string, sawSeparator bool) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:], true
		}
	}
	return args, nil, false
}

func envPortValue(getenv func(string) string, key string, fallback int, warnf func(string, ...any)) int {
	raw := getenv(key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 || n > 65535 {
		warnf("ignoring invalid %s=%q, using %d", key, raw, fallback)
		return fallback
	}
	return n
}

func parseLevel(s string) (slog.Level, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(strings.TrimSpace(s))); err != nil {
		return slog.LevelInfo, err
	}
	return lvl, nil
}
