package collisions

import (
	"math"

	"github.com/go-mclib/data/hitboxes"
	"github.com/go-mclib/data/packet_ids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
)

type borderState struct {
	initialized              bool
	x, z, limit              float64
	from, to, size, previous float64
	duration, remaining      int64
	minX, minZ, maxX, maxZ   float64
	parts                    [4]hitboxes.AABB
	xCoords, zCoords         [4]float64
	yCoords                  [2]float64
}

func defaultBorder() borderState {
	b := borderState{initialized: true, limit: 29999984}
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
	clamp := func(v float64) float64 { return math.Max(-b.limit, math.Min(b.limit, v)) }
	b.minX, b.maxX = clamp(b.x-b.previous/2), clamp(b.x+b.previous/2)
	b.minZ, b.maxZ = clamp(b.z-b.previous/2), clamp(b.z+b.previous/2)
	minX, maxX, minZ, maxZ := math.Floor(b.minX), math.Ceil(b.maxX), math.Floor(b.minZ), math.Ceil(b.maxZ)
	inf := math.Inf(1)
	b.xCoords = [4]float64{-inf, minX, maxX, inf}
	b.yCoords = [2]float64{-inf, inf}
	b.zCoords = [4]float64{-inf, minZ, maxZ, inf}
	b.parts = [4]hitboxes.AABB{
		{MinX: -inf, MinY: -inf, MinZ: -inf, MaxX: minX, MaxY: inf, MaxZ: inf},
		{MinX: maxX, MinY: -inf, MinZ: -inf, MaxX: inf, MaxY: inf, MaxZ: inf},
		{MinX: minX, MinY: -inf, MinZ: -inf, MaxX: maxX, MaxY: inf, MaxZ: minZ},
		{MinX: minX, MinY: -inf, MinZ: maxZ, MaxX: maxX, MaxY: inf, MaxZ: inf},
	}
}

// TickBorder follows ClientLevel.tick, after entity movement, not elapsed wall
// time. In 26.2 border interpolation duration and progress are measured in ticks.
func (m *Module) TickBorder() {
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

func (m *Module) HandlePacket(pkt *jp.WirePacket) {
	if m.client == nil || m.client.State() != jp.StatePlay {
		return
	}
	b := &m.border
	switch pkt.PacketID {
	case packet_ids.S2CInitializeBorderID:
		var p packets.S2CInitializeBorder
		if pkt.ReadInto(&p) != nil {
			return
		}
		b.initialized = true
		b.x, b.z, b.limit = float64(p.X), float64(p.Z), float64(p.PortalTeleportBoundary)
		b.setSize(float64(p.OldDiameter), float64(p.NewDiameter), int64(p.Speed))
	case packet_ids.S2CSetBorderCenterID:
		var p packets.S2CSetBorderCenter
		if pkt.ReadInto(&p) != nil {
			return
		}
		b.x, b.z = float64(p.X), float64(p.Z)
		b.updateBounds()
	case packet_ids.S2CSetBorderLerpSizeID:
		var p packets.S2CSetBorderLerpSize
		if pkt.ReadInto(&p) != nil {
			return
		}
		b.setSize(float64(p.OldDiameter), float64(p.NewDiameter), int64(p.Speed))
	case packet_ids.S2CSetBorderSizeID:
		var p packets.S2CSetBorderSize
		if pkt.ReadInto(&p) != nil {
			return
		}
		b.setSize(float64(p.Diameter), float64(p.Diameter), 0)
	}
}

func (m *Module) appendBorder(shapes []voxelShape, region AABB, x, z float64) []voxelShape {
	b := &m.border
	if !b.initialized {
		return shapes
	}
	margin := math.Max(math.Max(math.Abs(region.MaxX-region.MinX), math.Abs(region.MaxZ-region.MinZ)), 1)
	distance := math.Min(math.Min(x-b.minX, b.maxX-x), math.Min(z-b.minZ, b.maxZ-z))
	if distance >= margin*2 || x < b.minX-margin || x >= b.maxX+margin || z < b.minZ-margin || z >= b.maxZ+margin {
		return shapes
	}
	return append(shapes, voxelShape{parts: b.parts[:], coords: [3][]float64{b.xCoords[:], b.yCoords[:], b.zCoords[:]}})
}
