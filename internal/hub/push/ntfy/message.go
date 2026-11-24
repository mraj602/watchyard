// Package ntfy delivers pushes through an ntfy server (push.md 7): it builds the JSON
// message (title, body, priority, tags, click and the Open/Snooze/Seen actions), publishes
// it with an optional Bearer token, maps the answer to a model.Result and makes topics.
package ntfy

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mraj602/watchyard/internal/hub/push/model"
)

// MaxMessageBytes is ntfy's limit for a published message (push.md 7.2).
const MaxMessageBytes = 4096

// Action is one ntfy action button. http actions are run by the ntfy app on the phone.
type Action struct {
	Action  string            `json:"action"`
	Label   string            `json:"label"`
	URL     string            `json:"url"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	Clear   bool              `json:"clear"`
}

// Message is the JSON published to the server root (push.md 7.2).
type Message struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags,omitempty"`
	Click    string   `json:"click,omitempty"`
	Actions  []Action `json:"actions,omitempty"`
}

// BuildOptions are the inputs the message needs besides the payload.
type BuildOptions struct {
	Topic string
	// SnoozeMin labels the Snooze button ("Snooze 15 min"); 0 labels it "Snooze".
	SnoozeMin int
}

// Build makes the ntfy message for m (push.md 7.2). The payload already carries the text
// (private text included: the engine renders it), the deep link, the Remote Control link,
// the tap preference and the action tokens; m.HubURL makes the relative links absolute.
//
// Private: the click is the payload's deep link (the alert page), never the Remote Control
// link, and there is no Remote Control action, because ntfy sees the text unlike Web Push.
// Snooze and Seen stay: their URLs reveal only the hub's host name.
func Build(m model.Message, o BuildOptions) Message {
	p := m.Payload
	hub := strings.TrimRight(m.HubURL, "/")
	deep := absolute(hub, p.URL)
	remote := ""
	if !p.Private {
		remote = p.RemoteURL
	}

	out := Message{Topic: o.Topic, Title: p.Title, Message: p.Body, Priority: clampPriority(m.Priority), Tags: tagsFor(p)}
	out.Click = deep
	if remote != "" && p.Tap == model.TapClaude {
		out.Click = remote
	}
	if remote != "" {
		out.Actions = append(out.Actions, Action{Action: "view", Label: "Open in Claude", URL: remote, Clear: true})
	} else if deep != "" {
		out.Actions = append(out.Actions, Action{Action: "view", Label: "Open", URL: deep, Clear: true})
	}
	if a := p.Actions; a != nil && hub != "" {
		if a.Snooze != "" {
			label := "Snooze"
			if o.SnoozeMin > 0 {
				label = "Snooze " + strconv.Itoa(o.SnoozeMin) + " min"
			}
			out.Actions = append(out.Actions, httpAction(label, hub+"/v1/actions/"+a.Snooze))
		}
		if a.Seen != "" {
			out.Actions = append(out.Actions, httpAction("Seen", hub+"/v1/actions/"+a.Seen))
		}
	}
	return out
}

func httpAction(label, url string) Action {
	return Action{Action: "http", Label: label, URL: url, Method: "POST",
		Headers: map[string]string{"Content-Type": "application/json"}, Body: "{}", Clear: true}
}

func absolute(hub, link string) string {
	switch {
	case link == "":
		return ""
	case strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://"):
		return link
	case hub == "":
		return ""
	}
	return hub + link
}

func clampPriority(p int) int {
	switch {
	case p == 0:
		return 3
	case p < 1:
		return 1
	case p > 5:
		return 5
	}
	return p
}

// tagsFor are the ntfy emoji tags of push.md 7.2.
func tagsFor(p model.Payload) []string {
	if p.Type == model.TypeSummary {
		return []string{"bell"}
	}
	if p.Type == model.TypeTest {
		return []string{"white_check_mark"}
	}
	switch p.Trigger {
	case model.TriggerNeedsPermission:
		return []string{"lock"}
	case model.TriggerAsked:
		return []string{"question"}
	case model.TriggerFailed:
		return []string{"rotating_light"}
	case model.TriggerLimit80, model.TriggerLimit95:
		return []string{"warning"}
	case model.TriggerReset:
		return []string{"arrows_counterclockwise"}
	case model.TriggerMachineSilent:
		return []string{"zzz"}
	case model.TriggerLimitChange, model.TriggerUsageReset, model.TriggerSeatChange, model.TriggerWeeklyDrop:
		return []string{"bar_chart"}
	case model.TriggerSessionMoved, model.TriggerSwitchDigest:
		return []string{"twisted_rightwards_arrows"}
	}
	return nil
}

// Marshal encodes the message and, when it exceeds MaxMessageBytes, cuts the body first
// and then the title (at rune boundaries, with an ellipsis) until it fits.
func Marshal(m Message) ([]byte, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	for _, field := range []*string{&m.Message, &m.Title} {
		for len(b) > MaxMessageBytes && len(*field) > 0 {
			n := len(*field) - (len(b) - MaxMessageBytes) - 3
			if n < 0 {
				n = 0
			}
			*field = cutRunes(*field, n)
			if n > 0 {
				*field += "..."
			}
			if b, err = json.Marshal(m); err != nil {
				return nil, err
			}
		}
	}
	if len(b) > MaxMessageBytes {
		return nil, fmt.Errorf("ntfy: message is %d bytes without its text (limit %d)", len(b), MaxMessageBytes)
	}
	return b, nil
}

func cutRunes(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
