package devserver

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestSwitchesMock(t *testing.T) {
	srv := New(Options{})
	get := func(path string, want int) map[string]any {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+OwnerToken)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	all := get("/v1/switches", 200)
	sum := all["summary"].(map[string]any)
	if len(all["moves"].([]any)) != 6 || sum["moves"].(float64) != 5 || sum["failed"].(float64) != 1 || sum["sessions_twice"].(float64) != 2 {
		t.Fatalf("all %v", all)
	}
	// The failed move is not a step: the deploy session has two that worked, numbered 1 and 2.
	deploy := get("/v1/switches?session=c41e0000-0000-4000-8000-000000000004", 200)["moves"].([]any)
	if len(deploy) != 3 || deploy[0].(map[string]any)["result"] != "failed" || deploy[1].(map[string]any)["move_no"].(float64) != 2 {
		t.Fatalf("deploy %v", deploy)
	}
	p1 := get("/v1/switches?limit=4", 200)
	if len(p1["moves"].([]any)) != 4 || p1["next"] == nil {
		t.Fatalf("page 1 %v", p1)
	}
	p2 := get("/v1/switches?limit=4&cursor="+p1["next"].(string), 200)
	if len(p2["moves"].([]any)) != 2 || p2["next"] != nil {
		t.Fatalf("page 2 %v", p2)
	}
	if n := len(get("/v1/switches?mode=manual", 200)["moves"].([]any)); n != 1 {
		t.Fatalf("manual %d", n)
	}
	get("/v1/switches?mode=x", 400)
}
