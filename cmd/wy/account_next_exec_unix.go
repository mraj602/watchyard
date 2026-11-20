//go:build !windows

package main

import "syscall"

// execWithEnv replaces this process with path and the given environment.
func execWithEnv(path string, argv, env []string) error {
	return syscall.Exec(path, argv, env)
}
