package qdc507

import "os"

// Abandon releases host resources and process claims without contacting the
// module. Halo uses it when the original USB generation is gone; signalling
// a replacement device at the same port would be unsafe.
func (r *Runtime) Abandon() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	snapshot := r.snapshot
	r.snapshot = ""
	r.stage = ""
	processes := r.toStop
	r.toStop = nil
	r.mu.Unlock()
	for _, process := range processes {
		r.release(process)
	}
	if snapshot != "" {
		return os.RemoveAll(snapshot)
	}
	return nil
}
