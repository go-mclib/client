package inventory

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/go-mclib/data/items"
	"github.com/go-mclib/data/packets"
	"github.com/go-mclib/data/slots"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

// Snapshot is an independent copy of the active authoritative menu. WindowID 0
// uses all 46 player protocol slots (main 9..35, hotbar 36..44, offhand 45).
// Open menus use their protocol order: ContainerSlots menu-specific slots then
// 27 main and 9 hotbar slots, except menus without a player section (lectern).
// Ready is false before complete content arrives or after an unconfirmed click.
// Revision increases on accepted server updates and local view/selection changes;
// it never resets across sessions. StateID is the server's menu synchronization ID.
// Known reports, per player protocol slot (0..45, independent of the open
// menu), whether the server sent the slot's contents since the last reset or
// respawn; unknown slots read as empty.
type Snapshot struct {
	WindowID       int32
	StateID        int32
	Revision       uint64
	Ready          bool
	MenuType       MenuType
	Title          string
	ContainerSlots int
	Slots          []ns.Slot
	Cursor         ns.Slot
	HeldSlot       int
	Known          [slots.PlayerInvSize]bool
}

// Operation describes move, swap, split, drop, select_hotbar, close, or sort.
// From and To are absolute indices in Snapshot.Slots, never Inventory.java indices.
// From=-1 explicitly addresses the cursor for move, split, and drop; this is also
// the recovery path after cancellation leaves an authoritative cursor stack.
// WindowID must equal the active window, including for select_hotbar and close.
// Nonzero Revision additionally requires an exact snapshot revision at submission.
// Count=0 moves/drops the whole source; positive counts must fit exactly. Split
// requires a positive count and accepts an empty or component-identical destination.
// Swap exchanges complete stacks. Hotbar is 0..8 for select_hotbar. Sort compacts
// matching stacks and orders storage by item ID/component bytes; it never sorts
// crafting, equipment, input, or output slots. Other fields are unused by each kind.
type Operation struct {
	Kind                    OperationKind
	WindowID                int32
	Revision                uint64
	From, To, Count, Hotbar int
}

// OperationResult contains the last authoritative view, including a cursor left
// behind by an interrupted operation. Clicks counts attempted click submissions,
// including synchronization probes. Status is confirmed only after server content
// proves the requested result (or it was already satisfied), sent for hotbar
// selection (vanilla has no acknowledgement), partial after interrupted click
// work, or failed before any click. Errors never imply rollback. Do not retry a
// drop blindly: an unacknowledged packet may already have executed on the server.
type OperationResult struct {
	Status   OperationStatus
	Clicks   int
	Snapshot Snapshot
}

// OperationKind selects what Execute does; see Operation for per-kind fields.
type OperationKind string

const (
	OpMove         OperationKind = "move"
	OpSplit        OperationKind = "split"
	OpSwap         OperationKind = "swap"
	OpDrop         OperationKind = "drop"
	OpSelectHotbar OperationKind = "select_hotbar"
	OpClose        OperationKind = "close"
	OpSort         OperationKind = "sort"
)

// OperationStatus reports how far an operation got; see OperationResult.
type OperationStatus string

const (
	StatusConfirmed OperationStatus = "confirmed"
	StatusSent      OperationStatus = "sent"
	StatusPartial   OperationStatus = "partial"
	StatusFailed    OperationStatus = "failed"
)

const (
	inventoryOperationTimeout = 2 * time.Minute
	inventoryClickTimeout     = 5 * time.Second
	inventoryClickLimit       = 512
)

var errInventoryChanged = errors.New("inventory changed or click rejected by server")

type inventorySession struct {
	ctx    context.Context
	cancel context.CancelFunc
	send   func(context.Context, jp.Packet) error
}

func (m *Module) startSession() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session != nil {
		m.session.cancel()
	}
	m.epoch++
	session := m.client.Session()
	if session == nil || session.Conn() == nil {
		m.session = nil
		return
	}
	// The inventory session also ends on module reset, before the client
	// session does.
	ctx, cancel := context.WithCancel(session.Context())
	s := &inventorySession{ctx: ctx, cancel: cancel}
	s.send = func(ctx context.Context, packet jp.Packet) error {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		return session.Write(ctx, inventoryPacket{Packet: packet, session: s.ctx})
	}
	m.session = s
	m.notifyLocked()
}

