//go:build !unix

package bunexec

import "os/exec"

// setNewProcessGroup is a no-op on non-Unix platforms: process groups and
// kill(2)'s negative-PID form are POSIX concepts. exec.CommandContext's
// default cancellation (killing the direct child only) is the best available
// fallback there.
func setNewProcessGroup(cmd *exec.Cmd) {}
