package installer

// rollback records undo steps to run, in reverse order, if an install fails
// partway through. Each step is best-effort; errors during rollback are ignored
// so every step gets a chance to run.
type rollback struct {
	undos []func() error
}

// add registers an undo step. Steps are run in reverse of registration order.
func (r *rollback) add(undo func() error) {
	r.undos = append(r.undos, undo)
}

// run executes all registered undo steps in reverse order and clears them.
func (r *rollback) run() {
	for i := len(r.undos) - 1; i >= 0; i-- {
		_ = r.undos[i]()
	}
	r.undos = nil
}
