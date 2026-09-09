package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/url"
	"strings"
	"time"
)

type chatDisplayReply struct {
	ThreadID        string `json:"thread_id"`
	RunID           string `json:"run_id"`
	Text            string `json:"text"`
	DocumentEventID uint64 `json:"document_event_id"`
}

func chatReplyDeliveryID(thread, run string) string {
	sum := sha256.Sum256([]byte(thread + "\x00" + run))
	return "chat-" + hex.EncodeToString(sum[:])
}

func (p *voiceProcessor) scanChatReplies(ctx context.Context, delivered map[string]string) {
	devices, err := p.files.broker.ListDevices(ctx)
	if err != nil {
		slog.Warn("chat delivery devices unavailable", "error", err)
		return
	}
	for _, device := range devices {
		if device.RevokedAt != nil || ctx.Err() != nil {
			continue
		}
		var snapshot struct {
			Reply *chatDisplayReply `json:"reply"`
		}
		_, err := p.agentRequest(ctx, "GET", "/migi/chat?owner="+url.QueryEscape("device:"+device.ID)+"&limit=1", nil, &snapshot)
		if err != nil {
			continue
		} // transient outage: retry; no cursor is advanced
		r := snapshot.Reply
		if r == nil || r.ThreadID == "" || r.RunID == "" || strings.TrimSpace(r.Text) == "" {
			continue
		}
		// Voice delivery owns recording-ID runs and their start/stop notices.
		// Do not race its start notice or final document handling.
		if raw, err := hex.DecodeString(r.RunID); err == nil && len(raw) == sha256.Size {
			continue
		}
		id := chatReplyDeliveryID(r.ThreadID, r.RunID)
		if delivered[device.ID] == id {
			continue
		}
		if r.DocumentEventID != 0 {
			delivered[device.ID] = id
			continue
		}
		if _, _, err = p.files.broker.PublishVoiceReply(ctx, id, "Ответ · Агент Migi", r.Text); err != nil {
			slog.Warn("chat reply delivery failed", "error", err)
			continue
		}
		delivered[device.ID] = id
	}
}

func (p *voiceProcessor) runChatReplies(ctx context.Context) {
	if p.config.AgentURL == "" {
		return
	}
	delivered := make(map[string]string)
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		p.scanChatReplies(ctx, delivered)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
