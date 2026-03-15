package playerlist

import (
	"sync"

	"github.com/go-mclib/client/pkg/client"
	"github.com/go-mclib/data/pkg/data/packet_ids"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
	"github.com/go-mclib/protocol/nbt"
)

const ModuleName = "playerlist"

// action bits in the S2CPlayerInfoUpdate FixedBitSet
const (
	actionAddPlayer       = 0x01
	actionInitializeChat  = 0x02
	actionUpdateGameMode  = 0x04
	actionUpdateListed    = 0x08
	actionUpdateLatency   = 0x10
	actionUpdateDisplay   = 0x20
	actionUpdateListOrder = 0x40
	actionUpdateHat       = 0x80
)

// Player represents a player in the server's player list (tab list).
type Player struct {
	UUID     [16]byte
	Name     string
	Gamemode int32
	Ping     int32
	Listed   bool
}

type Module struct {
	client *client.Client
	mu     sync.RWMutex

	players map[[16]byte]*Player

	onPlayerJoin   []func(p *Player)
	onPlayerLeave  []func(p *Player)
	onPlayerUpdate []func(p *Player)
}

func New() *Module {
	return &Module{
		players: make(map[[16]byte]*Player),
	}
}

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) {
	m.client = c
	c.OnTransfer(m.Reset)
}

func (m *Module) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.players = make(map[[16]byte]*Player)
}

// From retrieves the player list module from a client.
func From(c *client.Client) *Module {
	mod := c.Module(ModuleName)
	if mod == nil {
		return nil
	}
	return mod.(*Module)
}

// events

func (m *Module) OnPlayerJoin(cb func(p *Player)) {
	m.onPlayerJoin = append(m.onPlayerJoin, cb)
}

func (m *Module) OnPlayerLeave(cb func(p *Player)) {
	m.onPlayerLeave = append(m.onPlayerLeave, cb)
}

func (m *Module) OnPlayerUpdate(cb func(p *Player)) {
	m.onPlayerUpdate = append(m.onPlayerUpdate, cb)
}

// getters

func (m *Module) GetPlayer(uuid [16]byte) *Player {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.players[uuid]
}

func (m *Module) GetPlayerByName(name string) *Player {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.players {
		if p.Name == name {
			return p
		}
	}
	return nil
}

func (m *Module) GetAllPlayers() []*Player {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*Player, 0, len(m.players))
	for _, p := range m.players {
		result = append(result, p)
	}
	return result
}

func (m *Module) GetPlayerCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.players)
}

func (m *Module) HandlePacket(pkt *jp.WirePacket) {
	if m.client.State() != jp.StatePlay {
		return
	}
	switch pkt.PacketID {
	case packet_ids.S2CPlayerInfoUpdateID:
		m.handlePlayerInfoUpdate(pkt)
	case packet_ids.S2CPlayerInfoRemoveID:
		m.handlePlayerInfoRemove(pkt)
	}
}

