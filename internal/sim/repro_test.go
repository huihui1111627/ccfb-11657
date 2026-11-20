package sim

import (
	"testing"
)

// TestReproducibility 验证同一种子逐结果可复现,且多种子下全部场景零误判。
func TestReproducibility(t *testing.T) {
	modes := []SessionMode{ModeAsync, ModeEcho, ModeDemand}
	faults := []FaultKind{FaultOneWayCut, FaultTwoWayCut, FaultJitter, FaultLoss}
	seeds := []int64{1, 2, 3, 7, 13, 42, 99, 256, 2026, 31337, 88888, 999999}
	for _, seed := range seeds {
		for _, m := range modes {
			for _, f := range faults {
				spec := DefaultSpec(m, f, seed)
				r1 := RunScenario(spec)
				r2 := RunScenario(spec)
				if !resultsEqual(r1, r2) {
					t.Fatalf("种子 %d %s/%s 结果不可复现", seed, m, f)
				}
				if !r1.Passed {
					t.Fatalf("种子 %d %s/%s 未通过: %s (falseDown=%d falseUp=%d)",
						seed, m, f, r1.Detail, r1.FalseDowns, r1.FalseUps)
				}
			}
		}
	}
}

func resultsEqual(a, b ScenarioResult) bool {
	if a.Passed != b.Passed || a.FalseDowns != b.FalseDowns || a.FalseUps != b.FalseUps {
		return false
	}
	if len(a.Records) != len(b.Records) {
		return false
	}
	for i := range a.Records {
		x, y := a.Records[i], b.Records[i]
		if x.Elapsed != y.Elapsed || x.DetectedAt != y.DetectedAt ||
			x.TheoryMin != y.TheoryMin || x.TheoryMax != y.TheoryMax {
			return false
		}
	}
	return true
}
