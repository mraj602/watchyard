package peers

import (
	"fmt"

	"github.com/mraj602/watchyard/internal/ccjobs"
)

// Claude Code's per-account daemon ends background sessions that sit idle (peers.md 3.5);
// a tag holder that ended takes its tags with it. These texts tell the session, and whoever
// sends to a tag, how to keep a holder running. Pinning is the user's: nothing in Watchyard
// writes Claude Code's pins.

// PinHow is how a person pins a background session.
const PinHow = "`claude agents`, select the session, Ctrl+T"

// NoHolderHint follows a no_holder refusal (peers.md 3.4): the likely reason a tag that had
// a holder has none now, and the fix.
const NoHolderHint = "A holder may have been ended by Claude Code for being idle: it ends unpinned background " +
	"sessions after about 1 to 8 hours idle. Pin the holder (" + PinHow + "); `claude respawn <id>` starts an ended one again."

// KeepAliveNote is what `wy peers tag add` prints after a tag is added (peers.md 3.5) when
// the calling session is a Claude Code background session of configDir whose job is not
// pinned, or "" otherwise (an interactive session, a pinned one, or files that cannot be
// read).
func KeepAliveNote(configDir, sessionID, tag string) string {
	short := ccjobs.JobOf(configDir, sessionID)
	if short == "" || ccjobs.ReadPins(configDir)[short] {
		return ""
	}
	return fmt.Sprintf("Note: this is a Claude Code background session (%s) that is not pinned. Claude Code ends idle "+
		"background sessions after about 1 to 8 hours, and its tags end with it, so sends to tag %s would then find "+
		"no holder. If something sends to this tag, ask the user to pin this session (%s). Do not edit Claude "+
		"Code's files yourself.", short, tag, PinHow)
}
