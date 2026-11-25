package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata" // old zone names (Asia/Calcutta) on hosts whose tzdata drops them, e.g. Ubuntu 26.04
	"unicode"

	"github.com/mraj602/watchyard/internal/hub/auth"
	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/store"
)

func init() {
	Register("GET", "/v1/devices", func(s *Server) http.Handler { return pairRequire(s, s.devList, "owner") })
	Register("GET", "/v1/devices/{id}", func(s *Server) http.Handler { return pairRequire(s, s.devGet, "owner", "read", "push") })
	Register("PATCH", "/v1/devices/{id}", func(s *Server) http.Handler { return pairRequire(s, s.devPatch, "owner", "push") })
	Register("DELETE", "/v1/devices/{id}", func(s *Server) http.Handler { return pairRequire(s, s.devDelete, "owner", "push") })
}

// devTargetJSON is api.yaml PushTarget as the Devices list shows it.
type devTargetJSON struct {
	ID           string     `json:"id"`
	Kind         string     `json:"kind"`
	Device       string     `json:"device"`
	Name         *string    `json:"name"`
	EndpointHost string     `json:"endpoint_host"`
	Endpoint     string     `json:"endpoint"`
	Enabled      bool       `json:"enabled"`
	Status       string     `json:"status"`
	LastOKAt     store.Time `json:"last_ok_at"`
	LastError    *string    `json:"last_error"`
	LastErrorAt  store.Time `json:"last_error_at"`
	CreatedAt    store.Time `json:"created_at"`
}

// devJSON is api.yaml Device.
type devJSON struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	Name         string          `json:"name"`
	Hint         string          `json:"hint"`
	Scopes       []string        `json:"scopes"`
	OwnerSession bool            `json:"owner_session"`
	Machine      *string         `json:"machine"`
	CreatedAt    store.Time      `json:"created_at"`
	CreatedBy    string          `json:"created_by"`
	LastUsedAt   store.Time      `json:"last_used_at"`
	LastUsedIP   *string         `json:"last_used_ip"`
	ExpiresAt    store.Time      `json:"expires_at"`
	RevokedAt    store.Time      `json:"revoked_at"`
	Current      bool            `json:"current"`
	Prefs        model.Prefs     `json:"prefs"`
	Targets      []devTargetJSON `json:"targets"`
	Keys         *devKeyJSON     `json:"keys"`
	Link         *linkStatusJSON `json:"link"`
}

// devKeyJSON is api.yaml DeviceKeyInfo (keys.md 4).
type devKeyJSON struct {
	SignFP      string     `json:"sign_fp"`
	BoxFP       string     `json:"box_fp"`
	Reg         string     `json:"reg"`
	OwnerSigned bool       `json:"owner_signed"`
	CertifiedAt store.Time `json:"certified_at"`
}

// devKeys returns every device's verified key summary; nil without a hub key or on error
// (the Devices list then shows no keys rather than failing).
func (s *Server) devKeys(ctx context.Context) map[string]*devKeyJSON {
	if s.cfg.Keys == nil {
		return nil
	}
	m, err := s.cfg.Keys.Certs(ctx)
	if err != nil {
		return nil
	}
	out := make(map[string]*devKeyJSON, len(m))
	for id, i := range m {
		out[id] = &devKeyJSON{SignFP: i.SignFP, BoxFP: i.BoxFP, Reg: i.Reg, OwnerSigned: i.OwnerSigned, CertifiedAt: store.Time(i.CertifiedAt)}
	}
	return out
}

func devStrp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// devPrefs decodes device.prefs with the defaults of push.md 5.4 filled in.
func devPrefs(raw string) model.Prefs {
	var p model.Prefs
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &p)
	}
	if p.Mute == nil {
		p.Mute = []model.Trigger{}
	}
	p.Tap = p.TapMode()
	return p
}

