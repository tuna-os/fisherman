package sshkeys

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func withServer(handler http.HandlerFunc, fn func(baseURL string)) {
	srv := httptest.NewServer(handler)
	defer srv.Close()
	fn(srv.URL)
}

func overrideClient(srv *httptest.Server) func() {
	orig := client
	client = srv.Client()
	return func() { client = orig }
}

func TestFetch_Success(t *testing.T) {
	const keysBody = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA test@example\nssh-rsa AAAAB3Nza test2@example\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, keysBody)
	}))
	defer srv.Close()
	restore := overrideClient(srv)
	defer restore()

	keys, err := fetch(srv.URL + "/testuser.keys")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d: %v", len(keys), keys)
	}
	if keys[0] != "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA test@example" {
		t.Errorf("unexpected first key: %q", keys[0])
	}
}

func TestFetch_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "\n\n")
	}))
	defer srv.Close()
	restore := overrideClient(srv)
	defer restore()

	keys, err := fetch(srv.URL + "/nokeys.keys")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected 0 keys, got %d", len(keys))
	}
}

func TestFetch_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	restore := overrideClient(srv)
	defer restore()

	_, err := fetch(srv.URL + "/nobody.keys")
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
}

func TestFetch_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	restore := overrideClient(srv)
	defer restore()

	_, err := fetch(srv.URL + "/error.keys")
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
}
