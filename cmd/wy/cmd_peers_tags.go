package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mraj602/watchyard/internal/agent"
	peerspkg "github.com/mraj602/watchyard/internal/peers"
)

// Session tags (peers.md 3.4): `wy peers tag add|rm <tag>`, `wy peers tags`, and the tag
// address of `wy peers send tag:<tag> [--one] [--same-host]`.

const peersTagUsage = "usage: wy peers tag add <tag>\n       wy peers tag rm <tag>\n       wy peers tags\n"

// peersTagCmd is `wy peers tag add|rm <tag>` (args after "tag") and, with op list, `wy peers tags`.
func peersTagCmd(op string, args []string, stdout, stderr io.Writer) error {
	tag := ""
	switch op {
	case peerspkg.TagAdd, peerspkg.TagRm:
		if len(args) != 1 {
			fmt.Fprint(stderr, peersTagUsage)
			return errUsage
		}
		tag = strings.TrimSpace(args[0])
		if rf := peerspkg.CheckTag(tag); rf != nil {
			fmt.Fprintf(stderr, "wy peers tag: %s\n", rf.Detail)
			return errUsage
		}
	case peerspkg.TagList:
		if len(args) != 0 {
			fmt.Fprint(stderr, peersTagUsage)
			return errUsage
		}
	default:
		fmt.Fprint(stderr, peersTagUsage)
		return errUsage
	}
	pe, err := peersEnv()
	if err != nil {
		return err
	}
	if !pe.Enabled {
		return peersExit(peerspkg.ExitPolicy, errors.New("peers are not enabled on this machine (wy peers enable)"))
	}
	caller, rf := pe.Env.Whoami()
	if rf != nil {
		return peersExit(peerspkg.ExitCode(rf.Code), errors.New(rf.Detail))
	}
	res, rf, err := peersTagCall(pe, caller, op, tag)
	if err != nil {
		return err
	}
	if rf != nil {
		fmt.Fprintln(stdout, "not done: "+strings.TrimPrefix(peerspkg.Describe(rf), "not delivered: "))
		return peersExit(peerspkg.ExitCode(rf.Code), nil)
	}
	printTagResult(stdout, op, tag, res)
	return nil
}

// peersTagCall runs the tag op through the agent; without one the direct path answers that
// tags need the agent's link to the hub (Env.Tag with no Remote).
func peersTagCall(pe peerEnv, caller peerspkg.Caller, op, tag string) (peerspkg.TagResult, *peerspkg.Refusal, error) {
	rep, err := peersAgent(pe, agent.Request{Op: agent.OpTag, TagOp: op, Tag: tag})
	switch {
	case err == nil:
		if rf := rep.Refusal(); rf != nil {
			return peerspkg.TagResult{}, rf, nil
		}
		if rep.TagResult == nil {
			return peerspkg.TagResult{}, peerspkg.Refuse(peerspkg.CodeUnsupported, "the running wy agent does not know tags; restart it (wy agent enable)"), nil
		}
		return *rep.TagResult, nil, nil
	case !errors.Is(err, agent.ErrNoAgent):
		return peerspkg.TagResult{}, nil, fmt.Errorf("wy agent: %w", err)
	}
	res, rf := pe.Env.Tag(caller, op, tag)
	return res, rf, nil
}

// printTagResult prints what a tag op did. After an add the other holders come first, so the
// new holder learns of them in its own context (peers.md 3.4), then the notices they were sent.
func printTagResult(w io.Writer, op, tag string, res peerspkg.TagResult) {
	switch op {
	case peerspkg.TagList:
		if len(res.Tags) == 0 {
			fmt.Fprintln(w, "This session has no tags.")
			return
		}
		fmt.Fprintln(w, strings.Join(res.Tags, "\n"))
		return
	case peerspkg.TagRm:
		if res.Changed {
			fmt.Fprintf(w, "removed tag %s.\n", tag)
		} else {
			fmt.Fprintf(w, "this session did not have tag %s.\n", tag)
		}
		return
	}
	if res.Changed {
		fmt.Fprintf(w, "tagged this session %s.\n", tag)
	} else {
		fmt.Fprintf(w, "this session already has tag %s.\n", tag)
	}
	if len(res.Holders) == 0 {
		fmt.Fprintf(w, "No other session you may message holds %s.\n", tag)
		return
	}
	fmt.Fprintf(w, "Other holders of %s (decide between you which keeps it; wy peers tag rm %s gives it up):\n", tag, tag)
	for _, h := range res.Holders {
		fmt.Fprintf(w, "  %s  (%s)\n", h.Handle, h.State)
	}
	if len(res.Notices) > 0 {
		fmt.Fprintln(w, "Told each of them:")
		for _, n := range res.Notices {
			fmt.Fprintf(w, "  %s\n", n.Line)
		}
	}
}

