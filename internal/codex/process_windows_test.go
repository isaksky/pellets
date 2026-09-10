package codex

import "os/exec"

// Windows jobs contain children regardless of console or process group changes.
func detachTestDescendant(cmd *exec.Cmd) {}
