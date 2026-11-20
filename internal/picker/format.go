package picker

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// EnvArgs is what `--env` prints for a pick: `CLAUDE_CONFIG_DIR=<dir>`, or for the default
// view of ~/.claude `-u CLAUDE_CONFIG_DIR` (that view exists only with the variable unset:
// set to ~/.claude, Claude Code reads another state file and maybe another login), so
// `env $(wy account next --env) claude` starts the right login either way.
func EnvArgs(r Ranked) string {
	if r.Default {
		return "-u CLAUDE_CONFIG_DIR"
	}
	return "CLAUDE_CONFIG_DIR=" + r.Dir
}

// Line is the default output: "claude-3  ~/.claude-3  (7d 23% → ~41% at reset Wed; …)".
func Line(r Ranked) string {
	return fmt.Sprintf("%s  %s  (%s)", r.Label, r.Folder, r.Reason)
}

// NoneLine says that no account can be used now and when the earliest one frees up.
func NoneLine(res Result) string {
	s := "No account can take a new session now"
	if res.NextFree != nil {
		s += "; the earliest frees up " + when(*res.NextFree, res.Now) + " (" + res.NextFree.Local().Format("2006-01-02 15:04 MST") + ")"
	} else {
		s += "; none frees up at a known time (log in, or change next.accounts settings)"
	}
	return s + ". `wy account next --explain` shows why."
}

// Doc is the `--json` document.
type Doc struct {
	V        int        `json:"v"`
	Time     time.Time  `json:"time"`
	Strategy string     `json:"strategy"`
	Model    string     `json:"model,omitempty"`
	Data     string     `json:"data"` // hub | local
	HubError string     `json:"hub_error,omitempty"`
	Reserved bool       `json:"reserved"`
	Picked   *Picked    `json:"picked"`
	NextFree *time.Time `json:"next_free"`
	Accounts []Ranked   `json:"accounts"`
	Warnings []string   `json:"warnings,omitempty"`
}

// Picked names the pick in Doc.
type Picked struct {
	Label  string `json:"label"`
	Folder string `json:"folder"`
	Dir    string `json:"dir"`
	Env    string `json:"env"`
	Reason string `json:"reason"`
}

// NewDoc builds the `--json` document of a result.
func NewDoc(res Result, data, hubErr string, reserved bool) Doc {
	d := Doc{V: 1, Time: res.Now.UTC(), Strategy: res.Strategy, Model: res.Model, Data: data, HubError: hubErr,
		Reserved: reserved && res.Pick != nil, NextFree: res.NextFree, Accounts: res.Ranked, Warnings: res.Warnings}
	if d.Accounts == nil {
		d.Accounts = []Ranked{}
	}
	if res.Pick != nil {
		d.Picked = &Picked{Label: res.Pick.Label, Folder: res.Pick.Folder, Dir: res.Pick.Dir, Env: EnvArgs(*res.Pick), Reason: res.Pick.Reason}
	}
	return d
}

// WriteJSON writes the document, indented.
func WriteJSON(w io.Writer, d Doc) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// Explain writes the ranking as a table.
func Explain(w io.Writer, res Result, data, hubErr string) error {
	head := fmt.Sprintf("Strategy %s", res.Strategy)
	if res.Model != "" {
		head += ", model " + res.Model
	}
	switch data {
	case "local":
		head += "; local data from this machine's status line ("
		if hubErr != "" {
			head += "the hub was not read: " + hubErr
		} else {
			head += "no hub is set up"
		}
		head += "), other machines' use is missing"
	default:
		head += "; data from the hub"
	}
	fmt.Fprintln(w, head+".")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tACCOUNT\tFOLDER\tOK\tSCORE\t5H NOW→END/CAP\t7D NOW→RESET/CAP\tOTHER\tDATA\tWHY")
	for i, r := range res.Ranked {
		n := "-"
		ok := "no"
		score := "-"
		if r.Eligible {
			n, ok, score = fmt.Sprint(i+1), "yes", fmt.Sprintf("%.2f", r.Score)
		}
		var five, seven, other []string
		for _, p := range r.Windows {
			switch p.Kind {
			case Kind5h:
				five = append(five, fmt.Sprintf("%.0f→%.0f/%.0f", p.Now+p.Uncertainty, p.AtSessionEnd, p.Cap))
			case Kind7d:
				s := fmt.Sprintf("%.0f→%.0f/%.0f", p.Now+p.Uncertainty, p.AtReset, p.Cap)
				if !p.ResetsAt.IsZero() {
					s += " " + when(p.ResetsAt, res.Now)
				}
				seven = append(seven, s)
			default:
				other = append(other, fmt.Sprintf("%s %.0f→%.0f/%.0f", shortKey(p.Key), p.Now+p.Uncertainty, p.AtSessionEnd, p.Cap))
			}
		}
		dataCol := r.Source
		if r.Freshest != nil {
			dataCol += " " + age(res.Now.Sub(*r.Freshest))
		}
		if r.Stale {
			dataCol += " stale"
		}
		why := r.Reason
		if r.Eligible {
			why = r.Mode
			if r.Pending > 0 {
				why += fmt.Sprintf(", %d reserved", r.Pending)
			}
			if r.Live > 0 {
				why += fmt.Sprintf(", %d live", r.Live)
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", n, r.Label, r.Folder, ok, score, dash(five), dash(seven),
			dash(other), dataCol, why)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if res.Pick != nil {
		fmt.Fprintln(w, "Pick: "+Line(*res.Pick))
	} else {
		fmt.Fprintln(w, NoneLine(res))
	}
	for _, x := range res.Warnings {
		fmt.Fprintln(w, "note: "+x)
	}
	return nil
}

func dash(xs []string) string {
	if len(xs) == 0 {
		return "-"
	}
	return strings.Join(xs, ", ")
}
