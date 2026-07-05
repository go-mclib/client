package playerlist

import (
	"sync"

	"github.com/go-mclib/client"
	"github.com/go-mclib/data/packet_ids"
	"github.com/go-mclib/data/packets"
	"github.com/go-mclib/nbt"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
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

// Property is a profile property (e.g. the "textures" skin/cape blob with its
// Mojang signature). Kept so a proxy can replay the tab list with skins intact.
type Property struct {
	Name         string
	Value        string
	Signature    string
	HasSignature bool
}

// Player represents a player in the server's player list (tab list).
type Player struct {
	UUID       [16]byte
	Name       string
	Gamemode   int32
	Ping       int32
	Listed     bool
	Properties []Property
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

func (m *Module) OnPlayerJoin(cb func(p *Player)) {
	m.onPlayerJoin = append(m.onPlayerJoin, cb)
}

func (m *Module) OnPlayerLeave(cb func(p *Player)) {
	m.onPlayerLeave = append(m.onPlayerLeave, cb)
}

func (m *Module) OnPlayerUpdate(cb func(p *Player)) {
	m.onPlayerUpdate = append(m.onPlayerUpdate, cb)
}

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

		var u entryUpdate
		if err := u.decode(buf, actionByte); err != nil {
			return
		}

		m.applyEntry([16]byte(uuid), &u)
	}
}

// entryUpdate accumulates decoded state for one player_info_update entry.
type entryUpdate struct {
	name        string
	props       []Property
	gamemode    ns.VarInt
	ping        ns.VarInt
	listed      bool
	gotGamemode bool
	gotListed   bool
	gotPing     bool
	isNew       bool
}

// decode reads all fields present in the action bitfield, in enum ordinal order.
func (u *entryUpdate) decode(buf *ns.PacketBuffer, actionByte byte) error {
	if actionByte&actionAddPlayer != 0 {
		if err := u.decodeAddPlayer(buf); err != nil {
			return err
		}
	}
	if actionByte&actionInitializeChat != 0 {
		if err := skipChatSession(buf); err != nil {
			return err
		}
	}
	if actionByte&actionUpdateGameMode != 0 {
		if err := u.decodeGameMode(buf); err != nil {
			return err
		}
	}
	if actionByte&actionUpdateListed != 0 {
		if err := u.decodeListed(buf); err != nil {
			return err
		}
	}
	if actionByte&actionUpdateLatency != 0 {
		if err := u.decodeLatency(buf); err != nil {
			return err
		}
	}
	if actionByte&actionUpdateDisplay != 0 {
		if err := skipDisplayName(buf); err != nil {
			return err
		}
	}
	if actionByte&actionUpdateListOrder != 0 {
		if _, err := buf.ReadVarInt(); err != nil {
			return err
		}
	}
	if actionByte&actionUpdateHat != 0 {
		if _, err := buf.ReadBool(); err != nil {
			return err
		}
	}
	return nil
}

// decodeAddPlayer reads the player name and profile properties.
func (u *entryUpdate) decodeAddPlayer(buf *ns.PacketBuffer) error {
	u.isNew = true
	// player name (String, max 16 chars)
	n, err := buf.ReadString(16)
	if err != nil {
		return err
	}
	u.name = string(n)

	// properties: VarInt count, each has name + value + optional signature
	propCount, err := buf.ReadVarInt()
	if err != nil {
		return err
	}
	for range int(propCount) {
		pname, err := buf.ReadString(64)
		if err != nil {
			return err
		}
		pval, err := buf.ReadString(32767)
		if err != nil {
			return err
		}
		hasSig, err := buf.ReadBool()
		if err != nil {
			return err
		}
		var sig ns.String
		if hasSig {
			if sig, err = buf.ReadString(1024); err != nil {
				return err
			}
		}
		u.props = append(u.props, Property{Name: string(pname), Value: string(pval), Signature: string(sig), HasSignature: bool(hasSig)})
	}
	return nil
}

func (u *entryUpdate) decodeGameMode(buf *ns.PacketBuffer) error {
	gm, err := buf.ReadVarInt()
	if err != nil {
		return err
	}
	u.gamemode = gm
	u.gotGamemode = true
	return nil
}

func (u *entryUpdate) decodeListed(buf *ns.PacketBuffer) error {
	l, err := buf.ReadBool()
	if err != nil {
		return err
	}
	u.listed = bool(l)
	u.gotListed = true
	return nil
}

