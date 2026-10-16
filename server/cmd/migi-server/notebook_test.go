package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirmik/migi/server/internal/agentauth"
	"github.com/mirmik/migi/server/internal/notebook"
)

func TestNotebookAgentAuthorizationAndSharedKnowledge(t *testing.T) {
	broker := newTestBroker(t)
	s, err := notebook.Open(filepath.Join(t.TempDir(), "notebook.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mux := newAgentMuxWithAllStores(broker, nil, nil, nil, newAgentSecurity(), s)
	issue := func(name string) (string, string) {
		id, plain, hash, e := agentauth.Generate()
		if e != nil {
			t.Fatal(e)
		}
		if e = broker.CreateAgentToken(t.Context(), id, name, hash[:]); e != nil {
			t.Fatal(e)
		}
		return id, plain
	}
	a, plainA := issue("writer")
	b, plainB := issue("reader")
	call := func(token, op, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/notebook/"+op, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != status {
			t.Fatalf("%s: %d %s", op, rr.Code, rr.Body.String())
		}
		return rr
	}
	call("", "list", `{}`, 401)
	call(plainA, "list", `{}`, 403)
	if err = s.SetPermission(t.Context(), a, notebook.Permission{Read: true, Write: true}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetPermission(t.Context(), b, notebook.Permission{Read: true}); err != nil {
		t.Fatal(err)
	}
	create := `{"key":"workshop/printer","title":"Принтер","body":"192.0.2.1","operation_id":"first"}`
	call(plainB, "create", create, 403)
	rr := call(plainA, "create", create, 200)
	var r notebook.Record
	if err = json.Unmarshal(rr.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.UpdatedBy != a || r.Revision != 1 {
		t.Fatal(r)
	}
	call(plainA, "create", create, 200)
	rr = call(plainB, "search", `{"query":"принтер"}`, 200)
	if !strings.Contains(rr.Body.String(), "workshop/printer") {
		t.Fatal(rr.Body.String())
	}
	call(plainB, "read", `{"keys":["workshop/printer"]}`, 200)
	call(plainA, "update", `{"key":"workshop/printer","expected_revision":1,"changes":{"body":"192.0.2.2"},"operation_id":"second"}`, 200)
	call(plainA, "update", `{"key":"workshop/printer","expected_revision":1,"changes":{"body":"stale"},"operation_id":"third"}`, 409)
	call(plainA, "export", `{}`, 403)
	call(plainA, "import", `{}`, 403)
	call(plainA, "read", `{"keys":["workshop/printer"],"owner":true}`, 400)
	call(plainA, "read", `{"keys":["workshop/printer"]} {}`, 400)
	if err = broker.RevokeAgentToken(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	call(plainA, "read", `{"keys":["workshop/printer"]}`, 401)
	if err = s.SetPermission(t.Context(), b, notebook.Permission{}); err != nil {
		t.Fatal(err)
	}
	call(plainB, "read", `{"keys":["workshop/printer"]}`, 403)
}
