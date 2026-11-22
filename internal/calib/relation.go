package calib

import (
	"math"
	"sort"
	"time"
)

// The usage relation (setup.md 8.5, Usage relation): per day of the hub's time zone, the
// 5-hour to weekly ratio of the seat's pairs, its most-used model and the 5-hour capacity
// index (API dollars per 5-hour point) per model.
const (
	RelationDays    = 15  // days kept
	RelationShown   = 14  // days returned
	RatioMinD7      = 2.0 // Σ d7 for a day's ratio
	CapacityMinD5   = 5.0 // Σ d5 for a day's capacity index
	CapacityShare   = 0.8 // the model's share of a pair's dollars
	WeekMinDays     = 3   // daily indexes for a week's median
	HeavyRise       = 20.0
	HeavyShare      = 0.8
	LimitUp         = 1.2
	LimitDown       = 0.83
	LimitMinRefs    = 2
	LimitDedupe     = 14 * 24 * time.Hour
	relationDayForm = "2006-01-02"
)

// ModelUse is one token row of the seat: its model's display label, weighted units and API
// dollars (Priced false: the model has no price).
type ModelUse struct {
	At     time.Time
	Model  string
	Units  float64
	USD    float64
	Priced bool
}

// ModelMeter sums a seat's API dollars per model between two times.
type ModelMeter struct{ uses []ModelUse }

// NewModelMeter makes a meter over us (any order).
func NewModelMeter(us []ModelUse) *ModelMeter {
	s := append([]ModelUse(nil), us...)
	sort.SliceStable(s, func(i, j int) bool { return s[i].At.Before(s[j].At) })
	return &ModelMeter{uses: s}
}

// Between is the priced dollars per model with from < at <= to (milliseconds).
func (m *ModelMeter) Between(from, to int64) map[string]float64 {
	out := map[string]float64{}
	if m == nil || to <= from {
		return out
	}
	i := sort.Search(len(m.uses), func(k int) bool { return m.uses[k].At.UnixMilli() > from })
	for ; i < len(m.uses) && m.uses[i].At.UnixMilli() <= to; i++ {
		if u := m.uses[i]; u.Priced && u.USD > 0 {
			out[u.Model] += u.USD
		}
	}
	return out
}

// CapSum is one model's capacity sums of a day: dollars and 5-hour points.
type CapSum struct {
	USD float64 `json:"usd"`
	D5  float64 `json:"d5"`
}

// RelDay is one day's sums.
type RelDay struct {
	Day    string             `json:"day"` // YYYY-MM-DD in the hub's time zone
	D5     float64            `json:"d5"`  // of the pairs with a d7
	D7     float64            `json:"d7"`
	Pairs  int                `json:"pairs"`
	Rise   float64            `json:"rise"`    // 5-hour points the day's pairs rose
	RiseCC float64            `json:"rise_cc"` // of which in pairs with units
	Units  map[string]float64 `json:"units,omitempty"`
	Cap    map[string]*CapSum `json:"cap,omitempty"`
}

// Relation is a seat's usage relation, kept by the hub between runs (seat_watch.relation).
type Relation struct {
	V         int      `json:"v"`
	PairsUpto int64    `json:"pairs_upto"` // pairs with t1 up to here are folded
	UsesUpto  int64    `json:"uses_upto"`  // token rows up to here are folded
	Days      []RelDay `json:"days"`
}

// NewRelation is an empty relation.
func NewRelation() *Relation { return &Relation{V: 1, Days: []RelDay{}} }

func (r *Relation) day(t time.Time, loc *time.Location) *RelDay {
	d := t.In(loc).Format(relationDayForm)
	for i := len(r.Days) - 1; i >= 0; i-- {
		if r.Days[i].Day == d {
			return &r.Days[i]
		}
		if r.Days[i].Day < d {
			break
		}
	}
	r.Days = append(r.Days, RelDay{Day: d})
	sort.SliceStable(r.Days, func(i, j int) bool { return r.Days[i].Day < r.Days[j].Day })
	for i := range r.Days {
		if r.Days[i].Day == d {
			return &r.Days[i]
		}
	}
	return nil
}

