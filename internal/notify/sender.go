package notify

import (
	"context"
	"errors"
	"fmt"
)

type Sender interface {
	Send(ctx context.Context, messages []string) error
}

type Target struct {
	Name   string
	Sender Sender
}

type MultiSender struct {
	targets []Target
}

func New(targets ...Target) *MultiSender {
	return &MultiSender{targets: append([]Target(nil), targets...)}
}

func (sender *MultiSender) Send(ctx context.Context, messages []string) error {
	var sendErrors []error
	for _, target := range sender.targets {
		if err := target.Sender.Send(ctx, messages); err != nil {
			sendErrors = append(sendErrors, fmt.Errorf("%s: %w", target.Name, err))
		}
	}
	return errors.Join(sendErrors...)
}
