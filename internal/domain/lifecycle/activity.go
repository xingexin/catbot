package lifecycle

// ActivityState is the lifecycle view of legacy execution states. Conversion
// happens at this boundary; lifecycle rules compare typed integer values.
type ActivityState int

const (
	ActivityUnknown   ActivityState = 0
	ActivityPending   ActivityState = 1
	ActivityRunning   ActivityState = 2
	ActivitySettled   ActivityState = 3
	ActivityUncertain ActivityState = 4
)

func ParseActivityState(value string) ActivityState {
	switch value {
	case "queued", "pending", "provisioning":
		return ActivityPending
	case "running":
		return ActivityRunning
	case "completed", "cancelled", "failed", "interrupted", "notification_failed", "sent", "saved", "paused", "active", "error", "skipped":
		return ActivitySettled
	case "uncertain":
		return ActivityUncertain
	default:
		return ActivityUnknown
	}
}

func (s ActivityState) Mutable() bool { return s == ActivityPending || s == ActivityRunning }

// ReferenceLock serializes reference creation against catalog removal.
const ReferenceLock = "record-references"