// inventoryPacket is not serialized once its inventory session ended.
type inventoryPacket struct {
	jp.Packet
	session context.Context
}

func (p inventoryPacket) Write(buf *ns.PacketBuffer) error {
	if err := p.session.Err(); err != nil {
		return err
	}
	return p.Packet.Write(buf)
}

// Snapshot returns a deep copy; callers may modify slots and component bytes.
func (m *Module) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshotLocked()
}

func (m *Module) snapshotLocked() Snapshot {
	s := Snapshot{StateID: m.stateID, Revision: m.revision, Ready: m.ready,
		MenuType: MenuPlayer, Cursor: cloneRawSlot(m.cursor.raw), HeldSlot: m.heldSlot, Known: m.known}
	if c := m.container; c != nil {
		s.WindowID, s.StateID, s.MenuType, s.Title = c.windowID, c.stateID, c.menuType, c.title
		s.ContainerSlots, s.Ready = len(c.slots), c.ready
		s.Slots = make([]ns.Slot, len(c.view))
		for i := range c.view {
			s.Slots[i] = cloneRawSlot(c.view[i].raw)
		}
	} else {
		s.Slots = make([]ns.Slot, slots.PlayerInvSize)
		for i := range m.slots {
			s.Slots[i] = cloneRawSlot(m.slots[i].raw)
		}
	}
	return s
}

type inventoryTransaction struct {
	m       *Module
	ctx     context.Context
	session *inventorySession
	epoch   uint64
	window  int32
	clicks  int
	current Snapshot
}

func (m *Module) beginTransaction(ctx context.Context, op Operation) (*inventoryTransaction, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, inventoryOperationTimeout)
	m.mu.Lock()
	if m.transactions == nil {
		m.transactions = make(chan struct{}, 1)
	}
	gate, epoch, session := m.transactions, m.epoch, m.session
	m.mu.Unlock()
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		cancel()
		return nil, nil, ctx.Err()
	}
	done := func() { <-gate; cancel() }
	m.mu.RLock()
	snapshot := m.snapshotLocked()
	valid := epoch == m.epoch && session == m.session
	m.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		done()
		return nil, nil, err
	}
	if !valid || session == nil || session.ctx.Err() != nil {
		done()
		return nil, nil, errors.New("inventory session changed or inactive")
	}
	if snapshot.WindowID != op.WindowID || (op.Revision != 0 && snapshot.Revision != op.Revision) {
		done()
		return nil, nil, errors.New("stale inventory window or revision")
	}
	stop := context.AfterFunc(session.ctx, cancel)
	return &inventoryTransaction{m: m, ctx: ctx, session: session, epoch: epoch, window: op.WindowID, current: snapshot},
		func() { stop(); done() }, nil
}

// Execute serializes the complete operation, waits up to five seconds per click
// and two minutes overall, and submits at most 512 clicks. Call outside packet
// callbacks: dispatch must continue while Execute awaits authoritative updates.
// It never predicts local slots. Cancellation/session or window replacement stops
// further clicks. Existing cursor contents are not displaced by multi-click work;
// use move with From=-1 to place them first. On a non-cancellation failure, a
// known cursor is returned to its source only if that is demonstrably safe.
// Otherwise the returned Snapshot exposes it for explicit recovery.
func (m *Module) Execute(ctx context.Context, op Operation) (result OperationResult, err error) {
	tx, done, err := m.beginTransaction(ctx, op)
	if err != nil {
		return OperationResult{Status: StatusFailed, Snapshot: m.Snapshot()}, err
	}
	defer done()
	defer func() {
		result.Clicks, result.Snapshot = tx.clicks, m.Snapshot()
		if err != nil {
			result.Status = StatusFailed
			if tx.clicks > 0 {
				result.Status = StatusPartial
			}
		}
	}()
	if err = tx.ensureReady(); err != nil {
		return result, err
	}
	result.Status = StatusConfirmed
	switch op.Kind {
	case OpSelectHotbar:
		if op.Hotbar < 0 || op.Hotbar > 8 {
			return result, errors.New("hotbar must be 0..8")
		}
		if err = tx.selectHotbar(op.Hotbar, tx.send); err != nil {
			return result, err
		}
		result.Status = StatusSent
	case OpClose:
		err = tx.close()
	case OpMove, OpSplit, OpSwap, OpDrop, OpSort:
		cursorOperation := op.From == -1 && (op.Kind == OpMove || op.Kind == OpSplit || op.Kind == OpDrop)
		if !tx.current.Cursor.IsEmpty() && !cursorOperation {
			return result, errors.New("cursor is not empty; place it with move From=-1 before starting an operation")
		}
		switch op.Kind {
		case OpMove, OpSplit:
			if op.Kind == OpSplit && op.Count <= 0 {
				return result, errors.New("split count must be positive")
			}
			err = tx.move(op.From, op.To, op.Count)
		case OpSwap:
			err = tx.swap(op.From, op.To)
		case OpDrop:
			err = tx.drop(op.From, op.Count)
		case OpSort:
			err = tx.sort()
		}
	default:
		err = fmt.Errorf("unknown inventory operation %q", op.Kind)
	}
	if err == nil {
		m.mu.RLock()
		err = tx.checkLocked()
		if err == nil && result.Status == StatusConfirmed && !m.matchesLocked(tx.current) {
			err = errInventoryChanged
		}
		m.mu.RUnlock()
	}
	return result, err
}

