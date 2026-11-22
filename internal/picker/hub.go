package picker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mraj602/watchyard/internal/calib"
)

// HistoryFor is how far back the hub's samples are fetched: the 7-day lookback plus room.
const HistoryFor = 26 * time.Hour

// maxReply bounds one hub reply.
const maxReply = 16 << 20

// HubWindow is api.yaml Window, the fields the picker reads.
type HubWindow struct {
	Key       string     `json:"key"`
	UsedPct   *float64   `json:"used_pct"`
	ResetsAt  *time.Time `json:"resets_at"`
	UpdatedAt *time.Time `json:"updated_at"`
	State     string     `json:"state"`
}

// HubSeat is api.yaml Seat, the fields the picker reads.
type HubSeat struct {
	ID      string      `json:"id"`
	Account string      `json:"account"`
	Org     string      `json:"org"`
	Email   *string     `json:"email"`
	Label   *string     `json:"label"`
	Mode    *string     `json:"mode"` // the seat's picker mode (setup.md 8.5, Kind); nil: none
	Windows []HubWindow `json:"windows"`
	Places  []struct {
		Machine string  `json:"machine"`
		Slot    *string `json:"slot"`
	} `json:"places"`
	SignedOut []struct {
		Machine string    `json:"machine"`
		Slot    string    `json:"slot"`
		Since   time.Time `json:"since"`
	} `json:"signed_out"`
	LiveSessions int `json:"live_sessions"`
}

// HubSample is api.yaml LimitSample.
type HubSample struct {
	TS       time.Time  `json:"ts"`
	Key      string     `json:"key"`
	Source   string     `json:"source"`
	UsedPct  *float64   `json:"used_pct"`
	ResetsAt *time.Time `json:"resets_at"`
}

// HubLease is api.yaml UsageLease: a lease of a seat as the hub has it.
type HubLease struct {
	ID          string    `json:"id"`
	At          time.Time `json:"at"`
	Kind        string    `json:"kind"`
	C5          float64   `json:"c5"`
	CF          float64   `json:"cf"`
	Urgent      bool      `json:"urgent"`
	HasSession  bool      `json:"has_session"`
	Launched    bool      `json:"launched"`
	Ended       bool      `json:"ended"`
	BurnedUnits float64   `json:"burned_units"`
	Seen        bool      `json:"seen"`
	// SessionHash is the SHA-256 (hex) of the lease's session id ("": none, or an older hub).
	SessionHash string `json:"session_hash,omitempty"`
	// Burn is the session's units per minute while the lease is live: [seconds after the
	// pick, units]. Nil: not measured (not live, an older hub, or the kept copy).
	Burn *[][2]float64 `json:"burn,omitempty"`
}

// HubData is what the picker reads from the hub.
type HubData struct {
	Seats []HubSeat
	// Samples per seat id, any order.
	Samples map[string][]HubSample
	// Leases per seat id: other machines' leases (only from /v1/usage).
	Leases map[string][]HubLease
	// OwnLeases per seat id: this machine's leases of the last 5 hours as the hub measured
	// them (only from /v1/usage, by a hub that has them).
	OwnLeases map[string][]HubLease
	// Calib per seat id: the hub's calibration factors (only from /v1/usage; a seat the hub
	// has not calibrated yet has none).
	Calib map[string]calib.Factors
}

// HubClient reads the hub: /v1/usage with the machine token (or any read token), or
// /v1/accounts and the windows history with the board token.
type HubClient struct {
	URL   string
	Token string
	HTTP  *http.Client
}

