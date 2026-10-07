// SPDX-License-Identifier: Apache-2.0

package identityclient

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestRegisterVariants(t *testing.T) {
	c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["invite_token"] != "" {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"code": "registration_forbidden"}})
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	if err := c.Register(context.Background(), "ker", "pw", ""); err != nil {
		t.Fatalf("open register: %v", err)
	}
	if err := c.Register(context.Background(), "ker", "pw", "tok"); !IsCode(err, "registration_forbidden") {
		t.Fatalf("invite register: %v", err)
	}
	// Transport failure surfaces as a plain error (retryable by the caller).
	broken := New("http://127.0.0.1:1")
	if _, err := broken.Login(context.Background(), "k", "p"); err == nil {
		t.Fatal("connection refused must error")
	}
}
