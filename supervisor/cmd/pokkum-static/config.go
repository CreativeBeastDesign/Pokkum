package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
)

// The image runtime contract, mirrored from internal/ports/packager.go.
//
// These literals are deliberately duplicated rather than imported, for the same
// reason pokkum-init duplicates them (see supervisor/cmd/pokkum-init/config.go):
// pokkum-static is embedded into the pokkum CLI with go:embed, so importing
// internal/ports would drag go-containerregistry's v1 types into a program whose
// entire job is to serve static files. The constants below must stay in
// lockstep with ports.EnvPort, ports.EnvStaticRoots, ports.EnvProbePort,
// ports.DefaultPort, ports.DefaultProbePort, ports.AppClientDirPrefix and
// ports.AppPrerenderedDirPrefix.
const (
	envPort       = "PORT"
	envProbePort  = "POKKUM_PROBE_PORT"
	envLogLevel   = "POKKUM_LOG_LEVEL"
	envStaticRoot = "POKKUM_STATIC_ROOTS"

	defaultPort = 3000
	// defaultProbePort mirrors ports.DefaultProbePort, deliberately distinct
	// from the serve port so probes keep answering independently of traffic.
	defaultProbePort = 8081
)

// StaticServerRoots returns the default serve roots, mirroring
// ports.AppClientDirPrefix and ports.AppPrerenderedDirPrefix. A function rather
// than a constant so the embedded binary never has to reference ports directly.
func StaticServerRoots() []string {
	return []string{"/app/client", "/app/prerendered"}
}

// version is stamped at link time with -ldflags "-X main.version=...". It is
// reported by -version and is what ports.StaticServerProvider.Version describes.
var version = "dev"

// errVersionRequested is returned by parseConfig when -version was handled and
// the process should exit successfully without starting anything.
var errVersionRequested = errors.New("version requested")

// Config is the fully resolved static server configuration. Every field is
// populated; there are no zero-means-default fields left by the time
// parseConfig returns.
type Config struct {
	// Port is the listen port for static content. Written from PORT (envPort).
	Port int

	// ProbePort is where /healthz and /readyz are served. Written from
	// POKKUM_PROBE_PORT (envProbePort).
	ProbePort int

	// Roots are the read-only directories served, in lookup order. The first
	// root that contains the requested path wins. Written from
	// POKKUM_STATIC_ROOTS (envStaticRoot).
	Roots []string

	// LogLevel is the minimum level for the stderr text handler.
	LogLevel slog.Level
}

// parseConfig resolves configuration from the environment and then applies flag
// overrides, giving the precedence the project requires: flag > env > default.
//
// getenv is injected rather than read through os.Getenv so that configuration
// can be tested without mutating process state.
//
// Environment parsing never fails: an unparseable value degrades to the default
// and records a warning, because a bad env value in a production container must
// not prevent the static server from starting. Flags are the opposite — a bad
// flag is an error a human typed at a terminal.
func parseConfig(args []string, getenv func(string) string, out io.Writer) (Config, []string, error) {
	cfg := Config{
		Port:      defaultPort,
		ProbePort: defaultProbePort,
		Roots:     StaticServerRoots(),
		LogLevel:  slog.LevelInfo,
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
	if raw := getenv(envStaticRoot); raw != "" {
		var roots []string
		for _, part := range strings.Split(raw, string(filepath.ListSeparator)) {
			if p := strings.TrimSpace(part); p != "" {
				roots = append(roots, p)
			}
		}
		if len(roots) > 0 {
			cfg.Roots = roots
		} else {
			warnf("ignoring empty %s=%q", envStaticRoot, raw)
		}
	}

	fs := flag.NewFlagSet("pokkum-static", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {
		fmt.Fprint(out, "usage: pokkum-static [flags]\n\n"+
			"Serves a prebuilt static site (SvelteKit client + prerendered pages)\n"+
			"as PID 1, with ETag, Range and Content-Encoding negotiation against\n"+
			".gz/.br/.zst sidecars.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	var (
		flagLevel = fs.String("log-level", cfg.LogLevel.String(), "log level: debug, info, warn or error (env "+envLogLevel+")")
		flagPort  = fs.Int("port", cfg.Port, "static content listen port (env "+envPort+")")
		flagProbe = fs.Int("probe-port", cfg.ProbePort, "port for /healthz and /readyz (env "+envProbePort+")")
		flagRoots = fs.String("roots", strings.Join(cfg.Roots, string(filepath.ListSeparator)), "read-only static roots, path-list separated (env "+envStaticRoot+")")
		flagVer   = fs.Bool("version", false, "print the static server version and exit")
	)
	if err := fs.Parse(args); err != nil {
		return cfg, warnings, err
	}
	if *flagVer {
		fmt.Fprintf(out, "pokkum-static %s\n", version)
		return cfg, warnings, errVersionRequested
	}

	lvl, err := parseLevel(*flagLevel)
	if err != nil {
		return cfg, warnings, fmt.Errorf("invalid -log-level %q", *flagLevel)
	}
	cfg.LogLevel = lvl
	cfg.Port = *flagPort
	cfg.ProbePort = *flagProbe

	if *flagRoots != "" {
		var roots []string
		for _, part := range strings.Split(*flagRoots, string(filepath.ListSeparator)) {
			if p := strings.TrimSpace(part); p != "" {
				roots = append(roots, p)
			}
		}
		if len(roots) > 0 {
			cfg.Roots = roots
		}
	}

	if err := cfg.validate(); err != nil {
		return cfg, warnings, err
	}
	if cfg.Port == cfg.ProbePort {
		warnf("serve port and probe port are both %d; the probe server will fail to bind", cfg.Port)
	}
	return cfg, warnings, nil
}

func (c Config) validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("serve port %d out of range", c.Port)
	}
	if c.ProbePort < 1 || c.ProbePort > 65535 {
		return fmt.Errorf("probe port %d out of range", c.ProbePort)
	}
	if len(c.Roots) == 0 {
		return errors.New("at least one static root is required")
	}
	for _, r := range c.Roots {
		if !filepath.IsAbs(r) {
			return fmt.Errorf("static root %q must be absolute", r)
		}
	}
	return nil
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
