package notify

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stubSender struct {
	calls int
	err   error
}

func (sender *stubSender) Send(context.Context, []string) error {
	sender.calls++
	return sender.err
}

func TestMultiSenderAttemptsEveryTarget(t *testing.T) {
	first := &stubSender{err: errors.New("unavailable")}
	second := &stubSender{}
	sender := New(
		Target{Name: "企业微信", Sender: first},
		Target{Name: "飞书", Sender: second},
	)

	err := sender.Send(context.Background(), []string{"message"})
	if err == nil || !strings.Contains(err.Error(), "企业微信") {
		t.Fatalf("Send() error = %v", err)
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("calls = %d, %d", first.calls, second.calls)
	}
}
