package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/mirmik/migi/server/internal/agentauth"
	"github.com/mirmik/migi/server/internal/events"
)

const agentMaxConcurrentRequests = 32

const agentMessageMaxBytes = 1 << 20

var agentEventKindPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type agentSecurity struct {
	publishRequests *keyedRateLimiter
	healthChecks    *keyedRateLimiter
	authFailures    *keyedRateLimiter
	rejectionLogs   *keyedRateLimiter
	requestSlots    chan struct{}
}

func newAgentSecurity() *agentSecurity {
	return &agentSecurity{
		publishRequests: newKeyedRateLimiter(100, 200, 10, 20),
		healthChecks:    newKeyedRateLimiter(50, 100, 5, 10),
		authFailures:    newKeyedRateLimiter(20, 40, 2, 5),
		rejectionLogs:   newKeyedRateLimiter(5, 20, 0.2, 2),
		requestSlots:    make(chan struct{}, agentMaxConcurrentRequests),
	}
}

func newAgentMux(broker *events.Broker) http.Handler {
	return newAgentMuxWithSecurity(broker, newAgentSecurity())
}

func newAgentMuxWithSecurity(broker *events.Broker, security *agentSecurity) http.Handler {
	return newAgentMuxWithReleases(broker, nil, security)
}

func newAgentMuxWithReleases(broker *events.Broker, releases *releaseStore, security *agentSecurity) http.Handler {
	return newAgentMuxWithStores(broker, releases, nil, security)
}

func newAgentMuxWithStores(
	broker *events.Broker,
	releases *releaseStore,
	transfers *transferStore,
	security *agentSecurity,
) http.Handler {
	return newAgentMuxWithAllStores(broker, releases, transfers, nil, security)
}

func newAgentMuxWithAllStores(
	broker *events.Broker,
	releases *releaseStore,
	transfers *transferStore,
	media *mediaStore,
	security *agentSecurity,
) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", security.rateLimit("health", security.healthChecks, healthHandler(broker)))
	mux.Handle("POST /v1/agent-events", security.rateLimit(
		"publish",
		security.publishRequests,
		authenticateAgent(broker, security, publishAgentEventHandler(broker)),
	))
	mux.Handle("POST /v1/agent-messages", security.rateLimit(
		"publish-message",
		security.publishRequests,
		authenticateAgent(broker, security, publishAgentMessageHandler(broker)),
	))
	if releases != nil {
		mux.Handle("POST /v1/releases", security.rateLimit(
			"release-publish",
			security.publishRequests,
			releases.authenticatePublisher(releases.publishHandler()),
		))
	}
	if transfers != nil {
		transfers.routes(mux, func(next http.Handler) http.Handler {
			return security.rateLimit(
				"files",
				security.publishRequests,
				authenticateAgent(broker, security, next),
			)
		}, func(r *http.Request) string {
			agent, _ := r.Context().Value(agentContextKey{}).(events.AgentTokenInfo)
			return "agent:" + agent.Name
		})
	}
	if media != nil {
		media.agentRoutes(mux, func(next http.Handler) http.Handler {
			return security.rateLimit(
				"media",
				security.publishRequests,
				authenticateAgent(broker, security, next),
			)
		}, func(r *http.Request) string {
			agent, _ := r.Context().Value(agentContextKey{}).(events.AgentTokenInfo)
			return agent.Name
		})
	}
	return security.limitConcurrency(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	}))
}

func publishAgentMessageHandler(broker *events.Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agent, ok := r.Context().Value(agentContextKey{}).(events.AgentTokenInfo)
		if !ok {
			http.Error(w, "agent authentication required", http.StatusUnauthorized)
			return
		}
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" {
			http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
		defer r.Body.Close()
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, agentMessageMaxBytes))
		decoder.DisallowUnknownFields()
		var input struct {
			ThreadID string `json:"thread_id"`
			TurnID   string `json:"turn_id"`
			CWD      string `json:"cwd,omitempty"`
			Title    string `json:"title"`
			Body     string `json:"body"`
		}
		if err := decoder.Decode(&input); err != nil {
			writeAgentMessageDecodeError(w, err)
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeAgentMessageDecodeError(w, err)
			return
		}
		input.ThreadID = strings.TrimSpace(input.ThreadID)
		input.TurnID = strings.TrimSpace(input.TurnID)
		input.Title = strings.TrimSpace(input.Title)
		if input.ThreadID == "" || input.TurnID == "" || input.Title == "" || strings.TrimSpace(input.Body) == "" {
			http.Error(w, "thread_id, turn_id, title, and body are required", http.StatusBadRequest)
			return
		}
		if !utf8.ValidString(input.ThreadID) || !utf8.ValidString(input.TurnID) ||
			!utf8.ValidString(input.CWD) || !utf8.ValidString(input.Title) || !utf8.ValidString(input.Body) ||
			utf8.RuneCountInString(input.ThreadID) > 256 || utf8.RuneCountInString(input.TurnID) > 256 ||
			utf8.RuneCountInString(input.CWD) > 4096 || utf8.RuneCountInString(input.Title) > 256 {
			http.Error(w, "agent message fields exceed the allowed size", http.StatusBadRequest)
			return
		}
		message, created, err := broker.PublishAgentMessage(r.Context(), events.AgentMessageDraft{
			Agent: agent.Name, ThreadID: input.ThreadID, TurnID: input.TurnID,
			CWD: input.CWD, Title: input.Title, Body: input.Body,
		})
		if err != nil {
			slog.Error("failed to persist agent message", "error", err, "agent", agent.Name)
			http.Error(w, "failed to persist agent message", http.StatusInternalServerError)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		slog.Info("authenticated agent message accepted",
			"message_id", message.ID, "event_id", message.EventID, "agent", agent.Name,
			"token_id", agent.ID, "created", created, "remote_addr", r.RemoteAddr,
		)
		writeJSON(w, status, message)
	}
}

