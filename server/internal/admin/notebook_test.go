package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirmik/migi/server/internal/notebook"
)

func TestNotebookAdminEditingCSRFAndPermissions(t *testing.T) {
	h, broker := newTestHandler(t)
	s, e := notebook.Open(filepath.Join(t.TempDir(), "notebook.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	h.config.Notebook = s
	if e = broker.CreateAgentToken(t.Context(), "token-id", "reader", make([]byte, 32)); e != nil {
		t.Fatal(e)
	}
	routes := h.Routes()
	page := httptest.NewRecorder()
	routes.ServeHTTP(page, httptest.NewRequest("GET", "/admin/notebook/", nil))
	if page.Code != 200 || !strings.Contains(page.Body.String(), "reader") || !strings.Contains(page.Body.String(), h.csrfToken) {
		t.Fatalf("page %d %s", page.Code, page.Body.String())
	}
	call := func(op, body string, csrf bool, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "/admin/notebook/api/"+op, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if csrf {
			req.Header.Set("X-CSRF-Token", h.csrfToken)
		}
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, req)
		if rr.Code != status {
			t.Fatalf("%s: %d %s", op, rr.Code, rr.Body.String())
		}
		return rr
	}
	create := `{"key":"tools/slicer","title":"Слайсер","body":"<script>alert(1)</script>","operation_id":"create"}`
	call("create", create, false, 403)
	call("create", create, true, 200)
	call("update", `{"key":"tools/slicer","expected_revision":1,"changes":{"body":"new command"},"operation_id":"update"}`, true, 200)
	call("update", `{"key":"tools/slicer","expected_revision":1,"changes":{"body":"stale"},"operation_id":"stale"}`, true, 409)
	rr := call("export", `{}`, true, 200)
	var out notebook.Export
	if e = json.Unmarshal(rr.Body.Bytes(), &out); e != nil || len(out.Records) != 1 || out.Records[0].UpdatedBy != "owner" {
		t.Fatal(out, e)
	}
	call("import", rr.Body.String(), true, 409)
	grant := func(csrf bool, status int) {
		t.Helper()
		values := url.Values{"token_id": {"token-id"}, "write": {"on"}}
		if csrf {
			values.Set("csrf_token", h.csrfToken)
		}
		req := httptest.NewRequest("POST", "/admin/notebook/permissions", strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, req)
		if rr.Code != status {
			t.Fatalf("grant %d %s", rr.Code, rr.Body.String())
		}
	}
	grant(false, 403)
	grant(true, http.StatusSeeOther)
	p, e := s.Permission(t.Context(), "token-id")
	if e != nil || !p.Read || !p.Write {
		t.Fatal(p, e)
	}
	for _, asset := range []string{"notebook.js", "notebook.css"} {
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, httptest.NewRequest("GET", "/admin/assets/"+asset, nil))
		if rr.Code != 200 {
			t.Fatal(asset, rr.Code)
		}
	}
}
