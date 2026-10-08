package messaging

import (
	"errors"
	"testing"
	"time"
)

func TestUncertainDeliveryNeverBecomesRetryableFailure(t *testing.T) {
	for _, tc := range []struct {
		result SendResult
		err    error
		status string
	}{
		{SendResult{Status: Sent}, errors.New("lost confirmation"), "uncertain"},
		{SendResult{}, nil, "uncertain"},
		{SendResult{Status: Failed}, errors.New("explicit rejection"), "failed"},
		{SendResult{Status: Sent, MessageID: "remote"}, nil, "sent"},
	} {
		d := Delivery{SessionID: "same"}
		d.RecordResult(tc.result, tc.err)
		if d.Status != tc.status {
			t.Fatal(d, tc.status)
		}
		n := Notification{}
		n.BeginAttempt(time.Now())
		n.RecordDelivery(tc.err, d.Status)
		if tc.err != nil && (n.CheckRetry() == nil) != (tc.status == "failed") {
			t.Fatal("incorrect retry classification", n)
		}
		if _, err := d.CheckReuse("different"); err == nil {
			t.Fatal("delivery reused for another recipient")
		}
	}
}
func TestNotificationIdempotencyIncludesOriginAndContent(t *testing.T) {
	n := Notification{SessionID: "s", Text: "content", TaskID: "task", PluginID: "plugin", Status: "saved"}
	if done, err := n.CheckDuplicate(n); !done || err != nil {
		t.Fatal(done, err)
	}
	for _, changed := range []Notification{
		{SessionID: "other", Text: n.Text, TaskID: n.TaskID, PluginID: n.PluginID},
		{SessionID: n.SessionID, Text: "changed", TaskID: n.TaskID, PluginID: n.PluginID},
		{SessionID: n.SessionID, Text: n.Text, TaskID: "other", PluginID: n.PluginID},
		{SessionID: n.SessionID, Text: n.Text, TaskID: n.TaskID, PluginID: "other"},
	} {
		if _, err := n.CheckDuplicate(changed); err == nil {
			t.Fatal("notification identity collision accepted")
		}
	}
	for _, status := range []string{"pending", "uncertain", "saved", "sent"} {
		n.Status = status
		if n.CheckRetry() == nil {
			t.Fatal("unsafe retry allowed", status)
		}
	}
}
