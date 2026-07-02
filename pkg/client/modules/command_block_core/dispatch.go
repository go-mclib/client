package command_block_core

import "time"

// Dispatch queues a single command to run once through an impulse slot.
func (m *Module) Dispatch(cmd string) {
	m.mu.Lock()
	m.queue = append(m.queue, cmd)
	m.mu.Unlock()
}

// DispatchBatch queues several commands at once.
func (m *Module) DispatchBatch(cmds []string) {
	m.mu.Lock()
	m.queue = append(m.queue, cmds...)
	m.mu.Unlock()
}

// QueueLen returns the number of pending commands.
func (m *Module) QueueLen() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.queue)
}

func (m *Module) startTicker() {
	cancel := make(chan struct{})
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()

	go func() {
		t := time.NewTicker(m.cfg.TickInterval)
		defer t.Stop()
		for {
			select {
			case <-cancel:
				return
			case <-t.C:
				m.flush()
			}
		}
	}()
}

type dispatchJob struct {
	slot Pos
	cmd  string
}

// flush programs up to CommandsPerTick queued commands into impulse slots.
func (m *Module) flush() {
	m.mu.Lock()
	if m.state != StateReady || len(m.queue) == 0 || len(m.dispatch) == 0 {
		m.mu.Unlock()
		return
	}
	n := min(m.cfg.CommandsPerTick, len(m.queue))
	jobs := make([]dispatchJob, n)
	for i := range n {
		slot := m.dispatch[m.nextSlot%len(m.dispatch)]
		m.nextSlot++
		jobs[i] = dispatchJob{slot, m.queue[i]}
	}
	m.queue = m.queue[n:]
	m.mu.Unlock()

	// program outside the lock — packet writes can block on the socket
	for _, j := range jobs {
		// an impulse block only fires on an automatic false->true transition,
		// so reset then fire to re-run a slot we've used before.
		_ = m.programBlock(j.slot, j.cmd, modeImpulse, false)
		_ = m.programBlock(j.slot, j.cmd, modeImpulse, true)
	}
}
