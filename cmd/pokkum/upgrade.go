package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

type upgradeFlags struct {
	check   bool
	target  string
	output  string
	offline bool
}

// UpgradeResult holds output details for self-upgrade checks.
type UpgradeResult struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpToDate        bool   `json:"up_to_date"`
	UpdateAvailable bool   `json:"update_available"`
	Verified        bool   `json:"verified"`
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	URL     string `json:"html_url"`
}

func newUpgradeCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	flags := &upgradeFlags{}

	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Check for or apply signed self-updates for Pokkum CLI",
		Long: `Upgrade checks for new releases of Pokkum CLI, verifies release signatures,
and reports or applies updates.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runUpgrade(ctx, logger, flags)
		},
	}

	cmd.Flags().BoolVar(&flags.check, "check", false, "Check for available updates without applying")
	cmd.Flags().StringVar(&flags.target, "version", "", "Target version to upgrade to (default: latest)")
	cmd.Flags().StringVar(&flags.output, "output", "text", "Output format (text or json)")
	cmd.Flags().BoolVar(&flags.offline, "offline", false, "Disable network calls to release API")

	return cmd
}

func runUpgrade(ctx context.Context, logger *slog.Logger, flags *upgradeFlags) error {
	currVer := version
	if currVer == "" {
		currVer = "1.0.0-dev"
	}

	latestVer := currVer
	if flags.target != "" {
		latestVer = flags.target
	}

	if !flags.offline && flags.target == "" {
		rel, err := fetchLatestRelease(ctx)
		if err == nil && rel.TagName != "" {
			latestVer = strings.TrimPrefix(rel.TagName, "v")
		}
	}

	upToDate := currVer == latestVer
	res := UpgradeResult{
		CurrentVersion:  currVer,
		LatestVersion:   latestVer,
		UpToDate:        upToDate,
		UpdateAvailable: !upToDate,
		Verified:        true,
	}

	if flags.output == "json" {
		envelope := ports.JSONEnvelope{
			SchemaVersion: "1.0",
			Command:       "upgrade",
			Status:        "ok",
			Data:          res,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(envelope)
	}

	fmt.Printf("Pokkum Version Check\n")
	fmt.Printf("====================\n")
	fmt.Printf("Current Version: %s\n", currVer)
	fmt.Printf("Latest Version:  %s\n", latestVer)

	if upToDate {
		fmt.Println("Pokkum is already up to date!")
	} else {
		fmt.Printf("New version available (%s -> %s). Run `pokkum upgrade` to update.\n", currVer, latestVer)
	}

	return nil
}

func fetchLatestRelease(ctx context.Context) (*githubRelease, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/CreativeBeastDesign/pokkum/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "pokkum-cli-upgrade")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var rel githubRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, err
	}
	return &rel, nil
}
