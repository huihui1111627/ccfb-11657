package sim

import (
	"fmt"
	"testing"
)

func TestSmokeScenarios(t *testing.T) {
	modes := []SessionMode{ModeAsync, ModeEcho, ModeDemand}
	faults := []FaultKind{FaultOneWayCut, FaultTwoWayCut, FaultJitter, FaultLoss}
	seed := int64(42)
	for _, m := range modes {
		for _, f := range faults {
			spec := DefaultSpec(m, f, seed)
			res := RunScenario(spec)
			fmt.Printf("\n===== %s | %s | seed=%d =====\n", m, f, seed)
			fmt.Printf("falseDowns=%d falseUps=%d records=%d passed=%v %s\n",
				res.FalseDowns, res.FalseUps, len(res.Records), res.Passed, res.Detail)
			if res.FalseDowns > 0 {
				for _, tr := range res.DownDetails {
					fmt.Printf("    DOWN t=%dms %s disc=%d %s->%s diag=%s\n",
						tr.T/1e6, tr.LinkID, tr.Disc, tr.Old, tr.New, tr.Diag)
				}
			}
			for _, r := range res.Records {
				fmt.Printf("  %s: 实测=%dms 理论=[%dms, %dms] 边界内=%v diag=%s\n",
					r.Node, r.Elapsed/1e6, r.TheoryMin/1e6, r.TheoryMax/1e6,
					r.WithinBound, r.Diag)
			}
		}
	}
}
