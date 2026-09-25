package main

import (
	"strings"
	"testing"

	ctrlclient "github.com/canblmz1/stowline/agent/internal/control"
	"github.com/canblmz1/stowline/agent/internal/domain"
)

func TestScheduleAdapterForwardsSafeCommandDiagnostics(t *testing.T) {
	exec := &ctrlclient.Executor{}
	adapter := &scheduleAdapter{exec: exec}
	var got string
	adapter.SetCommandDiagnosticSink(func(message string) { got = message })
	if exec.OnCommandEvent == nil {
		t.Fatal("service diagnostic sink was not attached")
	}
	exec.OnCommandEvent(ctrlclient.CommandDiagnostic{
		Event:      ctrlclient.EventAckFailed,
		Stage:      ctrlclient.StageAck,
		CommandID:  "cmd-1",
		Kind:       "BROWSE_LOCAL_DIR",
		ErrorClass: domain.ErrorNetwork,
	})
	for _, want := range []string{"event=ACK_FAILED", "stage=ack", "command_id=cmd-1", "kind=BROWSE_LOCAL_DIR", "error_class=NETWORK"} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostic missing %q: %s", want, got)
		}
	}
	adapter.SetCommandDiagnosticSink(nil)
	if exec.OnCommandEvent != nil {
		t.Fatal("service diagnostic sink was not detached")
	}
}
