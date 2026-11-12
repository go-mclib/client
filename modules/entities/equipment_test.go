package entities

import (
	"reflect"
	"sync"
	"testing"

	"github.com/go-mclib/client"
	dataentities "github.com/go-mclib/data/entities"
	"github.com/go-mclib/data/items"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

func equipmentPacket(t *testing.T, packet jp.Packet) *jp.WirePacket {
	t.Helper()
	wire, err := jp.ToWire(packet)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestPartialEquipmentUpdatesAndIsolation(t *testing.T) {
	m := New()
	m.entities[42] = &Entity{ID: 42, Metadata: dataentities.Metadata{{Index: 1, Data: []byte{7}}}}
	sword := items.NewStack(items.ID("minecraft:diamond_sword"), 1)
	sword.Components.CustomName = &items.ItemNameComponent{Text: "Grace"}
	sword.Components.Lore = []string{"Forged here"}
	sword.Components.Enchantments = map[string]int32{"minecraft:sharpness": 3}
	raw, err := sword.ToSlot()
	if err != nil {
		t.Fatal(err)
	}
	m.handleSetEquipment(equipmentPacket(t, &packets.S2CSetEquipment{EntityID: 42, Slots: []packets.EquipmentEntry{{Slot: 0, Item: raw}, {Slot: 5, Item: ns.NewSlot(ns.VarInt(items.ID("minecraft:iron_helmet")), 1)}}}))
	m.handleSetEquipment(equipmentPacket(t, &packets.S2CSetEquipment{EntityID: 42, Slots: []packets.EquipmentEntry{{Slot: 5, Item: ns.EmptySlot()}}}))
	got := m.GetEntity(42)
	if item := got.Equipment[0]; item.Name != "minecraft:diamond_sword" || item.CustomName != "Grace" || item.Enchantments["minecraft:sharpness"] != 3 || len(item.Lore) != 1 || item.Lore[0] != "Forged here" {
		t.Fatalf("decoded equipment = %+v", item)
	}
	if item, known := got.Equipment[5]; !known || item.Count != 0 {
		t.Fatalf("cleared helmet should be known empty: %+v, %v", item, known)
	}
	if _, known := got.Equipment[1]; known {
		t.Fatal("unreceived offhand should remain unknown")
	}
	got.X = 100
	got.Metadata[0].Data[0] = 99
	got.Equipment[0].Lore[0] = "mutated"
	got.Equipment[0].Enchantments["minecraft:sharpness"] = 99
	delete(got.Equipment, 5)
	fresh := m.GetEntity(42)
	if fresh.X != 0 || fresh.Metadata[0].Data[0] != 7 || fresh.Equipment[0].Lore[0] != "Forged here" || fresh.Equipment[0].Enchantments["minecraft:sharpness"] != 3 {
		t.Fatal("snapshot mutation escaped into tracked entity")
	}
	if _, known := fresh.Equipment[5]; !known {
		t.Fatal("snapshot map deletion escaped")
	}
}

func TestEquipmentClearsWithEntityLifecycle(t *testing.T) {
	for _, event := range []string{"despawn", "chunk_unload", "respawn", "transfer", "disconnect"} {
		t.Run(event, func(t *testing.T) {
			c := client.New("unused", "Grace", false)
			client.NewSimulator(c).SetState(jp.StatePlay)
			m := New()
			c.Register(m)
			m.entities[42] = &Entity{ID: 42, X: -1, Z: -1, Equipment: map[uint8]EquipmentItem{0: {Count: 1}}}
			switch event {
			case "despawn":
				m.handleRemoveEntities(equipmentPacket(t, &packets.S2CRemoveEntities{EntityIds: []ns.VarInt{42}}))
			case "chunk_unload":
				m.handleForgetLevelChunk(equipmentPacket(t, &packets.S2CForgetLevelChunk{ChunkX: -1, ChunkZ: -1}))
			case "respawn":
				m.HandlePacket(&jp.WirePacket{PacketID: (&packets.S2CRespawn{}).ID()})
			case "transfer":
				client.NewSimulator(c).Transfer()
			case "disconnect":
				client.NewSimulator(c).Disconnect()
			}
			if m.GetEntity(42) != nil {
				t.Fatal("stale entity/equipment survived lifecycle reset")
			}
		})
	}
}

func TestEquipmentReadDuringUpdates(t *testing.T) {
	m := New()
	m.entities[42] = &Entity{ID: 42}
	wire := equipmentPacket(t, &packets.S2CSetEquipment{EntityID: 42, Slots: []packets.EquipmentEntry{{Slot: 0, Item: ns.NewSlot(ns.VarInt(items.ID("minecraft:stone")), 1)}}})
	swap := equipmentPacket(t, &packets.S2CEntityEvent{EntityID: 42, EntityStatus: 55})
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 100 {
			m.handleSetEquipment(wire)
			m.handleEntityEvent(swap)
			m.handleEntityEvent(swap)
		}
	})
	for range 100 {
		if e := m.GetEntity(42); e != nil {
			e.X++
			delete(e.Equipment, 0)
		}
	}
	wg.Wait()
	if got := m.GetEntity(42).Equipment[0]; got.Name != "minecraft:stone" {
		t.Fatalf("lost update: %+v", got)
	}
}

