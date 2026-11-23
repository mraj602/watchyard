package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Version is the release version. Overridden at link time; "dev" for local builds.
var Version = "dev"

// Repo is the GitHub repository (owner/name) releases are published to; the hub's phone-app
// mirror and the app's updater read releases from it (phone.md P16.2). Overridden at link
// time like Version.
var Repo = "mraj602/watchyard"

// Commit is the git commit, set at link time. When empty, the VCS info embedded by the
// Go toolchain is used, if any.
var Commit = ""

// String returns a one-line description, e.g. "wy v0.1.0 (abc1234) linux/amd64 go1.27.2".
func String() string {
	commit := Commit
	if commit == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, s := range info.Settings {
				if s.Key == "vcs.revision" && len(s.Value) >= 7 {
					commit = s.Value[:7]
				}
			}
		}
	}
	if commit == "" {
		commit = "unknown"
	}
	return fmt.Sprintf("wy %s%s (%s) %s/%s %s", Version, buildSuffix, commit, runtime.GOOS, runtime.GOARCH, runtime.Version())
}
