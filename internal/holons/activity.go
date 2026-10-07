package holons

import "github.com/holark-ai/holark/internal/protocol"

// AgentActivity folds lifecycle into observed activity only for display. A live
// process never establishes work, and a clean process exit is not a turn result.
func AgentActivity(a AgentSession) protocol.AgentActivity {
	switch Status(a.Status) {
	case StatusFailed:
		return protocol.ActivityFailed
	case StatusQueued, StatusNaming, StatusPreparing:
		return protocol.ActivityStarting
	case StatusLost:
		return protocol.ActivityUnknown
	case StatusCancelled, StatusCancelling, StatusCompleted, StatusExpired:
		return protocol.ActivityIdle
	}
	if a.Activity == "" {
		return protocol.ActivityUnknown
	}
	return a.Activity
}

func AggregateActivity(agents []AgentSession) protocol.AgentActivity {
	result, rank := protocol.ActivityIdle, 0
	priority := map[protocol.AgentActivity]int{protocol.ActivityIdle: 0, protocol.ActivityCompleted: 1, protocol.ActivityUnknown: 2, protocol.ActivityStarting: 3, protocol.ActivityFailed: 4, protocol.ActivityWorking: 5, protocol.ActivityNeedsInput: 6}
	for _, a := range agents {
		if a.ClosedAt != nil {
			continue
		}
		activity := AgentActivity(a)
		if priority[activity] > rank {
			result, rank = activity, priority[activity]
		}
	}
	return result
}