func writeAgentMessageDecodeError(w http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		http.Error(w, "JSON body exceeds 1 MiB", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "invalid JSON body", http.StatusBadRequest)
}

func (s *agentSecurity) rateLimit(scope string, limiter *keyedRateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allowRemote(r.RemoteAddr) {
			s.logRejection("agent request rate limited", r.RemoteAddr, "scope", scope)
			writeRateLimited(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *agentSecurity) limitConcurrency(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case s.requestSlots <- struct{}{}:
			defer func() { <-s.requestSlots }()
			next.ServeHTTP(w, r)
		default:
			s.logRejection("agent request capacity reached", r.RemoteAddr)
			writeRateLimited(w)
		}
	})
}

func (s *agentSecurity) logRejection(message, remoteAddr string, attributes ...any) {
	if !s.rejectionLogs.allowRemote(remoteAddr) {
		return
	}
	attributes = append(attributes, "remote_addr", remoteAddr)
	slog.Warn(message, attributes...)
}

type agentContextKey struct{}

func authenticateAgent(broker *events.Broker, security *agentSecurity, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !security.authFailures.readyRemote(r.RemoteAddr) {
			security.logRejection("agent authentication rate limited", r.RemoteAddr)
			writeRateLimited(w)
			return
		}
		header := r.Header.Get("Authorization")
		plain, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || plain == "" || strings.TrimSpace(plain) != plain {
			security.authFailures.allowRemote(r.RemoteAddr)
			writeAgentUnauthorized(w)
			return
		}
		tokenID, tokenHash, ok := agentauth.Parse(plain)
		if !ok {
			security.authFailures.allowRemote(r.RemoteAddr)
			writeAgentUnauthorized(w)
			return
		}
		agent, err := broker.AuthenticateAgent(r.Context(), tokenID, tokenHash[:])
		if errors.Is(err, events.ErrAgentUnauthorized) {
			security.authFailures.allowRemote(r.RemoteAddr)
			security.logRejection("agent authentication rejected", r.RemoteAddr)
			writeAgentUnauthorized(w)
			return
		}
		if err != nil {
			slog.Error("failed to authenticate agent", "error", err, "remote_addr", r.RemoteAddr)
			http.Error(w, "failed to authenticate agent", http.StatusInternalServerError)
			return
		}
		ctx := context.WithValue(r.Context(), agentContextKey{}, agent)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeAgentUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="migi-agent"`)
	http.Error(w, "invalid or revoked agent token", http.StatusUnauthorized)
}

func publishAgentEventHandler(broker *events.Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agent, ok := r.Context().Value(agentContextKey{}).(events.AgentTokenInfo)
		if !ok {
			http.Error(w, "agent authentication required", http.StatusUnauthorized)
			return
		}
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" {
			http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
		defer r.Body.Close()
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		var input struct {
			Kind  string `json:"kind"`
			Title string `json:"title"`
			Body  string `json:"body,omitempty"`
		}
		if err := decoder.Decode(&input); err != nil {
			var maxBytesError *http.MaxBytesError
			if errors.As(err, &maxBytesError) {
				http.Error(w, "JSON body exceeds 64 KiB", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			var maxBytesError *http.MaxBytesError
			if errors.As(err, &maxBytesError) {
				http.Error(w, "JSON body exceeds 64 KiB", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "JSON body must contain one object", http.StatusBadRequest)
			return
		}
		input.Title = strings.TrimSpace(input.Title)
		if !agentEventKindPattern.MatchString(input.Kind) || input.Title == "" {
			http.Error(w, "valid kind and non-empty title are required", http.StatusBadRequest)
			return
		}
		if input.Kind == playbackQueueEventKind {
			http.Error(w, "media.queue.set must use /v1/playback/queue", http.StatusBadRequest)
			return
		}
		if !utf8.ValidString(input.Title) || !utf8.ValidString(input.Body) ||
			utf8.RuneCountInString(input.Title) > 256 || utf8.RuneCountInString(input.Body) > 8192 {
			http.Error(w, "title or body exceeds the allowed size", http.StatusBadRequest)
			return
		}
		event, err := broker.Publish(r.Context(), events.Input{
			Kind: input.Kind, Agent: agent.Name, Title: input.Title, Body: input.Body,
		})
		if err != nil {
			slog.Error("failed to persist agent event", "error", err, "agent", agent.Name)
			http.Error(w, "failed to persist event", http.StatusInternalServerError)
			return
		}
		slog.Info("authenticated agent event accepted",
			"event_id", event.ID,
			"kind", event.Kind,
			"agent", agent.Name,
			"token_id", agent.ID,
			"remote_addr", r.RemoteAddr,
		)
		writeJSON(w, http.StatusCreated, event)
	}
}
