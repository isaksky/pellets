package storage

// CheckpointOutcome is a concise durable read for one exact checkpoint
// generation. It deliberately excludes reviewer prose, assessment reasons,
// prompts, commands, snapshots and conversation content.
type CheckpointOutcome struct {
	ProjectID              int64
	CheckpointNumber       int64
	ImplementationRevision int64
	RunID                  int64
	Status                 string
	NeedsAttention         bool
	ReviewCompleted        bool
	Findings               int
	Assessed               int
	Triage                 string
	Dispositions           []CheckpointDisposition
}

// FindingNumber follows the distinct findings' durable reconciliation order.
// PelletNumber is a permanent reference even if the follow-up is later purged.
type CheckpointDisposition struct {
	FindingNumber int
	Decision      string
	PelletNumber  int64
	PelletPresent bool
}
