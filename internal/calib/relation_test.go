package calib

import (
	"testing"
	"time"
)

var relLoc = time.FixedZone("hub", 5*3600+1800) // +05:30

// relPair is a pair ending at noon (hub time) daysAgo days before relNow's day.
var relNow = time.Date(2026, 11, 20, 18, 0, 0, 0, relLoc)

func relPair(daysAgo int, hour int, d5 float64, d7 *float64, u float64) Pair {
	t1 := time.Date(2026, 11, 20-daysAgo, hour, 0, 0, 0, relLoc)
	return Pair{T0: t1.Add(-20 * time.Minute).UnixMilli(), T1: t1.UnixMilli(), D5: d5, D7: d7, U: u}
}

func relUse(daysAgo, hour int, min int, model string, usd float64) ModelUse {
	at := time.Date(2026, 11, 20-daysAgo, hour, 0, 0, 0, relLoc).Add(-time.Duration(min) * time.Minute)
	return ModelUse{At: at, Model: model, Units: usd * 1e5, USD: usd, Priced: usd > 0}
}

func TestRelationRatioAndModel(t *testing.T) {
	r := NewRelation()
	var uses []ModelUse
	uses = append(uses, relUse(0, 10, 10, "Opus 4.6", 1), relUse(0, 11, 10, "Sonnet 4.5", 0.2))
	m := NewModelMeter(uses)
	for _, u := range uses {
		r.AddUse(u, relLoc)
	}
	r.AddPair(relPair(0, 10, 6, pct(1), 1e6), m, relLoc)
	r.AddPair(relPair(0, 11, 8, pct(1), 1e6), m, relLoc)
	r.AddPair(relPair(0, 11, 8, pct(1), 1e6), m, relLoc) // folded already: skipped
	v := r.View(relNow, relLoc)
	if len(v.Days) != RelationShown || v.Today != "2026-11-20" || v.Days[13].Day != "2026-11-20" || v.Days[0].Day != "2026-11-07" {
		t.Fatalf("days = %d today %s first %s", len(v.Days), v.Today, v.Days[0].Day)
	}
	d := v.Days[13]
	if d.Ratio == nil || *d.Ratio != 7 || d.Pairs != 2 || d.D5 != 14 || d.D7 != 2 {
		t.Fatalf("day = %+v", d)
	}
	if d.Model == nil || *d.Model != "Opus 4.6" {
		t.Fatalf("model = %v", d.Model)
	}
	if v.Days[12].Ratio != nil || v.Days[12].Model != nil {
		t.Fatalf("an empty day: %+v", v.Days[12])
	}
	// Σ d7 under 2 points: no ratio.
	r2 := NewRelation()
	r2.AddPair(relPair(0, 10, 6, pct(1.5), 0), nil, relLoc)
	if v := r2.View(relNow, relLoc); v.Days[13].Ratio != nil {
		t.Fatal("ratio with Σ d7 < 2")
	}
}

// buildWeeks gives a seat three days a week of Opus pairs at the given dollars per point,
// oldest first (pairs reach the relation in time order).
func buildWeeks(r *Relation, last, this float64) {
	for _, days := range [][2]int{{10, 0}, {9, 0}, {8, 0}, {3, 1}, {2, 1}, {1, 1}} {
		perPt := last
		if days[1] == 1 {
			perPt = this
		}
		ago := days[0]
		u := relUse(ago, 12, 10, "Opus 4.6", 10*perPt)
		r.AddUse(u, relLoc)
		r.AddPair(relPair(ago, 12, 10, pct(1.4), 1e6), NewModelMeter([]ModelUse{u}), relLoc)
	}
}

