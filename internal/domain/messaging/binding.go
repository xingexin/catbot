package messaging

import (
	"slices"
	"strconv"
)

type ChannelBinding struct {
	Enabled             bool
	Account             string
	AllowedPeers        []string
	AllowedRooms        []string
	ConfigID, PersonaID string
	RoomPersonaID       string
	AllowLegacyAccount  bool
}

func (b ChannelBinding) Allows(account, peer, roomID string, outbound bool) bool {
	accountMatches := account == b.Account || (outbound && b.AllowLegacyAccount && account == "")
	if !b.Enabled || !accountMatches || peer == "" {
		return false
	}
	if roomID != "" {
		return peer != account && slices.Contains(b.AllowedRooms, roomID)
	}
	return slices.Contains(b.AllowedPeers, peer)
}
func ValidQQID(id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}