func (m *Module) handlePlayerInfoUpdate(pkt *jp.WirePacket) {
	buf := ns.NewReader(pkt.Data)

	// actions encoded as FixedBitSet(8) = 1 byte
	actionByte, err := buf.ReadByte()
	if err != nil {
		return
	}

	// entry count
	count, err := buf.ReadVarInt()
	if err != nil {
		return
	}

	for range int(count) {
		uuid, err := buf.ReadUUID()
		if err != nil {
			return
		}

		var name string
		var gamemode, ping ns.VarInt
		var listed bool
		gotGamemode := false
		gotListed := false
		gotPing := false
		isNew := false

		// actions are processed in enum ordinal order
		if actionByte&actionAddPlayer != 0 {
			isNew = true
			// player name (String, max 16 chars)
			n, err := buf.ReadString(16)
			if err != nil {
				return
			}
			name = string(n)

			// properties: VarInt count, each has name + value + optional signature
			propCount, err := buf.ReadVarInt()
			if err != nil {
				return
			}
			for range int(propCount) {
				if _, err := buf.ReadString(64); err != nil { // property name
					return
				}
				if _, err := buf.ReadString(32767); err != nil { // property value
					return
				}
				hasSig, err := buf.ReadBool()
				if err != nil {
					return
				}
				if hasSig {
					if _, err := buf.ReadString(1024); err != nil { // signature
						return
					}
				}
			}
		}

		if actionByte&actionInitializeChat != 0 {
			// optional chat session: bool + (UUID + Long + ByteArray(512) + ByteArray(4096))
			hasSession, err := buf.ReadBool()
			if err != nil {
				return
			}
			if hasSession {
				if _, err := buf.ReadUUID(); err != nil { // session UUID
					return
				}
				if _, err := buf.ReadInt64(); err != nil { // expires at (epoch millis)
					return
				}
				if _, err := buf.ReadByteArray(512); err != nil { // public key
					return
				}
				if _, err := buf.ReadByteArray(4096); err != nil { // key signature
					return
				}
			}
		}

		if actionByte&actionUpdateGameMode != 0 {
			gm, err := buf.ReadVarInt()
			if err != nil {
				return
			}
			gamemode = gm
			gotGamemode = true
		}

		if actionByte&actionUpdateListed != 0 {
			l, err := buf.ReadBool()
			if err != nil {
				return
			}
			listed = bool(l)
			gotListed = true
		}

		if actionByte&actionUpdateLatency != 0 {
			p, err := buf.ReadVarInt()
			if err != nil {
				return
			}
			ping = p
			gotPing = true
		}

		if actionByte&actionUpdateDisplay != 0 {
			// optional text component: bool + NBT if present
			hasDisplay, err := buf.ReadBool()
			if err != nil {
				return
			}
			if hasDisplay {
				// skip the NBT text component
				nbtReader := nbt.NewReaderFrom(buf.Reader())
				if _, _, err := nbtReader.ReadTag(true); err != nil {
					return
				}
			}
		}

		if actionByte&actionUpdateListOrder != 0 {
			if _, err := buf.ReadVarInt(); err != nil {
				return
			}
		}

		if actionByte&actionUpdateHat != 0 {
			if _, err := buf.ReadBool(); err != nil {
				return
			}
		}

		// apply to player map
		if isNew {
			p := &Player{
				UUID:     [16]byte(uuid),
				Name:     name,
				Gamemode: int32(gamemode),
				Ping:     int32(ping),
				Listed:   listed,
			}

			m.mu.Lock()
			m.players[p.UUID] = p
			m.mu.Unlock()

			for _, cb := range m.onPlayerJoin {
				cb(p)
			}
		} else if gotGamemode || gotListed || gotPing {
			key := [16]byte(uuid)

			m.mu.Lock()
			p := m.players[key]
			if p != nil {
				if gotGamemode {
					p.Gamemode = int32(gamemode)
				}
				if gotListed {
					p.Listed = listed
				}
				if gotPing {
					p.Ping = int32(ping)
				}
			}
			m.mu.Unlock()

			if p != nil {
				for _, cb := range m.onPlayerUpdate {
					cb(p)
				}
			}
		}
	}
}

func (m *Module) handlePlayerInfoRemove(pkt *jp.WirePacket) {
	// wire format: VarInt(count) + UUID[count]
	buf := ns.NewReader(pkt.Data)
	count, err := buf.ReadVarInt()
	if err != nil {
		return
	}

	removed := make([]*Player, 0, int(count))

	for range int(count) {
		uuid, err := buf.ReadUUID()
		if err != nil {
			return
		}
		key := [16]byte(uuid)

		m.mu.Lock()
		p := m.players[key]
		if p != nil {
			delete(m.players, key)
		}
		m.mu.Unlock()

		if p != nil {
			removed = append(removed, p)
		}
	}

	for _, p := range removed {
		for _, cb := range m.onPlayerLeave {
			cb(p)
		}
	}
}
