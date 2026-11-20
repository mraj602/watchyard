package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	peerspkg "github.com/mraj602/watchyard/internal/peers"
)

func tagTarget(handle string) peerspkg.ListEntry {
	return peerspkg.ListEntry{Session: "s-" + handle, Handle: handle, Can: "start", Tags: []string{"x"}}
}

// fakeDeliver answers per session id: a refusal code, "queued", or delivered.
func fakeDeliver(outcomes map[string]string, calls *[]string) func(string) (peerspkg.SendResult, *peerspkg.Refusal, error) {
	return func(to string) (peerspkg.SendResult, *peerspkg.Refusal, error) {
		*calls = append(*calls, to)
		switch o := outcomes[to]; o {
		case "":
			return peerspkg.SendResult{Msg: "m-" + to, Thread: "t_1"}, nil, nil
		case "queued":
			return peerspkg.SendResult{Msg: "m-" + to, Status: "queued", Offline: true}, nil, nil
		default:
			return peerspkg.SendResult{}, peerspkg.Refuse(o, "refused %s", to), nil
		}
	}
}

func exitOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return -1
}

func TestDeliverTagNoHolder(t *testing.T) {
	var out bytes.Buffer
	var calls []string
	err := deliverTag(&out, "x", nil, true, true, fakeDeliver(nil, &calls))
	if exitOf(err) != 3 || len(calls) != 0 || !strings.Contains(out.String(), "no session you may message on this machine holds tag x") {
		t.Fatalf("exit %d calls %v out %q", exitOf(err), calls, out.String())
	}
}

// --one: exactly one delivery, to the first target; one that has gone passes it to the next,
// any other refusal ends it with that refusal's code.
func TestDeliverTagOne(t *testing.T) {
	ts := []peerspkg.ListEntry{tagTarget("a@x.m"), tagTarget("b@x.m"), tagTarget("c@x.m")}
	var out bytes.Buffer
	var calls []string
	if err := deliverTag(&out, "x", ts, true, false, fakeDeliver(nil, &calls)); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "s-a@x.m" || strings.Count(out.String(), "\n") != 1 || !strings.Contains(out.String(), "delivered to a@x.m") {
		t.Fatalf("calls %v out %q", calls, out.String())
	}

	out.Reset()
	calls = nil
	err := deliverTag(&out, "x", ts, true, false, fakeDeliver(map[string]string{"s-a@x.m": peerspkg.CodeTargetGone}, &calls))
	if err != nil || strings.Join(calls, ",") != "s-a@x.m,s-b@x.m" {
		t.Fatalf("fallback: %v %v %q", err, calls, out.String())
	}

	out.Reset()
	calls = nil
	err = deliverTag(&out, "x", ts, true, false, fakeDeliver(map[string]string{"s-a@x.m": peerspkg.CodeRateLimited}, &calls))
	if exitOf(err) != peerspkg.ExitLimits || len(calls) != 1 || !strings.HasPrefix(out.String(), "a@x.m: not delivered") {
		t.Fatalf("rate limited: %d %v %q", exitOf(err), calls, out.String())
	}

	out.Reset()
	calls = nil
	gone := map[string]string{"s-a@x.m": peerspkg.CodeTargetGone, "s-b@x.m": peerspkg.CodeTargetGone, "s-c@x.m": peerspkg.CodeTargetGone}
	if err := deliverTag(&out, "x", ts, true, false, fakeDeliver(gone, &calls)); exitOf(err) != peerspkg.ExitNotSent || len(calls) != 3 {
		t.Fatalf("all gone: %d %v", exitOf(err), calls)
	}
}

// Broadcast: every target, one line each; exit 0 only when all were delivered, else the code
// of the first line that was not.
func TestDeliverTagBroadcast(t *testing.T) {
	ts := []peerspkg.ListEntry{tagTarget("a@x.m"), tagTarget("b@x.m"), tagTarget("c@x.m")}
	var out bytes.Buffer
	var calls []string
	if err := deliverTag(&out, "x", ts, false, false, fakeDeliver(nil, &calls)); err != nil || len(calls) != 3 ||
		strings.Count(out.String(), "delivered to ") != 3 {
		t.Fatalf("%v %v %q", err, calls, out.String())
	}
	out.Reset()
	calls = nil
	err := deliverTag(&out, "x", ts, false, false, fakeDeliver(map[string]string{"s-b@x.m": "queued", "s-c@x.m": peerspkg.CodeNotAllowed}, &calls))
	if exitOf(err) != peerspkg.ExitQueued || len(calls) != 3 || strings.Count(out.String(), "\n") != 3 {
		t.Fatalf("mixed: %d %v %q", exitOf(err), calls, out.String())
	}
	if !strings.Contains(out.String(), "queued for b@x.m") || !strings.Contains(out.String(), "c@x.m: not delivered") {
		t.Errorf("lines: %q", out.String())
	}
}

func TestPrintTagResult(t *testing.T) {
	var out bytes.Buffer
	printTagResult(&out, peerspkg.TagAdd, "x", peerspkg.TagResult{Tags: []string{"x"}, Changed: true,
		Holders: []peerspkg.ListEntry{{Handle: "b@x.m", State: "idle"}},
		Notices: []peerspkg.TagNotice{{Handle: "b@x.m", Status: "delivered", Line: "delivered to b@x.m (msg 1, thread t_1)"}}})
	s := out.String()
	for _, want := range []string{"tagged this session x.", "Other holders of x", "  b@x.m  (idle)", "delivered to b@x.m"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %q", want, s)
		}
	}
	out.Reset()
	printTagResult(&out, peerspkg.TagList, "", peerspkg.TagResult{})
	if out.String() != "This session has no tags.\n" {
		t.Errorf("tags: %q", out.String())
	}
}

func TestPeersSendTagUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if err := peersSend([]string{"worker", "--one", "hi"}, strings.NewReader(""), &out, &errb); !errors.Is(err, errUsage) {
		t.Errorf("--one without a tag: %v", err)
	}
	errb.Reset()
	if err := peersSend([]string{"tag:Bad", "hi"}, strings.NewReader(""), &out, &errb); !errors.Is(err, errUsage) {
		t.Errorf("bad tag: %v", err)
	}
	if err := runPeers([]string{"tag", "add", "Not_A_Tag"}, &out, &errb); !errors.Is(err, errUsage) {
		t.Errorf("tag add bad: %v", err)
	}
	if err := runPeers([]string{"tag", "frob", "x"}, &out, &errb); !errors.Is(err, errUsage) {
		t.Errorf("tag frob: %v", err)
	}
}
