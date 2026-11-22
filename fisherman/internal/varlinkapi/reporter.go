package varlinkapi

import "sync"

// reporter turns an Executor's step callbacks into Install events. It owns
// the step index, the total, and the overall percentage, so every backend
// produces the same stream shape and a bar that never runs backwards.
type reporter struct {
	mu    sync.Mutex
	emit  func(Event)
	steps []string
	index map[string]int
	total int // sum of planned step weights
	cur   string
	open  bool // cur started and not done
	max   int  // highest overallPct sent
}

func newReporter(plan InstallPlan, emit func(Event)) *reporter {
	r := &reporter{emit: emit, steps: PlannedSteps(plan), index: map[string]int{}}
	for i, s := range r.steps {
		r.index[s] = i
		r.total += stepWeights[s]
	}
	return r
}

// pct converts a position inside a step to the overall percentage. Before
// the complete event the bar stops at 99.
func (r *reporter) pct(step string, within int) int {
	i, ok := r.index[step]
	if !ok || r.total == 0 {
		return r.max
	}
	base := 0
	for _, s := range r.steps[:i] {
		base += stepWeights[s]
	}
	w := stepWeights[step]
	p := (base*100 + w*within) / r.total
	if p > 99 {
		p = 99
	}
	if p < r.max {
		p = r.max
	}
	r.max = p
	return p
}

func (r *reporter) event(kind, step string, overall int) Event {
	ev := Event{Event: kind, OverallPct: overall}
	if step != "" {
		ev.StepID = ptr(step)
		if i, ok := r.index[step]; ok {
			ev.StepIndex = ptr(i + 1)
			ev.TotalSteps = ptr(len(r.steps))
		}
	}
	return ev
}

func (r *reporter) known(step string) bool {
	_, ok := r.index[step]
	return ok || step == StepCleanup
}

func (r *reporter) finishLocked() {
	if r.open {
		r.open = false
		r.emit(r.event(EvStepFinished, r.cur, r.pct(r.cur, 100)))
	}
}

func (r *reporter) Start(step string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.known(step) {
		return
	}
	if step == StepCleanup {
		// Cleanup follows a failure or a cancel; it must not close the step
		// that failed as if it had succeeded.
		r.emit(r.event(EvStepStarted, step, r.max))
		return
	}
	r.finishLocked()
	r.cur, r.open = step, true
	r.emit(r.event(EvStepStarted, step, r.pct(step, 0)))
}

func (r *reporter) Progress(step string, pct int, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.known(step) {
		return
	}
	pct = max(0, min(100, pct))
	ev := r.event(EvProgress, step, r.pct(step, pct))
	ev.stepPct = pct
	if message != "" {
		ev.Message = ptr(message)
	}
	r.emit(ev)
}

func (r *reporter) Warn(step, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev := r.event(EvWarning, step, r.max)
	ev.Message = ptr(message)
	r.emit(ev)
}

func (r *reporter) Done(step string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if step == StepCleanup {
		r.emit(r.event(EvStepFinished, step, r.max))
		return
	}
	if !r.known(step) || !r.open || r.cur != step {
		return
	}
	r.finishLocked()
}

// finish closes the last open step. The service calls it when the Executor
// returns without an error.
func (r *reporter) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishLocked()
}

// last returns the highest overallPct sent.
func (r *reporter) last() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.max
}

// current returns the step that was running, for a failure report.
func (r *reporter) current() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cur
}
