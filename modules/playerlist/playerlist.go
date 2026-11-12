package playerlist

import (
	"slices"
	"sync"

	"github.com/go-mclib/client"
	"github.com/go-mclib/data/packetids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

const ModuleName = "playerlist"

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
	UUID        [16]byte
	Name        string
	Gamemode    int32
	Ping        int32
	Listed      bool
	Properties  []Property
	DisplayName *string
	ListOrder   int32
	ShowHat     bool
	// display is the original component, kept for snapshot replay; it is
	// never mutated, so clones share it.
	display ns.TextComponent
}

type Module struct {
	client *client.Client
	mu     sync.RWMutex

	players        map[[16]byte]*Player
	header, footer string
	hasTabList     bool

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
	c.OnDisconnect(m.Reset)
}

func (m *Module) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.players = make(map[[16]byte]*Player)
	m.header, m.footer = "", ""
	m.hasTabList = false
}

// From retrieves the player list module from a client.
func From(c *client.Client) *Module { return client.ModuleAs[*Module](c, ModuleName) }

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
	return clonePlayer(m.players[uuid])
}

func (m *Module) GetPlayerByName(name string) *Player {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.players {
		if p.Name == name {
			return clonePlayer(p)
		}
	}
	return nil
}

func (m *Module) GetAllPlayers() []*Player {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*Player, 0, len(m.players))
	for _, p := range m.players {
		result = append(result, clonePlayer(p))
	}
	return result
}

func (m *Module) PlayerCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.players)
}

func (m *Module) HandlePacket(pkt *jp.WirePacket) {
	if m.client.State() != jp.StatePlay {
		return
	}
	switch pkt.PacketID {
	case packetids.S2CPlayerInfoUpdateID:
		m.handlePlayerInfoUpdate(pkt)
	case packetids.S2CPlayerInfoRemoveID:
		m.handlePlayerInfoRemove(pkt)
	case packetids.S2CTabListID:
		m.handleTabList(pkt)
	}
}

func (m *Module) handlePlayerInfoUpdate(pkt *jp.WirePacket) {
	var d packets.S2CPlayerInfoUpdate
	if m.client.Decode(pkt, &d) {
		m.applyPlayerInfoUpdate(&d)
	}
}

// applyPlayerInfoUpdate applies every entry to the player map and fires callbacks.
func (m *Module) applyPlayerInfoUpdate(d *packets.S2CPlayerInfoUpdate) {
	for i := range d.Entries {
		m.applyEntry(d.Actions, &d.Entries[i])
	}
}

func (m *Module) applyEntry(actions packets.PlayerInfoActions, e *packets.PlayerInfoEntry) {
	key := [16]byte(e.UUID)
	isNew := actions.Has(packets.PlayerInfoAddPlayer)
	m.mu.Lock()
	p := m.players[key]
	if isNew {
		p = &Player{UUID: key, Name: string(e.Name), Properties: make([]Property, len(e.Properties))}
		for i, prop := range e.Properties {
			sig, hasSig := prop.Signature.Get()
			p.Properties[i] = Property{Name: string(prop.Name), Value: string(prop.Value), Signature: string(sig), HasSignature: hasSig}
		}
		m.players[key] = p
	}
	if p == nil {
		m.mu.Unlock()
		return
	}
	if actions.Has(packets.PlayerInfoUpdateGameMode) {
		p.Gamemode = int32(e.GameMode)
	}
	if actions.Has(packets.PlayerInfoUpdateListed) {
		p.Listed = bool(e.Listed)
	}
	if actions.Has(packets.PlayerInfoUpdateLatency) {
		p.Ping = int32(e.Latency)
	}
	if actions.Has(packets.PlayerInfoUpdateDisplayName) {
		p.DisplayName, p.display = nil, ns.TextComponent{}
		if text, ok := e.DisplayName.Get(); ok {
			plain := text.String()
			p.DisplayName, p.display = &plain, text
		}
	}
	if actions.Has(packets.PlayerInfoUpdateListOrder) {
		p.ListOrder = int32(e.ListOrder)
	}
	if actions.Has(packets.PlayerInfoUpdateHat) {
		p.ShowHat = bool(e.ShowHat)
	}
	p = clonePlayer(p)
	m.mu.Unlock()
	callbacks := m.onPlayerUpdate
	if isNew {
		callbacks = m.onPlayerJoin
	}
	for _, cb := range callbacks {
		cb(clonePlayer(p))
	}
}

