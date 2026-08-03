package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mirmik/migi/server/internal/events"
)

type fakeFileExchange struct {
	files   []SharedFile
	content map[string][]byte
	max     int64
}

func (f *fakeFileExchange) ListSharedFiles(context.Context) ([]SharedFile, error) {
	return append([]SharedFile(nil), f.files...), nil
}

func (f *fakeFileExchange) ShareFile(
	_ context.Context,
	name string,
	contentType string,
	source string,
	body io.Reader,
	size int64,
) (SharedFile, error) {
	if size > f.max {
		return SharedFile{}, ErrFileTooLarge
	}
	content, err := io.ReadAll(io.LimitReader(body, f.max+1))
	if err != nil {
		return SharedFile{}, err
	}
	if int64(len(content)) != size {
		return SharedFile{}, ErrFileLengthMismatch
	}
	digest := sha256.Sum256(content)
	file := SharedFile{
		ID:        "0123456789abcdef0123456789abcdef",
		Name:      name,
		MIME:      contentType,
		Size:      size,
		SHA256:    fmt.Sprintf("%x", digest),
		Source:    source,
		CreatedAt: time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC),
		ExpiresAt: time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC),
	}
	f.files = append([]SharedFile{file}, f.files...)
	if f.content == nil {
		f.content = make(map[string][]byte)
	}
	f.content[file.ID] = content
	return file, nil
}

func (f *fakeFileExchange) OpenSharedFile(
	_ context.Context,
	id string,
) (SharedFile, io.ReadCloser, error) {
	for _, file := range f.files {
		if file.ID == id {
			return file, io.NopCloser(bytes.NewReader(f.content[id])), nil
		}
	}
	return SharedFile{}, nil, ErrFileNotFound
}

func (f *fakeFileExchange) MaxSharedFileBytes() int64 {
	return f.max
}

func TestDashboardAndPairing(t *testing.T) {
	handler, broker := newTestHandler(t)

	request := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	response := httptest.NewRecorder()
	handler.Routes().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d, want %d", response.Code, http.StatusOK)
	}
	if !strings.Contains(response.Body.String(), "Migi") ||
		!strings.Contains(response.Body.String(), "Send test notification") ||
		!strings.Contains(response.Body.String(), `href="devices/"`) {
		t.Fatalf("dashboard is missing expected server details: %s", response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", response.Header().Get("Cache-Control"))
	}
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("dashboard has no Content-Security-Policy")
	}

	form := url.Values{
		"csrf_token": {handler.csrfToken},
		"endpoint":   {"https://198.51.100.20:10443"},
		"ttl":        {"10m"},
	}
	request = httptest.NewRequest(http.MethodPost, "/admin/pair", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	handler.Routes().ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("pairing status = %d, want %d: %s", response.Code, http.StatusCreated, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "data:image/png;base64,") {
		t.Fatal("pairing page has no embedded QR image")
	}
	if !strings.Contains(body, "Endpoint: <code>https://198.51.100.20:10443</code>") {
		t.Fatal("pairing page does not show the selected endpoint")
	}
	if strings.Contains(body, "migi://pair") || strings.Contains(body, "secret=") {
		t.Fatal("pairing secret leaked into the HTML response")
	}
	stats, err := broker.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.ActivePairingCodes != 1 {
		t.Fatalf("active pairing codes = %d, want 1", stats.ActivePairingCodes)
	}
}