func devTarget(devID string, t store.DeviceTargetBrief) devTargetJSON {
	host := ""
	if u, err := url.Parse(t.Endpoint); err == nil {
		host = u.Host
	}
	status := "ok"
	switch {
	case t.GoneAt != 0:
		status = "gone"
	case !t.Enabled:
		status = "disabled"
	case t.FailCount > 0:
		status = "failing"
	case t.LastOKAt == 0:
		status = "new"
	}
	return devTargetJSON{ID: t.ID, Kind: t.Kind, Device: devID, Name: devStrp(t.Name), EndpointHost: host, Endpoint: t.Endpoint,
		Enabled: t.Enabled, Status: status, LastOKAt: store.Time(t.LastOKAt), LastError: devStrp(t.LastError),
		LastErrorAt: store.Time(t.LastErrAt), CreatedAt: store.Time(t.CreatedAt)}
}

// devView builds the Device of the API. current is the caller's device id.
func devView(d store.DeviceDetail, current string, targets []store.DeviceTargetBrief) devJSON {
	v := devJSON{ID: d.ID, Kind: d.Kind, Name: d.Name, Hint: d.TokenHint, Scopes: d.Scopes, OwnerSession: auth.IsOwnerSession(d.Device),
		Machine: devStrp(d.MachineID), CreatedAt: store.Time(d.CreatedAt), CreatedBy: d.CreatedBy, LastUsedAt: store.Time(d.LastUsedAt),
		LastUsedIP: devStrp(d.LastUsedIP), ExpiresAt: store.Time(d.ExpiresAt), RevokedAt: store.Time(d.RevokedAt),
		Current: d.ID == current, Prefs: devPrefs(d.Prefs), Targets: []devTargetJSON{}}
	if v.Scopes == nil {
		v.Scopes = []string{}
	}
	for _, t := range targets {
		v.Targets = append(v.Targets, devTarget(d.ID, t))
	}
	return v
}

func (s *Server) devList(w http.ResponseWriter, r *http.Request) {
	dev, _ := auth.DeviceFrom(r.Context())
	all := r.URL.Query().Get("all") == "true"
	ds, err := s.Store().DeviceDetails(r.Context(), all, s.now().UnixMilli())
	var targets map[string][]store.DeviceTargetBrief
	if err == nil {
		targets, err = s.Store().DeviceTargets(r.Context())
	}
	if err != nil {
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "database unavailable")
		return
	}
	out := make([]devJSON, 0, len(ds))
	ks := s.devKeys(r.Context())
	ls := s.devLinks(r.Context(), ks)
	for _, d := range ds {
		v := devView(d, dev.ID, targets[d.ID])
		v.Keys, v.Link = ks[d.ID], ls[d.ID]
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// devResolve resolves {id}: "self" is the caller; any other id needs the owner scope.
// ok is false when the response has been written.
func (s *Server) devResolve(w http.ResponseWriter, r *http.Request) (id string, caller store.Device, ok bool) {
	caller, _ = auth.DeviceFrom(r.Context())
	id = r.PathValue("id")
	if id == "self" {
		id = caller.ID
	}
	if id != caller.ID && !caller.HasScope("owner") {
		auth.WriteError(w, http.StatusForbidden, "forbidden", "this token can only use its own device (self)")
		return "", caller, false
	}
	return id, caller, true
}

func (s *Server) devGet(w http.ResponseWriter, r *http.Request) {
	id, caller, ok := s.devResolve(w, r)
	if !ok {
		return
	}
	s.devReply(w, r, id, caller.ID, http.StatusOK)
}

func (s *Server) devReply(w http.ResponseWriter, r *http.Request, id, current string, status int) {
	d, err := s.Store().DeviceDetailByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		auth.WriteError(w, http.StatusNotFound, "not_found", "no such device")
		return
	}
	var targets map[string][]store.DeviceTargetBrief
	if err == nil {
		targets, err = s.Store().DeviceTargets(r.Context())
	}
	if err != nil {
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "database unavailable")
		return
	}
	v := devView(d, current, targets[d.ID])
	ks := s.devKeys(r.Context())
	v.Keys, v.Link = ks[d.ID], s.devLinks(r.Context(), ks)[d.ID]
	writeJSON(w, status, v)
}

var (
	devHHMM   = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	devTZName = regexp.MustCompile(`^[A-Za-z0-9_+\-]+(/[A-Za-z0-9_+\-]+){0,2}$`)
)

