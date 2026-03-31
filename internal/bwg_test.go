package internal

import "testing"

func TestUsedLessThan(t *testing.T) {
	plan := int64(2147483648000) // ~2TB, realistic BWG quota

	yl := &BWGStatus{DataCounter: 4693808782, PlanMonthlyData: plan}         // 0.2% used
	shuzuan := &BWGStatus{DataCounter: 76342530449, PlanMonthlyData: plan}   // 3.5% used
	metask := &BWGStatus{DataCounter: 235495887275, PlanMonthlyData: plan}   // 11% used

	if !yl.UsedLessThan(shuzuan) {
		t.Error("yl (0.2%) should be less used than shuzuan (3.5%)")
	}
	if !yl.UsedLessThan(metask) {
		t.Error("yl (0.2%) should be less used than metask (11%)")
	}
	if !shuzuan.UsedLessThan(metask) {
		t.Error("shuzuan (3.5%) should be less used than metask (11%)")
	}
	if shuzuan.UsedLessThan(yl) {
		t.Error("shuzuan (3.5%) should NOT be less used than yl (0.2%)")
	}
}

func TestUsedLessThanNil(t *testing.T) {
	tracked := &BWGStatus{DataCounter: 100, PlanMonthlyData: 1000}

	if !(*BWGStatus)(nil).UsedLessThan(tracked) {
		t.Error("nil (unlimited) should sort before tracked")
	}
	if tracked.UsedLessThan(nil) {
		t.Error("tracked should NOT sort before nil (unlimited)")
	}
	if (*BWGStatus)(nil).UsedLessThan(nil) {
		t.Error("nil vs nil should be equal (false)")
	}
}