func TestAgentResponseListAndDetail(t *testing.T) {
	handler, broker := newTestHandler(t)
	message, created, err := broker.PublishAgentMessage(t.Context(), events.AgentMessageDraft{
		Agent: "codex-aion", ThreadID: "thread-1", TurnID: "turn-1", CWD: "/work/migi",
		Title: "Codex response: migi", Body: "Answer with $$E=mc^2$$ and **emphasis**.",
	})
	if err != nil || !created {
		t.Fatalf("publish message=%#v created=%v error=%v", message, created, err)
	}

	list := httptest.NewRecorder()
	handler.Routes().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/admin/messages/", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "Codex response: migi") {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}

	detail := httptest.NewRecorder()
	path := fmt.Sprintf("/admin/messages/%d", message.ID)
	handler.Routes().ServeHTTP(detail, httptest.NewRequest(http.MethodGet, path, nil))
	body := detail.Body.String()
	if detail.Code != http.StatusOK || !strings.Contains(body, "$$E=mc^2$$") ||
		!strings.Contains(body, "<strong>emphasis</strong>") ||
		!strings.Contains(body, `src="../assets/katex/katex.min.js"`) ||
		!strings.Contains(body, `href="./">All responses`) {
		t.Fatalf("detail status=%d body=%s", detail.Code, body)
	}
	if !strings.Contains(detail.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("CSP=%q", detail.Header().Get("Content-Security-Policy"))
	}
}