// filterTag keeps the entries that hold tag.
func filterTag(entries []peerspkg.ListEntry, tag string) []peerspkg.ListEntry {
	var out []peerspkg.ListEntry
	for _, le := range entries {
		if peerspkg.HasTag(le.Tags, tag) {
			out = append(out, le)
		}
	}
	return out
}

// peersSendTag is `wy peers send tag:<tag>` (peers.md 3.4): the holders this session may
// message, from its own list, then one normal send per target (every check of 4.2 applies to
// each). The list needs the agent: without it no tag is known (exit 7, hub_unreachable).
func peersSendTag(pe peerEnv, caller peerspkg.Caller, tag, text string, one, sameHost bool, ttl time.Duration, wait int, stdout io.Writer) error {
	if _, rf := peerspkg.CheckText(text); rf != nil {
		fmt.Fprintln(stdout, peerspkg.Describe(rf))
		return peersExit(peerspkg.ExitCode(rf.Code), nil)
	}
	rep, err := peersAgent(pe, agent.Request{Op: agent.OpList})
	switch {
	case errors.Is(err, agent.ErrNoAgent):
		fmt.Fprintln(stdout, "not delivered: tags are held at the hub and need the wy agent (wy agent enable). Do not retry.")
		return peersExit(peerspkg.ExitNotSent, nil)
	case err != nil:
		return fmt.Errorf("wy agent: %w", err)
	}
	if rf := rep.Refusal(); rf != nil {
		fmt.Fprintln(stdout, peerspkg.Describe(rf))
		return peersExit(peerspkg.ExitCode(rf.Code), nil)
	}
	targets := peerspkg.TagTargets(rep.Entries, tag, sameHost)
	return deliverTag(stdout, tag, targets, one, sameHost, func(to string) (peerspkg.SendResult, *peerspkg.Refusal, error) {
		return peersDeliver(pe, caller, to, text, ttl, wait)
	})
}

// deliverTag sends to the targets of a tag send and prints one line per target. With one, it
// delivers to the first; a target that has gone meanwhile (target_gone, unknown_target) passes
// the message to the next. No target: exit 3 (no_holder). Exit code: with one, that of the
// target it ended on; else 0 when every target was delivered, otherwise the code of the first
// line that was not (8 for queued).
func deliverTag(w io.Writer, tag string, targets []peerspkg.ListEntry, one, sameHost bool,
	deliver func(to string) (peerspkg.SendResult, *peerspkg.Refusal, error)) error {
	if len(targets) == 0 {
		where := ""
		if sameHost {
			where = " on this machine"
		}
		rf := peerspkg.Refuse(peerspkg.CodeNoHolder, "no session you may message%s holds tag %s", where, tag)
		fmt.Fprintln(w, "not delivered: "+rf.Detail+".")
		return peersExit(peerspkg.ExitCode(rf.Code), nil)
	}
	code := 0
	for _, t := range targets {
		res, rf, err := deliver(t.Session)
		if err != nil {
			return err
		}
		c := 0
		switch {
		case rf != nil:
			fmt.Fprintf(w, "%s: %s\n", t.Handle, peerspkg.Describe(rf))
			c = peerspkg.ExitCode(rf.Code)
			if one && (rf.Code == peerspkg.CodeTargetGone || rf.Code == peerspkg.CodeUnknownTarget) {
				code = c
				continue // that holder has gone: the next one
			}
		default:
			if res.Target.Handle == "" {
				res.Target.Handle = t.Handle
			}
			fmt.Fprintln(w, res.Line())
			if res.Status == "queued" {
				c = peerspkg.ExitQueued
			}
		}
		if one {
			code = c
			break
		}
		if code == 0 {
			code = c
		}
	}
	if code == 0 {
		return nil
	}
	return peersExit(code, nil)
}
