// Package registry exposes the registries the server sends during the
// configuration phase (dimension types, biomes, damage types, chat types, …)
// as typed, queryable data. It indexes the raw registry data captured by the
// protocol module, so the dimension's world height, the available biomes, etc.
// can be read at runtime instead of assuming vanilla defaults.
package registry

import (
	"fmt"
	"sync"

	"github.com/go-mclib/client/pkg/client"
	"github.com/go-mclib/client/pkg/client/modules/protocol"
	"github.com/go-mclib/client/pkg/client/modules/self"
	"github.com/go-mclib/nbt"
	jp "github.com/go-mclib/protocol/java_protocol"
)

const ModuleName = "registry"

// well-known registry identifiers.
const (
	DimensionTypeRegistry = "minecraft:dimension_type"
	BiomeRegistry         = "minecraft:worldgen/biome"
)

// Entry is one registry element, in the order the server sent it.
type Entry struct {
	ID    string       // namespaced id, e.g. "minecraft:overworld"
	Index int          // registry index (what packets reference by VarInt)
	Data  nbt.Compound // element data; nil when the server sent none
}

type Module struct {
	client *client.Client
	proto  *protocol.Module
	self   *self.Module // optional, for current-dimension helpers

	mu    sync.Mutex
	index map[string][]Entry // nil until built; cleared on reconnect/transfer
}

func New() *Module { return &Module{} }

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) {
	m.client = c
	c.OnTransfer(m.Reset)
}

func (m *Module) Wire() error {
	if m.proto = protocol.From(m.client); m.proto == nil {
		return fmt.Errorf("requires protocol module")
	}
	m.self = self.From(m.client)
	return nil
}

func (m *Module) HandlePacket(_ *jp.WirePacket) {}

// Reset drops the cached index; it is rebuilt lazily from fresh registry data.
func (m *Module) Reset() {
	m.mu.Lock()
	m.index = nil
	m.mu.Unlock()
}

// From retrieves the registry module from a client.
func From(c *client.Client) *Module {
	mod := c.Module(ModuleName)
	if mod == nil {
		return nil
	}
	return mod.(*Module)
}

// Registry returns all entries of the named registry, in index order, or nil.
func (m *Module) Registry(name string) []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buildLocked()
	return m.index[name]
}

// EntryByIndex returns the entry at the given registry index.
func (m *Module) EntryByIndex(name string, index int) (Entry, bool) {
	entries := m.Registry(name)
	if index < 0 || index >= len(entries) {
		return Entry{}, false
	}
	return entries[index], true
}

// Entry returns the entry with the given namespaced id.
func (m *Module) Entry(name, id string) (Entry, bool) {
	for _, e := range m.Registry(name) {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// EntryIDs returns the namespaced ids of every entry in the named registry.
func (m *Module) EntryIDs(name string) []string {
	entries := m.Registry(name)
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	return ids
}

// buildLocked indexes the protocol module's captured registry data. Caller holds m.mu.
func (m *Module) buildLocked() {
	if m.index != nil {
		return
	}
	idx := make(map[string][]Entry)
	for _, rd := range m.proto.RegistryData() {
		entries := make([]Entry, len(rd.Entries))
		for i, e := range rd.Entries {
			var data nbt.Compound
			if e.HasData {
				if c, ok := e.Data.(nbt.Compound); ok {
					data = c
				}
			}
			entries[i] = Entry{ID: string(e.EntryId), Index: i, Data: data}
		}
		idx[string(rd.RegistryId)] = entries
	}
	m.index = idx
}
