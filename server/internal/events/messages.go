package events

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const agentMessageEventPreviewRunes = 2000

func (j *SQLiteJournal) PublishAgentMessage(
	ctx context.Context,
	draft AgentMessageDraft,
) (AgentMessage, Event, bool, error) {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentMessage{}, Event{}, false, fmt.Errorf("begin agent message transaction: %w", err)
	}
	defer tx.Rollback()

	existing, err := readAgentMessageRow(tx.QueryRowContext(ctx, `
SELECT id, event_id, agent, thread_id, turn_id, cwd, title, body, created_at
FROM agent_messages WHERE agent = ? AND turn_id = ?`, draft.Agent, draft.TurnID))
	if err == nil {
		return existing, Event{}, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AgentMessage{}, Event{}, false, err
	}

	createdAt := time.Now().UTC()
	eventResult, err := tx.ExecContext(ctx, `
INSERT INTO events(kind, agent, title, body, created_at)
VALUES(?, ?, ?, ?, ?)`, "agent.message", draft.Agent, draft.Title,
		agentMessagePreview(draft.Body), createdAt.Format(time.RFC3339Nano))
	if err != nil {
		return AgentMessage{}, Event{}, false, fmt.Errorf("append agent message event: %w", err)
	}
	eventID, err := eventResult.LastInsertId()
	if err != nil {
		return AgentMessage{}, Event{}, false, fmt.Errorf("read agent message event id: %w", err)
	}
	messageResult, err := tx.ExecContext(ctx, `
INSERT INTO agent_messages(event_id, agent, thread_id, turn_id, cwd, title, body, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, eventID, draft.Agent, draft.ThreadID, draft.TurnID,
		draft.CWD, draft.Title, draft.Body, createdAt.Format(time.RFC3339Nano))
	if err != nil {
		return AgentMessage{}, Event{}, false, fmt.Errorf("store agent message: %w", err)
	}
	messageID, err := messageResult.LastInsertId()
	if err != nil {
		return AgentMessage{}, Event{}, false, fmt.Errorf("read agent message id: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return AgentMessage{}, Event{}, false, fmt.Errorf("commit agent message: %w", err)
	}
	message := AgentMessage{
		ID: uint64(messageID), EventID: uint64(eventID), Agent: draft.Agent,
		ThreadID: draft.ThreadID, TurnID: draft.TurnID, CWD: draft.CWD,
		Title: draft.Title, Body: draft.Body, CreatedAt: createdAt,
	}
	event := Event{
		ID: uint64(eventID), Kind: "agent.message", Agent: draft.Agent,
		Title: draft.Title, Body: agentMessagePreview(draft.Body), CreatedAt: createdAt,
	}
	return message, event, true, nil
}

func (j *SQLiteJournal) RecentAgentMessages(ctx context.Context, limit int) ([]AgentMessage, error) {
	if limit <= 0 || limit > 500 {
		return nil, errors.New("agent message limit must be between 1 and 500")
	}
	rows, err := j.db.QueryContext(ctx, `
SELECT id, event_id, agent, thread_id, turn_id, cwd, title, substr(body, 1, 1000), created_at
FROM agent_messages ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query agent messages: %w", err)
	}
	defer rows.Close()
	messages := make([]AgentMessage, 0)
	for rows.Next() {
		message, err := scanAgentMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent messages: %w", err)
	}
	return messages, nil
}

func (j *SQLiteJournal) AgentMessage(ctx context.Context, id uint64) (AgentMessage, error) {
	if id == 0 || id > math.MaxInt64 {
		return AgentMessage{}, ErrAgentMessageNotFound
	}
	message, err := readAgentMessageRow(j.db.QueryRowContext(ctx, `
SELECT id, event_id, agent, thread_id, turn_id, cwd, title, body, created_at
FROM agent_messages WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentMessage{}, ErrAgentMessageNotFound
	}
	return message, err
}

type rowScanner interface {
	Scan(...any) error
}

func readAgentMessageRow(row rowScanner) (AgentMessage, error) {
	message, err := scanAgentMessage(row)
	if err != nil {
		return AgentMessage{}, err
	}
	return message, nil
}

func scanAgentMessage(row rowScanner) (AgentMessage, error) {
	var message AgentMessage
	var createdAt string
	if err := row.Scan(
		&message.ID, &message.EventID, &message.Agent, &message.ThreadID, &message.TurnID,
		&message.CWD, &message.Title, &message.Body, &createdAt,
	); err != nil {
		return AgentMessage{}, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return AgentMessage{}, fmt.Errorf("parse agent message timestamp: %w", err)
	}
	message.CreatedAt = parsed
	return message, nil
}

func agentMessagePreview(body string) string {
	body = strings.TrimSpace(body)
	runes := []rune(body)
	if len(runes) <= agentMessageEventPreviewRunes {
		return body
	}
	return string(runes[:agentMessageEventPreviewRunes]) + "…"
}
