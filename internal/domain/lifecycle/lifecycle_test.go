package lifecycle

import (
	"encoding/json"
	"testing"
)

func TestResourceBoundaryMappingsAndStableNumbers(t *testing.T) {
	cases := []struct {
		resource Resource
		number   int
		wire     string
		kind     string
	}{
		{ResourceSession, 1, "sessions", "session"},
		{ResourceTask, 2, "tasks", "task"},
		{ResourceArtifact, 3, "artifacts", "artifact"},
		{ResourcePersona, 4, "personas", "persona"},
		{ResourcePlugin, 5, "plugins", "plugin"},
		{ResourceConfig, 6, "configs", "config"},
		{ResourceRun, 7, "runs", "run"},
		{ResourceExecution, 8, "executions", "execution"},
		{ResourceNotification, 9, "notifications", "notification"},
		{ResourceDelivery, 10, "deliveries", "delivery"},
		{ResourceModelCall, 11, "model-calls", "model-call"},
		{ResourceSecret, 12, "secrets", "secret"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			if int(tc.resource) != tc.number || !tc.resource.Valid() {
				t.Fatalf("resource number changed: %d", tc.resource)
			}
			if tc.resource.WireName() != tc.wire || tc.resource.StorageKind() != tc.kind {
				t.Fatal("compatibility mapping changed")
			}
			for _, name := range []string{tc.wire, tc.kind} {
				value, err := ParseResourceName(name)
				if err != nil || value != tc.resource {
					t.Fatal(value, err)
				}
			}
		})
	}
	for _, unknown := range []Resource{ResourceUnknown, -1, 13} {
		if unknown.Valid() || unknown.WireName() != "" || unknown.StorageKind() != "" {
			t.Fatal("unknown enum must not map to a real resource", unknown)
		}
	}
	if _, err := ParseResourceName("SESSION"); err == nil {
		t.Fatal("unsupported boundary value accepted")
	}
}

func TestArchiveAPIUsesNumericEnumsAndUnambiguousKeys(t *testing.T) {
	payload, err := json.Marshal(struct {
		Resource Resource    `json:"resource"`
		Action   Action      `json:"action"`
		State    RecordState `json:"state"`
	}{ResourceSession, ActionArchive, StateArchived})
	if err != nil || string(payload) != `{"resource":1,"action":1,"state":2}` {
		t.Fatal(string(payload), err)
	}
	if ActionUnknown.Valid() || Action(4).Valid() || StateUnknown.Valid() || RecordState(3).Valid() {
		t.Fatal("unknown lifecycle values accepted")
	}
	if ArchiveKey(ResourceSession, "2:a") == ArchiveKey(ResourceTask, "1:a") {
		t.Fatal("resource identifiers collided")
	}
}