// Fetch reads GET /v1/accounts, then the recent samples of each seat in want (all seats
// when want is nil) with GET /v1/accounts/{seat}/windows?since=…&samples=true. A seat whose
// history cannot be read keeps its current windows.
func (h HubClient) Fetch(ctx context.Context, now time.Time, want func(HubSeat) bool) (HubData, error) {
	var acc struct {
		Accounts []HubSeat `json:"accounts"`
	}
	if err := h.get(ctx, "/v1/accounts", &acc); err != nil {
		return HubData{}, err
	}
	d := HubData{Seats: acc.Accounts, Samples: map[string][]HubSample{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	since := now.Add(-HistoryFor).UTC().Format(time.RFC3339)
	for _, s := range d.Seats {
		if want != nil && !want(s) {
			continue
		}
		var keys []string
		for _, w := range s.Windows {
			if limitKey(w.Key) {
				keys = append(keys, w.Key)
			}
		}
		if len(keys) == 0 {
			continue
		}
		wg.Add(1)
		go func(id string, keys []string) {
			defer wg.Done()
			q := url.Values{"since": {since}, "samples": {"true"}, "key": {strings.Join(keys, ",")}}
			var out struct {
				Samples []HubSample `json:"samples"`
			}
			if h.get(ctx, "/v1/accounts/"+url.PathEscape(id)+"/windows?"+q.Encode(), &out) == nil {
				mu.Lock()
				d.Samples[id] = out.Samples
				mu.Unlock()
			}
		}(s.ID, keys)
	}
	wg.Wait()
	return d, nil
}

// ErrNoUsageEndpoint: the hub answered 404 for GET /v1/usage, so it is older than this wy.
var ErrNoUsageEndpoint = errors.New("the hub has no /v1/usage (it is older than this wy); upgrade the hub to give this machine hub data")

// FetchUsage reads GET /v1/usage?since=now-HistoryFor (setup.md 8.5): the narrow read a
// machine token may make. Places and signed-out folders in it are this machine's only.
func (h HubClient) FetchUsage(ctx context.Context, now time.Time) (HubData, error) {
	var doc struct {
		Machine *string `json:"machine"`
		Seats   []struct {
			ID           string          `json:"id"`
			Account      string          `json:"account"`
			Org          string          `json:"org"`
			Label        *string         `json:"label"`
			Mode         *string         `json:"mode"`
			Windows      []HubWindow     `json:"windows"`
			Samples      []HubSample     `json:"samples"`
			Leases       []HubLease      `json:"leases"`
			OwnLeases    []HubLease      `json:"own_leases"`
			Calibration  json.RawMessage `json:"calibration"`
			LiveSessions int             `json:"live_sessions"`
			Places       []struct {
				Slot string `json:"slot"`
			} `json:"places"`
			SignedOut []struct {
				Slot  string    `json:"slot"`
				Since time.Time `json:"since"`
			} `json:"signed_out"`
		} `json:"seats"`
	}
	q := url.Values{"since": {now.Add(-HistoryFor).UTC().Format(time.RFC3339)}}
	if err := h.get(ctx, "/v1/usage?"+q.Encode(), &doc); err != nil {
		return HubData{}, err
	}
	machine := ""
	if doc.Machine != nil {
		machine = *doc.Machine
	}
	d := HubData{Samples: map[string][]HubSample{}, Leases: map[string][]HubLease{}, OwnLeases: map[string][]HubLease{},
		Calib: map[string]calib.Factors{}}
	for _, x := range doc.Seats {
		s := HubSeat{ID: x.ID, Account: x.Account, Org: x.Org, Label: x.Label, Mode: x.Mode, Windows: x.Windows, LiveSessions: x.LiveSessions}
		for _, p := range x.Places {
			slot := p.Slot
			s.Places = append(s.Places, struct {
				Machine string  `json:"machine"`
				Slot    *string `json:"slot"`
			}{machine, &slot})
		}
		for _, p := range x.SignedOut {
			s.SignedOut = append(s.SignedOut, struct {
				Machine string    `json:"machine"`
				Slot    string    `json:"slot"`
				Since   time.Time `json:"since"`
			}{machine, p.Slot, p.Since})
		}
		d.Seats = append(d.Seats, s)
		d.Samples[x.ID] = x.Samples
		d.Leases[x.ID] = x.Leases
		d.OwnLeases[x.ID] = x.OwnLeases
		var f *calib.Factors
		if len(x.Calibration) > 0 && json.Unmarshal(x.Calibration, &f) == nil && f != nil && !f.At.IsZero() {
			d.Calib[x.ID] = *f
		}
	}
	return d, nil
}

func (h HubClient) get(ctx context.Context, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(h.URL, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "wy-account-next")
	hc := h.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	c := *hc
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("the hub refused this machine's token (%d)", resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound && strings.HasPrefix(path, "/v1/usage"):
		return ErrNoUsageEndpoint
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("the hub answered %d for %s", resp.StatusCode, strings.SplitN(path, "?", 2)[0])
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxReply+1))
	if err != nil {
		return err
	}
	if len(b) > maxReply {
		return fmt.Errorf("the hub's reply to %s is over %d bytes", strings.SplitN(path, "?", 2)[0], maxReply)
	}
	return json.Unmarshal(b, into)
}