func (u *entryUpdate) decodeLatency(buf *ns.PacketBuffer) error {
	p, err := buf.ReadVarInt()
	if err != nil {
		return err
	}
	u.ping = p
	u.gotPing = true
	return nil
}

// skipChatSession skips the optional chat session data (initialize_chat action).
func skipChatSession(buf *ns.PacketBuffer) error {
	// optional chat session: bool + (UUID + Long + ByteArray(512) + ByteArray(4096))
	hasSession, err := buf.ReadBool()
	if err != nil {
		return err
	}
	if !hasSession {
		return nil
	}
	if _, err := buf.ReadUUID(); err != nil { // session UUID
		return err
	}
	if _, err := buf.ReadInt64(); err != nil { // expires at (epoch millis)
		return err
	}
	if _, err := buf.ReadByteArray(512); err != nil { // public key
		return err
	}
	if _, err := buf.ReadByteArray(4096); err != nil { // key signature
		return err
	}
	return nil
}

// skipDisplayName skips the optional display-name text component (update_display action).
func skipDisplayName(buf *ns.PacketBuffer) error {
	// optional text component: bool + NBT if present
	hasDisplay, err := buf.ReadBool()
	if err != nil {
		return err
	}
	if hasDisplay {
		// skip the NBT text component
		nbtReader := nbt.NewReaderFrom(buf.Reader())
		if _, _, err := nbtReader.ReadTag(true); err != nil {
			return err
		}
	}
	return nil
}

// applyEntry applies a decoded entry to the player map and fires callbacks.
func (m *Module) applyEntry(key [16]byte, u *entryUpdate) {
	if u.isNew {
		p := &Player{
			UUID:       key,
			Name:       u.name,
			Gamemode:   int32(u.gamemode),
			Ping:       int32(u.ping),
			Listed:     u.listed,
			Properties: u.props,
		}

		m.mu.Lock()
		m.players[p.UUID] = p
		m.mu.Unlock()

		for _, cb := range m.onPlayerJoin {
			cb(p)
		}
	} else if u.gotGamemode || u.gotListed || u.gotPing {
		m.mu.Lock()
		p := m.players[key]
		if p != nil {
			if u.gotGamemode {
				p.Gamemode = int32(u.gamemode)
			}
			if u.gotListed {
				p.Listed = u.listed
			}
			if u.gotPing {
				p.Ping = int32(u.ping)
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

// BuildSnapshot synthesizes a single player_info_update carrying every known
// player with their profile properties (skins), game mode, listed flag and
// latency. A proxy replays this so a freshly-connected client sees the full tab
// list — including skins — that it missed by joining mid-session.
func (m *Module) BuildSnapshot() *packets.S2CPlayerInfoUpdate {
	m.mu.RLock()
	players := make([]*Player, 0, len(m.players))
	for _, p := range m.players {
		players = append(players, p)
	}
	m.mu.RUnlock()
	if len(players) == 0 {
		return nil
	}

	// action order is enum-ordinal: add_player, update_game_mode, update_listed, update_latency
	const actions = actionAddPlayer | actionUpdateGameMode | actionUpdateListed | actionUpdateLatency
	buf := ns.NewWriter()
	_ = buf.WriteByte(actions)
	_ = buf.WriteVarInt(ns.VarInt(len(players)))
	for _, p := range players {
		_ = buf.WriteUUID(ns.UUID(p.UUID))
		_ = buf.WriteString(ns.String(p.Name))
		_ = buf.WriteVarInt(ns.VarInt(len(p.Properties)))
		for _, prop := range p.Properties {
			_ = buf.WriteString(ns.String(prop.Name))
			_ = buf.WriteString(ns.String(prop.Value))
			_ = buf.WriteBool(ns.Boolean(prop.HasSignature))
			if prop.HasSignature {
				_ = buf.WriteString(ns.String(prop.Signature))
			}
		}
		_ = buf.WriteVarInt(ns.VarInt(p.Gamemode))
		_ = buf.WriteBool(ns.Boolean(p.Listed))
		_ = buf.WriteVarInt(ns.VarInt(p.Ping))
	}
	return &packets.S2CPlayerInfoUpdate{Data: ns.ByteArray(buf.Bytes())}
}
