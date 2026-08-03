package events

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalidPairingCode     = errors.New("pairing code is invalid, expired, or already used")
	ErrInvalidAcknowledgement = errors.New("acknowledgement exceeds the event journal")
	ErrUnauthorized           = errors.New("device credential is invalid or revoked")
	ErrAgentUnauthorized      = errors.New("agent credential is invalid or revoked")
	ErrAgentExists            = errors.New("agent token id or name already exists")
	ErrPublisherUnauthorized  = errors.New("release publisher credential is invalid or revoked")
	ErrPublisherExists        = errors.New("release publisher token id or name already exists")
	ErrReleaseConflict        = errors.New("release idempotency key conflicts with existing content")
	ErrReleaseNotFound        = errors.New("release does not exist")
	ErrAgentMessageNotFound   = errors.New("agent message does not exist")
)

type Event struct {
	ID        uint64             `json:"id"`
	Kind      string             `json:"kind"`
	Agent     string             `json:"agent,omitempty"`
	Title     string             `json:"title"`
	Body      string             `json:"body,omitempty"`
	CreatedAt time.Time          `json:"created_at"`
	Artifact  *ArtifactReference `json:"artifact,omitempty"`
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

type AgentTokenInfo struct {
	ID         string
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

type PublisherTokenInfo struct {
	ID         string
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

type ArtifactReference struct {
	ID          string `json:"id"`
	PackageName string `json:"package_name"`
	VersionCode int64  `json:"version_code"`
	VersionName string `json:"version_name"`
}

type Release struct {
	ArtifactID     string    `json:"artifact_id"`
	PackageName    string    `json:"package_name"`
	VersionCode    int64     `json:"version_code"`
	VersionName    string    `json:"version_name"`
	Size           int64     `json:"size"`
	SHA256         string    `json:"sha256"`
	SignerSHA256   string    `json:"signer_sha256,omitempty"`
	Publisher      string    `json:"publisher"`
	CreatedAt      time.Time `json:"created_at"`
	ReleaseNotes   string    `json:"release_notes,omitempty"`
	SourceRevision string    `json:"source_revision,omitempty"`
	BuildID        string    `json:"build_id,omitempty"`
	StorageName    string    `json:"-"`
	EventID        uint64    `json:"-"`
}

type ReleaseDraft struct {
	Release
	PublisherTokenID string
	IdempotencyKey   string
}

type AgentMessage struct {
	ID        uint64    `json:"id"`
	EventID   uint64    `json:"event_id"`
	Agent     string    `json:"agent"`
	ThreadID  string    `json:"thread_id"`
	TurnID    string    `json:"turn_id"`
	CWD       string    `json:"cwd"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

type AgentMessageDraft struct {
	Agent    string
	ThreadID string
	TurnID   string
	CWD      string
	Title    string
	Body     string
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
	CreateAgentToken(context.Context, string, string, []byte) error
	AuthenticateAgent(context.Context, string, []byte) (AgentTokenInfo, error)
	RevokeAgentToken(context.Context, string) error
	ListAgentTokens(context.Context) ([]AgentTokenInfo, error)
	CreatePublisherToken(context.Context, string, string, []byte) error
	AuthenticatePublisher(context.Context, string, []byte) (PublisherTokenInfo, error)
	RevokePublisherToken(context.Context, string) error
	ListPublisherTokens(context.Context) ([]PublisherTokenInfo, error)
	ReplayRelease(context.Context, ReleaseDraft) (Release, bool, error)
	PublishRelease(context.Context, ReleaseDraft) (Release, Event, bool, error)
	PublishAgentMessage(context.Context, AgentMessageDraft) (AgentMessage, Event, bool, error)
	RecentAgentMessages(context.Context, int) ([]AgentMessage, error)
	AgentMessage(context.Context, uint64) (AgentMessage, error)
	ReleaseForDevice(context.Context, string, string) (Release, error)
	ListReleaseStorage(context.Context) (map[string]int64, error)
	Stats(context.Context) (ServerStats, error)
	SetPagerMessage(context.Context, string) (Event, error)
	PagerState(context.Context) (PagerState, error)
	Ping(context.Context) error
	Close() error
}
