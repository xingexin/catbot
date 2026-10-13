package messaging

import "testing"

func TestGroupHistorySettingsDefaultsAndValidation(t *testing.T) {
	t.Parallel()
	defaults := DefaultGroupHistorySettings()
	if defaults.Enabled || defaults.RetentionDays != 7 || defaults.RecentMessages != 30 || defaults.MaxMessages != 10000 {
		t.Fatal(defaults)
	}
	normalized, err := (GroupHistorySettings{RoomIDs: []string{" 123 ", "123"}}).Normalize()
	if err != nil || normalized.RecentMessages != 0 || len(normalized.RoomIDs) != 1 || normalized.RoomIDs[0] != "123" {
		t.Fatal(normalized, err)
	}
	for _, tc := range []struct {
		name string
		edit func(*GroupHistorySettings)
	}{
		{"negative retention", func(s *GroupHistorySettings) { s.RetentionDays = -1 }},
		{"excess retention", func(s *GroupHistorySettings) { s.RetentionDays = 366 }},
		{"negative recent", func(s *GroupHistorySettings) { s.RecentMessages = -1 }},
		{"excess recent", func(s *GroupHistorySettings) { s.RecentMessages = 101 }},
		{"small quota", func(s *GroupHistorySettings) { s.MaxMessages = 99 }},
		{"excess quota", func(s *GroupHistorySettings) { s.MaxMessages = 100001 }},
		{"blank room", func(s *GroupHistorySettings) { s.RoomIDs = []string{" "} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := defaults
			tc.edit(&s)
			if _, err := s.Normalize(); err == nil {
				t.Fatal("expected invalid settings")
			}
		})
	}
}

func TestGroupHistoryRoomCollectionIsExplicit(t *testing.T) {
	t.Parallel()
	s := GroupHistorySettings{Enabled: true, RoomIDs: []string{"room"}}
	if !s.AllowsRoom("room") || s.AllowsRoom("other") {
		t.Fatal("room whitelist not enforced")
	}
	s.Enabled = false
	if s.AllowsRoom("room") {
		t.Fatal("disabled collector accepted room")
	}
}
