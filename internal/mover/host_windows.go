//go:build windows

package mover

import (
	"errors"

	"github.com/mraj602/watchyard/internal/peers"
)

// RealHost: moving sessions is not supported on Windows (no SIGKILL, no tmux).
type RealHost struct {
	Proc  peers.Proc
	State State
}

var errWindows = errors.New("moving sessions is not supported on Windows")

func (h RealHost) Alive(Launch) bool                    { return false }
func (h RealHost) Kill(int) error                       { return errWindows }
func (h RealHost) Tmux(...string) error                 { return errWindows }
func (h RealHost) Start(string, []string, string) error { return errWindows }
func (h RealHost) Order(int, RunOrder) error            { return errWindows }
func (h RealHost) Note(string, string) error            { return errWindows }
func (h RealHost) Carry(Launch, string) error           { return errWindows }
func (h RealHost) StopJob(string, string) error         { return errWindows }
func (h RealHost) Copies(string, string, int) []int     { return nil }
func (h RealHost) TTY(int) string                       { return "" }
func (h RealHost) WriteTTY(string, string) error        { return errWindows }

// CleanEnv is env unchanged on Windows.
func CleanEnv(env []string) []string { return env }