// AddPair folds one 5-hour pair (once: pairs up to PairsUpto are skipped) with the seat's
// dollars per model between its readings.
func (r *Relation) AddPair(p Pair, m *ModelMeter, loc *time.Location) {
	if p.T1 <= r.PairsUpto {
		return
	}
	r.PairsUpto = p.T1
	d := r.day(time.UnixMilli(p.T1), loc)
	if p.D7 != nil {
		d.D5 += p.D5
		d.D7 += *p.D7
		d.Pairs++
	}
	if p.D5 > 0 {
		d.Rise += p.D5
		if p.U > 0 {
			d.RiseCC += p.D5
		}
	}
	if p.U <= 0 || p.D5 < 0 {
		return
	}
	usd := m.Between(p.T0, p.T1)
	var total, top float64
	model := ""
	for k, v := range usd {
		total += v
		if v > top || (v == top && k < model) {
			top, model = v, k
		}
	}
	if total <= 0 || top < CapacityShare*total {
		return
	}
	if d.Cap == nil {
		d.Cap = map[string]*CapSum{}
	}
	c := d.Cap[model]
	if c == nil {
		c = &CapSum{}
		d.Cap[model] = c
	}
	c.USD += total
	c.D5 += p.D5
}

// AddUse folds one token row (once: rows up to UsesUpto are skipped by the caller's range).
func (r *Relation) AddUse(u ModelUse, loc *time.Location) {
	if u.Model == "" || u.Units <= 0 {
		return
	}
	d := r.day(u.At, loc)
	if d.Units == nil {
		d.Units = map[string]float64{}
	}
	d.Units[u.Model] += u.Units
}

// Prune keeps the last RelationDays days ending on now's day.
func (r *Relation) Prune(now time.Time, loc *time.Location) {
	first := dayOf(now, loc).AddDate(0, 0, -(RelationDays - 1)).Format(relationDayForm)
	keep := r.Days[:0]
	for _, d := range r.Days {
		if d.Day >= first {
			keep = append(keep, d)
		}
	}
	r.Days = keep
}

func dayOf(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 12, 0, 0, 0, loc) // noon: safe across DST
}

// RelationView is the relation as GET /v1/accounts/{seat}/relation returns it (api.yaml
// UsageRelation, without seat, time, calibration and verdict).
type RelationView struct {
	TZ     string      `json:"tz"`
	Today  string      `json:"today"`
	Days   []DayView   `json:"days"`
	Heavy  HeavyWeeks  `json:"heavy"`
	Models []ModelView `json:"models"`
}

// DayView is api.yaml RelationDay.
type DayView struct {
	Day    string   `json:"day"`
	Ratio  *float64 `json:"ratio"`
	D5     float64  `json:"d5"`
	D7     float64  `json:"d7"`
	Pairs  int      `json:"pairs"`
	Model  *string  `json:"model"`
	Rise   float64  `json:"rise"`
	RiseCC float64  `json:"rise_cc"`
}

// HeavyWeeks is this week's and last week's Claude-Code-heavy test.
type HeavyWeeks struct {
	ThisWeek Heavy `json:"this_week"`
	LastWeek Heavy `json:"last_week"`
}

// Heavy is api.yaml RelationHeavy.
type Heavy struct {
	Heavy   bool     `json:"heavy"`
	Rise    float64  `json:"rise"`
	CCShare *float64 `json:"cc_share"`
}

// ModelView is api.yaml RelationModel.
type ModelView struct {
	Model    string         `json:"model"`
	ThisWeek *float64       `json:"this_week"`
	LastWeek *float64       `json:"last_week"`
	Change   *float64       `json:"change"`
	Days     []ModelDayView `json:"days"`
	usd      float64
}

// ModelDayView is one day of a model's capacity index.
type ModelDayView struct {
	Day        string   `json:"day"`
	Index      *float64 `json:"index"`
	Normalised *float64 `json:"normalised"`
	D5         float64  `json:"d5"`
	USD        float64  `json:"usd"`
}

func ptr(x float64) *float64 { return &x }

