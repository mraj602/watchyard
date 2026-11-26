package api_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"
)

// A settings PATCH takes no long lock and does not queue behind analytics (analytics.md 2): with
// the analytics slot taken and more board reports waiting for it, the waiting reports hold no
// read connection, and a PATCH needs only one free connection. On 2026-11-25 four reports took
// the whole read pool for a minute and a drain PATCH took 47 s.
func TestPatchNotBehindAnalytics(t *testing.T) {
	h := startHub(t, "")
	machine := h.token("machine")
	owner := ownerToken(t, h)
	h.ingest(machine, webUsageRec(t, h.machine, "t_9c1e5f0a7b2d4e68", t0, usageDoc(t), nil))
	id := h.onlySeat(owner).ID

	release := h.srv.HoldAnalytics()
	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _ = h.do("GET", "/v1/analytics/heatmap?days=30", owner, nil)
		}()
	}
	// Give the reports time to pass auth and start waiting for the slot.
	time.Sleep(300 * time.Millisecond)
	if n := h.st.R().Stats().InUse; n != 0 {
		t.Errorf("reports waiting for the slot hold %d read connections", n)
	}

	// Every read connection but one busy elsewhere.
	ctx := context.Background()
	var held []*sql.Conn
	for range h.st.R().Stats().MaxOpenConnections - 1 {
		c, err := h.st.R().Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	until := time.Now().Add(50 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	start := time.Now()
	code, b := h.do("PATCH", "/v1/accounts/"+id, owner, []byte(`{"drain_until":"`+until+`"}`))
	if took := time.Since(start); code != 200 || took > 2*time.Second {
		t.Errorf("PATCH: %d %s in %v", code, b, took)
	}
	for _, c := range held {
		c.Close()
	}

	release()
	wg.Wait()
	for i, c := range codes {
		if c != 200 {
			t.Errorf("report %d after the slot freed: %d", i, c)
		}
	}
}
