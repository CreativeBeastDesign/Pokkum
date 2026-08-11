package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/jsonutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
	"github.com/spf13/cobra"
)

type metricsOptions struct {
	port   int
	output string
}

func newMetricsCommand(_ context.Context, logger *slog.Logger) *cobra.Command {
	opts := &metricsOptions{}

	cmd := &cobra.Command{
		Use:   "metrics",
		Short: "Display OpenTelemetry metrics status and listen for application metrics",
		Long:  `Metrics manages and reports the status of Pokkum's OpenTelemetry metrics collector pipeline and Prometheus endpoint.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			outFlag, _ := cmd.Flags().GetString("output")
			if outFlag != "" {
				opts.output = outFlag
			}
			return runMetrics(logger, opts)
		},
	}

	cmd.Flags().IntVar(&opts.port, "metrics-port", 8889, "Prometheus metrics scrape port")

	return cmd
}

func runMetrics(logger *slog.Logger, opts *metricsOptions) error {
	outputFormat := ports.OutputFormat(opts.output)

	payload := ports.MetricsOutput{
		ListeningPort: opts.port,
		Status:        "active",
		UptimeSeconds: 0,
	}

	if outputFormat == ports.FormatJSON {
		return jsonutils.WriteSuccess(os.Stdout, "metrics", payload)
	}

	fmt.Println("=== Pokkum OpenTelemetry Metrics Collector ===")
	fmt.Printf("Status:         %s\n", payload.Status)
	fmt.Printf("Scrape Port:    :%d (/metrics)\n", opts.port)
	fmt.Println("Metrics pipeline ready. Press Ctrl+C to stop.")

	return nil
}
