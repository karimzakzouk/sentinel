package slot

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/karimzakzouk/sentinel/internal/ledger"
)

// EventType is the Ledger event type of slot records.
const EventType = "sentinel.decision.recorded"

// LedgerRecorder stores slot records in the Ledger.
type LedgerRecorder struct {
	Ledger *ledger.Ledger
	Source string
}

// Record appends the record as one event whose subject is the slot name.
func (r LedgerRecorder) Record(ctx context.Context, rec Record) error {
	_, err := r.Ledger.Append(ctx, ledger.Draft{Type: EventType, Source: r.Source, Subject: rec.Slot, Data: rec})
	return err
}

// Records reads every recorded call of one slot from the Ledger, oldest first.
func Records(ctx context.Context, l *ledger.Ledger, slotName string) ([]Record, error) {
	var out []Record
	var after int64
	for {
		events, err := l.Events(ctx, after, 1000)
		if err != nil {
			return nil, err
		}
		if len(events) == 0 {
			return out, nil
		}
		for _, e := range events {
			after = e.Seq
			if e.Type != EventType || e.Subject != slotName {
				continue
			}
			var r Record
			if err := json.Unmarshal(e.Data, &r); err != nil {
				return nil, fmt.Errorf("decode slot record in event %d: %w", e.Seq, err)
			}
			out = append(out, r)
		}
	}
}
