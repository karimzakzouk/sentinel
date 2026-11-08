package slot

// The decision points the modules fill. Each is filled by deterministic
// engines first; learned or LLM engines can be added as shadows and promoted
// only after replay and shadow runs show they do better.
const (
	// WatcherDetect turns a window of one series into a tail probability.
	WatcherDetect = "watcher.detect"
	// BrainClassify returns a Distribution over fault classes for an incident.
	BrainClassify = "brain.classify"
	// BrainLocalize returns a Ranking of objects that may be the root cause.
	BrainLocalize = "brain.localize"
	// BrainAttribute returns a Ranking of recent changes that may have caused it.
	BrainAttribute = "brain.attribute"
	// BrainDecide returns a Distribution over allowed actions, "do nothing"
	// and "escalate", derived from expected loss.
	BrainDecide = "brain.decide"
	// BrainVerify judges whether an executed action helped.
	BrainVerify = "brain.verify"
	// LabVerdict judges a candidate fix from paired Lab runs.
	LabVerdict = "lab.verdict"
	// FixerLocalize returns a Ranking of source locations for a code fault.
	FixerLocalize = "fixer.localize"
	// FixerGenerate proposes candidate patches.
	FixerGenerate = "fixer.generate"
	// FixerRank orders candidate patches before the Lab judges them.
	FixerRank = "fixer.rank"
)