func TestRelationCapacityWeeks(t *testing.T) {
	r := NewRelation()
	buildWeeks(r, 0.04, 0.052)
	v := r.View(relNow, relLoc)
	if len(v.Models) != 1 {
		t.Fatalf("models = %+v", v.Models)
	}
	m := v.Models[0]
	if m.Model != "Opus 4.6" || m.LastWeek == nil || *m.LastWeek != 0.04 || m.ThisWeek == nil || *m.ThisWeek != 0.052 {
		t.Fatalf("model = %+v", m)
	}
	if m.Change == nil || *m.Change != 1.3 {
		t.Fatalf("change = %v", m.Change)
	}
	if n := m.Days[13-1].Normalised; n == nil || *n != 1.3 {
		t.Fatalf("normalised = %v", n)
	}
	if !v.Heavy.ThisWeek.Heavy || v.Heavy.ThisWeek.Rise != 30 || v.Heavy.LastWeek.Rise != 30 {
		t.Fatalf("heavy = %+v", v.Heavy)
	}
	// Two days only: no week median.
	r2 := NewRelation()
	for _, ago := range []int{1, 2} {
		u := relUse(ago, 12, 10, "Opus 4.6", 1)
		r2.AddPair(relPair(ago, 12, 10, nil, 1e6), NewModelMeter([]ModelUse{u}), relLoc)
	}
	if m := r2.View(relNow, relLoc).Models[0]; m.ThisWeek != nil || m.Change != nil {
		t.Fatalf("two days: %+v", m)
	}
}

func TestRelationCapacityRules(t *testing.T) {
	r := NewRelation()
	mixed := []ModelUse{relUse(0, 12, 10, "Opus 4.6", 0.7), relUse(0, 12, 5, "Sonnet 4.5", 0.3)}
	r.AddPair(relPair(0, 12, 10, nil, 1e6), NewModelMeter(mixed), relLoc) // no model with 80 %
	unpriced := []ModelUse{{At: relNow.Add(-5 * time.Hour), Model: "Mystery", Units: 1e6}}
	r.AddPair(relPair(0, 13, 10, nil, 1e6), NewModelMeter(unpriced), relLoc) // no price
	web := []ModelUse{relUse(0, 14, 10, "Opus 4.6", 1)}
	r.AddPair(relPair(0, 14, 10, nil, 0), NewModelMeter(web), relLoc) // no units: claude.ai
	r.AddPair(relPair(0, 15, 4, nil, 1e6), NewModelMeter([]ModelUse{relUse(0, 15, 10, "Opus 4.6", 1)}), relLoc)
	v := r.View(relNow, relLoc)
	if len(v.Models) != 1 || v.Models[0].Days[13].Index != nil || v.Models[0].Days[13].D5 != 4 {
		t.Fatalf("models = %+v", v.Models)
	}
	h := v.Heavy.ThisWeek
	if h.Rise != 34 || h.CCShare == nil || *h.CCShare != 0.706 || h.Heavy {
		t.Fatalf("heavy = %+v", h)
	}
}

func TestRelationPrune(t *testing.T) {
	r := NewRelation()
	r.AddPair(relPair(20, 12, 5, nil, 0), nil, relLoc)
	r.AddPair(relPair(14, 12, 5, nil, 0), nil, relLoc)
	r.AddPair(relPair(13, 12, 5, nil, 0), nil, relLoc)
	r.Prune(relNow, relLoc)
	if len(r.Days) != 2 || r.Days[0].Day != "2026-11-06" {
		t.Fatalf("days = %+v", r.Days)
	}
}

func TestLimitChanges(t *testing.T) {
	seat := func(last, this float64) RelationView {
		r := NewRelation()
		buildWeeks(r, last, this)
		return r.View(relNow, relLoc)
	}
	up := LimitChanges([]RelationView{seat(0.04, 0.052), seat(0.03, 0.04), seat(0.05, 0.05)})
	if len(up) != 1 || !up[0].Up || up[0].Moved != 2 || up[0].Refs != 3 || up[0].Model != "Opus 4.6" {
		t.Fatalf("up = %+v", up)
	}
	down := LimitChanges([]RelationView{seat(0.04, 0.03), seat(0.04, 0.032)})
	if len(down) != 1 || down[0].Up || down[0].Moved != 2 {
		t.Fatalf("down = %+v", down)
	}
	// Half is not most; one reference account is not enough.
	if got := LimitChanges([]RelationView{seat(0.04, 0.06), seat(0.04, 0.04)}); len(got) != 0 {
		t.Fatalf("half = %+v", got)
	}
	if got := LimitChanges([]RelationView{seat(0.04, 0.06)}); len(got) != 0 {
		t.Fatalf("one = %+v", got)
	}
	// A seat that is not heavy is no reference.
	light := seat(0.04, 0.06)
	light.Heavy.LastWeek.Heavy = false
	if got := LimitChanges([]RelationView{light, seat(0.04, 0.06), seat(0.04, 0.04)}); len(got) != 0 {
		t.Fatalf("light = %+v", got)
	}
}
