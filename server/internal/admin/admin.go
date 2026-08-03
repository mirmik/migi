package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mirmik/migi/server/internal/agentauth"
	"github.com/mirmik/migi/server/internal/events"
	qrcode "github.com/skip2/go-qrcode"
)

//go:embed templates/*.html assets/*
var content embed.FS

type Config struct {
	Broker                 *events.Broker
	Files                  FileExchange
	PublicEndpoint         string
	CertificateFingerprint string
	PublicListen           string
	IngestListen           string
	AgentListen            string
	AgentEndpoint          string
	AdminListen            string
	StartedAt              time.Time
}

type SharedFile struct {
	ID        string
	Name      string
	MIME      string
	Size      int64
	SHA256    string
	Source    string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type FileExchange interface {
	ListSharedFiles(context.Context) ([]SharedFile, error)
	ShareFile(context.Context, string, string, string, io.Reader, int64) (SharedFile, error)
	OpenSharedFile(context.Context, string) (SharedFile, io.ReadCloser, error)
	MaxSharedFileBytes() int64
}

var (
	ErrFileTooLarge       = errors.New("shared file is too large")
	ErrFileStorageFull    = errors.New("shared file storage is full")
	ErrFileLengthMismatch = errors.New("shared file length mismatch")
	ErrFileNotFound       = errors.New("shared file not found")
	ErrFileInvalid        = errors.New("shared file metadata is invalid")
)

type Handler struct {
	config    Config
	csrfToken string
	template  *template.Template
	assets    http.Handler
	now       func() time.Time
}

type pageData struct {
	CSRFToken              string
	PublicEndpoint         string
	CertificateFingerprint string
	PublicListen           string
	IngestListen           string
	AgentListen            string
	AgentEndpoint          string
	AdminListen            string
	StartedAt              time.Time
	Uptime                 time.Duration
	Stats                  events.ServerStats
	Pager                  events.PagerState
	Devices                []events.DeviceInfo
	AgentTokens            []events.AgentTokenInfo
	PublisherTokens        []events.PublisherTokenInfo
	Files                  []SharedFile
	FilesEnabled           bool
	ActiveStreams          int
	Pairing                *pairingView
	AgentCredential        *agentCredentialView
	PublisherCredential    *agentCredentialView
	Notice                 string
	AgentMessages          []events.AgentMessage
}

type messagesPageData struct {
	Messages []events.AgentMessage
}

type messagePageData struct {
	Message      events.AgentMessage
	RenderedBody template.HTML
}

type pairingView struct {
	QRDataURI template.URL
	Endpoint  string
	ExpiresAt time.Time
}

type agentCredentialView struct {
	Name   string
	Config string
}

var agentNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func New(config Config) (*Handler, error) {
	if config.Broker == nil {
		return nil, errors.New("admin broker is required")
	}
	if config.StartedAt.IsZero() {
		config.StartedAt = time.Now()
	}
	if config.CertificateFingerprint == "" {
		return nil, errors.New("certificate fingerprint is required")
	}
	if config.PublicEndpoint != "" {
		parsed, err := parsePublicEndpoint(config.PublicEndpoint)
		if err != nil {
			return nil, err
		}
		config.PublicEndpoint = parsed.String()
	}
	if config.AgentEndpoint != "" {
		parsed, err := parsePublicEndpoint(config.AgentEndpoint)
		if err != nil {
			return nil, fmt.Errorf("invalid agent endpoint: %w", err)
		}
		config.AgentEndpoint = parsed.String()
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate admin CSRF token: %w", err)
	}
	templates, err := template.New("dashboard.html").Funcs(template.FuncMap{
		"formatTime": func(value time.Time) string {
			return value.Local().Format("2006-01-02 15:04:05 MST")
		},
		"formatOptionalTime": func(value *time.Time) string {
			if value == nil {
				return "never"
			}
			return value.Local().Format("2006-01-02 15:04:05 MST")
		},
		"formatDuration": formatDuration,
		"formatBytes":    formatBytes,
		"messagePreview": messagePreview,
	}).ParseFS(content, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse admin templates: %w", err)
	}
	assetsFS, err := fs.Sub(content, "assets")
	if err != nil {
		return nil, fmt.Errorf("open admin assets: %w", err)
	}
	return &Handler{
		config:    config,
		csrfToken: base64.RawURLEncoding.EncodeToString(tokenBytes),
		template:  templates,
		assets:    http.FileServer(http.FS(assetsFS)),
		now:       time.Now,
	}, nil
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("GET /admin/", h.dashboard)
	mux.HandleFunc("GET /admin/messages/", h.agentMessages)
	mux.HandleFunc("GET /admin/messages/{messageID}", h.agentMessage)
	mux.HandleFunc("GET /admin/devices/", h.devices)
	mux.HandleFunc("GET /admin/agents/", h.agents)
	mux.HandleFunc("GET /admin/files/", h.files)
	mux.HandleFunc("GET /admin/system/", h.system)
	mux.HandleFunc("POST /admin/pair", h.createPairing)
	mux.HandleFunc("POST /admin/notifications/test", h.sendTestNotification)
	mux.HandleFunc("POST /admin/pager", h.setPagerMessage)
	mux.HandleFunc("POST /admin/devices/revoke", h.revokeDevice)
	mux.HandleFunc("POST /admin/agents/create", h.createAgentToken)
	mux.HandleFunc("POST /admin/agents/revoke", h.revokeAgentToken)
	mux.HandleFunc("POST /admin/publishers/create", h.createPublisherToken)
	mux.HandleFunc("POST /admin/publishers/revoke", h.revokePublisherToken)
	mux.HandleFunc("POST /admin/files", h.uploadFile)
	mux.HandleFunc("GET /admin/files/{fileID}/content", h.downloadFile)
	mux.Handle("GET /admin/assets/", http.StripPrefix("/admin/assets/", h.assets))
	return h.securityHeaders(mux)
}

func (h *Handler) agentMessages(w http.ResponseWriter, r *http.Request) {
	messages, err := h.config.Broker.RecentAgentMessages(r.Context(), 100)
	if err != nil {
		http.Error(w, "failed to read agent responses", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.template.ExecuteTemplate(w, "messages.html", messagesPageData{Messages: messages}); err != nil {
		slog.Error("failed to render agent response list", "error", err)
	}
}

func (h *Handler) agentMessage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("messageID"), 10, 64)
	if err != nil || id == 0 {
		http.NotFound(w, r)
		return
	}
	message, err := h.config.Broker.AgentMessage(r.Context(), id)
	if errors.Is(err, events.ErrAgentMessageNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "failed to read agent response", http.StatusInternalServerError)
		return
	}
	rendered, err := renderAgentMarkdown(message.Body)
	if err != nil {
		http.Error(w, "failed to render agent response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.template.ExecuteTemplate(w, "message.html", messagePageData{
		Message: message, RenderedBody: rendered,
	}); err != nil {
		slog.Error("failed to render agent response", "error", err, "message_id", id)
	}
}

