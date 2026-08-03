package calibrate_test

import (
	"testing"

	"github.com/justrunme/architecture-rehearsal/internal/calibrate"
)

func TestClassifyAndPrecision(t *testing.T) {
	if calibrate.Classify(calibrate.Outcome{Predicted: true, Observed: true, Verified: true}) != calibrate.TruePositive {
		t.Fatal()
	}
	st := calibrate.NewStore()
	st.Record(calibrate.Outcome{Scenario: "rwo", Predicted: true, Observed: true, Verified: true})
	st.Record(calibrate.Outcome{Scenario: "rwo", Predicted: true, Observed: true, Verified: true})
	st.Record(calibrate.Outcome{Scenario: "rwo", Predicted: true, Observed: false, Verified: true})
	rep := st.Report()
	if len(rep.Scenarios) != 1 {
		t.Fatalf("%+v", rep)
	}
	if rep.Scenarios[0].Precision < 0.6 {
		t.Fatalf("precision=%v", rep.Scenarios[0].Precision)
	}
	c := calibrate.Confidence(calibrate.ConfidenceFactors{
		OwnerReferencePresent: true, PVCToPVEdge: true, FailedAttachVolumeEvent: true,
		LostNodeConfirmed: true, ObservedWithinWindow: true,
	})
	if c < 0.99 {
		t.Fatalf("%v", c)
	}
}