// beginCurrent starts a transaction on the current window and revision and
// resynchronizes an unready view first.
func (m *Module) beginCurrent(ctx context.Context) (*inventoryTransaction, func(), error) {
	snapshot := m.Snapshot()
	tx, done, err := m.beginTransaction(ctx, Operation{WindowID: snapshot.WindowID, Revision: snapshot.Revision})
	if err != nil {
		return nil, nil, err
	}
	if err := tx.ensureReady(); err != nil {
		done()
		return nil, nil, err
	}
	return tx, done, nil
}

// ensureReady requests a full resync when the view is not ready and fails
// when the menu layout is still unsupported afterwards.
func (tx *inventoryTransaction) ensureReady() error {
	if tx.current.Ready {
		return nil
	}
	if err := tx.click(-1, 0, 0, nil); err != nil {
		return err
	}
	if !tx.current.Ready {
		return errors.New("unsupported or incomplete menu layout")
	}
	return nil
}

// selectHotbar sends a held-slot change through send and, if the transaction
// is still current, records it and notifies observers.
func (tx *inventoryTransaction) selectHotbar(slot int, send func(jp.Packet) error) error {
	if err := send(&packets.C2SSetCarriedItem{Slot: ns.Int16(slot)}); err != nil {
		return err
	}
	m := tx.m
	m.mu.Lock()
	if err := tx.checkLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	m.heldSlot = slot
	m.notifyLocked()
	m.mu.Unlock()
	for _, cb := range m.onHeldSlotChange {
		cb(slot)
	}
	return nil
}

func (tx *inventoryTransaction) checkLocked() error {
	if err := tx.ctx.Err(); err != nil {
		return err
	}
	if err := tx.session.ctx.Err(); err != nil {
		return err
	}
	if tx.m.session != tx.session || tx.m.epoch != tx.epoch {
		return errors.New("inventory window or session replaced")
	}
	return nil
}

func (tx *inventoryTransaction) send(packet jp.Packet) error {
	tx.m.mu.RLock()
	err := tx.checkLocked()
	tx.m.mu.RUnlock()
	if err != nil {
		return err
	}
	return tx.transmit(tx.ctx, packet, false)
}

type transactionPacket struct {
	jp.Packet
	tx            *inventoryTransaction
	checkContents bool
}

func (p transactionPacket) Write(buf *ns.PacketBuffer) error {
	p.tx.m.mu.RLock()
	defer p.tx.m.mu.RUnlock()
	if err := p.tx.checkLocked(); err != nil {
		return err
	}
	if p.checkContents && !p.tx.m.matchesLocked(p.tx.current) {
		return errInventoryChanged
	}
	return p.Packet.Write(buf)
}

func (tx *inventoryTransaction) transmit(ctx context.Context, packet jp.Packet, checkContents bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	guarded := transactionPacket{Packet: packet, tx: tx, checkContents: checkContents}
	return tx.session.send(ctx, guarded)
}

