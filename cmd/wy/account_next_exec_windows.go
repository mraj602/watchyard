//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
)

// execWithEnv runs path in this console with the given environment and exits with its
// code: Windows has no exec.
func execWithEnv(path string, argv, env []string) error {
	cmd := exec.Command(path, argv[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return exitWith(ee.ExitCode(), nil)
	}
	return err
}
