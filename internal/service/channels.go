package service

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"agentTest/internal/message"
)

// ChannelBinding belongs to the host, not the transport implementation.
type ChannelBinding struct {
	Enabled             bool
	Account             string
	AllowedPeers        []string
	ConfigID, PersonaID string
	AllowLegacyAccount  bool
}

func (b ChannelBinding) allows(account, peer string, outbound bool) bool {
	accountMatches := account == b.Account || (outbound && b.AllowLegacyAccount && account == "")
	return b.Enabled && accountMatches && peer != "" && slices.Contains(b.AllowedPeers, peer)
}

// Channel is registered once at startup. The routing key is independent of the
// concrete Sender; replacing it preserves existing sessions and delivery IDs.
type Channel struct {
	Title          string
	Implementation string
	LoginURL       string
	Sender         message.Sender
	Status         message.StatusChecker
	Receive        http.Handler
	Binding        func(context.Context) (ChannelBinding, error)
}

// RegisterChannel must be called before serving requests or starting workers.
func (a *App) RegisterChannel(key string, c Channel) error {
	if key == "" || c.Sender == nil || c.Binding == nil {
		return errors.New("channel requires a key, sender and binding policy")
	}
	if _, exists := a.channels[key]; exists {
		return errors.New("channel already registered: " + key)
	}
	a.channels[key] = c
	return nil
}

// IncomingHandler fixes the route in trusted startup code, never in a payload.
func (a *App) IncomingHandler(route string) message.IncomingHandler {
	return func(ctx context.Context, in message.InboundMessage) error {
		in.Route = route
		return a.HandleIncoming(ctx, in)
	}
}

func (a *App) channelWebhook(route string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := a.channels[route]
		if !ok || c.Receive == nil {
			JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "message receiver is not configured"})
			return
		}
		c.Receive.ServeHTTP(w, r)
	}
}

type channelStatus struct {
	message.ConnectionStatus
	Provider       string `json:"provider"`
	Implementation string `json:"implementation"`
}

func (a *App) channelStatus(ctx context.Context, route string) channelStatus {
	c, ok := a.channels[route]
	s := channelStatus{Provider: route, Implementation: c.Implementation,
		ConnectionStatus: message.ConnectionStatus{State: "unconfigured"}}
	if !ok {
		return s
	}
	if c.Status != nil {
		s.ConnectionStatus = c.Status.Status(ctx)
	} else {
		s.State, s.Configured = "unknown", true
	}
	b, err := c.Binding(ctx)
	switch {
	case err != nil:
		s.State, s.Error = "error", "无法读取绑定配置"
	case s.State == "configured" && (!b.Enabled || b.ConfigID == ""):
		s.State, s.Configured = "unconfigured", false
	case s.State == "online":
		if !b.Enabled {
			s.State = "disabled"
		} else if s.Account != "" && s.Account != b.Account {
			s.State = "account_mismatch"
		}
	}
	return s
}
