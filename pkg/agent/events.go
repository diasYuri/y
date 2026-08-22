package agent

// setState updates the lifecycle state and broadcasts the transition.
func (a *Agent) setState(state State) {
	a.mu.Lock()
	a.state = state
	a.mu.Unlock()
	a.emit(Event{Kind: EventStateChanged, State: state})
}

// emit sends an event to the configured primary sink and to all subscribers.
// The subscriber set is snapshotted before callbacks run so callbacks may
// safely subscribe or unsubscribe themselves.
func (a *Agent) emit(event Event) {
	a.mu.Lock()
	sink := a.onEvent
	a.mu.Unlock()
	if sink != nil {
		sink(event)
	}

	a.sinksMu.RLock()
	sinks := make([]EventSink, 0, len(a.sinks))
	for _, subscribed := range a.sinks {
		sinks = append(sinks, subscribed)
	}
	a.sinksMu.RUnlock()

	for _, subscribed := range sinks {
		if subscribed != nil {
			subscribed(event)
		}
	}
}
