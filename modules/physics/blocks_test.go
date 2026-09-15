package physics

import (
	"math"
	"testing"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/client/modules/world"
	"github.com/go-mclib/data/blocks"
	"github.com/go-mclib/data/chunks"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

func TestWebConsumesNextMoveAndClearsMomentum(t *testing.T) {
	m := New()
	s := self.New()
	web := blocks.DefaultStateID(blocks.BlockID("minecraft:cobweb"))
	m.velX, m.velY, m.velZ = 1, -0.4, 0.5
	m.applyInsideBlock(s, web, [3]int{}, [3]float64{}, true)
	if m.velX != 1 || m.velY != -0.4 || m.velZ != 0.5 {
		t.Fatal("entering a web prematurely consumed this tick's momentum")
	}
	dx, dy, dz := m.consumeStuckMovement(m.velX, m.velY, m.velZ)
	if dx != 0.25 || dy != -0.4*float64(float32(0.05)) || dz != 0.125 {
		t.Fatalf("web move = (%g,%g,%g)", dx, dy, dz)
	}
	if m.velX != 0 || m.velY != 0 || m.velZ != 0 {
		t.Fatal("web move retained pre-collision momentum")
	}
	dx, dy, dz = m.consumeStuckMovement(1, 1, 1)
	if dx != 1 || dy != 1 || dz != 1 {
		t.Fatal("stuck multiplier was not consumed exactly once")
	}
}

func TestWeavingChangesWebEscapeMovement(t *testing.T) {
	c := client.New("", "weaving-test", false)
	c.SetState(jp.StatePlay)
	s := self.New()
	c.Register(s)
	pkt, err := jp.ToWire(&packets.S2CUpdateMobEffect{EntityId: ns.VarInt(s.EntityID()), EffectId: ns.VarInt(effectWeaving), Amplifier: 0, Duration: 200})
	if err != nil {
		t.Fatal(err)
	}
	s.HandlePacket(pkt)
	m := New()
	m.applyInsideBlock(s, blocks.DefaultStateID(blocks.BlockID("minecraft:cobweb")), [3]int{}, [3]float64{}, true)
	dx, dy, dz := m.consumeStuckMovement(1, -1, 1)
	if dx != 0.5 || dy != -0.25 || dz != 0.5 {
		t.Fatalf("weaving escape move = (%g,%g,%g)", dx, dy, dz)
	}
}

func TestCreativeFlightIgnoresStuckBlocksAndBubbles(t *testing.T) {
	c := client.New("", "flying-block-test", false)
	c.SetState(jp.StatePlay)
	s := self.New()
	c.Register(s)
	abilities, err := jp.ToWire(&packets.S2CPlayerAbilities{Flags: 2, FlyingSpeed: 0.05})
	if err != nil {
		t.Fatal(err)
	}
	s.HandlePacket(abilities)
	for _, name := range []string{"minecraft:cobweb", "minecraft:sweet_berry_bush", "minecraft:powder_snow", "minecraft:bubble_column"} {
		t.Run(name, func(t *testing.T) {
			m := New()
			m.velX, m.velY, m.velZ, m.fallDistance = 0.2, -0.1, 0.3, 4
			m.world = blockTestWorld(t, map[[3]int]string{{0, 0, 0}: name})
			m.applyInsideBlock(s, blocks.DefaultStateID(blocks.BlockID(name)), [3]int{}, [3]float64{0.5, 0, 0.5}, true)
			dx, dy, dz := m.consumeStuckMovement(m.velX, m.velY, m.velZ)
			if dx != 0.2 || dy != -0.1 || dz != 0.3 || m.velX != 0.2 || m.velY != -0.1 || m.velZ != 0.3 || m.fallDistance != 4 {
				t.Fatalf("flight movement changed: (%g,%g,%g), velocity (%g,%g,%g), fall %g", dx, dy, dz, m.velX, m.velY, m.velZ, m.fallDistance)
			}
			if name == "minecraft:powder_snow" && !m.blocks.inPowderSnow {
				t.Fatal("flight erased independent powder-snow contact")
			}
		})
	}
}

func TestInsideContactsFollowCollisionAxesNotDiagonalEnvelope(t *testing.T) {
	contacts := make(map[[3]int]bool)
	visitMovementBlocks([3]float64{0.5, 0, 0.5}, [3]float64{2.5, 0, 2.5}, collisions.PlayerWidth, 1.8, 2, 2, make(map[[3]int]struct{}), func(pos [3]int, precise bool, _ float64) {
		contacts[pos] = precise
	})
	if _, hit := contacts[[3]int{1, 0, 1}]; hit {
		t.Fatal("diagonal envelope hit a web outside the Y-X-Z collision trajectory")
	}
	if !contacts[[3]int{1, 0, 0}] || !contacts[[3]int{2, 0, 1}] {
		t.Fatal("fast traversal missed an intermediate contacted block")
	}
}

func TestInsideContactInsetExcludesTouchingBlock(t *testing.T) {
	pos := [3]float64{1 - collisions.PlayerWidth/2, 0, 0.5}
	visitMovementBlocks(pos, pos, collisions.PlayerWidth, 1.8, 0, 0, make(map[[3]int]struct{}), func(block [3]int, _ bool, _ float64) {
		if block[0] == 1 {
			t.Fatal("touching an adjacent web counted as entering it")
		}
	})
}

func TestBubbleContactRequiresPrecisionForShortExit(t *testing.T) {
	var exited, entered bool
	visitMovementBlocks([3]float64{0.5, 0, 0.5}, [3]float64{1.5, 0, 0.5}, collisions.PlayerWidth, 1.8, 1, 0, make(map[[3]int]struct{}), func(pos [3]int, precise bool, _ float64) {
		if pos == [3]int{0, 0, 0} {
			exited = precise
		}
		if pos == [3]int{1, 0, 0} {
			entered = precise
		}
	})
	// A >= 1-block segment is precise even for cells already exited.
	if !exited || !entered {
		t.Fatal("fast crossed bubble column was not precise")
	}
	exited = true
	visitMovementBlocks([3]float64{0.8, 0, 0.5}, [3]float64{1.4, 0, 0.5}, collisions.PlayerWidth, 1.8, 0.6, 0, make(map[[3]int]struct{}), func(pos [3]int, precise bool, _ float64) {
		if pos == [3]int{0, 0, 0} {
			exited = precise
		}
	})
	if exited {
		t.Fatal("short movement out of a bubble applied a stale impulse")
	}
}

func TestHoneySlideUsesPostTravelVelocityAndSideBoundary(t *testing.T) {
	m := New()
	m.self = self.New()
	m.velX, m.velY = 0.2, (-0.5-0.08)*float64(float32(0.98))
	m.applyHoneySlide([3]int{}, [3]float64{1.24, 0.5, 0.5})
	if math.Abs(m.velX-0.02) > 1e-15 || m.velY != (-0.05-0.08)*float64(float32(0.98)) {
		t.Fatalf("fast honey slide = (%g,%g)", m.velX, m.velY)
	}
	m.velY = -0.5
	m.applyHoneySlide([3]int{}, [3]float64{1.24, 0.9375, 0.5})
	if m.velY != -0.5 {
		t.Fatal("standing level with honey top triggered side sliding")
	}
}

func TestSlimeRestitutionAccountsForPartialTickFall(t *testing.T) {
	incoming, resolved := -1.0, -0.5
	drag := float64(float32(0.98))
	want := 1.04 * (1 + 0.5*(drag-1))
	if got := collisionBounceY(incoming, resolved, 0.08, drag, 1); got != want {
		t.Fatalf("partial-tick bounce = %.17g, want %.17g", got, want)
	}
	if got := collisionBounceY(incoming, resolved, 0.08, drag, 0); got != 0 {
		t.Fatalf("suppressed bounce retained Y momentum: %g", got)
	}
}

func TestBubbleColumnSurfaceChangesTerminalVelocity(t *testing.T) {
	if got := bubbleColumnVelocity(-0.29, true, false); got != -0.3 {
		t.Fatalf("submerged magma downflow = %g", got)
	}
	if got := bubbleColumnVelocity(-0.89, true, true); got != -0.9 {
		t.Fatalf("surface magma downflow = %g", got)
	}
	if got := bubbleColumnVelocity(0.69, false, false); got != 0.7 {
		t.Fatalf("submerged soul-sand upflow = %g", got)
	}
	if got := bubbleColumnVelocity(1.75, false, true); got != 1.8 {
		t.Fatalf("surface soul-sand upflow = %g", got)
	}
	if movementEffectBlock(blocks.DefaultStateID(blocks.BlockID("minecraft:magma_block"))) {
		t.Fatal("solid magma was treated as a client-side motion effect")
	}
}

func blockTestWorld(t *testing.T, cells map[[3]int]string) *world.Module {
	t.Helper()
	column := chunks.NewColumn(0, 0)
	for pos, name := range cells {
		column.SetBlockState(pos[0], pos[1], pos[2], blocks.DefaultStateID(blocks.BlockID(name)))
	}
	data, err := column.EncodeSections()
	if err != nil {
		t.Fatal(err)
	}
	packet, err := jp.ToWire(&packets.S2CLevelChunkWithLight{ChunkData: ns.ChunkData{Data: data}})
	if err != nil {
		t.Fatal(err)
	}
	c := client.New("", "block-test", false)
	c.SetState(jp.StatePlay)
	w := world.New()
	c.Register(w)
	w.HandlePacket(packet)
	return w
}

func TestSlimeEdgeLandingSneakAndRestingContact(t *testing.T) {
	m, s := New(), self.New()
	m.self = s
	m.world = blockTestWorld(t, map[[3]int]string{{0, 0, 0}: "minecraft:slime_block"})
	m.onGround = true
	m.velY = -1
	m.applyLandingEffects(s, 1.2, 1, 0.5, -1, -0.5)
	if m.velY <= 1 {
		t.Fatalf("overhanging feet missed supporting slime restitution: %g", m.velY)
	}
	s.SetSneaking(true)
	m.velY = -1
	m.applyLandingEffects(s, 1.2, 1, 0.5, -1, -0.5)
	if m.velY != 0 {
		t.Fatalf("sneak did not suppress slime bounce: %g", m.velY)
	}
	s.SetSneaking(false)
	m.velY = -0.01
	m.applyLandingEffects(s, 1.2, 1, 0.5, -0.01, 0)
	if m.velY != 0 {
		t.Fatal("below-gravity impact bounced instead of coming to rest")
	}
	m.velX, m.velY = 1, -0.08*float64(float32(0.98))
	want := 0.4 + math.Abs(m.velY)*0.2
	m.applyInsideBlockEffects(s, 1.2, 1, 0.5, 1.2, 1, 0.5, 1.8)
	if m.velX != want {
		t.Fatalf("slime stepOn used pre-travel velocity: %g, want %g", m.velX, want)
	}
}

func TestHoneySupportFactorsUseCollisionFootprint(t *testing.T) {
	w := blockTestWorld(t, map[[3]int]string{{0, 0, 0}: "minecraft:honey_block"})
	if got := getBlockSpeedFactorAt(w, 1.2, 0.9375, 0.5, collisions.PlayerWidth); got != float64(float32(0.4)) {
		t.Fatalf("overhanging honey support speed factor = %g", got)
	}
	if got := blockJumpFactorAt(w, 1.2, 0.9375, 0.5, collisions.PlayerWidth); got != 0.5 {
		t.Fatalf("overhanging honey support jump factor = %g", got)
	}
	if got := blockJumpFactorAt(w, 1.3, 0.9375, 0.5, collisions.PlayerWidth); got != 1 {
		t.Fatalf("nearby but unsupported honey reduced jumping: %g", got)
	}
}

func TestPowderSnowFallingContactUsesCollisionTop(t *testing.T) {
	m, s := New(), self.New()
	m.self = s
	m.world = blockTestWorld(t, map[[3]int]string{{0, 0, 0}: "minecraft:powder_snow"})
	m.fallDistance = 3
	y := float64(float32(0.9))
	m.applyInsideBlockEffects(s, 0.5, y, 0.5, 0.5, y, 0.5, 1.8)
	if m.blocks.inPowderSnow || m.blocks.stuckX != 0 {
		t.Fatal("touching falling powder collision top applied an inside effect")
	}
	m.applyInsideBlockEffects(s, 0.5, y-0.01, 0.5, 0.5, y-0.01, 0.5, 1.8)
	dx, dy, _ := m.consumeStuckMovement(1, -0.1, 0)
	if dx != float64(float32(0.9)) || math.Abs(dy-(-0.1*1.5)) > 1e-16 || m.fallDistance != 0 {
		t.Fatalf("powder penetration = (%g,%g), fall distance %g", dx, dy, m.fallDistance)
	}
}

func TestScaledBodyWidthChangesSupportAndInsideContacts(t *testing.T) {
	w := blockTestWorld(t, map[[3]int]string{{0, 0, 0}: "minecraft:honey_block"})
	if got := blockJumpFactorAt(w, 1.4, 0.9375, 0.5, collisions.PlayerWidth); got != 1 {
		t.Fatalf("normal-width body reached distant honey support: %g", got)
	}
	if got := blockJumpFactorAt(w, 1.4, 0.9375, 0.5, collisions.PlayerWidth*2); got != 0.5 {
		t.Fatalf("wide body missed honey support: %g", got)
	}
	pos := [3]float64{1.4, 0, 0.5}
	wideTouched := false
	visitMovementBlocks(pos, pos, collisions.PlayerWidth*2, 1.8, 0, 0, make(map[[3]int]struct{}), func(block [3]int, _ bool, _ float64) {
		if block == [3]int{0, 0, 0} {
			wideTouched = true
		}
	})
	if !wideTouched {
		t.Fatal("wide body missed an overlapping inside-effect cell")
	}
	visitMovementBlocks(pos, pos, collisions.PlayerWidth, 1.8, 0, 0, make(map[[3]int]struct{}), func(block [3]int, _ bool, _ float64) {
		if block == [3]int{0, 0, 0} {
			t.Fatal("normal-width body touched the wide body's inside-effect cell")
		}
	})
}
