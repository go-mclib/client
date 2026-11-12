package entities

import (
	"encoding/binary"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/self"
	dataentities "github.com/go-mclib/data/entities"
	"github.com/go-mclib/data/packets"
	"github.com/go-mclib/data/registries"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

// IsBoat identifies registered AbstractBoat types, including bamboo rafts.
func IsBoat(name string) bool {
	return (strings.HasSuffix(name, "_boat") || strings.HasSuffix(name, "_raft")) && dataentities.TypeID(name) >= 0
}

func (m *Module) trackEntityLocked(e *Entity) {
	if _, exists := m.entities[e.ID]; !exists {
		m.entityOrder = append(m.entityOrder, e.ID)
	}
	m.entities[e.ID] = e
}

func (m *Module) removeTrackedEntityLocked(id int32) {
	delete(m.entities, id)
	m.removeVehicleLocked(id)
	if index := slices.Index(m.entityOrder, id); index >= 0 {
		m.entityOrder = slices.Delete(m.entityOrder, index, index+1)
	}
}

func decodeEntityDelta(base float64, delta int16) float64 {
	if delta == 0 {
		return base
	}
	return (math.Floor(base*4096+0.5) + float64(delta)) / 4096
}

func (e *Entity) moveTo(x, y, z float64, yaw, pitch float32, absolute bool) {
	e.movePath(packets.PositionPath{X: ns.Float64(x), Y: ns.Float64(y), Z: ns.Float64(z)}, yaw, pitch, absolute)
}

func (e *Entity) moveDelta(delta packets.VecDelta, yaw, pitch float32) {
	if len(delta.Steps) == 0 {
		e.moveTo(decodeEntityDelta(e.X, int16(delta.X)), decodeEntityDelta(e.Y, int16(delta.Y)), decodeEntityDelta(e.Z, int16(delta.Z)), yaw, pitch, false)
		return
	}
	path := packets.PositionPath{Steps: make([]packets.PositionStep, len(delta.Steps))}
	x, y, z := e.X, e.Y, e.Z
	for i, step := range delta.Steps {
		x = decodeEntityDelta(x, int16(step.X))
		y = decodeEntityDelta(y, int16(step.Y))
		z = decodeEntityDelta(z, int16(step.Z))
		path.Steps[i] = packets.PositionStep{X: ns.Float64(x), Y: ns.Float64(y), Z: ns.Float64(z), TickOffset: step.Ticks}
	}
	e.movePath(path, yaw, pitch, false)
}

// Living entities use Java's queued SteppedInterpolationHandler, including
// tick offsets and backlog catch-up. Boats retain linear interpolation.
func (e *Entity) movePath(path packets.PositionPath, yaw, pitch float32, absolute bool) {
	x, y, z := float64(path.X), float64(path.Y), float64(path.Z)
	if len(path.Steps) > 0 {
		last := path.Steps[len(path.Steps)-1]
		x, y, z = float64(last.X), float64(last.Y), float64(last.Z)
	}
	samePosition := e.X == x && e.Y == y && e.Z == z
	if (e.interpolationSteps > 0 || len(e.positionSteps) > 0) && samePosition && e.Yaw == yaw && e.Pitch == pitch {
		return
	}
	e.X, e.Y, e.Z, e.Yaw, e.Pitch = x, y, z, yaw, pitch
	dx, dy, dz := x-e.physicsX, y-e.physicsY, z-e.physicsZ
	living := dataentities.IsAttackable(e.TypeName)
	if e.TypeName == "minecraft:shulker" || !living && !IsBoat(e.TypeName) || absolute && dx*dx+dy*dy+dz*dz > 4096 {
		e.physicsX, e.physicsY, e.physicsZ = x, y, z
		e.interpolationSteps = 0
		e.positionSteps = nil
		e.remainingTicks = 0
		e.interpolationSpeed = 1
		return
	}
	if !living {
		e.interpolationSteps = 3
		return
	}
	if len(e.positionSteps) == 0 {
		e.stepStartX, e.stepStartY, e.stepStartZ = e.physicsX, e.physicsY, e.physicsZ
		e.currentStepTicks = 1
		e.remainingTicks = 0
		e.interpolationSpeed = 1
	}
	if len(e.positionSteps)+max(len(path.Steps), 1) > maxPositionSteps {
		// Only the physics tick drains the queue. Without it (or while it is
		// paused) a backlog would grow per move packet; snap to the target.
		e.physicsX, e.physicsY, e.physicsZ = x, y, z
		e.positionSteps = nil
		e.remainingTicks = 0
		e.interpolationSpeed = 1
		return
	}
	if len(path.Steps) == 0 || samePosition {
		e.positionSteps = append(e.positionSteps, packets.PositionStep{
			X: ns.Float64(x), Y: ns.Float64(y), Z: ns.Float64(z), TickOffset: ns.VarInt(e.interpolationInterval()),
		})
		e.remainingTicks += e.interpolationInterval()
	} else {
		if len(e.positionSteps) == 0 {
			e.positionSteps = path.Steps
		} else {
			e.positionSteps = append(e.positionSteps, path.Steps...)
		}
		for _, step := range path.Steps {
			e.remainingTicks += float32(step.TickOffset)
		}
	}
}

// maxPositionSteps caps the queued interpolation backlog of one entity.
const maxPositionSteps = 64

func (e *Entity) interpolationInterval() float32 {
	// EntityTypes overrides the default three-tick interval for these living types.
	switch e.TypeName {
	case "minecraft:allay", "minecraft:mannequin", "minecraft:player":
		return 2
	default:
		return 3
	}
}

func (e *Entity) tickPositionPath() {
	for len(e.positionSteps) > 0 {
		step := e.positionSteps[0]
		offset := float32(step.TickOffset)
		if e.currentStepTicks < offset {
			alpha := float64(e.currentStepTicks / offset)
			e.physicsX = e.stepStartX + alpha*(float64(step.X)-e.stepStartX)
			e.physicsY = e.stepStartY + alpha*(float64(step.Y)-e.stepStartY)
			e.physicsZ = e.stepStartZ + alpha*(float64(step.Z)-e.stepStartZ)
			break
		}
		e.currentStepTicks -= offset
		e.stepStartX, e.stepStartY, e.stepStartZ = float64(step.X), float64(step.Y), float64(step.Z)
		e.positionSteps = e.positionSteps[1:]
	}
	if len(e.positionSteps) == 0 {
		e.physicsX, e.physicsY, e.physicsZ = e.X, e.Y, e.Z
	}
	interval := e.interpolationInterval()
	targetSpeed := max(e.remainingTicks/interval, float32(1))
	e.interpolationSpeed += (float32(1) / interval) * (targetSpeed - e.interpolationSpeed)
	ticks := e.interpolationSpeed
	if ticks >= e.remainingTicks {
		ticks = e.remainingTicks
		e.interpolationSpeed = 1
	}
	e.currentStepTicks += ticks
	e.remainingTicks -= ticks
}

// TickPhysics advances remote collision positions once per client game tick.
// Packet-facing X/Y/Z remain the codec targets used by relative movement packets;
// collision and push queries use these independently interpolated positions.
// The physics module drives it once per tick; do not call it otherwise, or
// remote entities interpolate faster than vanilla.
func (m *Module) TickPhysics() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.entityOrder {
		e := m.entities[id]
		if len(e.positionSteps) > 0 {
			e.tickPositionPath()
		} else if e.interpolationSteps > 0 {
			alpha := 1 / float64(e.interpolationSteps)
			e.physicsX += alpha * (e.X - e.physicsX)
			e.physicsY += alpha * (e.Y - e.physicsY)
			e.physicsZ += alpha * (e.Z - e.physicsZ)
			e.interpolationSteps--
		}
		if e.TypeName == "minecraft:shulker" {
			target := float32(metadataByte(e, dataentities.ShulkerIndexPeek)) * float32(0.01)
			if e.peekAmount > target {
				e.peekAmount = min(max(e.peekAmount-float32(0.05), target), 1)
			} else {
				e.peekAmount = min(max(e.peekAmount+float32(0.05), 0), target)
			}
		}
	}
}

