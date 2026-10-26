package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/mraj602/watchyard/internal/account"
	"github.com/mraj602/watchyard/internal/atomicfile"
	"github.com/mraj602/watchyard/internal/clientenv"
	"github.com/mraj602/watchyard/internal/outbox"
	"github.com/mraj602/watchyard/internal/record"
)

const (
	// sentName is <state>/identity/sent.json: the login, label and colour last sent in a
	// slot_info, per slot (identity.md 6).
	sentName = "sent.json"
	// slotInfoEvery is the "once a day per slot" of record.md 4.7.
	slotInfoEvery = 24 * time.Hour
)

// sentSlot is what the last slot_info of one slot said.
type sentSlot struct {
	Account  string    `json:"account,omitempty"`
	Org      string    `json:"org,omitempty"`
	Email    string    `json:"email,omitempty"`
	LoggedIn bool      `json:"logged_in"`
	Label    string    `json:"label,omitempty"`
	Colour   string    `json:"colour,omitempty"`
	At       time.Time `json:"at"`
}

// same reports whether two reports say the same thing (At aside).
func (s sentSlot) same(o sentSlot) bool {
	s.At, o.At = time.Time{}, time.Time{}
	return s == o
}

// sameLogin reports whether two reports name the same login (label, colour and At aside).
func (s sentSlot) sameLogin(o sentSlot) bool {
	return s.Account == o.Account && s.Org == o.Org && s.Email == o.Email && s.LoggedIn == o.LoggedIn
}

// maybeSlotInfo queues a slot_info on SessionStart when the slot's login, label or colour
// differs from the last one sent for it, or that one is a day old, and on any other hook
// event when the login differs (record.md 4.7). That is how a folder that was logged in
// again, logged out, or labelled after `wy connect` reaches the hub, and the label follows
// the login to its new seat, even when `/login` ran inside a session that goes on. It reads
// only the login the hook already has and one small file. Errors are logged, never returned.
func maybeSlotInfo(env *clientenv.Env, getenv func(string) string, event string, now time.Time) {
	if env.Slot.Name == "" {
		return
	}
	cur := sentSlot{LoggedIn: env.Login.HasSeat, Label: env.Config.Slots.Labels[env.Slot.Name],
		Colour: env.Config.Slots.Colours[env.Slot.Name], At: now.UTC()}
	if env.Login.HasSeat {
		cur.Account, cur.Org, cur.Email = env.Login.Seat.Account, env.Login.Seat.Org, env.Login.Seat.Email
	}
	path := filepath.Join(env.Dirs.State.Path, "identity", sentName)
	sent := map[string]sentSlot{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &sent) // a torn file just means "send again"
	}
	last, ok := sent[env.Slot.Name]
	if event != "SessionStart" && (!ok || last.sameLogin(cur)) {
		return
	}
	if ok && last.same(cur) && now.Sub(last.At) < slotInfoEvery && !last.At.After(now) {
		return
	}
	p := record.SlotInfoPayload{Folder: env.Slot.Name, Default: env.Slot.Default, LoggedIn: cur.LoggedIn}
	if env.Login.HasSeat && env.Login.Seat.Src == "claude_json" {
		p.OAuthAccount = oauthAccount(env.Slot.StateFile, cur)
	}
	if !env.Login.AuthStatusAt.IsZero() {
		p.AuthStatus, _ = json.Marshal(env.Login.Auth)
	}
	// An invalid hand-edited label or colour is left out rather than failing the report.
	if x, err := account.SlotInfoExtra(cur.Label, cur.Colour); err == nil && len(x) > 0 {
		p.Extra = x
	}
	rec, err := record.NewSlotInfo(env.Meta(nil), p)
	if err != nil {
		logError(env, getenv, event, err)
		return
	}
	ob := outbox.New(env.OutboxDir())
	ob.NoFsync = !env.Config.Outbox.Fsync
	if _, err := ob.Append(rec); err != nil {
		logError(env, getenv, event, err)
		return
	}
	sent[env.Slot.Name] = cur
	b, _ := json.MarshalIndent(sent, "", "  ")
	err = os.MkdirAll(filepath.Dir(path), 0o700)
	if err == nil {
		err = atomicfile.Write(path, append(b, '\n'), 0o600)
	}
	if err != nil {
		logError(env, getenv, event, err)
	}
}

// oauthAccount is the state file's oauthAccount object, kept whole (record.md 4.7: it holds
// no tokens), when it still names the seat the hook read; otherwise just that seat's uuids
// and email. The file is read only here, on a login change or once a day.
func oauthAccount(stateFile string, cur sentSlot) json.RawMessage {
	var f struct {
		OAuthAccount json.RawMessage `json:"oauthAccount"`
	}
	if b, err := os.ReadFile(stateFile); err == nil && json.Unmarshal(b, &f) == nil && len(f.OAuthAccount) > 0 {
		var ids struct {
			Account string `json:"accountUuid"`
			Org     string `json:"organizationUuid"`
		}
		if json.Unmarshal(f.OAuthAccount, &ids) == nil && ids.Account == cur.Account && ids.Org == cur.Org {
			return f.OAuthAccount
		}
	}
	b, _ := json.Marshal(map[string]string{"accountUuid": cur.Account, "organizationUuid": cur.Org, "emailAddress": cur.Email})
	return b
}
