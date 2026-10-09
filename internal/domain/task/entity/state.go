package entity

// State keeps legacy task status strings at the persistence boundary.
type State int

const (
	StateUnknown            State = 0
	StateProvisioning       State = 1
	StateActive             State = 2
	StatePaused             State = 3
	StateCancelled          State = 4
	StateCompleted          State = 5
	StateFailed             State = 6
	StateInterrupted        State = 7
	StateNotificationFailed State = 8
	StateError              State = 9
)

func ParseState(value string) State {
	switch value {
	case "provisioning":
		return StateProvisioning
	case "active":
		return StateActive
	case "paused":
		return StatePaused
	case "cancelled":
		return StateCancelled
	case "completed":
		return StateCompleted
	case "failed":
		return StateFailed
	case "interrupted":
		return StateInterrupted
	case "notification_failed":
		return StateNotificationFailed
	case "error":
		return StateError
	default:
		return StateUnknown
	}
}

func (s State) WireName() string {
	switch s {
	case StateProvisioning:
		return "provisioning"
	case StateActive:
		return "active"
	case StatePaused:
		return "paused"
	case StateCancelled:
		return "cancelled"
	case StateCompleted:
		return "completed"
	case StateFailed:
		return "failed"
	case StateInterrupted:
		return "interrupted"
	case StateNotificationFailed:
		return "notification_failed"
	case StateError:
		return "error"
	default:
		return ""
	}
}

func (s State) Terminal() bool {
	switch s {
	case StateCancelled, StateCompleted, StateFailed, StateInterrupted, StateNotificationFailed:
		return true
	default:
		return false
	}
}

type ControlAction int

const (
	ControlUnknown ControlAction = 0
	ControlPause   ControlAction = 1
	ControlResume  ControlAction = 2
	ControlCancel  ControlAction = 3
	ControlTrigger ControlAction = 4
)

func ParseControlAction(value string) ControlAction {
	switch value {
	case "pause":
		return ControlPause
	case "resume":
		return ControlResume
	case "cancel":
		return ControlCancel
	case "trigger":
		return ControlTrigger
	default:
		return ControlUnknown
	}
}