// PhysicsEntitiesInAABB returns snapshots at client-tick positions. Unlike the
// observation APIs it also resolves pose-dependent bounds used by movement.
func (m *Module) PhysicsEntitiesInAABB(minX, minY, minZ, maxX, maxY, maxZ float64) []*Entity {
	ownID := m.ownEntityID()
	query := collisions.NewAABB(minX, minY, minZ, maxX, maxY, maxZ)
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []*Entity
	for _, id := range m.entityOrder {
		e := m.entities[id]
		if e.ID == ownID {
			continue
		}
		positioned := *e
		positioned.X, positioned.Y, positioned.Z = e.physicsX, e.physicsY, e.physicsZ
		if positioned.BoundingBox().Intersects(query) {
			result = append(result, cloneEntity(&positioned))
		}
	}
	return result
}

// BoundingBox resolves metadata-controlled collision geometry at the snapshot's
// position. A shulker lid changes collision bounds, not merely its rendered box.
func (e *Entity) BoundingBox() collisions.AABB {
	width, height := e.Width, e.Height
	pose := metadataInt(e, dataentities.EntityIndexPose)
	if dataentities.IsAttackable(e.TypeName) && pose == 2 {
		width, height = float64(float32(0.2)), float64(float32(0.2))
	} else if e.TypeName == "minecraft:player" || e.TypeName == "minecraft:mannequin" {
		switch pose {
		case 1, 3, 4: // fall flying, swimming, spin attack
			height = float64(float32(0.6))
		case 5: // crouching
			height = float64(float32(1.5))
		case 7: // dying
			width, height = float64(float32(0.2)), float64(float32(0.2))
		}
	} else {
		w, h := mobDimensions(e, pose, float32(width), float32(height))
		width, height = float64(w), float64(h)
	}
	scale := e.physicsScale()
	if dataentities.IsAttackable(e.TypeName) && pose != 2 && !((e.TypeName == "minecraft:player" || e.TypeName == "minecraft:mannequin") && pose == 7) {
		width = float64(float32(width) * scale)
		height = float64(float32(height) * scale)
	}
	box := collisions.EntityAABB(e.X, e.Y, e.Z, width, height)
	if e.TypeName == "minecraft:shulker" {
		// Shulker.getPhysicalPeek uses the same float sine table as Mth.sin.
		angle := float32(float32(0.5)+e.peekAmount) * float32(math.Pi)
		index := int(float64(angle)*10430.378350470453) & 65535
		sine := float32(math.Sin(float64(index) / 10430.378350470453))
		peek := float64(float32(0.5)-sine*float32(0.5)) * float64(scale)
		switch metadataInt(e, dataentities.ShulkerIndexAttachFace) { // lid opens opposite the attached face
		case 0:
			box.MaxY += peek
		case 1:
			box.MinY -= peek
		case 2:
			box.MaxZ += peek
		case 3:
			box.MinZ -= peek
		case 4:
			box.MaxX += peek
		case 5:
			box.MinX -= peek
		}
	}
	return box
}

