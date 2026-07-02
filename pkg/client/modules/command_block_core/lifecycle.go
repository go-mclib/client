package command_block_core

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/go-mclib/data/pkg/packets"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

// Build checks permissions, places the cube (via local auto:1b command blocks —
// no chat), programs the always-active reserved floor, and starts dispatch.
func (m *Module) Build(ctx context.Context) error {
	m.mu.Lock()
	if m.state == StateBuilding || m.state == StateReady {
		s := m.state
		m.mu.Unlock()
		return fmt.Errorf("core already %s", s)
	}
	m.state = StateBuilding
	m.mu.Unlock()

	if !m.self.CanUseGameMasterBlocks() {
		m.setState(StateError)
		return fmt.Errorf("need creative mode and op level >= 2 to program command blocks")
	}
	if m.cfg.PearlY == 0 {
		m.cfg.PearlY = m.resolvePearlY()
	}
	// the cube grows away from the nearer world boundary; reject a range that
	// still doesn't fit (e.g. a world shorter than Size) — /fill silently
	// refuses out-of-range blocks, leaving the core mysteriously absent.
	minY, maxY := m.dimensionBounds()
	bottom, top := m.cubeBounds()
	if bottom < minY || top >= maxY {
		m.setState(StateError)
		return fmt.Errorf("core cube Y [%d,%d] does not fit the buildable range [%d,%d)", bottom, top, minY, maxY)
	}
	m.computeLayout()

	if err := m.bootstrap(ctx); err != nil {
		m.setState(StateError)
		return err
	}
	if err := m.programReserved(); err != nil {
		m.setState(StateError)
		return err
	}

	// pearl holds the chunk now — drop the temporary forceload.
	if m.cfg.Forceload == ForceloadPearl {
		if err := sleepCtx(ctx, 500*time.Millisecond); err != nil {
			return err
		}
		if err := m.runTemp(ctx, m.forceloadCmd("remove")); err != nil {
			m.client.Debugf("forceload remove failed: %v", err)
		}
	}

	m.setState(StateReady)
	m.startTicker()
	return nil
}

// Teardown stops dispatch and clears the world state — also via command blocks.
func (m *Module) Teardown() error {
	m.mu.Lock()
	if m.cancel != nil {
		close(m.cancel)
		m.cancel = nil
	}
	m.state = StateTearingDown
	m.mu.Unlock()

	ctx := context.Background()
	if m.cfg.Forceload == ForceloadPearl {
		_ = m.runTemp(ctx, fmt.Sprintf("kill @e[type=ender_pearl,tag=%s]", m.cfg.PearlTag))
	}
	if m.cfg.Forceload != ForceloadNone {
		_ = m.runTemp(ctx, m.forceloadCmd("add"))
	}
	_ = m.runTemp(ctx, m.fillCmd("minecraft:air"))
	if m.cfg.Forceload != ForceloadNone {
		_ = m.runTemp(ctx, m.forceloadCmd("remove"))
	}
	m.setState(StateIdle)
	return nil
}

// bootstrap forceloads the chunk, summons the pearl, and fills the cube with
// command blocks — each as a locally-placed, self-running auto:1b command block.
func (m *Module) bootstrap(ctx context.Context) error {
	var cmds []string
	if m.cfg.Forceload != ForceloadNone {
		cmds = append(cmds, m.forceloadCmd("add"))
	}
	if m.cfg.Forceload == ForceloadPearl {
		cmds = append(cmds, m.pearlSummonCommand())
	}
	cmds = append(cmds, m.fillCmd("minecraft:command_block"))
	for _, cmd := range cmds {
		if err := m.runTemp(ctx, cmd); err != nil {
			return err
		}
	}
	return nil
}

// runTemp places a one-shot auto:1b command block running cmd next to the bot,
// waits for it to fire, then breaks it.
func (m *Module) runTemp(ctx context.Context, cmd string) error {
	place, clicked, face, ok := m.placementTarget()
	if !ok {
		return fmt.Errorf("no solid block near the bot to place a bootstrap command block")
	}
	item, err := commandBlockItem(cmd)
	if err != nil {
		return err
	}
	_ = m.client.SetHeldSlot(0)
	if err := m.client.SetCreativeSlot(bootstrapSlot, item); err != nil {
		return err
	}
	// place on the clicked block's top face -> command block lands at `place`
	if err := m.client.PlaceBlock(clicked.X, clicked.Y, clicked.Z, face, 0, 0.5, 1.0, 0.5); err != nil {
		return err
	}
	if err := sleepCtx(ctx, 150*time.Millisecond); err != nil { // let auto:1b fire
		return err
	}
	_ = m.client.BreakBlock(place.X, place.Y, place.Z, face, true)
	return sleepCtx(ctx, 80*time.Millisecond)
}