// devValidTZ reports whether name is a time zone name the hub can load. The pattern keeps
// odd characters (and file paths) away from time.LoadLocation; LoadLocation says whether it
// is a real zone, so a bad one is refused here and not when the engine renders a push.
func devValidTZ(name string) bool {
	if len(name) > 64 || !devTZName.MatchString(name) {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// devCheckPrefs validates a prefs object (push.md 5.4) and returns it normalised.
func devCheckPrefs(raw json.RawMessage) (model.Prefs, error) {
	var p model.Prefs
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, errors.New("prefs must be an object like {\"mute\": [], \"tap\": \"auto\"}")
	}
	if p.Mute == nil {
		p.Mute = []model.Trigger{}
	}
	seen := map[model.Trigger]bool{}
	for _, t := range p.Mute {
		if !t.Valid() {
			return p, errors.New("prefs.mute has an unknown trigger " + string(t))
		}
		if seen[t] {
			return p, errors.New("prefs.mute lists " + string(t) + " twice")
		}
		seen[t] = true
	}
	if msg := checkMuteCategories(p.MuteCategories); msg != "" {
		return p, errors.New("prefs." + msg)
	}
	switch p.Tap {
	case "", model.TapAuto, model.TapSession, model.TapClaude:
	default:
		return p, errors.New("prefs.tap must be auto, session or claude")
	}
	p.Tap = p.TapMode()
	if p.TZ != nil && !devValidTZ(*p.TZ) {
		return p, errors.New("prefs.tz must be an IANA time zone name like Asia/Kolkata")
	}
	if q := p.QuietHours; q != nil {
		if !devHHMM.MatchString(q.Start) || !devHHMM.MatchString(q.End) {
			return p, errors.New("prefs.quiet_hours start and end must be HH:MM")
		}
		if q.TZ != "" && !devValidTZ(q.TZ) {
			return p, errors.New("prefs.quiet_hours.tz must be an IANA time zone name")
		}
	}
	return p, nil
}

func (s *Server) devPatch(w http.ResponseWriter, r *http.Request) {
	id, caller, ok := s.devResolve(w, r)
	if !ok {
		return
	}
	var body struct {
		Name  *string         `json:"name"`
		Prefs json.RawMessage `json:"prefs"`
	}
	if !pairDecode(w, r, &body) {
		return
	}
	var name, prefs *string
	if body.Name != nil {
		n := strings.TrimSpace(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, *body.Name))
		if n == "" || len([]rune(n)) > 60 {
			auth.WriteError(w, http.StatusBadRequest, "bad_request", "name must be 1 to 60 characters")
			return
		}
		name = &n
	}
	if len(body.Prefs) > 0 {
		p, err := devCheckPrefs(body.Prefs)
		if err != nil {
			auth.WriteError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		b, _ := json.Marshal(p)
		str := string(b)
		prefs = &str
	}
	if name == nil && prefs == nil {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "send name and/or prefs")
		return
	}
	if err := s.Store().UpdateDevice(r.Context(), id, name, prefs); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			auth.WriteError(w, http.StatusNotFound, "not_found", "no such device")
		} else {
			auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "database unavailable")
		}
		return
	}
	s.devReply(w, r, id, caller.ID, http.StatusOK)
}

func (s *Server) devDelete(w http.ResponseWriter, r *http.Request) {
	id, caller, ok := s.devResolve(w, r)
	if !ok {
		return
	}
	if id == caller.ID && auth.IsOwnerSession(caller) {
		auth.WriteError(w, http.StatusConflict, "conflict", "this is the session you are signed in with; use sign out instead")
		return
	}
	if _, err := s.Store().DeviceDetailByID(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		auth.WriteError(w, http.StatusNotFound, "not_found", "no such device")
		return
	} else if err != nil {
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "database unavailable")
		return
	}
	// Already revoked counts as done (ErrNotFound from the cascade). With a hub key the
	// device's keys also go on the next revocation list (keys.md 6).
	revoke := func() error { return auth.Revoke(r.Context(), s.Store(), id) }
	if s.cfg.Keys != nil {
		revoke = func() error { return s.cfg.Keys.RevokeDevice(r.Context(), id, caller.ID, "revoked") }
	}
	if err := revoke(); err != nil && !errors.Is(err, store.ErrNotFound) {
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "database unavailable")
		return
	}
	s.linkRevoked(r.Context(), id)
	if id == caller.ID {
		w.Header().Del("Set-Cookie") // drop the daily refresh made by Require
		auth.ClearCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
}
