// Package mover decides when a running Claude Code session should continue under another
// account, and how to move it (docs/spec/setup.md 8.6, `wy move`).
//
// The decision is predictive: per account and window (5 hours, the week) it forecasts the
// level from the burn of the sessions running on it now and moves sessions only when the
// account would reach its limit before that window resets. A window that resets first needs
// nothing. Forecast and Plan are pure functions of their input, so the simulator
// (sim_test.go) and the command share them.
//
// The mechanics (session.go, exec.go) find a session's process and terminal, stop the claude
// process only (SIGKILL, so the session's background shells keep running) and start
// `claude --resume <id>` under the other account the way the session was started: in its
// tmux pane, under `wy run`, or headless. Everything that touches a real process goes through
// the Host interface; tests use a fake.
package mover
