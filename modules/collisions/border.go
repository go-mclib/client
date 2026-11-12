package collisions

import (
	"math"

	"github.com/go-mclib/data/blocks"
	"github.com/go-mclib/data/packetids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

// borderState is copied by value under Module.mu. shape references slices
// that updateBounds allocates afresh and never mutates afterwards, so a copy
// stays consistent while the module's border keeps moving.
type borderState struct {
	initialized              bool
	received                 bool // an S2CInitializeBorder arrived this session
	x, z, limit              float64
	from, to, size, previous float64
	duration, remaining      int64
	warningBlocks            int32
	warningTime              int32
	minX, minZ, maxX, maxZ   float64
	shape                    voxelShape
}

func defaultBorder() borderState {
	// WorldBorder defaults: 15 s warning time, 5 warning blocks.
	b := borderState{initialized: true, limit: 29999984, warningBlocks: 5, warningTime: 15}
	b.setSize(float64(float32(5.999997e7)), float64(float32(5.999997e7)), 0)
	return b
}

func (b *borderState) setSize(from, to float64, duration int64) {
	b.from, b.to, b.duration, b.remaining = from, to, duration, duration
	b.size = to
	if duration > 0 {
		b.size = from
	}
	b.previous = b.size
	b.updateBounds()
}

func (b *borderState) updateBounds() {
	clamp := func(v float64) float64 { return max(-b.limit, min(b.limit, v)) }
	b.minX, b.maxX = clamp(b.x-b.previous/2), clamp(b.x+b.previous/2)
	b.minZ, b.maxZ = clamp(b.z-b.previous/2), clamp(b.z+b.previous/2)
	minX, maxX, minZ, maxZ := math.Floor(b.minX), math.Ceil(b.maxX), math.Floor(b.minZ), math.Ceil(b.maxZ)
	inf := math.Inf(1)
	b.shape = voxelShape{
		parts: []blocks.AABB{
			{MinX: -inf, MinY: -inf, MinZ: -inf, MaxX: minX, MaxY: inf, MaxZ: inf},
			{MinX: maxX, MinY: -inf, MinZ: -inf, MaxX: inf, MaxY: inf, MaxZ: inf},
			{MinX: minX, MinY: -inf, MinZ: -inf, MaxX: maxX, MaxY: inf, MaxZ: minZ},
			{MinX: minX, MinY: -inf, MinZ: maxZ, MaxX: maxX, MaxY: inf, MaxZ: inf},
		},
		coords: [3][]float64{{-inf, minX, maxX, inf}, {-inf, inf}, {-inf, minZ, maxZ, inf}},
	}
}

// TickBorder follows ClientLevel.tick, after entity movement, not elapsed wall
// time. In 26.2 border interpolation duration and progress are measured in ticks.
// The physics module drives it once per client tick; do not call it otherwise,
// or the border interpolates faster than vanilla.
func (m *Module) TickBorder() {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := &m.border
	if b.remaining <= 0 {
		return
	}
	b.remaining--
	b.previous = b.size
	progress := float64(b.duration-b.remaining) / float64(b.duration)
	b.size = b.from + progress*(b.to-b.from)
	if b.remaining == 0 {
		b.size, b.previous = b.to, b.to
	}
	b.updateBounds()
}

// BorderSnapshot returns an S2CInitializeBorder describing the current border,
// including center, lerp and warning updates received after the initial
// packet, or nil before the server initialized the border this session.
func (m *Module) BorderSnapshot() *packets.S2CInitializeBorder {
	m.mu.RLock()
	b := m.border
	m.mu.RUnlock()
	if !b.received {
		return nil
	}
	return &packets.S2CInitializeBorder{
		X:                      ns.Float64(b.x),
		Z:                      ns.Float64(b.z),
		OldDiameter:            ns.Float64(b.size),
		NewDiameter:            ns.Float64(b.to),
		Speed:                  ns.VarLong(b.remaining),
		PortalTeleportBoundary: ns.VarInt(b.limit),
		WarningBlocks:          ns.VarInt(b.warningBlocks),
		WarningTime:            ns.VarInt(b.warningTime),
	}
}

func (m *Module) HandlePacket(pkt *jp.WirePacket) {
	if m.client == nil || m.client.State() != jp.StatePlay {
		return
	}
	switch pkt.PacketID {
	case packetids.S2CInitializeBorderID:
		var p packets.S2CInitializeBorder
		if !m.client.Decode(pkt, &p) {
			return
		}
		m.mu.Lock()
		b := &m.border
		b.initialized, b.received = true, true
		b.x, b.z, b.limit = float64(p.X), float64(p.Z), float64(p.PortalTeleportBoundary)
		b.warningBlocks, b.warningTime = int32(p.WarningBlocks), int32(p.WarningTime)
		b.setSize(float64(p.OldDiameter), float64(p.NewDiameter), int64(p.Speed))
		m.mu.Unlock()
	case packetids.S2CSetBorderCenterID:
		var p packets.S2CSetBorderCenter
		if !m.client.Decode(pkt, &p) {
			return
		}
		m.mu.Lock()
		m.border.x, m.border.z = float64(p.X), float64(p.Z)
		m.border.updateBounds()
		m.mu.Unlock()
	case packetids.S2CSetBorderLerpSizeID:
		var p packets.S2CSetBorderLerpSize
		if !m.client.Decode(pkt, &p) {
			return
		}
		m.mu.Lock()
		m.border.setSize(float64(p.OldDiameter), float64(p.NewDiameter), int64(p.Speed))
		m.mu.Unlock()
	case packetids.S2CSetBorderSizeID:
		var p packets.S2CSetBorderSize
		if !m.client.Decode(pkt, &p) {
			return
		}
		m.mu.Lock()
		m.border.setSize(float64(p.Diameter), float64(p.Diameter), 0)
		m.mu.Unlock()
	case packetids.S2CSetBorderWarningDelayID:
		var p packets.S2CSetBorderWarningDelay
		if !m.client.Decode(pkt, &p) {
			return
		}
		m.mu.Lock()
		m.border.warningTime = int32(p.WarningTime)
		m.mu.Unlock()
	case packetids.S2CSetBorderWarningDistanceID:
		var p packets.S2CSetBorderWarningDistance
		if !m.client.Decode(pkt, &p) {
			return
		}
		m.mu.Lock()
		m.border.warningBlocks = int32(p.WarningBlocks)
		m.mu.Unlock()
	}
}

// appendBorder adds the border shape from a snapshot when region is near it.
func appendBorder(b *borderState, shapes []voxelShape, region AABB, x, z float64) []voxelShape {
	if !b.initialized {
		return shapes
	}
	margin := max(math.Abs(region.MaxX-region.MinX), math.Abs(region.MaxZ-region.MinZ), 1)
	distance := min(x-b.minX, b.maxX-x, z-b.minZ, b.maxZ-z)
	if distance >= margin*2 || x < b.minX-margin || x >= b.maxX+margin || z < b.minZ-margin || z >= b.maxZ+margin {
		return shapes
	}
	return append(shapes, b.shape)
}
