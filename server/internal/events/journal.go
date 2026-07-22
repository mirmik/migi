package events

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalidPairingCode = errors.New("pairing code is invalid, expired, or already used")
	ErrUnauthorized       = errors.New("device credential is invalid or revoked")
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

type DeviceInfo struct {
	ID         string
	Name       string
	CreatedAt  time.Time
	LastSeenAt time.Time
	RevokedAt  *time.Time
	AckThrough uint64
}

type ServerStats struct {
	EventCount         uint64
	LatestEventID      uint64
	DeviceCount        uint64
	ActiveDeviceCount  uint64
	ActivePairingCodes uint64
}

type PagerState struct {
	Message   string
	EventID   uint64
	UpdatedAt time.Time
}

type Journal interface {
	Append(context.Context, Input) (Event, error)
	After(context.Context, uint64, int) ([]Event, error)
	Acknowledge(context.Context, string, uint64) error
	Acknowledged(context.Context, string) (uint64, error)
	CreatePairingCode(context.Context, []byte, time.Time) error
	RedeemPairingCode(context.Context, []byte, string, string, []byte) error
	AuthenticateDevice(context.Context, []byte) (string, error)
	RevokeDevice(context.Context, string) error
	ListDevices(context.Context) ([]DeviceInfo, error)
	Stats(context.Context) (ServerStats, error)
	SetPagerMessage(context.Context, string) (Event, error)
	PagerState(context.Context) (PagerState, error)
	Ping(context.Context) error
	Close() error
}