// An intentionally mismatched state ID asks vanilla to execute the click and
// send its entire resulting menu, including cursor. Vanilla masks state IDs to
// 0..32767, so -1 cannot match. Empty prediction alone would NOT acknowledge a
// no-op/rejection, and correct hashes would suppress the updates we need.
func (tx *inventoryTransaction) click(slot, button, mode int, expected *Snapshot) error {
	if tx.clicks >= inventoryClickLimit {
		return errors.New("inventory click limit exceeded")
	}
	m := tx.m
	m.mu.RLock()
	err := tx.checkLocked()
	before := m.contentRevision
	if err == nil && expected != nil && !m.matchesLocked(tx.current) {
		err = errInventoryChanged
	}
	m.mu.RUnlock()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(tx.ctx, inventoryClickTimeout)
	defer cancel()
	tx.clicks++
	err = tx.transmit(ctx, &packets.C2SContainerClick{
		WindowID: ns.VarInt(tx.window), StateID: -1, Slot: ns.Int16(slot),
		Button: ns.Int8(button), Mode: ns.VarInt(mode), CarriedItem: ns.EmptyHashedSlot(),
	}, expected != nil)
	if err != nil {
		tx.invalidate()
		return err
	}
	for {
		m.mu.RLock()
		err = tx.checkLocked()
		snapshot, revision, changed := m.snapshotLocked(), m.contentRevision, m.changed
		m.mu.RUnlock()
		if err != nil {
			tx.invalidate()
			return err
		}
		if revision > before {
			tx.current = snapshot
			if expected != nil && !sameContents(snapshot, *expected) {
				return errInventoryChanged
			}
			return nil
		}
		select {
		case <-ctx.Done():
			tx.invalidate()
			return ctx.Err()
		case <-changed:
		}
	}
}

func (tx *inventoryTransaction) invalidate() {
	m := tx.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.epoch != tx.epoch || m.session != tx.session {
		return
	}
	if m.container != nil {
		m.container.ready = false
	} else {
		m.ready = false
	}
	m.notifyLocked()
}

func (m *Module) matchesLocked(s Snapshot) bool {
	if !sameStack(m.cursor.raw, s.Cursor) {
		return false
	}
	if c := m.container; c != nil {
		if c.windowID != s.WindowID || len(c.view) != len(s.Slots) {
			return false
		}
		for i := range c.view {
			if !sameStack(c.view[i].raw, s.Slots[i]) {
				return false
			}
		}
		return true
	}
	if s.WindowID != 0 || len(s.Slots) != slots.PlayerInvSize {
		return false
	}
	for i := range m.slots {
		if !sameStack(m.slots[i].raw, s.Slots[i]) {
			return false
		}
	}
	return true
}

func sameContents(a, b Snapshot) bool {
	if a.WindowID != b.WindowID || len(a.Slots) != len(b.Slots) || !sameStack(a.Cursor, b.Cursor) {
		return false
	}
	for i := range a.Slots {
		if !sameStack(a.Slots[i], b.Slots[i]) {
			return false
		}
	}
	return true
}

func sameStack(a, b ns.Slot) bool {
	if a.IsEmpty() || b.IsEmpty() {
		return a.IsEmpty() && b.IsEmpty()
	}
	return a.Count == b.Count && sameIdentity(a, b)
}

// Component patches are maps/sets, not ordered lists. Compare all raw bytes;
// item ID alone would merge renamed, damaged, enchanted, or container items.
func sameIdentity(a, b ns.Slot) bool {
	if a.ItemID != b.ItemID || len(a.Components.Add) != len(b.Components.Add) || len(a.Components.Remove) != len(b.Components.Remove) {
		return false
	}
	for _, x := range a.Components.Add {
		if !slices.ContainsFunc(b.Components.Add, func(y ns.RawSlotComponent) bool {
			return x.ID == y.ID && bytes.Equal(x.Data, y.Data)
		}) {
			return false
		}
	}
	for _, id := range a.Components.Remove {
		if !slices.Contains(b.Components.Remove, id) {
			return false
		}
	}
	return true
}

func withCount(s ns.Slot, count int) ns.Slot {
	if count <= 0 {
		return ns.EmptySlot()
	}
	s.Count = ns.VarInt(count)
	return s
}

func copySnapshot(s Snapshot) Snapshot { s.Slots = slices.Clone(s.Slots); return s }

func (tx *inventoryTransaction) slot(index int) (ns.Slot, error) {
	if index < 0 || index >= len(tx.current.Slots) {
		return ns.Slot{}, fmt.Errorf("slot %d outside active menu", index)
	}
	return tx.current.Slots[index], nil
}

