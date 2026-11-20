package picker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
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
	TS      time.Time `json:"ts"`
	Key     string    `json:"key"`
	Source  string    `json:"source"`
	UsedPct *float64  `json:"used_pct"`
}

// HubData is what the picker reads from the hub.
type HubData struct {
	Seats []HubSeat
	// Samples per seat id, any order.
	Samples map[string][]HubSample
}

// HubClient reads the hub with a read-scoped token (the board token).
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
		return fmt.Errorf("the hub refused the board token (%d)", resp.StatusCode)
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
