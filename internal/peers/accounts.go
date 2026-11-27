package peers

// Accounts maps the account uuid of every managed slot whose login is known to the slot's
// label (identity.md 3, peers.md 3.2): the accounts this machine can name in the matrix. The
// login comes from the cache the hooks keep; no credential file is opened.
func (e Env) Accounts() map[string]string {
	out := map[string]string{}
	login := e.loginReader()
	for _, slot := range e.Slots() {
		if l, err := login(slot); err == nil && l.HasSeat && l.Seat.Account != "" {
			if _, dup := out[l.Seat.Account]; !dup {
				out[l.Seat.Account] = e.accountLabel(slot.Name)
			}
		}
	}
	for a, l := range e.HostAccounts() {
		if _, dup := out[a]; !dup {
			out[a] = l
		}
	}
	return out
}
