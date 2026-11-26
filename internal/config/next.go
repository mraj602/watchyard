package config

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Strategies of `wy account next` (docs/spec/setup.md 8.5).
const (
	NextStrategyLanes    = "lanes"
	NextStrategySmart    = "smart"
	NextStrategyExpiring = "expiring"
	NextStrategyMostRoom = "most-room"
	NextStrategySpread   = "spread"
	NextStrategyFill     = "fill"
	NextStrategySticky   = "sticky"
)

// NextStrategies lists the strategies in the order the help text names them.
var NextStrategies = []string{NextStrategyLanes, NextStrategySmart, NextStrategyExpiring, NextStrategyMostRoom, NextStrategySpread,
	NextStrategyFill, NextStrategySticky}

// ValidNextStrategy reports whether s names a strategy.
func ValidNextStrategy(s string) bool {
	for _, x := range NextStrategies {
		if x == s {
			return true
		}
	}
	return false
}

// Account modes (next.accounts.<key>.mode).
const (
	NextModeConservative = "conservative"
	NextModeNormal       = "normal"
	NextModeAggressive   = "aggressive"
	// NextModeProtected: kept for its owner (lanes); like conservative for the projection
	// strategies.
	NextModeProtected = "protected"
	// NextModeOff: the owner keeps the account out (stricter than protected): never picked,
	// nothing moved into it; sessions already there stay (setup.md 8.5, Kind).
	NextModeOff = "off"
)

// nextAccountsPrefix starts the free-form per-account keys: next.accounts.<key>.<field>,
// where <key> (an email, seat id, folder or label) may itself contain dots.
const nextAccountsPrefix = "next.accounts."

// nextAccountFields are the fields of one next.accounts.<key> table and their kinds.
var nextAccountFields = map[string]kind{
	"mode": kString, "cap_5h": kFloat, "cap_7d": kFloat, "cap_model": kFloat, "reserve_pct": kFloat,
	"priority": kFloat, "weight": kFloat, "exclude": kBool, "hours": kString, "days": kString,
	"drain_until": kString, "pin_reserve_5h": kFloat, "pin_reserve_7d": kFloat,
}

func nextAccountFieldNames() []string {
	out := make([]string, 0, len(nextAccountFields))
	for k := range nextAccountFields {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// nextAccountKey splits "next.accounts.<key>.<field>"; the field is the part after the last
// dot and must be a known field.
func nextAccountKey(key string) (acct, field string, ok bool) {
	rest, found := strings.CutPrefix(key, nextAccountsPrefix)
	if !found {
		return "", "", false
	}
	i := strings.LastIndexByte(rest, '.')
	if i <= 0 || i == len(rest)-1 {
		return "", "", false
	}
	acct, field = rest[:i], rest[i+1:]
	if _, known := nextAccountFields[field]; !known {
		return "", "", false
	}
	return acct, field, true
}

func parseFloat(key, v string) (any, error) {
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n < 0 || math.IsInf(n, 0) || math.IsNaN(n) {
		return nil, fmt.Errorf("%s: want a number of 0 or more, got %q", key, v)
	}
	return n, nil
}

func parseNextAccountField(key, field, v string) (any, error) {
	switch field {
	case "mode":
		switch v {
		case NextModeConservative, NextModeNormal, NextModeAggressive, NextModeProtected, NextModeOff:
			return v, nil
		}
		return nil, fmt.Errorf("%s: want protected, conservative, normal, aggressive or off, got %q", key, v)
	case "hours":
		if _, _, err := ParseNextHours(v); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		return v, nil
	case "days":
		if _, err := ParseNextDays(v); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		return v, nil
	case "exclude":
		return parse(key, kBool, []string{v})
	case "drain_until":
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			return nil, fmt.Errorf("%s: want a time as 2006-01-02T15:04:05+05:30 (wy account drain sets it), got %q", key, v)
		}
		return v, nil
	}
	n, err := parseFloat(key, v)
	if err != nil {
		return nil, err
	}
	switch field {
	case "cap_5h", "cap_7d", "cap_model":
		if f := n.(float64); f <= 0 || f > 100 {
			return nil, fmt.Errorf("%s: want a percentage above 0 and at most 100, got %q", key, v)
		}
	case "reserve_pct":
		if n.(float64) >= 100 {
			return nil, fmt.Errorf("%s: want a percentage below 100, got %q", key, v)
		}
	}
	return n, nil
}

// unknownNextAccountKeys lists next.accounts entries that are not tables of known fields.
func unknownNextAccountKeys(v any) []string {
	m, ok := v.(map[string]any)
	if !ok {
		return []string{"next.accounts"}
	}
	var out []string
	for acct, t := range m {
		tm, ok := t.(map[string]any)
		if !ok {
			out = append(out, nextAccountsPrefix+acct)
			continue
		}
		for field := range tm {
			if _, known := nextAccountFields[field]; !known {
				out = append(out, nextAccountsPrefix+acct+"."+field)
			}
		}
	}
	return out
}

// ParseNextHours reads an allowed-hours range "HH:MM-HH:MM" (or "H-H"), local time. The
// end may be before the start (a range over midnight); start == end is refused.
func ParseNextHours(s string) (start, end time.Duration, err error) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), "-")
	if !ok {
		return 0, 0, fmt.Errorf("want hours as HH:MM-HH:MM, got %q", s)
	}
	clock := func(x string) (time.Duration, bool) {
		x = strings.TrimSpace(x)
		h, m, hasMin := strings.Cut(x, ":")
		hh, err := strconv.Atoi(h)
		if err != nil || hh < 0 || hh > 24 {
			return 0, false
		}
		mm := 0
		if hasMin {
			if mm, err = strconv.Atoi(m); err != nil || mm < 0 || mm > 59 || len(m) != 2 {
				return 0, false
			}
		}
		d := time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute
		return d, d <= 24*time.Hour
	}
	start, ok1 := clock(a)
	end, ok2 := clock(b)
	if !ok1 || !ok2 || start == end {
		return 0, 0, fmt.Errorf("want hours as HH:MM-HH:MM, got %q", s)
	}
	return start, end, nil
}

var weekdays = map[string]time.Weekday{"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday,
	"wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday}

// ParseNextDays reads allowed days: "mon-fri", "sat,sun" or a mix ("mon-wed,fri").
func ParseNextDays(s string) (map[time.Weekday]bool, error) {
	out := map[time.Weekday]bool{}
	for _, part := range strings.Split(strings.ToLower(s), ",") {
		part = strings.TrimSpace(part)
		a, b, isRange := strings.Cut(part, "-")
		da, ok := weekdays[strings.TrimSpace(a)]
		if !ok {
			return nil, fmt.Errorf("want days such as mon-fri or sat,sun, got %q", s)
		}
		if !isRange {
			out[da] = true
			continue
		}
		db, ok := weekdays[strings.TrimSpace(b)]
		if !ok {
			return nil, fmt.Errorf("want days such as mon-fri or sat,sun, got %q", s)
		}
		for d := da; ; d = (d + 1) % 7 {
			out[d] = true
			if d == db {
				break
			}
		}
	}
	return out, nil
}
