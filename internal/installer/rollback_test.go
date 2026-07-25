package installer

import (
	"errors"
	"testing"
)

func TestRollbackRunsInReverse(t *testing.T) {
	var order []int
	rb := &rollback{}
	rb.add(func() error { order = append(order, 1); return nil })
	rb.add(func() error { order = append(order, 2); return nil })
	rb.add(func() error { order = append(order, 3); return nil })

	rb.run()

	want := []int{3, 2, 1}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestRollbackContinuesPastErrors(t *testing.T) {
	var ran []int
	rb := &rollback{}
	rb.add(func() error { ran = append(ran, 1); return nil })
	rb.add(func() error { ran = append(ran, 2); return errors.New("boom") })
	rb.add(func() error { ran = append(ran, 3); return nil })

	rb.run()

	// all three should have run despite the middle one erroring
	if len(ran) != 3 {
		t.Errorf("ran = %v, want all three steps", ran)
	}
}

func TestRollbackClearsAfterRun(t *testing.T) {
	count := 0
	rb := &rollback{}
	rb.add(func() error { count++; return nil })
	rb.run()
	rb.run() // second run should be a no-op
	if count != 1 {
		t.Errorf("undo ran %d times, want 1", count)
	}
}
