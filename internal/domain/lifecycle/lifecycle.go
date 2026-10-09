// Package lifecycle defines stable resource identities and record lifecycle operations.
package lifecycle

import (
	"fmt"
	"strconv"
	"time"
)

// Resource values are persisted and exposed by the API. Never reorder or reuse them.
type Resource int

const (
	ResourceUnknown      Resource = 0
	ResourceSession      Resource = 1
	ResourceTask         Resource = 2
	ResourceArtifact     Resource = 3
	ResourcePersona      Resource = 4
	ResourcePlugin       Resource = 5
	ResourceConfig       Resource = 6
	ResourceRun          Resource = 7
	ResourceExecution    Resource = 8
	ResourceNotification Resource = 9
	ResourceDelivery     Resource = 10
	ResourceModelCall    Resource = 11
	ResourceSecret       Resource = 12
)

func (r Resource) Valid() bool { return r >= ResourceSession && r <= ResourceSecret }

// WireName is an HTTP boundary mapping retained for existing collection routes.
func (r Resource) WireName() string {
	switch r {
	case ResourceSession:
		return "sessions"
	case ResourceTask:
		return "tasks"
	case ResourceArtifact:
		return "artifacts"
	case ResourcePersona:
		return "personas"
	case ResourcePlugin:
		return "plugins"
	case ResourceConfig:
		return "configs"
	case ResourceRun:
		return "runs"
	case ResourceExecution:
		return "executions"
	case ResourceNotification:
		return "notifications"
	case ResourceDelivery:
		return "deliveries"
	case ResourceModelCall:
		return "model-calls"
	case ResourceSecret:
		return "secrets"
	default:
		return ""
	}
}

// StorageKind is a persistence boundary mapping; it preserves existing record keys.
func (r Resource) StorageKind() string {
	switch r {
	case ResourceSession:
		return "session"
	case ResourceTask:
		return "task"
	case ResourceArtifact:
		return "artifact"
	case ResourcePersona:
		return "persona"
	case ResourcePlugin:
		return "plugin"
	case ResourceConfig:
		return "config"
	case ResourceRun:
		return "run"
	case ResourceExecution:
		return "execution"
	case ResourceNotification:
		return "notification"
	case ResourceDelivery:
		return "delivery"
	case ResourceModelCall:
		return "model-call"
	case ResourceSecret:
		return "secret"
	default:
		return ""
	}
}

// ParseResourceName converts legacy route and persistence names at an input boundary.
func ParseResourceName(name string) (Resource, error) {
	for r := ResourceSession; r <= ResourceSecret; r++ {
		if name == r.WireName() || name == r.StorageKind() {
			return r, nil
		}
	}
	return ResourceUnknown, fmt.Errorf("unknown resource %q", name)
}

type Action int

const (
	ActionUnknown Action = 0
	ActionArchive Action = 1
	ActionRestore Action = 2
	ActionPurge   Action = 3
)

func (a Action) Valid() bool { return a >= ActionArchive && a <= ActionPurge }

type RecordState int

const (
	StateUnknown  RecordState = 0
	StateActive   RecordState = 1
	StateArchived RecordState = 2
)

func (s RecordState) Valid() bool { return s == StateActive || s == StateArchived }

const ArchiveStorageKind = "archive"

// ArchiveRecord stores only index metadata; the original record remains in place.
type ArchiveRecord struct {
	ID         string    `json:"id"`
	Resource   Resource  `json:"resource"`
	RecordID   string    `json:"recordId"`
	Name       string    `json:"name"`
	ArchivedAt time.Time `json:"archivedAt"`
}

func ArchiveKey(resource Resource, id string) string {
	return strconv.Itoa(int(resource)) + ":" + id
}
