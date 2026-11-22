package devserver

import (
	"net/http"
	"time"
)

// Usage relation mock (api.yaml UsageRelation): GET /v1/accounts/{seat}/relation. The personal
// seat carries one protected account's real-looking numbers (a measured ratio of 6.78, the
// default session cost, Opus with only this week's index); the work seat is not chosen, was
// skipped by the burst cap and has both weeks of Opus; the others have nothing yet.

func (s *Server) relationRoutes() {
	s.mux.HandleFunc("GET /v1/accounts/{seat}/relation", s.guard(func(w http.ResponseWriter, r *http.Request, _ *caller) {
		s.mu.Lock()
		defer s.mu.Unlock()
		seat, _ := findByID(s.data.Seats, r.PathValue("seat"))
		if seat == nil {
			writeErr(w, 404, "not_found", "no such seat")
			return
		}
		writeJSON(w, 200, relationFor(seat["id"].(string), s.data.Now))
	}, "read"))
}

func relationFor(id string, now time.Time) M {
	day := func(back int) string { return now.UTC().AddDate(0, 0, -back).Format("2006-01-02") }
	factor := func(value, fallback float64, weight float64, n int, days float64) M {
		var measured any = value
		if weight == 0 {
			measured = nil
		}
		return M{"value": value, "fallback": fallback, "measured": measured, "weight": weight, "n": n, "days": days, "implausible": false}
	}
	heavy := func(h bool, rise float64, share any) M { return M{"heavy": h, "rise": rise, "cc_share": share} }
	out := M{"seat": id, "time": ts(now), "tz": "Asia/Kolkata", "today": day(0), "heavy": M{"this_week": heavy(true, 40, 0.9), "last_week": heavy(true, 35, 0.85)},
		"models": []M{}, "calibration": nil, "verdict": nil}
	days := make([]M, 14)
	for i := range days {
		days[i] = M{"day": day(13 - i), "ratio": nil, "d5": 0, "d7": 0, "pairs": 0, "model": nil, "rise": 0, "rise_cc": 0}
	}
	setRatio := func(i int, ratio float64, pairs int, model string) {
		days[i]["ratio"], days[i]["pairs"], days[i]["model"] = ratio, pairs, model
		days[i]["d5"], days[i]["d7"] = ratio*3, 3.0
	}
	modelDays := func(idx map[int]float64) []M {
		ds := make([]M, 14)
		for i := range ds {
			ds[i] = M{"day": day(13 - i), "index": nil, "normalised": nil, "d5": 0, "usd": 0}
			if v, ok := idx[i]; ok {
				ds[i]["index"], ds[i]["d5"], ds[i]["usd"] = v, 12.0, v*12
			}
		}
		return ds
	}
	switch id {
	case "seat_personal":
		for i, r := range map[int]float64{5: 7.1, 8: 6.2, 9: 6.9, 11: 7.4, 12: 6.5, 13: 6.8} {
			setRatio(i, r, 30, "Opus 5.5")
		}
		out["days"] = days
		out["models"] = []M{
			{"model": "Opus 5.5", "this_week": 1.67, "last_week": nil, "change": nil, "days": modelDays(map[int]float64{10: 1.6, 12: 1.7, 13: 1.67})},
			{"model": "Sonnet 5.5", "this_week": nil, "last_week": nil, "change": nil, "days": modelDays(nil)},
			{"model": "Haiku 5.5", "this_week": nil, "last_week": nil, "change": nil, "days": modelDays(nil)},
		}
		out["calibration"] = M{"f": factor(1.38, 1.0, 1, 1504, 6.7), "ratio": factor(6.78, 5.0, 1, 1480, 6.7), "lane_price": factor(4, 4, 0, 1, 0), "lane_charge": 8.0, "reset": nil}
		out["verdict"] = M{"at": ts(now.Add(-12 * time.Minute)), "machine": "m_gpu", "machine_name": "gpu-box", "strategy": "lanes", "kind": "protected", "eligible": true,
			"reason": nil, "skip_level": nil, "rank": 3, "expiring": false, "free_at": nil, "next_free": nil, "weekly_pct": 29.0, "five_hour_pct": 1.0,
			"burst": 0, "burst_cap": 2, "stale": false, "mode_from": "hub", "weekly_cap": 52.0, "five_hour_cap": 45.0}
	case "seat_work":
		for i := 0; i < 14; i++ {
			if i%3 != 2 {
				setRatio(i, 5.5+float64(i%4), 20, "Opus 5.5")
			}
		}
		for _, i := range []int{4, 9} {
			days[i]["model"] = "Sonnet 5.5"
		}
		out["days"] = days
		idx := map[int]float64{}
		for i := 0; i < 14; i++ {
			if i%4 != 1 {
				idx[i] = 1.4 + float64(i%5)*0.08
			}
		}
		out["models"] = []M{{"model": "Opus 5.5", "this_week": 1.62, "last_week": 1.5, "change": 1.08, "days": modelDays(idx)}}
		out["heavy"] = M{"this_week": heavy(false, 40, 0.62), "last_week": heavy(true, 35, 0.85)}
		out["calibration"] = M{"f": factor(1.0, 1.0, 0, 120, 1.2), "ratio": factor(5.0, 5.0, 0.4, 600, 2.4), "lane_price": factor(4, 4, 0, 3, 0), "lane_charge": 8.0,
			"reset": M{"at": ts(now.Add(-30 * time.Hour)), "reason": "under_read"}}
		out["verdict"] = M{"at": ts(now.Add(-5 * time.Minute)), "machine": "m_laptop", "machine_name": "laptop", "strategy": "lanes", "kind": "protected", "eligible": false,
			"reason": "burst", "skip_level": 0, "rank": nil, "expiring": false, "free_at": ts(now.Add(40 * time.Minute)), "next_free": nil, "weekly_pct": 30.0, "five_hour_pct": 41.0,
			"burst": 2, "burst_cap": 2, "stale": false, "mode_from": "local", "weekly_cap": 70.0, "five_hour_cap": 45.0}
	default:
		out["days"] = days
	}
	return out
}
