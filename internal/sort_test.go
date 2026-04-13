package internal

import (
	"fmt"
	"sort"
	"testing"
)

func TestRebalanceSort(t *testing.T) {
	plan := int64(2147483648000)
	
	type transitBW struct {
		name    string
		bwg     *BWGStatus
		exhaust bool
	}
	
	transits := []transitBW{
		// Original order from CF (unknown, try different orders)
		{name: "shuzuan", bwg: &BWGStatus{DataCounter: 76342530449, PlanMonthlyData: plan}},
		{name: "yl", bwg: &BWGStatus{DataCounter: 4693808782, PlanMonthlyData: plan}},
		{name: "metask", bwg: &BWGStatus{DataCounter: 235495887275, PlanMonthlyData: plan}},
	}
	for i := range transits {
		transits[i].exhaust = transits[i].bwg.IsExhausted()
	}
	
	sort.SliceStable(transits, func(i, j int) bool {
		if transits[i].exhaust != transits[j].exhaust {
			return !transits[i].exhaust
		}
		return transits[i].bwg.UsedLessThan(transits[j].bwg)
	})
	
	for _, t := range transits {
		fmt.Printf("%s: %.1f%% remaining\n", t.name, t.bwg.RemainingPct())
	}
	
	if transits[0].name != "yl" {
		t.Errorf("expected yl first, got %s", transits[0].name)
	}
}
