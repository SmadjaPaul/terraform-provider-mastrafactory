package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const projectID = "00000000-0000-4000-8000-000000000001"

func TestEndpointValidation(t *testing.T) {
	if _, err := New(Config{Endpoint: "https://factory.example.com", SessionToken: "s"}); err == nil {
		t.Fatal("accepted credentialed client without a runner-pinned trusted endpoint")
	}
	for _, endpoint := range []string{"http://public.example.com", "http://10.0.20.10", "ftp://factory.svc", "https://user:pass@example.com", "https://example.com/other", "https://example.com?token=bad"} {
		if _, err := New(Config{Endpoint: endpoint, TrustedEndpoint: endpoint, SessionToken: "s"}); err == nil {
			t.Errorf("accepted %q", endpoint)
		}
	}
	for _, endpoint := range []string{"https://example.com", "http://factory.svc.cluster.local:4111", "http://localhost:4111", "http://127.0.0.1:4111"} {
		if _, err := New(Config{Endpoint: endpoint, TrustedEndpoint: endpoint, SessionToken: "s"}); err != nil {
			t.Errorf("rejected %q: %v", endpoint, err)
		}
	}
	for _, authOrigin := range []string{"http://public.example.com", "https://example.com/path", "https://user:pass@example.com", "https://example.com/?token=secret"} {
		if _, err := New(Config{Endpoint: "http://factory.svc.cluster.local:4111", TrustedEndpoint: "http://factory.svc.cluster.local:4111", AuthOrigin: authOrigin, SessionToken: "s"}); err == nil {
			t.Errorf("accepted invalid auth origin %q", authOrigin)
		}
	}
	if _, err := New(Config{Endpoint: "http://factory.svc.cluster.local:4111", TrustedEndpoint: "http://factory.svc.cluster.local:4111", AuthOrigin: "https://factory.example.com", SessionToken: "s"}); err != nil {
		t.Fatalf("rejected valid public auth origin for internal service endpoint: %v", err)
	}
	if _, err := New(Config{Endpoint: "https://attacker.example", TrustedEndpoint: "https://factory.example.com", SessionToken: "s"}); err == nil {
		t.Fatal("accepted configured endpoint that differs from the runner-pinned destination")
	}
}