// Output slots can consume inputs/remainders; a generic stack transaction cannot
// promise those side effects. Raw click helpers remain available for crafting.
func (tx *inventoryTransaction) writable(index int) error {
	if _, err := tx.slot(index); err != nil {
		return err
	}
	menu := tx.current.MenuType
	output := -1
	switch menu {
	case MenuPlayer, MenuCrafting:
		output = 0
	case MenuAnvil, MenuBlastFurnace, MenuFurnace, MenuGrindstone, MenuMerchant, MenuSmoker, MenuCartography:
		output = 2
	case MenuLoom, MenuSmithing:
		output = 3
	case MenuStonecutter:
		output = 1
	case MenuLectern:
		return errors.New("lectern slots cannot be manipulated by inventory clicks")
	}
	if index == output {
		return errors.New("output slots require menu-specific crafting actions")
	}
	return nil
}

func (tx *inventoryTransaction) slotLimit(index int, stack ns.Slot) int {
	limit := min(99, int(items.SlotMaxStackSize(stack)))
	if tx.window == 0 && index >= slots.PlayerArmorHead && index <= slots.PlayerArmorFeet {
		return 1
	}
	if tx.window != 0 && index < tx.current.ContainerSlots {
		switch tx.current.MenuType {
		case MenuBrewingStand:
			if index < 3 {
				return 1
			}
		case MenuBeacon:
			return 1
		case MenuEnchantment:
			if index == 0 {
				return 1
			}
		case MenuFurnace, MenuBlastFurnace, MenuSmoker:
			if index == 1 && stack.ItemID == ns.VarInt(items.ID("minecraft:bucket")) {
				return 1
			}
		}
	}
	return limit
}

func (tx *inventoryTransaction) pickup(index, button int) error {
	slot, err := tx.slot(index)
	if err != nil {
		return err
	}
	cursor := tx.current.Cursor
	expected := copySnapshot(tx.current)
	switch {
	case cursor.IsEmpty():
		count := int(slot.Count)
		if button == 1 {
			count = (count + 1) / 2
		}
		expected.Cursor, expected.Slots[index] = withCount(slot, count), withCount(slot, int(slot.Count)-count)
	case slot.IsEmpty() || sameIdentity(slot, cursor):
		count := min(int(cursor.Count), tx.slotLimit(index, cursor)-int(slot.Count))
		if button == 1 {
			count = min(count, 1)
		}
		if count < 0 {
			return errors.New("invalid overfull destination stack")
		}
		expected.Slots[index], expected.Cursor = withCount(cursor, int(slot.Count)+count), withCount(cursor, int(cursor.Count)-count)
	default:
		if int(cursor.Count) > tx.slotLimit(index, cursor) {
			return errors.New("cursor exceeds destination slot limit")
		}
		expected.Slots[index], expected.Cursor = cursor, slot
	}
	return tx.click(index, button, 0, &expected)
}

func (tx *inventoryTransaction) move(from, to, count int) (err error) {
	if from != -1 {
		if err = tx.writable(from); err != nil {
			return err
		}
	}
	if err = tx.writable(to); err != nil {
		return err
	}
	if from == to {
		return errors.New("source and destination must differ")
	}
	source, dest := tx.current.Cursor, tx.current.Slots[to]
	if from != -1 {
		source = tx.current.Slots[from]
	}
	if source.IsEmpty() {
		return errors.New("source slot is empty")
	}
	if count == 0 {
		count = int(source.Count)
	}
	if count < 1 || count > int(source.Count) {
		return errors.New("count exceeds source stack")
	}
	if !dest.IsEmpty() && !sameIdentity(source, dest) {
		return errors.New("destination contains a component-distinct item; use swap")
	}
	if int(dest.Count)+count > tx.slotLimit(to, source) {
		return errors.New("requested move exceeds destination stack limit")
	}
	work := count + 2
	if count == int(source.Count) {
		work = 2
	}
	if work+tx.clicks > inventoryClickLimit {
		return errors.New("requested move exceeds click limit")
	}
	if from != -1 {
		defer func() {
			if err != nil {
				tx.recoverCursor(from, source)
			}
		}()
		if err = tx.pickup(from, 0); err != nil {
			return err
		}
	}
	if count == int(source.Count) {
		return tx.pickup(to, 0)
	}
	for range count {
		if err = tx.pickup(to, 1); err != nil {
			return err
		}
	}
	if from != -1 {
		return tx.pickup(from, 0)
	}
	return nil
}

