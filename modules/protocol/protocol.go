package protocol

import (
	"slices"
	"sync"

	"github.com/go-mclib/client"
	"github.com/go-mclib/data/packetids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

const ModuleName = "protocol"

// Module caches the configuration-phase data the server sends (registries,
// tags, feature flags, known packs) and the active post-processing effects.
// The client itself drives login -> configuration -> play.
type Module struct {
	client *client.Client

	// typed config-phase state, guarded by configMu
	configMu     sync.RWMutex
	registryData []packets.S2CRegistryData
	tags         *packets.S2CUpdateTagsConfiguration
	featureFlags []ns.Identifier
	knownPacks   []packets.KnownPack

	effectsMu     sync.RWMutex
	postEffects   []ns.Identifier
	onPostEffects []func(effects []ns.Identifier)
}

func New() *Module {
	return &Module{}
}

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) {
	m.client = c
	c.OnTransfer(m.Reset)
	c.OnDisconnect(m.Reset)
}

func (m *Module) Reset() {
	m.configMu.Lock()
	m.registryData = nil
	m.tags = nil
	m.featureFlags = nil
	m.knownPacks = nil
	m.configMu.Unlock()
	m.setPostEffects(nil)
}

// From retrieves the protocol module from a client.
func From(c *client.Client) *Module { return client.ModuleAs[*Module](c, ModuleName) }

// RegistryData returns a snapshot of the registry data received during
// configuration. The slice is a copy; its elements share decoded contents.
func (m *Module) RegistryData() []packets.S2CRegistryData {
	m.configMu.RLock()
	defer m.configMu.RUnlock()
	return slices.Clone(m.registryData)
}

// Tags returns the parsed tags received during configuration. The value is
// replaced, never mutated, when new tags arrive.
func (m *Module) Tags() *packets.S2CUpdateTagsConfiguration {
	m.configMu.RLock()
	defer m.configMu.RUnlock()
	return m.tags
}

// FeatureFlags returns a snapshot of the feature flags received during configuration.
func (m *Module) FeatureFlags() []ns.Identifier {
	m.configMu.RLock()
	defer m.configMu.RUnlock()
	return slices.Clone(m.featureFlags)
}

// KnownPacks returns a snapshot of the known packs negotiated during configuration.
func (m *Module) KnownPacks() []packets.KnownPack {
	m.configMu.RLock()
	defer m.configMu.RUnlock()
	return slices.Clone(m.knownPacks)
}

// PostEffects returns an independent snapshot of the active post-processing effects.
func (m *Module) PostEffects() []ns.Identifier {
	m.effectsMu.RLock()
	defer m.effectsMu.RUnlock()
	return slices.Clone(m.postEffects)
}

// OnPostEffects observes replacement lists in configuration and play; empty clears.
func (m *Module) OnPostEffects(cb func(effects []ns.Identifier)) {
	m.onPostEffects = append(m.onPostEffects, cb)
}

func (m *Module) setPostEffects(effects []ns.Identifier) {
	m.effectsMu.Lock()
	m.postEffects = slices.Clone(effects)
	m.effectsMu.Unlock()
	for _, cb := range m.onPostEffects {
		cb(slices.Clone(effects))
	}
}

func (m *Module) HandlePacket(pkt *jp.WirePacket) {
	switch m.client.State() {
	case jp.StateConfiguration:
		m.handleConfiguration(pkt)
	case jp.StatePlay:
		if pkt.PacketID == packetids.S2CPostEffectsPlayID {
			var d packets.S2CPostEffectsPlay
			if m.client.Decode(pkt, &d) {
				m.setPostEffects(d.Effects)
			}
		}
	}
}

func (m *Module) handleConfiguration(pkt *jp.WirePacket) {
	c := m.client

	switch pkt.PacketID {
	case packetids.S2CPostEffectsConfigurationID:
		var d packets.S2CPostEffectsConfiguration
		if c.Decode(pkt, &d) {
			m.setPostEffects(d.Effects)
		}
	case packetids.S2CRegistryDataID:
		var d packets.S2CRegistryData
		if c.Decode(pkt, &d) {
			m.configMu.Lock()
			m.registryData = append(m.registryData, d)
			m.configMu.Unlock()
		}
	case packetids.S2CUpdateTagsConfigurationID:
		var d packets.S2CUpdateTagsConfiguration
		if c.Decode(pkt, &d) {
			m.configMu.Lock()
			m.tags = &d
			m.configMu.Unlock()
		}
	case packetids.S2CUpdateEnabledFeaturesID:
		var d packets.S2CUpdateEnabledFeatures
		if c.Decode(pkt, &d) {
			m.configMu.Lock()
			m.featureFlags = d.FeatureFlags
			m.configMu.Unlock()
		}
	case packetids.S2CSelectKnownPacksID:
		var d packets.S2CSelectKnownPacks
		if c.Decode(pkt, &d) {
			m.configMu.Lock()
			m.knownPacks = d.KnownPacks
			m.configMu.Unlock()
		}
	}
}
