package rt

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

const (
	schedDeadline = 6
	// A deadline task that forks gets children of the normal class: the
	// kernel refuses a fork of a deadline task otherwise.
	schedFlagResetOnFork = 0x01
)

// SetDeadline gives task tid (0: the calling thread) the reservation. The
// kernel refuses it with EBUSY when the bandwidth of the task's root domain
// does not take it, with EPERM when the task may run outside that domain.
func SetDeadline(tid int, d Deadline) error {
	attr := unix.SchedAttr{Policy: schedDeadline, Flags: schedFlagResetOnFork,
		Runtime: uint64(d.Runtime), Deadline: uint64(d.Deadline), Period: uint64(d.Period)}
	err := unix.SchedSetAttr(tid, &attr, 0)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EBUSY):
		return fmt.Errorf("%s (%.0f%% of a CPU) does not fit in the deadline bandwidth left", d, d.Bandwidth()*100)
	case errors.Is(err, unix.EPERM):
		return fmt.Errorf("%s: the task may run outside its CPUs' scheduling domain", d)
	}
	return fmt.Errorf("sched_setattr %s: %w", d, err)
}

// CurrentDeadline is the task's reservation, if it runs under SCHED_DEADLINE.
func CurrentDeadline(tid int) (Deadline, bool, error) {
	attr, err := unix.SchedGetAttr(tid, 0)
	if err != nil {
		return Deadline{}, false, err
	}
	if attr.Policy != schedDeadline {
		return Deadline{}, false, nil
	}
	return Deadline{Runtime: time.Duration(attr.Runtime), Deadline: time.Duration(attr.Deadline),
		Period: time.Duration(attr.Period)}, true, nil
}
