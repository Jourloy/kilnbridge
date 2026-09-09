package agent

import (
	"testing"

	"github.com/jourloy/kilnbridge/internal/model"
)

func TestTrayStopRespectsWork(t *testing.T) {
	for _, work := range []string{"source upload", "tool setup", "queued build", "idle", "no shutdown handler"} {
		t.Run(work, func(t *testing.T) {
			a, _ := fixtureApp(t)
			stopped := false
			a.Shutdown = func() { stopped = true }
			switch work {
			case "source upload":
				if err := a.M.BeginSubmission(); err != nil {
					t.Fatal(err)
				}
				defer a.M.EndSubmission()
			case "tool setup":
				a.M.active = "tools"
			case "queued build":
				a.M.jobs["queued"] = model.Job{ID: "queued", Status: "queued"}
			case "no shutdown handler":
				a.Shutdown = nil
			}
			err := a.stop()
			if work == "idle" {
				if err != nil || !stopped {
					t.Fatalf("idle worker did not stop: %v", err)
				}
				if err := a.M.BeginSubmission(); err == nil {
					a.M.EndSubmission()
					t.Fatal("accepted new source after shutdown was reserved")
				}
			} else if err == nil || stopped {
				t.Fatalf("stopped worker during %s: %v", work, err)
			}
		})
	}
}