func (tx *inventoryTransaction) swap(from, to int) (err error) {
	if err = tx.writable(from); err != nil {
		return err
	}
	if err = tx.writable(to); err != nil {
		return err
	}
	if from == to {
		return nil
	}
	a, b := tx.current.Slots[from], tx.current.Slots[to]
	if sameStack(a, b) {
		return nil
	}
	if a.IsEmpty() {
		return tx.move(to, from, 0)
	}
	if b.IsEmpty() {
		return tx.move(from, to, 0)
	}
	if int(a.Count) > tx.slotLimit(to, a) || int(b.Count) > tx.slotLimit(from, b) {
		return errors.New("swap exceeds destination slot limit")
	}
	if sameIdentity(a, b) {
		if a.Count > b.Count {
			return tx.move(from, to, int(a.Count-b.Count))
		}
		return tx.move(to, from, int(b.Count-a.Count))
	}
	if tx.clicks+3 > inventoryClickLimit {
		return errors.New("swap exceeds click limit")
	}
	recoverIdentity := a
	defer func() {
		if err != nil {
			tx.recoverCursor(from, recoverIdentity)
		}
	}()
	if err = tx.pickup(from, 0); err != nil {
		return err
	}
	if err = tx.pickup(to, 0); err != nil {
		return err
	}
	recoverIdentity = b
	return tx.pickup(from, 0)
}

func (tx *inventoryTransaction) hotbarSwap(from, hotbar int) error {
	if err := tx.writable(from); err != nil {
		return err
	}
	if hotbar < 0 || hotbar > 8 {
		return errors.New("hotbar must be 0..8")
	}
	to := slots.PlayerHotbarStart + hotbar
	if tx.window != 0 {
		to = tx.current.ContainerSlots + 27 + hotbar
	}
	if _, err := tx.slot(to); err != nil {
		return err
	}
	if from == to {
		return nil
	}
	if int(tx.current.Slots[to].Count) > tx.slotLimit(from, tx.current.Slots[to]) {
		return errors.New("hotbar stack exceeds destination slot limit")
	}
	expected := copySnapshot(tx.current)
	expected.Slots[from], expected.Slots[to] = expected.Slots[to], expected.Slots[from]
	return tx.click(from, hotbar, 2, &expected)
}

func (tx *inventoryTransaction) drop(from, count int) error {
	source := tx.current.Cursor
	if from != -1 {
		if err := tx.writable(from); err != nil {
			return err
		}
		source = tx.current.Slots[from]
	}
	if source.IsEmpty() {
		return errors.New("source slot is empty")
	}
	if count == 0 {
		count = int(source.Count)
	}
	if count < 1 || count > int(source.Count) {
		return errors.New("count exceeds source stack")
	}
	whole := count == int(source.Count)
	work := count
	if whole {
		work = 1
	}
	if work+tx.clicks > inventoryClickLimit {
		return errors.New("requested drop exceeds click limit")
	}
	for range work {
		expected := copySnapshot(tx.current)
		amount, button := 1, 0
		if whole {
			amount, button = count, 1
		}
		slot, mode := from, 4
		if from == -1 {
			slot, mode = -999, 0
			if whole {
				button = 0
			} else {
				button = 1
			}
			expected.Cursor = withCount(expected.Cursor, int(expected.Cursor.Count)-amount)
		} else {
			expected.Slots[from] = withCount(expected.Slots[from], int(expected.Slots[from].Count)-amount)
		}
		if err := tx.click(slot, button, mode, &expected); err != nil {
			return err
		}
	}
	return nil
}

func (tx *inventoryTransaction) recoverCursor(from int, identity ns.Slot) {
	// No recovery writes after cancellation, timeout, resync uncertainty, or view
	// replacement. Never drop a cursor or overwrite an unsolicited replacement.
	if tx.ctx.Err() != nil {
		return
	}
	m := tx.m
	m.mu.RLock()
	valid := tx.checkLocked() == nil
	current := m.snapshotLocked()
	m.mu.RUnlock()
	if !valid || !current.Ready || current.Cursor.IsEmpty() || from < 0 || from >= len(current.Slots) {
		return
	}
	if !sameIdentity(current.Cursor, identity) {
		return
	}
	dest := current.Slots[from]
	if !dest.IsEmpty() && !sameIdentity(dest, identity) {
		return
	}
	if int(dest.Count)+int(current.Cursor.Count) > tx.slotLimit(from, identity) {
		return
	}
	tx.current = current
	_ = tx.pickup(from, 0)
}

