package bunexec

// buildCompileArgs builds the argv (excluding the "bun" program name itself)
// for stage two: `bun build --compile` run directly by Pokkum against the
// entrypoint the adapter emitted in stage one.
//
// Flag order is fixed and deterministic — build, compile, target, outfile,
// then the optional flags in a stable order, then the entrypoint positional
// last — so that the exact argv is a stable, testable value rather than a set.
//
//	bun build --compile --target=bun-linux-x64 --outfile=<outputPath> [--minify] [--sourcemap] <entrypointPath>
func buildCompileArgs(entrypointPath, outputPath, bunTarget string, minify, sourcemap bool) []string {
	args := []string{
		"build",
		"--compile",
		"--target=" + bunTarget,
		"--outfile=" + outputPath,
	}
	if minify {
		args = append(args, "--minify")
	}
	if sourcemap {
		args = append(args, "--sourcemap")
	}
	args = append(args, entrypointPath)
	return args
}
