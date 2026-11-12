package combat

import (
	"sync"
	"testing"
	"time"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/inventory"
	"github.com/go-mclib/client/modules/physics"
	"github.com/go-mclib/data/items"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

func TestHeldWeaponControlsAttackReadiness(t *testing.T) {
	c := client.New("unused", "Grace", false)
	client.NewSimulator(c).SetState(jp.StatePlay)
	inv := inventory.New()
	c.Register(inv)
	m := New()
	m.inventory = inv
	m.physics = physics.New() // tick-driven cooldown
	m.ticksSinceLastAttack = 4
	if m.AttackCooldown() < 0.9 {
		t.Fatal("fist never becomes ready")
	}
	packet, err := jp.ToWire(&packets.S2CSetPlayerInventory{Slot: 0, SlotData: ns.NewSlot(ns.VarInt(items.ID("minecraft:diamond_sword")), 1)})
	if err != nil {
		t.Fatal(err)
	}
	inv.HandlePacket(packet)
	if m.AttackCooldown() >= 0.9 {
		t.Fatal("sword incorrectly kept fist's short cooldown")
	}
	m.ticksSinceLastAttack = 10
	if m.AttackCooldown() >= 0.9 {
		t.Fatal("sword attacked before its cooldown threshold")
	}
	m.ticksSinceLastAttack = 12
	if m.AttackCooldown() < 0.9 {
		t.Fatal("sword did not become ready after cooldown")
	}
}

func TestCooldownWithoutPhysicsUsesWallTime(t *testing.T) {
	m := New()
	if m.AttackCooldown() < 0.9 {
		t.Fatal("never-attacked client is not ready without physics")
	}
	m.lastAttack = time.Now()
	if m.AttackCooldown() >= 0.9 {
		t.Fatal("cooldown ignored a fresh attack")
	}
	m.lastAttack = time.Now().Add(-5 * tickDuration)
	if m.AttackCooldown() < 0.9 {
		t.Fatal("cooldown never recovers without physics ticks")
	}
}

func TestAttackControlConcurrentWithCooldownReads(t *testing.T) {
	m := New()
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 1000 {
			m.StartAttacking(42)
			m.StopAttacking()
		}
	})
	wg.Go(func() {
		for range 1000 {
			m.AttackCooldown()
			m.Reset()
		}
	})
	wg.Wait()
	m.StopAttacking()
	if m.attacking {
		t.Fatal("stop left automatic attacks enabled")
	}
}