func (h *Handler) uploadFile(w http.ResponseWriter, r *http.Request) {
	if h.config.Files == nil {
		http.Error(w, "file exchange is disabled", http.StatusServiceUnavailable)
		return
	}
	limit := h.config.Files.MaxSharedFileBytes()
	if limit <= 0 {
		http.Error(w, "file exchange is misconfigured", http.StatusInternalServerError)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "file exceeds configured size", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid multipart form", http.StatusBadRequest)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if !h.validCSRF(r.FormValue("csrf_token")) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "a file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()
	if header.Size <= 0 {
		http.Error(w, "file must not be empty", http.StatusBadRequest)
		return
	}
	contentType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil || contentType == "" {
		contentType = "application/octet-stream"
	}
	shared, err := h.config.Files.ShareFile(
		r.Context(), header.Filename, contentType, "admin", file, header.Size,
	)
	if errors.Is(err, ErrFileTooLarge) {
		http.Error(w, "file exceeds configured size", http.StatusRequestEntityTooLarge)
		return
	}
	if errors.Is(err, ErrFileStorageFull) {
		http.Error(w, "file storage is full", http.StatusInsufficientStorage)
		return
	}
	if errors.Is(err, ErrFileLengthMismatch) {
		http.Error(w, "uploaded byte count differs from the form metadata", http.StatusBadRequest)
		return
	}
	if errors.Is(err, ErrFileInvalid) {
		http.Error(w, "file name or media type is invalid", http.StatusBadRequest)
		return
	}
	if err != nil {
		slog.Error("admin file upload failed", "error", err)
		http.Error(w, "failed to store file", http.StatusInternalServerError)
		return
	}
	slog.Info("file shared from admin panel",
		"file_id", shared.ID,
		"name", shared.Name,
		"size", shared.Size,
		"remote_addr", r.RemoteAddr,
	)
	h.redirectTo(w, "files/", "File shared")
}

func (h *Handler) downloadFile(w http.ResponseWriter, r *http.Request) {
	if h.config.Files == nil {
		http.Error(w, "file exchange is disabled", http.StatusServiceUnavailable)
		return
	}
	file, content, err := h.config.Files.OpenSharedFile(r.Context(), r.PathValue("fileID"))
	if errors.Is(err, ErrFileNotFound) {
		http.Error(w, "file does not exist or has expired", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to open file", http.StatusInternalServerError)
		return
	}
	defer content.Close()
	w.Header().Set("Content-Type", file.MIME)
	w.Header().Set("Content-Length", fmt.Sprint(file.Size))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": file.Name,
	}))
	w.Header().Set("X-Content-SHA256", file.SHA256)
	if _, err := io.Copy(w, content); err != nil {
		slog.Warn("admin file download failed", "file_id", file.ID, "error", err)
	}
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, r, "dashboard.html", nil, nil, nil, r.URL.Query().Get("notice"), http.StatusOK)
}

