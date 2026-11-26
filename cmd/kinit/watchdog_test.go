package main

import (
	"testing"
	"time"

	"github.com/kuberoot-dev/kuberoot/pkg/supervisor"
	"github.com/kuberoot-dev/kuberoot/pkg/upgrade"
)

func TestExitAccountingFeedsBothAssessors(t *testing.T) {
	start := time.Unix(1000, 0)
	st := supervisor.ServiceStatus{Name: "broken", State: supervisor.StateRunning, PID: 42}
	for i := 0; i < 5; i++ {
		recordExit(&st, "signal terminated", true)
	}
	a := upgrade.Assessment{Start: start}
	for exits := 0; exits <= upgrade.CrashLoopExits; exits++ {
		if exits > 0 {
			recordExit(&st, "code 0", false)
		} // even a successful exit is unexpected
		statuses := []supervisor.ServiceStatus{st}
		_, why := a.Observe(start.Add(time.Duration(exits)*time.Second), false, statuses)
		watchdog := upgrade.WatchdogFailure(time.Duration(exits)*time.Second, statuses)
		if (why != "") != (exits == upgrade.CrashLoopExits) || why != watchdog {
			t.Fatalf("exit %d: assessment=%q watchdog=%q", exits, why, watchdog)
		}
	}
	if st.Restarts != 5+upgrade.CrashLoopExits || st.UnrequestedExits != upgrade.CrashLoopExits || st.PID != 0 || st.State != supervisor.StateRestarting {
		t.Fatalf("%+v", st)
	}
}
