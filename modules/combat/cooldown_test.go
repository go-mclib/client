package combat

import (
	"sync"
	"testing"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/inventory"
	"github.com/go-mclib/data/items"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

func TestHeldWeaponControlsAttackReadiness(t *testing.T) {
	c := client.New("unused", "Grace", false)
	c.SetState(jp.StatePlay)
	inv := inventory.New()
	c.Register(inv)
	m := New()
	m.inventory = inv
	m.ticksSinceLastAttack = 4
	if m.GetAttackCooldown() < 0.9 {
		t.Fatal("fist never becomes ready")
	}
	packet, err := jp.ToWire(&packets.S2CSetPlayerInventory{Slot: 0, SlotData: ns.NewSlot(ns.VarInt(items.ItemID("minecraft:diamond_sword")), 1)})
	if err != nil {
		t.Fatal(err)
	}
	inv.HandlePacket(packet)
	if m.GetAttackCooldown() >= 0.9 {
		t.Fatal("sword incorrectly kept fist's short cooldown")
	}
	m.ticksSinceLastAttack = 10
	if m.GetAttackCooldown() >= 0.9 {
		t.Fatal("sword attacked before its cooldown threshold")
	}
	m.ticksSinceLastAttack = 12
	if m.GetAttackCooldown() < 0.9 {
		t.Fatal("sword did not become ready after cooldown")
	}
}

func TestAttackControlConcurrentWithCooldownReads(t *testing.T) {
	m := New()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 1000 {
			m.StartAttacking(42)
			m.StopAttacking()
		}
	}()
	go func() {
		defer wg.Done()
		for range 1000 {
			m.GetAttackCooldown()
			m.Reset()
		}
	}()
	wg.Wait()
	m.StopAttacking()
	if m.attacking {
		t.Fatal("stop left automatic attacks enabled")
	}
}
