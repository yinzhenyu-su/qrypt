package task

import (
	"context"
	"sync"
)

const defaultSubscriptionBuffer = 128

type Subscription struct {
	once   sync.Once
	close  func()
	events chan Event
}

func (s *Subscription) Read(ctx context.Context) ([]Event, error) {
	if s == nil {
		return nil, ErrNotFound
	}
	select {
	case event, ok := <-s.events:
		if !ok {
			return nil, context.Canceled
		}
		out := []Event{event}
		for {
			select {
			case event, ok := <-s.events:
				if !ok {
					return out, nil
				}
				out = append(out, event)
			default:
				return out, nil
			}
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Subscription) ReadAvailable() ([]Event, error) {
	if s == nil {
		return nil, ErrNotFound
	}
	var out []Event
	for {
		select {
		case event, ok := <-s.events:
			if !ok {
				return out, context.Canceled
			}
			out = append(out, event)
		default:
			if out == nil {
				out = []Event{}
			}
			return out, nil
		}
	}
}

func (s *Subscription) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		if s.close != nil {
			s.close()
		}
	})
}
