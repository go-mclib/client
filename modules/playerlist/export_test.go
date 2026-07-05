package playerlist

import jp "github.com/go-mclib/protocol/java_protocol"

// test-only exports
const (
	ActionAddPlayer      = actionAddPlayer
	ActionUpdateGameMode = actionUpdateGameMode
	ActionUpdateListed   = actionUpdateListed
	ActionUpdateLatency  = actionUpdateLatency
)

func (m *Module) HandlePlayerInfoUpdate(pkt *jp.WirePacket) {
	m.handlePlayerInfoUpdate(pkt)
}