func (tx *inventoryTransaction) close() error {
	if tx.window == 0 {
		return nil
	}
	if err := tx.send(&packets.C2SContainerClose{WindowID: ns.VarInt(tx.window)}); err != nil {
		return err
	}
	m := tx.m
	m.mu.Lock()
	if err := tx.checkLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	m.container = nil
	m.ready = false
	m.epoch++
	tx.epoch, tx.window = m.epoch, 0
	m.notifyLocked()
	tx.current = m.snapshotLocked()
	m.mu.Unlock()
	for _, cb := range m.onContainerClose {
		cb()
	}
	// Vanilla does not acknowledge client close. A no-op click on window 0
	// proves the server switched to the player menu, and recovers returned cursor.
	return tx.click(-1, 0, 0, nil)
}

func storageRange(s Snapshot) (int, int, error) {
	if s.WindowID == 0 {
		return slots.PlayerMainStart, slots.PlayerHotbarEnd, nil
	}
	if s.MenuType >= MenuGeneric9x1 && s.MenuType <= MenuGeneric3x3 || s.MenuType == MenuHopper || s.MenuType == MenuShulkerBox {
		return 0, s.ContainerSlots, nil
	}
	return 0, 0, errors.New("sort supports player storage, chests, barrels, dispensers, droppers, hoppers, and shulker boxes")
}

func (tx *inventoryTransaction) sort() error {
	start, end, err := storageRange(tx.current)
	if err != nil {
		return err
	}
	// First compact only exactly matching component patches.
	for i := start; i < end; i++ {
		if tx.current.Slots[i].IsEmpty() {
			continue
		}
		for j := i + 1; j < end; j++ {
			a, b := tx.current.Slots[i], tx.current.Slots[j]
			if b.IsEmpty() || !sameIdentity(a, b) {
				continue
			}
			count := min(int(b.Count), int(items.SlotMaxStackSize(a))-int(a.Count))
			if count > 0 {
				if err := tx.move(j, i, count); err != nil {
					return err
				}
			}
		}
	}
	keys := make([][]byte, end-start)
	for i := range keys {
		keys[i] = componentKey(tx.current.Slots[start+i])
	}
	for i := start; i < end; i++ {
		best := i
		for j := i + 1; j < end; j++ {
			if compareSlots(tx.current.Slots[j], tx.current.Slots[best], keys[j-start], keys[best-start]) < 0 {
				best = j
			}
		}
		if best != i {
			if err := tx.swap(i, best); err != nil {
				return err
			}
			keys[i-start], keys[best-start] = keys[best-start], keys[i-start]
		}
	}
	return nil
}

func compareSlots(a, b ns.Slot, aKey, bKey []byte) int {
	if a.IsEmpty() {
		if b.IsEmpty() {
			return 0
		}
		return 1
	}
	if b.IsEmpty() {
		return -1
	}
	return cmp.Or(
		cmp.Compare(a.ItemID, b.ItemID),
		bytes.Compare(aKey, bKey),
		cmp.Compare(b.Count, a.Count),
	)
}

func componentKey(s ns.Slot) []byte {
	if s.IsEmpty() || len(s.Components.Add) == 0 && len(s.Components.Remove) == 0 {
		return nil
	}
	add := slices.Clone(s.Components.Add)
	remove := slices.Clone(s.Components.Remove)
	slices.SortFunc(add, func(a, b ns.RawSlotComponent) int { return cmp.Compare(a.ID, b.ID) })
	slices.Sort(remove)
	buf := ns.NewWriter()
	buf.WriteVarInt(ns.VarInt(len(add)))
	for _, c := range add {
		buf.WriteVarInt(c.ID)
		buf.WriteVarInt(ns.VarInt(len(c.Data)))
		buf.WriteFixedByteArray(c.Data)
	}
	buf.WriteVarInt(ns.VarInt(len(remove)))
	for _, id := range remove {
		buf.WriteVarInt(id)
	}
	return buf.Bytes()
}
