package api

import (
	"fmt"
	"net/http"
	"sync"
)

// The operations beyond the read API (pairing, devices, push, rules, alerts, machines, seats,
// keys, link, peers, app updates) are listed here once, with the scopes of api.yaml. Each is
// reserved (501 for everyone, before auth) until a handler file registers a factory for it
// from init():
//
//	func init() {
//		Register("GET", "/v1/devices", func(s *Server) http.Handler { return http.HandlerFunc(s.listDevices) })
//	}
//
// server.go merges this table into Routes() and Handler(): adding a handler means adding a
// file and removing the operation's `x-reserved` line in api.yaml, and nothing else.
// TestEveryAPIPathIsRouted checks the table, the registry and api.yaml agree (Reserved is
// true exactly while no factory is registered).

// HandlerFactory builds the handler of one operation. It runs once, in Server.Handler, so a
// handler may capture s and use s.Store(), s.Engine(), s.Auth, s.Broker, s.Config().
type HandlerFactory func(s *Server) http.Handler

var (
	registryMu sync.RWMutex
	registry   = map[string]HandlerFactory{}
)

// Register installs the handler of an operation of handlerRoutes. Call it from init(). It
// panics when the operation is not in that table (a typo, or a path owned by server.go) or
// is registered twice, so a mistake fails every test of the package at start.
func Register(method, path string, f HandlerFactory) {
	key := method + " " + path
	if f == nil {
		panic("api: Register " + key + ": nil factory")
	}
	if !inTable(handlerRoutes, method, path) {
		panic(fmt.Sprintf("api: Register %s: not an operation of handlerRoutes (routes.go)", key))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[key]; dup {
		panic("api: Register " + key + ": registered twice")
	}
	registry[key] = f
}

func registered(method, path string) (HandlerFactory, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	f, ok := registry[method+" "+path]
	return f, ok
}

func inTable(table []Route, method, path string) bool {
	for _, r := range table {
		if r.Method == method && r.Path == path {
			return true
		}
	}
	return false
}

// handlerRoutes: method, path, x-scopes exactly as api.yaml (nil = no bearer or cookie
// required: the operation authenticates itself). Reserved is filled in by Routes().
var handlerRoutes = []Route{
	// Pairing, devices, push, rules and alerts (pairing.md, push.md).
	{Method: "GET", Path: "/v1/me", Scopes: []string{"read", "write", "push", "owner"}},
	{Method: "POST", Path: "/v1/session/owner"},
	{Method: "POST", Path: "/v1/pair/codes", Scopes: []string{"owner"}},
	{Method: "GET", Path: "/v1/pair/codes/{id}", Scopes: []string{"owner"}},
	{Method: "DELETE", Path: "/v1/pair/codes/{id}", Scopes: []string{"owner"}},
	{Method: "POST", Path: "/v1/pair/claim"},
	{Method: "GET", Path: "/v1/devices", Scopes: []string{"owner"}},
	{Method: "GET", Path: "/v1/devices/{id}", Scopes: []string{"owner", "read", "push"}},
	{Method: "PATCH", Path: "/v1/devices/{id}", Scopes: []string{"owner", "push"}},
	{Method: "DELETE", Path: "/v1/devices/{id}", Scopes: []string{"owner", "push"}},
	{Method: "GET", Path: "/v1/push/config", Scopes: []string{"read", "push", "owner"}},
	{Method: "PUT", Path: "/v1/push/settings", Scopes: []string{"owner"}},
	{Method: "PUT", Path: "/v1/push/channels/{kind}", Scopes: []string{"owner"}},
	{Method: "GET", Path: "/v1/push/targets", Scopes: []string{"push", "owner"}},
	{Method: "POST", Path: "/v1/push/targets", Scopes: []string{"push", "owner"}},
	{Method: "PATCH", Path: "/v1/push/targets/{id}", Scopes: []string{"push", "owner"}},
	{Method: "DELETE", Path: "/v1/push/targets/{id}", Scopes: []string{"push", "owner"}},
	{Method: "POST", Path: "/v1/push/targets/{id}/test", Scopes: []string{"push", "owner"}},
	{Method: "GET", Path: "/v1/rules", Scopes: []string{"read", "push", "owner"}},
	{Method: "POST", Path: "/v1/rules", Scopes: []string{"owner"}},
	{Method: "PUT", Path: "/v1/rules/{id}", Scopes: []string{"owner"}},
	{Method: "DELETE", Path: "/v1/rules/{id}", Scopes: []string{"owner"}},
	{Method: "GET", Path: "/v1/alerts", Scopes: []string{"read", "push", "owner"}},
	{Method: "GET", Path: "/v1/alerts/{id}", Scopes: []string{"read", "push", "owner"}},
	{Method: "POST", Path: "/v1/alerts/{id}/snooze", Scopes: []string{"push", "owner"}},
	{Method: "POST", Path: "/v1/alerts/{id}/seen", Scopes: []string{"push", "owner"}},
	{Method: "POST", Path: "/v1/sessions/{id}/seen", Scopes: []string{"push", "owner"}},
	{Method: "POST", Path: "/v1/actions/{token}"},
	// An owner session makes an owner link for another device (pairing.md 4.1).
	{Method: "POST", Path: "/v1/owner-links", Scopes: []string{"owner"}},

	// Machines (machines.md 6.2). `GET /v1/machines` is a read operation in server.go.
	{Method: "PATCH", Path: "/v1/machines/{id}", Scopes: []string{"owner"}},
	{Method: "DELETE", Path: "/v1/machines/{id}", Scopes: []string{"owner"}},
	{Method: "POST", Path: "/v1/machines/{id}/pull", Scopes: []string{"owner"}},
	{Method: "GET", Path: "/v1/push/direct-policy", Scopes: []string{"write"}},

	// Seat usage for `wy account next` on a machine (setup.md 8.5).
	{Method: "GET", Path: "/v1/usage", Scopes: []string{"read", "write"}},

	// Seats (desktop.md C10, pwa.md Accounts).
	{Method: "PATCH", Path: "/v1/accounts/{seat}", Scopes: []string{"owner"}},
	{Method: "PUT", Path: "/v1/accounts/{seat}/drain", Scopes: []string{"owner", "write"}},
	{Method: "PUT", Path: "/v1/seats/order", Scopes: []string{"owner"}},
	// An account's usage relation, calibration and picker verdict (setup.md 8.5).
	{Method: "GET", Path: "/v1/accounts/{seat}/relation", Scopes: []string{"read"}},

	// Keys and the device link (keys.md, link.md). /v1/link and /v1/link/poll authenticate
	// themselves with the link's challenge login, never with a bearer.
	{Method: "GET", Path: "/v1/link"},
	{Method: "POST", Path: "/v1/link/poll"},
	{Method: "GET", Path: "/v1/keys", Scopes: []string{"owner", "read", "push", "write"}},
	{Method: "POST", Path: "/v1/keys/owner", Scopes: []string{"owner", "write"}},
	{Method: "POST", Path: "/v1/keys/owner/confirm", Scopes: []string{"owner"}},
	{Method: "GET", Path: "/v1/keys/revocations", Scopes: []string{"owner", "read", "push", "write"}},
	{Method: "POST", Path: "/v1/keys/revocations", Scopes: []string{"owner", "write"}},
	{Method: "PUT", Path: "/v1/devices/{id}/keys", Scopes: []string{"write", "push"}},
	{Method: "GET", Path: "/v1/devices/{id}/cert", Scopes: []string{"owner", "write"}},
	{Method: "POST", Path: "/v1/devices/{id}/cert", Scopes: []string{"owner", "write"}},
	{Method: "POST", Path: "/v1/devices/{id}/accounts", Scopes: []string{"owner", "write"}},

	// Peers (peers.md).
	{Method: "GET", Path: "/v1/peers", Scopes: []string{"owner", "read"}},
	{Method: "GET", Path: "/v1/peers/matrix", Scopes: []string{"owner", "read", "push", "write"}},
	{Method: "PUT", Path: "/v1/peers/matrix", Scopes: []string{"owner", "push", "write"}},
	{Method: "POST", Path: "/v1/peers/pause", Scopes: []string{"owner", "push", "write"}},
	{Method: "POST", Path: "/v1/peers/threads/{id}/resume", Scopes: []string{"owner"}},
	{Method: "GET", Path: "/v1/peers/messages", Scopes: []string{"owner", "read"}},
	{Method: "GET", Path: "/v1/peers/audit", Scopes: []string{"owner", "read"}},
	{Method: "GET", Path: "/v1/peers/trusted", Scopes: []string{"owner", "read"}},
	{Method: "POST", Path: "/v1/peers/trusted/revoke", Scopes: []string{"owner", "push"}},

	// Android app updates (phone.md P16).
	{Method: "GET", Path: "/v1/app/android", Scopes: []string{"owner", "read", "push"}},
	{Method: "GET", Path: "/v1/app/android/apk", Scopes: []string{"owner", "read", "push"}},
}

// handlerRoutesNow returns the table with Reserved set from the registry.
func handlerRoutesNow() []Route {
	out := make([]Route, len(handlerRoutes))
	for i, r := range handlerRoutes {
		_, ok := registered(r.Method, r.Path)
		r.Reserved = !ok
		out[i] = r
	}
	return out
}
