package api

import (
	"encoding/json"
	"net/http"

	"github.com/mraj602/watchyard/internal/calib"
	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/push/rules"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// GET /v1/accounts/{seat}/relation (setup.md 8.5, Usage relation; api.yaml seatRelation): what
// the calibration job stored about the seat, its factors and the newest picker verdict.
// Nothing is recomputed but the weeks' medians over the stored days.
func init() {
	Register("GET", "/v1/accounts/{seat}/relation", func(s *Server) http.Handler { return http.HandlerFunc(s.seatRelation) })
}

// RelationDoc is api.yaml UsageRelation.
type RelationDoc struct {
	Seat string     `json:"seat"`
	Time store.Time `json:"time"`
	calib.RelationView
	Calibration json.RawMessage `json:"calibration"`
	Verdict     *VerdictDoc     `json:"verdict"`
}

// VerdictDoc is api.yaml Verdict.
type VerdictDoc struct {
	At          store.Time `json:"at"`
	Machine     string     `json:"machine"`
	MachineName *string    `json:"machine_name"`
	Strategy    string     `json:"strategy"`
	Kind        *string    `json:"kind"`
	Eligible    bool       `json:"eligible"`
	Reason      *string    `json:"reason"`
	SkipLevel   *int       `json:"skip_level"`
	Rank        *int       `json:"rank"`
	Expiring    bool       `json:"expiring"`
	FreeAt      *string    `json:"free_at"`
	NextFree    *string    `json:"next_free"`
	WeeklyPct   *float64   `json:"weekly_pct"`
	FiveHourPct *float64   `json:"five_hour_pct"`
	Burst       *int       `json:"burst"`
	BurstCap    *int       `json:"burst_cap"`
	Stale       bool       `json:"stale"`
}

func (s *Server) seatRelation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("seat")
	ok, err := s.store.SeatExists(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !ok {
		auth.WriteError(w, http.StatusNotFound, "not_found", "no such seat")
		return
	}
	now := s.now()
	tz, _ := s.hubZone(r)
	rel := calib.NewRelation()
	raw, found, err := s.store.SeatRelation(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if found && (json.Unmarshal(raw, rel) != nil || rel.V != 1) {
		rel = calib.NewRelation()
	}
	doc := RelationDoc{Seat: id, Time: store.Time(now.UnixMilli()), RelationView: rel.View(now, rules.Location(tz))}
	doc.TZ = tz
	if f, ok, err := s.store.SeatCalibration(ctx, id); err != nil {
		s.fail(w, err)
		return
	} else if ok {
		doc.Calibration = f
	}
	v, ok, err := s.store.SeatVerdict(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if ok {
		d := VerdictDoc{}
		if json.Unmarshal(v.Verdict, &d) == nil {
			d.At, d.Machine, d.Strategy = store.Time(v.TS), v.Machine, v.Strategy
			if v.MachineName != "" {
				name := v.MachineName
				d.MachineName = &name
			}
			doc.Verdict = &d
		}
	}
	writeJSON(w, http.StatusOK, doc)
}