func (m *Module) handlePlayerInfoRemove(pkt *jp.WirePacket) {
	var d packets.S2CPlayerInfoRemove
	if m.client.Decode(pkt, &d) {
		m.applyPlayerInfoRemove(&d)
	}
}

func (m *Module) applyPlayerInfoRemove(d *packets.S2CPlayerInfoRemove) {
	removed := make([]*Player, 0, len(d.UUIDs))
	m.mu.Lock()
	for _, uuid := range d.UUIDs {
		key := [16]byte(uuid)
		if p := m.players[key]; p != nil {
			delete(m.players, key)
			removed = append(removed, p)
		}
	}
	m.mu.Unlock()
	for _, p := range removed {
		for _, cb := range m.onPlayerLeave {
			cb(clonePlayer(p))
		}
	}
}

// BuildSnapshot synthesizes a single player_info_update carrying every known
// player with their profile properties (skins), game mode, listed flag and
// latency. A proxy replays this so a freshly-connected client sees the full tab
// list — including skins — that it missed by joining mid-session.
func (m *Module) BuildSnapshot() *packets.S2CPlayerInfoUpdate {
	players := m.GetAllPlayers()
	if len(players) == 0 {
		return nil
	}

	// Every retained per-player action is replayed.
	snap := &packets.S2CPlayerInfoUpdate{
		Actions: packets.PlayerInfoAddPlayer | packets.PlayerInfoUpdateGameMode | packets.PlayerInfoUpdateListed |
			packets.PlayerInfoUpdateLatency | packets.PlayerInfoUpdateDisplayName | packets.PlayerInfoUpdateListOrder | packets.PlayerInfoUpdateHat,
		Entries: make([]packets.PlayerInfoEntry, len(players)),
	}
	for i, p := range players {
		e := &snap.Entries[i]
		e.UUID, e.Name = ns.UUID(p.UUID), ns.String(p.Name)
		e.Properties = make([]packets.GameProfileProperty, len(p.Properties))
		for j, prop := range p.Properties {
			e.Properties[j] = packets.GameProfileProperty{Name: ns.String(prop.Name), Value: ns.String(prop.Value)}
			if prop.HasSignature {
				e.Properties[j].Signature = ns.Some(ns.String(prop.Signature))
			}
		}
		e.GameMode, e.Listed, e.Latency = ns.VarInt(p.Gamemode), ns.Boolean(p.Listed), ns.VarInt(p.Ping)
		if p.DisplayName != nil {
			e.DisplayName = ns.Some(p.display)
		}
		e.ListOrder, e.ShowHat = ns.VarInt(p.ListOrder), ns.Boolean(p.ShowHat)
	}
	return snap
}

func clonePlayer(p *Player) *Player {
	if p == nil {
		return nil
	}
	result := *p
	result.Properties = slices.Clone(p.Properties)
	if p.DisplayName != nil {
		name := *p.DisplayName
		result.DisplayName = &name
	}
	return &result
}

func (m *Module) handleTabList(pkt *jp.WirePacket) {
	var d packets.S2CTabList
	if !m.client.Decode(pkt, &d) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.header, m.footer = d.Header.String(), d.Footer.String()
	m.hasTabList = true
}

// TabList returns plain-text header/footer and whether the server sent them.
func (m *Module) TabList() (header, footer string, known bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.header, m.footer, m.hasTabList
}