func TestAdminRejectsInvalidCSRF(t *testing.T) {
	handler, _ := newTestHandler(t)
	form := url.Values{"csrf_token": {"wrong"}, "ttl": {"10m"}}
	request := httptest.NewRequest(http.MethodPost, "/admin/pair", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	handler.Routes().ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestAdminListsUploadsAndDownloadsSharedFiles(t *testing.T) {
	handler, _ := newTestHandler(t)
	exchange := &fakeFileExchange{max: 1024, content: make(map[string][]byte)}
	handler.config.Files = exchange

	filesPage := httptest.NewRecorder()
	handler.Routes().ServeHTTP(filesPage, httptest.NewRequest(http.MethodGet, "/admin/files/", nil))
	if filesPage.Code != http.StatusOK ||
		!strings.Contains(filesPage.Body.String(), "Shared files") ||
		!strings.Contains(filesPage.Body.String(), "No shared files") {
		t.Fatalf("file exchange page = %d: %s", filesPage.Code, filesPage.Body.String())
	}

	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("csrf_token", handler.csrfToken); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "browser-note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("from the web panel")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	upload := httptest.NewRequest(http.MethodPost, "/admin/files", body)
	upload.Header.Set("Content-Type", writer.FormDataContentType())
	uploaded := httptest.NewRecorder()
	handler.Routes().ServeHTTP(uploaded, upload)
	if uploaded.Code != http.StatusSeeOther {
		t.Fatalf("upload returned %d: %s", uploaded.Code, uploaded.Body.String())
	}
	if len(exchange.files) != 1 || exchange.files[0].Name != "browser-note.txt" ||
		exchange.files[0].Source != "admin" {
		t.Fatalf("uploaded files = %#v", exchange.files)
	}

	filesPage = httptest.NewRecorder()
	handler.Routes().ServeHTTP(filesPage, httptest.NewRequest(http.MethodGet, "/admin/files/", nil))
	if !strings.Contains(filesPage.Body.String(), "browser-note.txt") ||
		!strings.Contains(filesPage.Body.String(), "files/"+exchange.files[0].ID+"/content") {
		t.Fatalf("files page does not show uploaded file: %s", filesPage.Body.String())
	}

	download := httptest.NewRecorder()
	handler.Routes().ServeHTTP(
		download,
		httptest.NewRequest(http.MethodGet, "/admin/files/"+exchange.files[0].ID+"/content", nil),
	)
	if download.Code != http.StatusOK || download.Body.String() != "from the web panel" {
		t.Fatalf("download returned %d: %q", download.Code, download.Body.String())
	}
	if download.Header().Get("X-Content-SHA256") != exchange.files[0].SHA256 ||
		!strings.Contains(download.Header().Get("Content-Disposition"), "browser-note.txt") {
		t.Fatalf("download headers = %#v", download.Header())
	}
}

func TestAdminFileUploadRequiresCSRF(t *testing.T) {
	handler, _ := newTestHandler(t)
	exchange := &fakeFileExchange{max: 1024}
	handler.config.Files = exchange
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("csrf_token", "wrong"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "rejected.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("must not persist"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/files", body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	handler.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || len(exchange.files) != 0 {
		t.Fatalf("CSRF upload returned %d, files %#v", response.Code, exchange.files)
	}
}

func TestAdminSendsTestNotification(t *testing.T) {
	handler, broker := newTestHandler(t)
	form := url.Values{"csrf_token": {handler.csrfToken}}
	request := httptest.NewRequest(
		http.MethodPost,
		"/admin/notifications/test",
		strings.NewReader(form.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	handler.Routes().ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusSeeOther, response.Body.String())
	}
	if location := response.Header().Get("Location"); location != "../?notice=Test+notification+sent" {
		t.Fatalf("Location = %q", location)
	}
	stats, err := broker.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.EventCount != 1 || stats.LatestEventID != 1 {
		t.Fatalf("unexpected stats after test notification: %#v", stats)
	}
}

func TestAdminCreatesOneTimeAgentConfigurationAndRevokesIt(t *testing.T) {
	handler, broker := newTestHandler(t)
	form := url.Values{
		"csrf_token": {handler.csrfToken},
		"name":       {"builder-1"},
		"endpoint":   {"https://203.0.113.10:10444"},
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/agents/create", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create agent token returned %d: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "https://203.0.113.10:10444/v1/agent-events") ||
		!strings.Contains(body, "AA:BB:CC:DD") || !strings.Contains(body, "migi_at_") {
		t.Fatal("created agent configuration is incomplete")
	}
	tokens, err := broker.ListAgentTokens(t.Context())
	if err != nil || len(tokens) != 1 || tokens[0].Name != "builder-1" {
		t.Fatalf("agent tokens %#v, error %v", tokens, err)
	}

	dashboard := httptest.NewRecorder()
	handler.Routes().ServeHTTP(dashboard, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	if strings.Contains(dashboard.Body.String(), "migi_at_") {
		t.Fatal("plain agent token was shown again on the dashboard")
	}

	revokeForm := url.Values{
		"csrf_token": {handler.csrfToken},
		"token_id":   {tokens[0].ID},
	}
	revoke := httptest.NewRequest(http.MethodPost, "/admin/agents/revoke", strings.NewReader(revokeForm.Encode()))
	revoke.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	revokeResponse := httptest.NewRecorder()
	handler.Routes().ServeHTTP(revokeResponse, revoke)
	if revokeResponse.Code != http.StatusSeeOther {
		t.Fatalf("revoke agent token returned %d", revokeResponse.Code)
	}
	tokens, err = broker.ListAgentTokens(t.Context())
	if err != nil || tokens[0].RevokedAt == nil {
		t.Fatalf("revoked agent tokens %#v, error %v", tokens, err)
	}
}

func TestAdminCreatesReusablePublisher(t *testing.T) {
	handler, broker := newTestHandler(t)
	form := url.Values{
		"csrf_token": {handler.csrfToken},
		"endpoint":   {"https://203.0.113.10:10444"},
		"name":       {"app-builder"},
	}
	request := httptest.NewRequest(
		http.MethodPost, "/admin/publishers/create", strings.NewReader(form.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create publisher returned %d: %s", response.Code, response.Body.String())
	}
	if body := response.Body.String(); !strings.Contains(body, "/v1/releases") ||
		!strings.Contains(body, "migi_at_") ||
		strings.Contains(body, "package_name") || strings.Contains(body, "signer_sha256") {
		t.Fatal("created publisher configuration is incomplete")
	}
	publishers, err := broker.ListPublisherTokens(t.Context())
	if err != nil || len(publishers) != 1 || publishers[0].Name != "app-builder" {
		t.Fatalf("publisher tokens %#v, error %v", publishers, err)
	}
}

func TestAdminUpdatesAndClearsPager(t *testing.T) {
	handler, broker := newTestHandler(t)

	postPager := func(message string) *httptest.ResponseRecorder {
		t.Helper()
		form := url.Values{"csrf_token": {handler.csrfToken}, "message": {message}}
		request := httptest.NewRequest(http.MethodPost, "/admin/pager", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.Routes().ServeHTTP(response, request)
		return response
	}

	response := postPager("  Agent needs a decision  ")
	if response.Code != http.StatusSeeOther {
		t.Fatalf("set pager status = %d: %s", response.Code, response.Body.String())
	}
	if location := response.Header().Get("Location"); location != "./?notice=Pager+message+updated" {
		t.Fatalf("pager Location = %q", location)
	}
	state, err := broker.PagerState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Message != "Agent needs a decision" || state.EventID != 1 {
		t.Fatalf("unexpected pager state %#v", state)
	}

	request := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	response = httptest.NewRecorder()
	handler.Routes().ServeHTTP(response, request)
	if !strings.Contains(response.Body.String(), "Agent needs a decision") {
		t.Fatalf("dashboard does not show pager state: %s", response.Body.String())
	}

	response = postPager("")
	if response.Code != http.StatusSeeOther {
		t.Fatalf("clear pager status = %d: %s", response.Code, response.Body.String())
	}
	state, err = broker.PagerState(context.Background())
	if err != nil || state.Message != "" || state.EventID != 2 {
		t.Fatalf("unexpected cleared pager state %#v, error %v", state, err)
	}
}

func TestAdminRevokesDevice(t *testing.T) {
	handler, broker := newTestHandler(t)
	secretHash := sha256.Sum256([]byte("pairing secret"))
	tokenHash := sha256.Sum256([]byte("device token"))
	if err := broker.CreatePairingCode(context.Background(), secretHash[:], time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := broker.RedeemPairingCode(
		context.Background(), secretHash[:], "phone-1", "Samsung A54", tokenHash[:],
	); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"csrf_token": {handler.csrfToken}, "device_id": {"phone-1"}}
	request := httptest.NewRequest(http.MethodPost, "/admin/devices/revoke", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.Routes().ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusSeeOther, response.Body.String())
	}
	if location := response.Header().Get("Location"); location != "./?notice=Device+revoked" {
		t.Fatalf("revoke Location = %q", location)
	}
	if _, err := broker.AuthenticateDevice(context.Background(), tokenHash[:]); !errors.Is(err, events.ErrUnauthorized) {
		t.Fatalf("authenticate revoked device error = %v, want %v", err, events.ErrUnauthorized)
	}
}

func TestPublicEndpointValidation(t *testing.T) {
	valid, err := parsePublicEndpoint("https://192.0.2.1:10443/")
	if err != nil || valid.String() != "https://192.0.2.1:10443" {
		t.Fatalf("valid endpoint = %v, %v", valid, err)
	}
	for _, value := range []string{"http://192.0.2.1", "https://user@host", "https://host/path"} {
		if _, err := parsePublicEndpoint(value); err == nil {
			t.Errorf("parsePublicEndpoint(%q) succeeded", value)
		}
	}
}

func TestPairingEndpointIsRequiredAndValidatedByTheForm(t *testing.T) {
	for _, endpoint := range []string{"", "http://192.0.2.1:10443", "https://host/path"} {
		handler, broker := newTestHandler(t)
		form := url.Values{
			"csrf_token": {handler.csrfToken},
			"endpoint":   {endpoint},
			"ttl":        {"10m"},
		}
		request := httptest.NewRequest(http.MethodPost, "/admin/pair", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.Routes().ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("endpoint %q status = %d, want 400", endpoint, response.Code)
		}
		stats, err := broker.Stats(context.Background())
		if err != nil || stats.ActivePairingCodes != 0 {
			t.Errorf("endpoint %q created pairing code: %#v, %v", endpoint, stats, err)
		}
	}
}

func TestPairingEndpointCanBeEnteredWithoutAConfiguredDefault(t *testing.T) {
	handler, _ := newTestHandler(t)
	handler.config.PublicEndpoint = ""
	form := url.Values{
		"csrf_token": {handler.csrfToken},
		"endpoint":   {"https://192.0.2.44:8443"},
		"ttl":        {"10m"},
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/pair", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("pairing without default status = %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "Endpoint: <code>https://192.0.2.44:8443</code>") {
		t.Fatal("pairing response does not contain the entered endpoint")
	}
}

func TestAdminURLsSurviveARewritingProxy(t *testing.T) {
	handler, broker := newTestHandler(t)
	routes := handler.Routes()
	proxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/migi/") {
			http.NotFound(w, r)
			return
		}
		upstream := r.Clone(r.Context())
		upstream.URL.Path = "/" + strings.TrimPrefix(r.URL.Path, "/migi/")
		routes.ServeHTTP(w, upstream)
	})

	dashboard := httptest.NewRecorder()
	proxy.ServeHTTP(dashboard, httptest.NewRequest(http.MethodGet, "/migi/admin/", nil))
	if dashboard.Code != http.StatusOK {
		t.Fatalf("proxied dashboard status = %d", dashboard.Code)
	}
	body := dashboard.Body.String()
	for _, expected := range []string{
		`href="assets/style.css"`,
		`action="pager"`,
		`action="notifications/test"`,
		`href="devices/"`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("proxied dashboard is missing %s", expected)
		}
	}
	devices := httptest.NewRecorder()
	proxy.ServeHTTP(devices, httptest.NewRequest(http.MethodGet, "/migi/admin/devices/", nil))
	if devices.Code != http.StatusOK || !strings.Contains(devices.Body.String(), `action="../pair"`) {
		t.Fatalf("proxied devices page = %d: %s", devices.Code, devices.Body.String())
	}

	asset := httptest.NewRecorder()
	proxy.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/migi/admin/assets/style.css", nil))
	if asset.Code != http.StatusOK {
		t.Fatalf("proxied asset status = %d", asset.Code)
	}

	form := url.Values{"csrf_token": {handler.csrfToken}}
	request := httptest.NewRequest(
		http.MethodPost,
		"/migi/admin/notifications/test",
		strings.NewReader(form.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther ||
		response.Header().Get("Location") != "../?notice=Test+notification+sent" {
		t.Fatalf("prefixed POST = %d %q", response.Code, response.Header().Get("Location"))
	}
	externalAction, err := url.Parse("http://example.test/migi/admin/notifications/test")
	if err != nil {
		t.Fatal(err)
	}
	redirect, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if resolved := externalAction.ResolveReference(redirect).RequestURI(); resolved != "/migi/admin/?notice=Test+notification+sent" {
		t.Fatalf("external redirect resolves to %q", resolved)
	}
	stats, err := broker.Stats(context.Background())
	if err != nil || stats.EventCount != 1 {
		t.Fatalf("prefixed POST stats = %#v, %v", stats, err)
	}
}

func newTestHandler(t *testing.T) (*Handler, *events.Broker) {
	t.Helper()
	journal, err := events.OpenSQLite(filepath.Join(t.TempDir(), "migi.db"))
	if err != nil {
		t.Fatal(err)
	}
	broker := events.NewBroker(journal)
	t.Cleanup(func() {
		if err := broker.Close(); err != nil {
			t.Error(err)
		}
	})
	handler, err := New(Config{
		Broker:                 broker,
		PublicEndpoint:         "https://203.0.113.10:443",
		CertificateFingerprint: "AA:BB:CC:DD",
		PublicListen:           ":8443",
		IngestListen:           "127.0.0.1:8787",
		AgentListen:            ":8790",
		AgentEndpoint:          "https://203.0.113.10:10444",
		AdminListen:            "127.0.0.1:8788",
		StartedAt:              time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler, broker
}
