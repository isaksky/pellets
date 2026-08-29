package domain

// LocalPath is a canonical slash-separated local path. Relative paths are
// interpreted from the selected Pellets database root; absolute paths are
// retained only when the Git location cannot be represented beneath it.
type LocalPath struct {
	Value    string
	Relative bool
}

// GitIdentity is the Git-reported identity of one checked-out worktree and
// its shared repository. GitCommonDir identifies the logical repository;
// WorkTreeRoot and GitDir together identify the workspace.
type GitIdentity struct {
	WorkTreeRoot string
	GitCommonDir string
	GitDir       string
}
