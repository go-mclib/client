package entities

import (
	"sync"
	"testing"

	"github.com/go-mclib/client"
	dataentities "github.com/go-mclib/data/entities"
	"github.com/go-mclib/data/items"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
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
	sword := items.NewStack(items.ItemID("minecraft:diamond_sword"), 1)
	sword.Components.CustomName = &items.ItemNameComponent{Text: "Grace"}
	sword.Components.Lore = []string{"Forged here"}
	sword.Components.Enchantments = map[string]int32{"minecraft:sharpness": 3}
	raw, err := sword.ToSlot()
	if err != nil {
		t.Fatal(err)
	}
	m.handleSetEquipment(equipmentPacket(t, &packets.S2CSetEquipment{EntityId: 42, Slots: []packets.EquipmentEntry{{Slot: 0, Item: raw}, {Slot: 5, Item: ns.NewSlot(ns.VarInt(items.ItemID("minecraft:iron_helmet")), 1)}}}))
	m.handleSetEquipment(equipmentPacket(t, &packets.S2CSetEquipment{EntityId: 42, Slots: []packets.EquipmentEntry{{Slot: 5, Item: ns.EmptySlot()}}}))
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
			c.SetState(jp.StatePlay)
			m := New()
			c.Register(m)
			m.entities[42] = &Entity{ID: 42, X: -1, Z: -1, Equipment: map[uint8]EquipmentItem{0: {Count: 1}}}
			switch event {
			case "despawn":
				buf := ns.NewWriter()
				_ = buf.WriteVarInt(1)
				_ = buf.WriteVarInt(42)
				m.handleRemoveEntities(&jp.WirePacket{Data: buf.Bytes()})
			case "chunk_unload":
				m.handleForgetLevelChunk(equipmentPacket(t, &packets.S2CForgetLevelChunk{ChunkX: -1, ChunkZ: -1}))
			case "respawn":
				m.HandlePacket(&jp.WirePacket{PacketID: (&packets.S2CRespawn{}).ID()})
			case "transfer":
				c.FireTransfer()
			case "disconnect":
				c.FireDisconnect()
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
	wire := equipmentPacket(t, &packets.S2CSetEquipment{EntityId: 42, Slots: []packets.EquipmentEntry{{Slot: 0, Item: ns.NewSlot(ns.VarInt(items.ItemID("minecraft:stone")), 1)}}})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			m.handleSetEquipment(wire)
		}
	}()
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