// placementTarget finds a solid ground neighbour to place a temp block on so it
// doesn't collide with the bot's own body.
func (m *Module) placementTarget() (place, clicked Pos, face int8, ok bool) {
	x, y, z := m.self.Position()
	fx, fy, fz := int(math.Floor(x)), int(math.Floor(y)), int(math.Floor(z))
	groundY := fy - 1
	for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
		gx, gz := fx+d[0], fz+d[1]
		if m.world == nil ||
			(m.world.GetBlock(gx, groundY, gz) != 0 && m.world.GetBlock(gx, fy, gz) == 0) {
			return Pos{gx, fy, gz}, Pos{gx, groundY, gz}, 1, true // face 1 = top
		}
	}
	return Pos{}, Pos{}, 0, false
}

// SetReserved sets the user's always-active commands (besides the pearl
// keep-alive, prepended automatically). Reprograms live if ready.
func (m *Module) SetReserved(cmds []string) error {
	m.mu.Lock()
	m.reservedCmds = append([]string(nil), cmds...)
	ready := m.state == StateReady
	m.mu.Unlock()
	if ready {
		return m.programReserved()
	}
	return nil
}

// programReserved writes the reserved floor as repeating (AUTO) blocks.
func (m *Module) programReserved() error {
	cmds := m.buildReservedCommands()
	if len(cmds) > len(m.reserved) {
		return fmt.Errorf("reserved commands (%d) exceed floor slots (%d)", len(cmds), len(m.reserved))
	}
	for i, cmd := range cmds {
		if err := m.programBlock(m.reserved[i], cmd, modeAuto, true); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) buildReservedCommands() []string {
	var cmds []string
	if m.cfg.Forceload == ForceloadPearl {
		cmds = append(cmds, m.pearlKeepAliveCommand())
	}
	return append(cmds, m.reservedCmds...)
}

// programBlock sends a ServerboundSetCommandBlockPacket for one slot.
func (m *Module) programBlock(p Pos, command string, mode int, automatic bool) error {
	var flags ns.Int8
	if automatic {
		flags |= flagAutomatic
	}
	return m.client.WritePacket(&packets.C2SSetCommandBlock{
		Location: ns.Position{X: p.X, Y: p.Y, Z: p.Z},
		Command:  ns.String(command),
		Mode:     ns.VarInt(mode),
		Flags:    flags,
	})
}

func (m *Module) pearlPos() (x, y, z int) {
	return chunkFloor(m.cfg.Origin.X) + 8, m.cfg.PearlY, chunkFloor(m.cfg.Origin.Z) + 8
}

func (m *Module) pearlSummonCommand() string {
	x, y, z := m.pearlPos()
	return fmt.Sprintf(`summon ender_pearl %d %d %d {Tags:["%s"],NoGravity:1b}`, x, y, z, m.cfg.PearlTag)
}

func (m *Module) pearlKeepAliveCommand() string {
	x, y, z := m.pearlPos()
	return fmt.Sprintf(
		`execute unless entity @e[type=ender_pearl,tag=%s,limit=1] run summon ender_pearl %d %d %d {Tags:["%s"],NoGravity:1b}`,
		m.cfg.PearlTag, x, y, z, m.cfg.PearlTag,
	)
}

func (m *Module) forceloadCmd(action string) string {
	return fmt.Sprintf("forceload %s %d %d", action, chunkFloor(m.cfg.Origin.X), chunkFloor(m.cfg.Origin.Z))
}

func (m *Module) fillCmd(block string) string {
	x0, z0 := chunkFloor(m.cfg.Origin.X), chunkFloor(m.cfg.Origin.Z)
	bottom, top := m.cubeBounds()
	n := m.cfg.Size
	return fmt.Sprintf("fill %d %d %d %d %d %d %s", x0, bottom, z0, x0+n-1, top, z0+n-1, block)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
