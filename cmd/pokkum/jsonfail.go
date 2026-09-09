package main

import (
	"os"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/jsonutils"
)

// failJSON writes a JSON error envelope to stdout and returns a failure the
// caller must propagate, so the process exits non-zero.
//
// It exists because `return jsonutils.WriteError(...)` reads like "return this
// error" and does the opposite. WriteError returns the *write* result — nil
// when the envelope was written successfully — so every call site shaped like
//
//	if outputFormat == ports.FormatJSON {
//	    return jsonutils.WriteError(os.Stdout, "cmd", "ERR_X", msg, "")
//	}
//	return fmt.Errorf("%s", msg)
//
// printed status:"error" and then exited 0, while the text branch immediately
// below it exited 1 on the identical input. Thirteen call sites across six
// commands had that shape; `pokkum adopt`, `pokkum explain` and `pokkum
// history` were confirmed by running them, and the rest are the same two lines.
//
// A `set -e` step or a `&&` chain reads only the exit status, so a caller
// following the documented contract saw a failure as a success with nothing to
// contradict it. See Lessons.md 2026-09-09 and mem:self_review_checklist row
// 73: an output-format flag selects a serialization and must never change
// whether the command succeeded.
//
// The returned error is silent (empty message) because the envelope on stdout
// is already the complete report. main.go's isSilentExit recognises it and
// exits 1 without logging a second line that would say less and would corrupt
// the JSON stream for anyone reading stdout and stderr together.
//
// If the envelope itself cannot be written, that write error is returned
// instead — it is a real, un-reported failure and must not be swallowed.
func failJSON(command, code, message, details string) error {
	if err := jsonutils.WriteError(os.Stdout, command, code, message, details); err != nil {
		return err
	}
	return errJSONCommandFailed
}

// errJSONCommandFailed is the silent failure failJSON returns once the error
// envelope has been written.
var errJSONCommandFailed = &silentExitError{}
