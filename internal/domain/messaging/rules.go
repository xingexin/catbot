package messaging

import (
	"errors"
	"time"
)

func (d Delivery) CheckReuse(sessionID string) (bool, error) {
	if d.SessionID != sessionID {
		return false, errors.New("delivery operation belongs to another session")
	}
	if d.Status == "sent" {
		return true, nil
	}
	return false, errors.New("message delivery has a recorded failure or uncertain outcome; inspect before sending again")
}
func (d *Delivery) RecordResult(result SendResult, sendErr error) {
	// A missing/invalid result or contradictory success is never safe to retry.
	switch result.Status {
	case Sent:
		if sendErr != nil {
			result.Status = Uncertain
		}
	case Failed, Uncertain:
	default:
		result.Status = Uncertain
	}
	d.Status, d.MessageID = string(result.Status), result.MessageID
	if sendErr != nil {
		d.Error = sendErr.Error()
	} else if result.Status != Sent {
		d.Error = "message delivery " + d.Status
	}
}
func (n Notification) CheckDuplicate(request Notification) (bool, error) {
	if n.SessionID != request.SessionID || n.Text != request.Text || n.TaskID != request.TaskID || n.PluginID != request.PluginID {
		return false, errors.New("notification operation ID is already used for a different recipient or message")
	}
	return n.Status == "sent" || n.Status == "saved", nil
}
func (n Notification) CheckRetry() error {
	if n.Status != "failed" {
		return errors.New("投递结果不确定，不能自动重发；请先核对接收方消息")
	}
	return nil
}
func (n *Notification) BeginAttempt(now time.Time) {
	if n.CreatedAt.IsZero() {
		n.CreatedAt = now
	}
	n.UpdatedAt = now
	n.Status = "pending"
	n.Error = ""
	n.Attempts++
}
func (n *Notification) RecordDelivery(err error, deliveryStatus string) {
	n.Status = "sent"
	if err == nil {
		return
	}
	n.Status = "uncertain"
	n.Error = err.Error()
	if deliveryStatus == "failed" {
		n.Status = "failed"
	}
}
func (n Notification) Terminal() bool {
	switch n.Status {
	case "sent", "saved", "failed", "uncertain":
		return true
	}
	return false
}
