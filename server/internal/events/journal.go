package events

import (
	"context"
	"time"
)

type Event struct {
	ID        uint64    `json:"id"`
	Kind      string    `json:"kind"`
	Agent     string    `json:"agent,omitempty"`
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Input struct {
	Kind  string `json:"kind"`
	Agent string `json:"agent,omitempty"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
}

type Journal interface {
	Append(context.Context, Input) (Event, error)
	After(context.Context, uint64) ([]Event, error)
	Acknowledge(context.Context, string, uint64) error
	Acknowledged(context.Context, string) (uint64, error)
	Ping(context.Context) error
	Close() error
}