// median of xs (non-empty).
func median(xs []float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// View is the relation's 14 days ending on now's day, with the weeks' medians.
func (r *Relation) View(now time.Time, loc *time.Location) RelationView {
	byDay := map[string]*RelDay{}
	for i := range r.Days {
		byDay[r.Days[i].Day] = &r.Days[i]
	}
	today := dayOf(now, loc)
	v := RelationView{TZ: loc.String(), Today: today.Format(relationDayForm), Days: make([]DayView, 0, RelationShown),
		Models: []ModelView{}}
	days := make([]string, RelationShown)
	for i := range days {
		days[i] = today.AddDate(0, 0, i-(RelationShown-1)).Format(relationDayForm)
	}
	var weekRise, weekCC [2]float64 // [last week, this week]
	models := map[string]*ModelView{}
	for i, day := range days {
		week := 0
		if i >= RelationShown-7 {
			week = 1
		}
		dv := DayView{Day: day}
		if d := byDay[day]; d != nil {
			dv.D5, dv.D7, dv.Pairs = round(d.D5, 2), round(d.D7, 2), d.Pairs
			dv.Rise, dv.RiseCC = round(d.Rise, 2), round(d.RiseCC, 2)
			if d.D7 >= RatioMinD7 {
				dv.Ratio = ptr(round(d.D5/d.D7, 2))
			}
			if m := topModel(d.Units); m != "" {
				dv.Model = &m
			}
			weekRise[week] += d.Rise
			weekCC[week] += d.RiseCC
			for k := range d.Cap {
				if models[k] == nil {
					models[k] = &ModelView{Model: k}
				}
			}
		}
		v.Days = append(v.Days, dv)
	}
	v.Heavy.LastWeek = heavy(weekRise[0], weekCC[0])
	v.Heavy.ThisWeek = heavy(weekRise[1], weekCC[1])
	for name, mv := range models {
		var idx [2][]float64
		mv.Days = make([]ModelDayView, 0, RelationShown)
		for i, day := range days {
			md := ModelDayView{Day: day}
			if d := byDay[day]; d != nil {
				if c := d.Cap[name]; c != nil {
					md.D5, md.USD = round(c.D5, 2), round(c.USD, 4)
					mv.usd += c.USD
					if c.D5 >= CapacityMinD5 {
						x := c.USD / c.D5
						md.Index = ptr(round(x, 5))
						week := 0
						if i >= RelationShown-7 {
							week = 1
						}
						idx[week] = append(idx[week], x)
					}
				}
			}
			mv.Days = append(mv.Days, md)
		}
		if len(idx[0]) >= WeekMinDays {
			mv.LastWeek = ptr(median(idx[0]))
		}
		if len(idx[1]) >= WeekMinDays {
			mv.ThisWeek = ptr(median(idx[1]))
		}
		if mv.LastWeek != nil && *mv.LastWeek > 0 {
			for i := range mv.Days {
				if x := mv.Days[i].Index; x != nil {
					mv.Days[i].Normalised = ptr(round(*x / *mv.LastWeek, 3))
				}
			}
			if mv.ThisWeek != nil {
				mv.Change = ptr(round(*mv.ThisWeek / *mv.LastWeek, 3))
			}
		}
		if mv.LastWeek != nil {
			mv.LastWeek = ptr(round(*mv.LastWeek, 5))
		}
		if mv.ThisWeek != nil {
			mv.ThisWeek = ptr(round(*mv.ThisWeek, 5))
		}
		v.Models = append(v.Models, *mv)
	}
	sort.Slice(v.Models, func(i, j int) bool {
		if v.Models[i].usd != v.Models[j].usd {
			return v.Models[i].usd > v.Models[j].usd
		}
		return v.Models[i].Model < v.Models[j].Model
	})
	return v
}

func heavy(rise, cc float64) Heavy {
	h := Heavy{Rise: round(rise, 2)}
	if rise > 0 {
		s := cc / rise
		h.CCShare = ptr(round(s, 3))
		h.Heavy = rise >= HeavyRise && s >= HeavyShare
	}
	return h
}

func topModel(units map[string]float64) string {
	best, top := "", 0.0
	for k, v := range units {
		if v > top || (v == top && v > 0 && k < best) {
			best, top = k, v
		}
	}
	return best
}

// LimitChange is one limit_change notice: a model whose capacity moved on most reference
// accounts (push.md 3.9).
type LimitChange struct {
	Model string
	Up    bool
	Moved int // reference accounts past the threshold
	Refs  int // reference accounts
	// Change is the median change of the moved accounts.
	Change float64
}

// LimitChanges finds the limit_change notices over the seats' views (push.md 3.9).
func LimitChanges(views []RelationView) []LimitChange {
	type acc struct{ changes []float64 }
	refs := map[string]*acc{}
	for _, v := range views {
		if !v.Heavy.ThisWeek.Heavy || !v.Heavy.LastWeek.Heavy {
			continue
		}
		for _, m := range v.Models {
			if m.ThisWeek == nil || m.LastWeek == nil || m.Change == nil || math.IsNaN(*m.Change) {
				continue
			}
			if refs[m.Model] == nil {
				refs[m.Model] = &acc{}
			}
			refs[m.Model].changes = append(refs[m.Model].changes, *m.Change)
		}
	}
	var out []LimitChange
	for model, a := range refs {
		n := len(a.changes)
		if n < LimitMinRefs {
			continue
		}
		var up, down []float64
		for _, c := range a.changes {
			if c > LimitUp {
				up = append(up, c)
			} else if c < LimitDown {
				down = append(down, c)
			}
		}
		switch {
		case 2*len(up) > n:
			out = append(out, LimitChange{Model: model, Up: true, Moved: len(up), Refs: n, Change: round(median(up), 2)})
		case 2*len(down) > n:
			out = append(out, LimitChange{Model: model, Moved: len(down), Refs: n, Change: round(median(down), 2)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}
