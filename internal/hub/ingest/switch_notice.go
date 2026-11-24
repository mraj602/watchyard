package ingest

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/record"
)

// switchNotice hands a machine's switch notice to the alert engine (record.md 4.12, push.md
// 3.9). The episode is prefixed with the sending machine, so one machine cannot end or
// repeat another's notice.
func (t *typer) switchNotice(c *ctx) error {
	var p record.SwitchNoticePayload
	if err := json.Unmarshal(c.r.Payload, &p); err != nil {
		return fmt.Errorf("switch_notice payload: %w", err)
	}
	if err := p.Validate(); err != nil {
		return err
	}
	since, _ := time.Parse(time.RFC3339, p.SinceAt)
	t.ch.Notices = append(t.ch.Notices, model.Notice{
		Trigger: model.Trigger(p.Trigger),
		Episode: "sw:" + c.r.Machine + ":" + p.Episode,
		Machine: c.r.Machine,
		SinceMS: since.UnixMilli(),
		Facts:   model.Facts{Message: p.Text, Moves: p.Moves}, // the engine adds the machine name
		Direct:  p.Direct,
	})
	return nil
}
