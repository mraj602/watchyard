package peers

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Session tags (peers.md 3.4): short slugs a session gives itself so others can address it
// by role (`wy peers send tag:<tag>`). They are held at the hub per session, set over the
// agent's link (`peer_tag`, link.md 6.2), carried in the hub's directory, and cleared when the
// session ends or goes away. A tag is a name a session claims, not an identity: every message
// to a tag holder passes the same checks as any other send.

// Tag limits (peers.md 3.4).
const (
	MaxTagLen         = 40
	MaxTagsPerSession = 8
	// TagPrefix starts a tag address (peers.md 3.3).
	TagPrefix = "tag:"
)

// Tag operations of the agent's `tag` op and the `peer_tag` frame.
const (
	TagAdd  = "add"
	TagRm   = "rm"
	TagList = "list"
)

// Reason words of a refused `peer_tag` (link.md 6.2), carried in the `peers` answer's tag.error.
const (
	TagErrBadTag    = "bad_tag"
	TagErrTooMany   = "too_many_tags"
	TagErrForbidden = "forbidden"
	TagErrUnknown   = "unknown_session"
)

var tagRE = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// ValidTag reports whether t is a tag: 1-40 characters of lowercase a-z, 0-9 and '-'.
func ValidTag(t string) bool { return tagRE.MatchString(t) }

// CheckTag returns a usage refusal for an invalid tag.
func CheckTag(t string) *Refusal {
	if ValidTag(t) {
		return nil
	}
	return Refuse(CodeUsage, "%q is not a tag: use 1-%d characters of lowercase a-z, 0-9 and '-'", t, MaxTagLen)
}

// ParseTagAddress reports whether to is a tag address `tag:<tag>` (peers.md 3.3) and returns
// the tag. The prefix matches without case. With '@' or '.' after it, to is a handle (a session
// named "tag:x") and resolves as before; any other text that is not a valid tag is a usage
// error (bad), never a fall-through to handle matching.
func ParseTagAddress(to string) (tag string, isTag bool, bad *Refusal) {
	to = strings.TrimSpace(to)
	if len(to) < len(TagPrefix) || !strings.EqualFold(to[:len(TagPrefix)], TagPrefix) {
		return "", false, nil
	}
	rest := to[len(TagPrefix):]
	if strings.ContainsAny(rest, "@.") {
		return "", false, nil // a handle such as tag:x@work.laptop: resolve it as one
	}
	if rf := CheckTag(rest); rf != nil {
		return "", true, rf
	}
	return rest, true, nil
}

// NormTags is a sorted copy of tags without duplicates.
func NormTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	out := append([]string(nil), tags...)
	sort.Strings(out)
	n := 0
	for i, t := range out {
		if i > 0 && t == out[n-1] {
			continue
		}
		out[n] = t
		n++
	}
	return out[:n]
}

// HasTag reports whether tags holds t.
func HasTag(tags []string, t string) bool {
	for _, x := range tags {
		if x == t {
			return true
		}
	}
	return false
}

// TagHolders are the entries that hold tag and that the asking session may message: `can` is
// not none (the list is already filtered by the matrix inside a session) and nothing blocks it.
// Order: the --one preference (StateRank), then handle, then session id.
func TagHolders(entries []ListEntry, tag string) []ListEntry {
	var out []ListEntry
	for _, le := range entries {
		if !HasTag(le.Tags, tag) || le.Can == "none" || le.Can == "" || le.Blocked != "" {
			continue
		}
		out = append(out, le)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := StateRank(a.State), StateRank(b.State); ra != rb {
			return ra < rb
		}
		if ha, hb := strings.ToLower(a.Handle), strings.ToLower(b.Handle); ha != hb {
			return ha < hb
		}
		return a.Session < b.Session
	})
	return out
}

// TagTargets are the holders a tag send may go to (peers.md 3.4), in the order of
// TagHolders: every eligible holder, and with sameHost only those on this machine under this
// OS user. The sender is never in the list (Entries leaves it out). A --one send delivers to
// the first; the next ones are tried only when the first has gone (target_gone,
// unknown_target).
func TagTargets(entries []ListEntry, tag string, sameHost bool) []ListEntry {
	hs := TagHolders(entries, tag)
	if !sameHost {
		return hs
	}
	n := 0
	for _, h := range hs {
		if h.SameHost {
			hs[n] = h
			n++
		}
	}
	return hs[:n]
}

// StateRank orders holders for --one: ready first (idle, done), then states that wait on
// their person or are not known, then busy ones (working, busy).
func StateRank(state string) int {
	switch state {
	case "idle", "done":
		return 0
	case "working", "busy":
		return 2
	}
	return 1
}

