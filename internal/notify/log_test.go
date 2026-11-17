package notify_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/techverito/parking-service/internal/notify"
)

// capturingHandler keeps every record so a test can assert on the log line
// without touching the network.
type capturingHandler struct {
	records []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, record slog.Record) error {
	h.records = append(h.records, record.Clone())
	return nil
}

func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *capturingHandler) WithGroup(string) slog.Handler { return h }

func TestLogNotifierLogsMessageAndReturnsNil(t *testing.T) {
	handler := &capturingHandler{}
	notifier := notify.NewLogNotifier(slog.New(handler))

	message := notify.Message{
		To:   "+14155552671",
		Body: "Parking confirmed. Lot: Central Plaza. Plate: KA 01 AB 1234. Start: 2026-11-17T09:00:00Z.",
	}
	if err := notifier.Send(context.Background(), message); err != nil {
		t.Fatalf("send: %v", err)
	}

	if len(handler.records) != 1 {
		t.Fatalf("records = %d, want 1", len(handler.records))
	}

	attributes := map[string]string{}
	handler.records[0].Attrs(func(attr slog.Attr) bool {
		attributes[attr.Key] = attr.Value.String()
		return true
	})
	if attributes["to"] != message.To {
		t.Errorf("to = %q, want %q", attributes["to"], message.To)
	}
	if attributes["body"] != message.Body {
		t.Errorf("body = %q, want %q", attributes["body"], message.Body)
	}
}

func TestNewLogNotifierFallsBackToDefaultLogger(t *testing.T) {
	notifier := notify.NewLogNotifier(nil)
	if notifier.Logger == nil {
		t.Fatal("logger is nil, want the default logger")
	}

	if err := notifier.Send(context.Background(), notify.Message{
		To:   "+14155552671",
		Body: "Parking confirmed.",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
}
