package sessions

// HarnessLifecycleStatus is the sessions-domain view of a harness process
// lifecycle. Transport and persistence adapters project these values into
// their existing public models.
type HarnessLifecycleStatus string

const (
	HarnessNaming         HarnessLifecycleStatus = "naming"
	HarnessQueued         HarnessLifecycleStatus = "queued"
	HarnessPreparing      HarnessLifecycleStatus = "preparing"
	HarnessRunning        HarnessLifecycleStatus = "running"
	HarnessCancelling     HarnessLifecycleStatus = "cancelling"
	HarnessCancelled      HarnessLifecycleStatus = "cancelled"
	HarnessCompleted      HarnessLifecycleStatus = "completed"
	HarnessFailed         HarnessLifecycleStatus = "failed"
	HarnessLost           HarnessLifecycleStatus = "lost"
	HarnessRestoring      HarnessLifecycleStatus = "restoring"
	HarnessRecoveryFailed HarnessLifecycleStatus = "recovery_failed"
	HarnessExpired        HarnessLifecycleStatus = "expired"
)

const ProcessExitErrorReason = "Process exited with an error."

// HarnessOutcome is the terminal product outcome of a harness process exit.
type HarnessOutcome struct {
	Status HarnessLifecycleStatus
	Reason string
}

// IsTerminal reports whether later process completions must leave the status
// unchanged.
func (status HarnessLifecycleStatus) IsTerminal() bool {
	switch status {
	case HarnessCancelled, HarnessCompleted, HarnessFailed, HarnessLost, HarnessExpired, HarnessRecoveryFailed:
		return true
	default:
		return false
	}
}

// RequestHarnessCancellation records cancellation intent without reopening a
// harness that has already reached a terminal outcome.
func RequestHarnessCancellation(current HarnessLifecycleStatus) HarnessLifecycleStatus {
	if current.IsTerminal() {
		return current
	}
	return HarnessCancelling
}

// ResolveHarnessProcessExit applies cancellation precedence to a process
// completion. The boolean is false when the current status is already terminal
// and the duplicate completion must be ignored.
func ResolveHarnessProcessExit(current HarnessLifecycleStatus, exitCode int) (HarnessOutcome, bool) {
	if current.IsTerminal() {
		return HarnessOutcome{Status: current}, false
	}
	if current == HarnessCancelling {
		return HarnessOutcome{Status: HarnessCancelled}, true
	}
	if current == HarnessRestoring {
		return HarnessOutcome{Status: HarnessRecoveryFailed, Reason: "Agent exited before its saved conversation could be restored."}, true
	}
	if exitCode == 0 {
		return HarnessOutcome{Status: HarnessCompleted}, true
	}
	return HarnessOutcome{Status: HarnessFailed, Reason: ProcessExitErrorReason}, true
}