func TestEquipmentHandSwapEvent(t *testing.T) {
	for _, offhand := range []string{"shield", "empty", "unknown"} {
		t.Run(offhand, func(t *testing.T) {
			c := client.New("unused", "Grace", false)
			client.NewSimulator(c).SetState(jp.StatePlay)
			m := New()
			c.Register(m)
			m.HandlePacket(equipmentPacket(t, &packets.S2CAddEntity{EntityID: 300, Type: ns.VarInt(dataentities.TypeID("minecraft:player"))}))
			sword := items.NewStack(items.ID("minecraft:diamond_sword"), 1)
			sword.Components.CustomName = &items.ItemNameComponent{Text: "Swapping sword"}
			sword.Components.Enchantments = map[string]int32{"minecraft:sharpness": 3}
			raw, err := sword.ToSlot()
			if err != nil {
				t.Fatal(err)
			}
			slots := []packets.EquipmentEntry{
				{Slot: 0, Item: raw},
				{Slot: 5, Item: ns.NewSlot(ns.VarInt(items.ID("minecraft:iron_helmet")), 1)},
			}
			switch offhand {
			case "shield":
				slots = append(slots, packets.EquipmentEntry{Slot: 1, Item: ns.NewSlot(ns.VarInt(items.ID("minecraft:shield")), 1)})
			case "empty":
				slots = append(slots, packets.EquipmentEntry{Slot: 1, Item: ns.EmptySlot()})
			}
			m.HandlePacket(equipmentPacket(t, &packets.S2CSetEquipment{EntityID: 300, Slots: slots}))
			before := m.GetEntity(300)
			// Vanilla replaces the two SetEquipment entries with SWAP_HANDS.
			// Entity Event uses an Int32 entity ID, not a VarInt.
			swap := &jp.WirePacket{PacketID: (&packets.S2CEntityEvent{}).ID(), Data: []byte{0, 0, 1, 44, 55}}
			m.HandlePacket(swap)
			after := m.GetEntity(300)
			for destination, source := range map[uint8]uint8{0: 1, 1: 0, 5: 5} {
				got, known := after.Equipment[destination]
				want, wantKnown := before.Equipment[source]
				if known != wantKnown || !reflect.DeepEqual(got, want) {
					t.Fatalf("slot %d after swap = %+v (known=%v), want %+v (known=%v)", destination, got, known, want, wantKnown)
				}
			}
			if before.Equipment[0].Name != "minecraft:diamond_sword" {
				t.Fatal("hand swap mutated an existing equipment snapshot")
			}
			m.HandlePacket(swap)
			if !reflect.DeepEqual(m.GetEntity(300).Equipment, before.Equipment) {
				t.Fatal("second hand swap did not restore known and unknown slots")
			}
		})
	}
}

func TestEquipmentHandSwapIgnoresUnrelatedEvents(t *testing.T) {
	c := client.New("unused", "Grace", false)
	client.NewSimulator(c).SetState(jp.StatePlay)
	m := New()
	c.Register(m)
	m.entities[300] = &Entity{ID: 300, Equipment: map[uint8]EquipmentItem{0: {Name: "minecraft:stone", Count: 1}}}
	before := m.GetEntity(300)
	for _, packet := range []*jp.WirePacket{
		equipmentPacket(t, &packets.S2CEntityEvent{EntityID: 301, EntityStatus: 55}),
		equipmentPacket(t, &packets.S2CEntityEvent{EntityID: 300, EntityStatus: 35}),
		{PacketID: (&packets.S2CEntityEvent{}).ID(), Data: []byte{0, 0, 1, 44}},
	} {
		m.HandlePacket(packet)
		if !reflect.DeepEqual(m.GetEntity(300).Equipment, before.Equipment) || m.GetEntity(301) != nil {
			t.Fatal("unknown entity, unrelated status or truncated event changed equipment")
		}
	}
	client.NewSimulator(c).SetState(jp.StateConfiguration)
	m.HandlePacket(equipmentPacket(t, &packets.S2CEntityEvent{EntityID: 300, EntityStatus: 55}))
	if !reflect.DeepEqual(m.GetEntity(300).Equipment, before.Equipment) {
		t.Fatal("non-play packet changed equipment")
	}
}

func TestRemoveEntitiesRejectsInvalidCount(t *testing.T) {
	c := client.New("unused", "Grace", false)
	m := New()
	c.Register(m)
	m.entities[42] = &Entity{ID: 42}
	for _, count := range []int32{-1, 1<<31 - 1} {
		buf := ns.NewWriter()
		_ = buf.WriteVarInt(ns.VarInt(count))
		m.handleRemoveEntities(&jp.WirePacket{PacketID: (&packets.S2CRemoveEntities{}).ID(), Data: buf.Bytes()})
	}
	if m.GetEntity(42) == nil {
		t.Fatal("malformed removal packet removed an entity")
	}
}