// TagNoticeText is the notice each existing holder gets when another session tags itself.
func TagNoticeText(handle, tag string) string {
	return fmt.Sprintf("%s also tagged itself %s. The holders of %s decide between themselves which of them keeps it; one may run `wy peers tag rm %s`.",
		handle, tag, tag, tag)
}

// TagNotice is the outcome of one notice sent on `tag add`.
type TagNotice struct {
	Handle string `json:"handle"`
	Status string `json:"status"` // delivered | queued | the refusal code
	Line   string `json:"line"`
}

// TagResult is the outcome of a tag op: the session's tags afterwards; for add, whether it was
// new, the other holders the session may message and the notices sent to them.
type TagResult struct {
	Tags    []string    `json:"tags"`
	Changed bool        `json:"changed"`
	Holders []ListEntry `json:"holders,omitempty"`
	Notices []TagNotice `json:"notices,omitempty"`
}

// TagAnswer is the hub's answer to one `peer_tag` (PeersReply.Tag).
type TagAnswer struct {
	Session string   `json:"session"`
	Tags    []string `json:"tags"`
	Changed bool     `json:"changed"`
	Error   string   `json:"error,omitempty"`
}

// tagRefusal turns a refused peer_tag into the caller's refusal.
func tagRefusal(word, tag string) *Refusal {
	switch word {
	case TagErrBadTag:
		return CheckTag(tag)
	case TagErrTooMany:
		return Refuse(CodeLimitTags, "this session already has %d tags, the most it may hold; remove one with wy peers tag rm", MaxTagsPerSession)
	case TagErrForbidden, TagErrUnknown:
		return Refuse(CodeNotASession, "the hub does not know this session as one of this machine's live sessions yet; try again in a moment")
	}
	return Refuse(CodeFailed, "the hub refused the tag (%s)", word)
}

// Tag is `wy peers tag add|rm` and `wy peers tags` for the calling session (peers.md 3.4).
// Tags live at the hub, so it needs Env.Remote (the agent's link). After an add that is new,
// each other holder the matrix lets this session message is sent the notice of TagNoticeText
// through the normal send path (SendWith: content, rates, matrix and loop guard apply), and
// the holders are returned so the command can print them to the new holder.
func (e Env) Tag(c Caller, op, tag string) (TagResult, *Refusal) {
	if e.Remote == nil {
		return TagResult{}, Refuse(CodeHubUnreachable, "tags are held at the hub and need the wy agent with a link to it (wy agent enable)")
	}
	if op != TagList {
		if rf := CheckTag(tag); rf != nil {
			return TagResult{}, rf
		}
	}
	if op == TagAdd && e.Off() {
		return TagResult{}, Refuse(CodeOff, "peers are off on this machine (wy peers on turns them back on)")
	}
	if op == TagList {
		dir, _, err := e.directoryAll()
		if err != nil {
			return TagResult{}, Refuse(CodeHubUnreachable, "the hub cannot be asked for this session's tags now (%v)", err)
		}
		for _, s := range dir {
			if s.Entry.SessionID == c.Entry.SessionID {
				return TagResult{Tags: NormTags(s.Tags)}, nil
			}
		}
		return TagResult{}, Refuse(CodeNotASession, "this session's inbox socket is not answering")
	}
	ans, err := e.Remote.Tag(c.Entry.SessionID, op, tag)
	if err != nil {
		return TagResult{}, Refuse(CodeHubUnreachable, "the hub cannot be reached now (%v)", err)
	}
	if ans.Error != "" {
		return TagResult{}, tagRefusal(ans.Error, tag)
	}
	res := TagResult{Tags: NormTags(ans.Tags), Changed: ans.Changed}
	if op != TagAdd {
		return res, nil
	}
	// The other holders, as this session sees them (the hub's answer refreshed the directory).
	dir, everyone, _ := e.directoryAll()
	var from *Session
	for i := range dir {
		if dir[i].Entry.SessionID == c.Entry.SessionID {
			from = &dir[i]
		}
	}
	if from == nil {
		return res, nil
	}
	res.Holders = TagHolders(e.Entries(everyone, from, false), tag)
	if !ans.Changed {
		return res, nil // already held: the holders heard of it the first time
	}
	text := TagNoticeText(from.Handle, tag)
	for _, h := range res.Holders {
		n := TagNotice{Handle: h.Handle}
		r, rf := e.SendWith(c, h.Session, text, SendOpts{})
		switch {
		case rf != nil:
			n.Status, n.Line = rf.Code, Describe(rf)
		default:
			n.Status, n.Line = "delivered", r.Line()
			if r.Status == "queued" {
				n.Status = "queued"
			}
		}
		res.Notices = append(res.Notices, n)
	}
	return res, nil
}
