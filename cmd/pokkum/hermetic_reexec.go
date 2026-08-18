package main

import (
	"context"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunexec"
)

// newHermeticReexecCommand registers the hidden re-exec helper
// --hermetic-mount-isolation depends on. It is not a user-facing command —
// Hidden: true keeps it out of `pokkum --help` and `pokkum help` — and is
// never invoked directly by a person; bunexec.Compiler retargets a hermetic
// build subprocess's own argv to run this instead of the real command,
// already inside the fresh CLONE_NEWNS mount namespace that subprocess was
// about to run in (see internal/adapters/bunexec/hermetic_reexec_linux.go's
// applyHermeticMountIsolation for the full mechanism and why a reexec is
// necessary at all).
//
// ctx/logger parameters are accepted only to match every other newXCommand
// constructor's signature in this file set — RunHermeticReexec needs
// neither: it reads its one input (the real target command) from
// bunexec.HermeticReexecEnvVar, and on success replaces this process's
// image entirely via syscall.Exec, so there is nothing left to log or
// cancel afterward.
func newHermeticReexecCommand(_ context.Context, _ *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use:          "__hermetic-reexec",
		Hidden:       true,
		Short:        "internal: re-exec helper for --hermetic-mount-isolation (not for direct use)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return bunexec.RunHermeticReexec()
		},
	}
}