func (e *Entity) physicsScale() float32 {
	scale := e.scale
	if scale == 0 {
		scale = 1
	}
	switch e.TypeName {
	case "minecraft:shulker":
		return min(scale, 3)
	case "minecraft:happy_ghast":
		return min(scale, 1)
	default:
		return scale
	}
}

func (m *Module) handleUpdateAttributes(pkt *jp.WirePacket) {
	var d packets.S2CUpdateAttributes
	if !m.client.Decode(pkt, &d) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entities[int32(d.EntityID)]
	if e == nil || !dataentities.IsAttackable(e.TypeName) {
		return
	}
	for _, attribute := range d.Attributes {
		if registries.Attribute.ByID(int32(attribute.Type)) != "minecraft:scale" {
			continue
		}
		value := self.Attribute(attribute).Value()
		if math.IsNaN(value) {
			value = 0.0625
		}
		e.scale = float32(min(max(value, 0.0625), 16))
	}
}

func metadataByte(e *Entity, index byte) byte {
	data := e.Metadata.Get(index)
	if len(data) == 0 {
		return 0
	}
	return data[0]
}

func metadataInt(e *Entity, index byte) int32 {
	data := e.Metadata.Get(index)
	value, n := binary.Uvarint(data)
	if n <= 0 {
		return 0
	}
	return int32(value)
}

func entityAlive(e *Entity) bool {
	data := e.Metadata.Get(dataentities.LivingEntityIndexHealth)
	return len(data) != 4 || math.Float32frombits(binary.BigEndian.Uint32(data)) > 0
}

func (m *Module) entityScoreName(e *Entity) (string, bool) {
	if e.TypeName == "minecraft:player" && m.playerlist != nil {
		if player := m.playerlist.GetPlayer(e.UUID); player != nil {
			return player.Name, player.Gamemode == 3
		}
	}
	return e.scoreName, false
}