func (h *Handler) devices(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, r, "devices.html", nil, nil, nil, r.URL.Query().Get("notice"), http.StatusOK)
}

func (h *Handler) agents(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, r, "agents.html", nil, nil, nil, r.URL.Query().Get("notice"), http.StatusOK)
}

func (h *Handler) files(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, r, "files.html", nil, nil, nil, r.URL.Query().Get("notice"), http.StatusOK)
}

func (h *Handler) system(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, r, "system.html", nil, nil, nil, r.URL.Query().Get("notice"), http.StatusOK)
}

func (h *Handler) createAgentToken(w http.ResponseWriter, r *http.Request) {
	if !h.validForm(w, r) {
		return
	}
	endpoint, err := parsePublicEndpoint(strings.TrimSpace(r.FormValue("endpoint")))
	if err != nil {
		http.Error(w, "agent endpoint must be a plain https://host[:port] URL", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if !agentNamePattern.MatchString(name) {
		http.Error(w, "agent name must contain 1-128 letters, digits, dots, underscores, or hyphens", http.StatusBadRequest)
		return
	}
	tokenID, plain, tokenHash, err := agentauth.Generate()
	if err != nil {
		http.Error(w, "failed to generate agent token", http.StatusInternalServerError)
		return
	}
	if err := h.config.Broker.CreateAgentToken(r.Context(), tokenID, name, tokenHash[:]); err != nil {
		if errors.Is(err, events.ErrAgentExists) {
			http.Error(w, "an agent with this name already exists", http.StatusConflict)
			return
		}
		http.Error(w, "failed to persist agent token", http.StatusInternalServerError)
		return
	}
	clientConfig := struct {
		Endpoint       string `json:"endpoint"`
		Token          string `json:"token"`
		TLSFingerprint string `json:"tls_fingerprint"`
	}{
		Endpoint:       endpoint.String() + "/v1/agent-events",
		Token:          plain,
		TLSFingerprint: h.config.CertificateFingerprint,
	}
	encoded, err := json.MarshalIndent(clientConfig, "", "  ")
	if err != nil {
		http.Error(w, "failed to render agent configuration", http.StatusInternalServerError)
		return
	}
	slog.Info("agent token created", "agent", name, "token_id", tokenID, "remote_addr", r.RemoteAddr)
	h.renderPage(w, r, "agents.html", nil, &agentCredentialView{
		Name: name, Config: string(encoded),
	}, nil, "Agent token created; copy it now", http.StatusCreated)
}

func (h *Handler) createPublisherToken(w http.ResponseWriter, r *http.Request) {
	if !h.validForm(w, r) {
		return
	}
	endpoint, err := parsePublicEndpoint(strings.TrimSpace(r.FormValue("endpoint")))
	if err != nil {
		http.Error(w, "publisher endpoint must be a plain https://host[:port] URL", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if !agentNamePattern.MatchString(name) {
		http.Error(w, "valid publisher name is required", http.StatusBadRequest)
		return
	}
	tokenID, plain, tokenHash, err := agentauth.Generate()
	if err != nil {
		http.Error(w, "failed to generate publisher token", http.StatusInternalServerError)
		return
	}
	if err := h.config.Broker.CreatePublisherToken(r.Context(), tokenID, name, tokenHash[:]); err != nil {
		if errors.Is(err, events.ErrPublisherExists) {
			http.Error(w, "a publisher with this name already exists", http.StatusConflict)
			return
		}
		http.Error(w, "failed to persist publisher token", http.StatusInternalServerError)
		return
	}
	clientConfig := struct {
		Endpoint       string `json:"endpoint"`
		Token          string `json:"token"`
		TLSFingerprint string `json:"tls_fingerprint"`
	}{
		Endpoint:       endpoint.String() + "/v1/releases",
		Token:          plain,
		TLSFingerprint: h.config.CertificateFingerprint,
	}
	encoded, err := json.MarshalIndent(clientConfig, "", "  ")
	if err != nil {
		http.Error(w, "failed to render publisher configuration", http.StatusInternalServerError)
		return
	}
	slog.Info("release publisher created",
		"publisher", name, "token_id", tokenID,
	)
	h.renderPage(w, r, "agents.html", nil, nil, &agentCredentialView{
		Name: name, Config: string(encoded),
	}, "Publisher token created; copy it now", http.StatusCreated)
}

func (h *Handler) revokePublisherToken(w http.ResponseWriter, r *http.Request) {
	if !h.validForm(w, r) {
		return
	}
	tokenID := r.FormValue("token_id")
	if err := h.config.Broker.RevokePublisherToken(r.Context(), tokenID); err != nil {
		if errors.Is(err, events.ErrPublisherUnauthorized) {
			http.Error(w, "publisher token is unknown or already revoked", http.StatusConflict)
			return
		}
		http.Error(w, "failed to revoke publisher token", http.StatusInternalServerError)
		return
	}
	h.redirectTo(w, "./", "Publisher token revoked")
}

func (h *Handler) revokeAgentToken(w http.ResponseWriter, r *http.Request) {
	if !h.validForm(w, r) {
		return
	}
	tokenID := r.FormValue("token_id")
	if tokenID == "" {
		http.Error(w, "token_id is required", http.StatusBadRequest)
		return
	}
	if err := h.config.Broker.RevokeAgentToken(r.Context(), tokenID); err != nil {
		if errors.Is(err, events.ErrAgentUnauthorized) {
			http.Error(w, "agent token is unknown or already revoked", http.StatusConflict)
			return
		}
		http.Error(w, "failed to revoke agent token", http.StatusInternalServerError)
		return
	}
	slog.Info("agent token revoked", "token_id", tokenID, "remote_addr", r.RemoteAddr)
	h.redirectTo(w, "./", "Agent token revoked")
}

func (h *Handler) setPagerMessage(w http.ResponseWriter, r *http.Request) {
	if !h.validForm(w, r) {
		return
	}
	message := strings.TrimSpace(r.FormValue("message"))
	if utf8.RuneCountInString(message) > 512 {
		http.Error(w, "pager message must not exceed 512 characters", http.StatusBadRequest)
		return
	}
	event, err := h.config.Broker.SetPagerMessage(r.Context(), message)
	if err != nil {
		http.Error(w, "failed to update pager message", http.StatusInternalServerError)
		return
	}
	slog.Info("pager message updated",
		"event_id", event.ID,
		"characters", utf8.RuneCountInString(message),
		"cleared", message == "",
		"remote_addr", r.RemoteAddr,
	)
	h.redirectToDashboard(w, r, "Pager message updated")
}

func (h *Handler) sendTestNotification(w http.ResponseWriter, r *http.Request) {
	if !h.validForm(w, r) {
		return
	}
	event, err := h.config.Broker.Publish(r.Context(), events.Input{
		Kind:  "agent.attention",
		Agent: "migi-admin",
		Title: "Migi test notification",
		Body:  "Sent from the server administration panel.",
	})
	if err != nil {
		http.Error(w, "failed to send test notification", http.StatusInternalServerError)
		return
	}
	slog.Info("test notification sent", "event_id", event.ID, "remote_addr", r.RemoteAddr)
	h.redirectToDashboard(w, r, "Test notification sent")
}

func (h *Handler) createPairing(w http.ResponseWriter, r *http.Request) {
	if !h.validForm(w, r) {
		return
	}
	endpoint, err := parsePublicEndpoint(strings.TrimSpace(r.FormValue("endpoint")))
	if err != nil {
		http.Error(w, "pairing endpoint must be a plain https://host[:port] URL", http.StatusBadRequest)
		return
	}
	ttl, err := time.ParseDuration(r.FormValue("ttl"))
	if err != nil || ttl < time.Minute || ttl > time.Hour {
		http.Error(w, "pairing TTL must be between 1m and 1h", http.StatusBadRequest)
		return
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		http.Error(w, "failed to generate pairing invitation", http.StatusInternalServerError)
		return
	}
	secretHash := sha256.Sum256(secret)
	expiresAt := h.now().UTC().Add(ttl)
	if err := h.config.Broker.CreatePairingCode(r.Context(), secretHash[:], expiresAt); err != nil {
		http.Error(w, "failed to persist pairing invitation", http.StatusInternalServerError)
		return
	}
	slog.Info("pairing invitation created",
		"expires_at", expiresAt,
		"public_endpoint", endpoint.String(),
		"remote_addr", r.RemoteAddr,
	)
	invitation := &url.URL{Scheme: "migi", Host: "pair"}
	query := invitation.Query()
	query.Set("endpoint", endpoint.String())
	query.Set("pin", strings.ReplaceAll(h.config.CertificateFingerprint, ":", ""))
	query.Set("secret", base64.RawURLEncoding.EncodeToString(secret))
	query.Set("expires", expiresAt.Format(time.RFC3339))
	invitation.RawQuery = query.Encode()
	png, err := qrcode.Encode(invitation.String(), qrcode.Medium, 384)
	if err != nil {
		http.Error(w, "failed to render pairing QR", http.StatusInternalServerError)
		return
	}
	h.renderPage(w, r, "devices.html", &pairingView{
		QRDataURI: template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png)),
		Endpoint:  endpoint.String(),
		ExpiresAt: expiresAt,
	}, nil, nil, "Pairing invitation created", http.StatusCreated)
}

func (h *Handler) revokeDevice(w http.ResponseWriter, r *http.Request) {
	if !h.validForm(w, r) {
		return
	}
	deviceID := r.FormValue("device_id")
	if deviceID == "" {
		http.Error(w, "device_id is required", http.StatusBadRequest)
		return
	}
	if err := h.config.Broker.RevokeDevice(r.Context(), deviceID); err != nil {
		if errors.Is(err, events.ErrUnauthorized) {
			http.Error(w, "device is unknown or already revoked", http.StatusConflict)
			return
		}
		http.Error(w, "failed to revoke device", http.StatusInternalServerError)
		return
	}
	slog.Info("device revoked", "device_id", deviceID, "remote_addr", r.RemoteAddr)
	h.redirectTo(w, "./", "Device revoked")
}

func (h *Handler) renderPage(
	w http.ResponseWriter,
	r *http.Request,
	templateName string,
	pairing *pairingView,
	agentCredential *agentCredentialView,
	publisherCredential *agentCredentialView,
	notice string,
	status int,
) {
	stats, err := h.config.Broker.Stats(r.Context())
	if err != nil {
		http.Error(w, "failed to read server statistics", http.StatusInternalServerError)
		return
	}
	devices, err := h.config.Broker.ListDevices(r.Context())
	if err != nil {
		http.Error(w, "failed to read paired devices", http.StatusInternalServerError)
		return
	}
	agentTokens, err := h.config.Broker.ListAgentTokens(r.Context())
	if err != nil {
		http.Error(w, "failed to read agent tokens", http.StatusInternalServerError)
		return
	}
	publisherTokens, err := h.config.Broker.ListPublisherTokens(r.Context())
	if err != nil {
		http.Error(w, "failed to read publisher tokens", http.StatusInternalServerError)
		return
	}
	pager, err := h.config.Broker.PagerState(r.Context())
	if err != nil {
		http.Error(w, "failed to read pager state", http.StatusInternalServerError)
		return
	}
	agentMessages, err := h.config.Broker.RecentAgentMessages(r.Context(), 4)
	if err != nil {
		http.Error(w, "failed to read agent responses", http.StatusInternalServerError)
		return
	}
	var files []SharedFile
	if h.config.Files != nil {
		files, err = h.config.Files.ListSharedFiles(r.Context())
		if err != nil {
			http.Error(w, "failed to list shared files", http.StatusInternalServerError)
			return
		}
	}
	now := h.now()
	data := pageData{
		CSRFToken:              h.csrfToken,
		PublicEndpoint:         h.config.PublicEndpoint,
		CertificateFingerprint: h.config.CertificateFingerprint,
		PublicListen:           h.config.PublicListen,
		IngestListen:           h.config.IngestListen,
		AgentListen:            h.config.AgentListen,
		AgentEndpoint:          h.config.AgentEndpoint,
		AdminListen:            h.config.AdminListen,
		StartedAt:              h.config.StartedAt,
		Uptime:                 now.Sub(h.config.StartedAt),
		Stats:                  stats,
		Pager:                  pager,
		Devices:                devices,
		AgentTokens:            agentTokens,
		PublisherTokens:        publisherTokens,
		Files:                  files,
		FilesEnabled:           h.config.Files != nil,
		ActiveStreams:          h.config.Broker.SubscriberCount(),
		Pairing:                pairing,
		AgentCredential:        agentCredential,
		PublisherCredential:    publisherCredential,
		Notice:                 notice,
		AgentMessages:          agentMessages,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.template.ExecuteTemplate(w, templateName, data); err != nil {
		return
	}
}

func (h *Handler) redirectTo(w http.ResponseWriter, location, notice string) {
	w.Header().Set("Location", location+"?notice="+url.QueryEscape(notice))
	w.WriteHeader(http.StatusSeeOther)
}

func (h *Handler) redirectToDashboard(w http.ResponseWriter, r *http.Request, notice string) {
	action := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/"), "/")
	location := "./"
	if depth := strings.Count(action, "/"); depth > 0 {
		location = strings.Repeat("../", depth)
	}
	w.Header().Set("Location", location+"?notice="+url.QueryEscape(notice))
	w.WriteHeader(http.StatusSeeOther)
}

func (h *Handler) validForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return false
	}
	if !h.validCSRF(r.FormValue("csrf_token")) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return false
	}
	return true
}

func (h *Handler) validCSRF(provided string) bool {
	return len(provided) == len(h.csrfToken) &&
		subtle.ConstantTimeCompare([]byte(provided), []byte(h.csrfToken)) == 1
}

func (h *Handler) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; font-src 'self'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func parsePublicEndpoint(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return nil, errors.New("public endpoint must be a plain https://host[:port] URL")
	}
	return parsed, nil
}

func NormalizeFingerprint(raw []byte) string {
	encoded := strings.ToUpper(hex.EncodeToString(raw))
	return strings.Join(chunk(encoded, 2), ":")
}

func chunk(value string, size int) []string {
	result := make([]string, 0, len(value)/size)
	for len(value) > 0 {
		length := min(size, len(value))
		result = append(result, value[:length])
		value = value[length:]
	}
	return result
}

func formatDuration(value time.Duration) string {
	if value < 0 {
		value = 0
	}
	return value.Round(time.Second).String()
}

func formatBytes(value int64) string {
	switch {
	case value >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(value)/float64(1<<30))
	case value >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(value)/float64(1<<20))
	case value >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(value)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", value)
	}
}

func messagePreview(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 240 {
		return string(runes[:240]) + "…"
	}
	return value
}