func TestMarkdownSafety(t *testing.T) {
	s, err := Markdown("factory-plan", `A "description": with colon`, "Start\n\nSubmit plan\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, `description: "A \"description\": with colon"`) || !strings.HasSuffix(s, "Submit plan\n") {
		t.Fatalf("unexpected Markdown: %s", s)
	}
	if _, err := Markdown("../bad", "desc", "instructions"); err == nil {
		t.Fatal("accepted invalid name")
	}
	if _, err := Markdown("factory-plan", "desc", " \n"); err == nil {
		t.Fatal("accepted empty instructions")
	}
}

func TestClientRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var stored string
	var authCalls, readCalls, putCalls, deleteCalls, boardCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/auth/api/sign-in/email" {
			authCalls++
			if r.Header.Get("Origin") != "https://factory.example.com" {
				http.Error(w, "MISSING_OR_NULL_ORIGIN", 403)
				return
			}
			if r.Method != http.MethodPost {
				http.Error(w, "method", 405)
				return
			}
			var input struct {
				Email      string `json:"email"`
				Password   string `json:"password"`
				RememberMe *bool  `json:"rememberMe"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input.Email != "bot@example.com" || input.Password != "secret" || input.RememberMe == nil || *input.RememberMe {
				http.Error(w, "denied", 401)
				return
			}
			_, _ = w.Write([]byte(`{"token":"test-session"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-session" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/web/factory/skills":
			readCalls++
			if r.URL.Query().Get("projectId") != projectID {
				http.Error(w, "project", 400)
				return
			}
			// Model the stored SKILL.md, rather than returning a constant response:
			// otherwise a malformed write can pass this round-trip test.
			description, instructions := "Plan safely", "Submit plan"
			var edited any
			if stored != "" {
				frontmatterAndBody := strings.SplitN(stored, "\n---\n\n", 2)
				if len(frontmatterAndBody) != 2 {
					http.Error(w, "malformed stored skill", 500)
					return
				}
				for _, line := range strings.Split(frontmatterAndBody[0], "\n") {
					if strings.HasPrefix(line, "description: ") {
						if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "description: ")), &description); err != nil {
							http.Error(w, "invalid description", 500)
							return
						}
					}
				}
				instructions = strings.TrimSpace(frontmatterAndBody[1])
				edited = map[string]string{"updatedAt": "2026-11-10T00:00:00Z"}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"skills": []map[string]any{{
					"name": "factory-plan", "description": description, "content": instructions,
					"origin": "code", "edited": edited,
				}},
			})
		case strings.HasSuffix(r.URL.Path, "/skills/factory-plan"):
			if r.Method == http.MethodPut {
				putCalls++
				var payload struct {
					Content string `json:"content"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					http.Error(w, "bad json", 400)
					return
				}
				if !strings.HasPrefix(payload.Content, "---\nname: factory-plan\ndescription: ") ||
					!strings.Contains(payload.Content, "\n---\n\n") {
					http.Error(w, "bad skill", 422)
					return
				}
				stored = payload.Content
				_, _ = w.Write([]byte(`{"skill":{"name":"factory-plan"}}`))
				return
			}
			if r.Method == http.MethodDelete {
				deleteCalls++
				stored = ""
				_, _ = w.Write([]byte(`{"skill":{"name":"factory-plan"}}`))
				return
			}
			http.Error(w, "method", 405)
		case strings.HasSuffix(r.URL.Path, "/boards/work"):
			boardCalls++
			_, _ = w.Write([]byte(`{"board":{"key":"work","name":"Work","source":"builtin","editable":false,"revision":null,"phases":[{"key":"planning"}],"problems":[]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := New(Config{Endpoint: srv.URL, TrustedEndpoint: srv.URL, AuthOrigin: "https://factory.example.com", Email: "bot@example.com", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	const count = 12
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Skill(context.Background(), projectID, "factory-plan"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := c.UpsertSkill(context.Background(), projectID, "factory-plan", "Plan safely", "Submit plan"); err != nil {
		t.Fatal(err)
	}
	skill, err := c.Skill(context.Background(), projectID, "factory-plan")
	if err != nil || skill.Edited == nil || skill.Description != "Plan safely" || skill.Content != "Submit plan" {
		t.Fatalf("remote override does not match the requested skill: skill=%+v err=%v", skill, err)
	}
	if err := c.UpsertSkill(context.Background(), projectID, "factory-plan", "Revised plan", "Write a new plan\nSubmit for review"); err != nil {
		t.Fatal(err)
	}
	skill, err = c.Skill(context.Background(), projectID, "factory-plan")
	if err != nil || skill.Edited == nil || skill.Description != "Revised plan" || skill.Content != "Write a new plan\nSubmit for review" {
		t.Fatalf("remote skill content did not change: skill=%+v err=%v", skill, err)
	}
	b, err := c.Board(context.Background(), projectID, "work")
	if err != nil || b.Editable || b.Source != "builtin" {
		t.Fatalf("board mismatch: %+v %v", b, err)
	}
	if err := c.DeleteSkillOverride(context.Background(), projectID, "factory-plan"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if authCalls != 1 || readCalls != count+2 || putCalls != 2 || deleteCalls != 1 || boardCalls != 1 {
		t.Fatalf("unexpected call counts sign=%d read=%d put=%d del=%d board=%d", authCalls, readCalls, putCalls, deleteCalls, boardCalls)
	}
}

func TestFailureCannotLeakCredentials(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, `password="secret"`, 403) }))
	defer s.Close()
	c, err := New(Config{Endpoint: s.URL, TrustedEndpoint: s.URL, SessionToken: "sensitive-token"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Skill(context.Background(), projectID, "factory-plan")
	if err == nil {
		t.Fatal("expected API failure")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 403 {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "sensitive-token") {
		t.Fatalf("credential leaked: %s", err)
	}
}

func TestAPIErrorStatusesAndConnectionFailure(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, `sensitive response body`, status)
			}))
			defer srv.Close()
			c, err := New(Config{Endpoint: srv.URL, TrustedEndpoint: srv.URL, SessionToken: "test-only"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Skill(context.Background(), projectID, "factory-plan")
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != status {
				t.Fatalf("expected sanitized HTTP %d, got %v", status, err)
			}
			if strings.Contains(err.Error(), "sensitive response body") {
				t.Fatalf("response body leaked: %v", err)
			}
		})
	}
	t.Run("connection failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		endpoint := srv.URL
		srv.Close()
		c, err := New(Config{Endpoint: endpoint, TrustedEndpoint: endpoint, SessionToken: "test-only"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Skill(context.Background(), projectID, "factory-plan"); err == nil {
			t.Fatal("expected connection error")
		}
	})
}