// CanPushLocal applies the contextual part of EntitySelector.pushableBy and
// Entity.push. The caller checks the local player's alive/climbing/sleeping state.
func (m *Module) CanPushLocal(e *Entity) bool {
	name, spectator := m.entityScoreName(e)
	if spectator {
		return false
	}
	ownID := m.ownEntityID()
	ownName := ""
	if m.client != nil {
		ownName = m.client.Username
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.rootVehicleLocked(e.ID) == m.rootVehicleLocked(ownID) {
		return false
	}
	ownTeam, theirTeam := m.memberTeams[ownName], m.memberTeams[name]
	return teamsAllowPush(m.teams[ownTeam], m.teams[theirTeam], ownTeam != "" && ownTeam == theirTeam)
}

func teamsAllowPush(own, other int32, allied bool) bool {
	if own == 1 || other == 1 {
		return false
	}
	if (own == 3 || other == 3) && allied {
		return false
	}
	return own != 2 && other != 2 || allied
}

func (m *Module) rootVehicleLocked(id int32) int32 {
	// The packet protocol describes a forest. Bound traversal to avoid cycling
	// forever on malformed server passenger data, without allocating a visited set.
	for range len(m.vehicles) + 1 {
		vehicle, ok := m.vehicles[id]
		if !ok {
			return id
		}
		id = vehicle
	}
	return id
}

func (m *Module) removeVehicleLocked(id int32) {
	delete(m.vehicles, id)
	maps.DeleteFunc(m.vehicles, func(_, vehicle int32) bool { return vehicle == id })
}

func (m *Module) handleSetPassengers(pkt *jp.WirePacket) {
	var d packets.S2CSetPassengers
	if !m.client.Decode(pkt, &d) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	maps.DeleteFunc(m.vehicles, func(_, vehicle int32) bool { return vehicle == int32(d.EntityID) })
	for _, passenger := range d.Passengers {
		m.vehicles[int32(passenger)] = int32(d.EntityID)
	}
}

func (m *Module) handleSetPlayerTeam(pkt *jp.WirePacket) {
	var d packets.S2CSetPlayerTeam
	if !m.client.Decode(pkt, &d) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	name := string(d.TeamName)
	switch d.Method {
	case 0, 2:
		m.teams[name] = int32(d.Parameters.CollisionRule)
	case 1:
		delete(m.teams, name)
		maps.DeleteFunc(m.memberTeams, func(_, team string) bool { return team == name })
	}
	for _, player := range d.Players {
		if d.Method == 0 || d.Method == 3 {
			m.memberTeams[string(player)] = name
		} else if d.Method == 4 && m.memberTeams[string(player)] == name {
			delete(m.memberTeams, string(player))
		}
	}
}

// CollisionBoxes supplies rigid entity colliders to the optional collision
// provider interface. Ordinary mobs and players are pushers, not solid obstacles;
// team collision rules govern pushing only, not Entity.canCollideWith.
func (m *Module) CollisionBoxes(minX, minY, minZ, maxX, maxY, maxZ float64) []collisions.AABB {
	ownID := m.ownEntityID()
	playerY := minY
	if m.self != nil {
		if m.self.IsSpectator() {
			return nil
		}
		_, playerY, _ = m.self.Position()
	}
	query := collisions.NewAABB(minX, minY, minZ, maxX, maxY, maxZ)
	m.mu.RLock()
	defer m.mu.RUnlock()
	var boxes []collisions.AABB
	for _, id := range m.entityOrder {
		e := m.entities[id]
		if e.ID == ownID || m.rootVehicleLocked(e.ID) == m.rootVehicleLocked(ownID) {
			continue
		}
		if !IsBoat(e.TypeName) && e.TypeName != "minecraft:shulker" && e.TypeName != "minecraft:happy_ghast" {
			continue
		}
		positioned := *e
		positioned.X, positioned.Y, positioned.Z = e.physicsX, e.physicsY, e.physicsZ
		box := positioned.BoundingBox()
		if !box.Intersects(query) || !entityAlive(e) && !IsBoat(e.TypeName) {
			continue
		}
		if e.TypeName == "minecraft:happy_ghast" && (metadataByte(e, dataentities.AgeableMobIndexIsBaby) != 0 || playerY < box.MaxY && metadataByte(e, dataentities.HappyGhastIndexStaysStill) == 0) {
			continue
		}
		boxes = append(boxes, box)
	}
	return boxes
}
