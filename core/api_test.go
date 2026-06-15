package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandleSend_AllowsAttachmentOnly(t *testing.T) {
	engine := NewEngine("test", &stubAgent{}, []Platform{&stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}, "", LangEnglish)
	engine.interactiveStates["session-1"] = &interactiveState{
		platform: &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}},
		replyCtx: "reply-ctx",
	}

	api := &APIServer{engines: map[string]*Engine{"test": engine}}
	reqBody := SendRequest{
		Project:    "test",
		SessionKey: "session-1",
		Images: []ImageAttachment{{
			MimeType: "image/png",
			Data:     []byte("img"),
			FileName: "chart.png",
		}},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleSend(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleSend_UnknownProjectReturns404 ensures the API does NOT silently
// fall back to the only registered engine when the caller named a different
// project. Previously a typo'd project name routed messages to whatever
// single engine happened to be loaded.
func TestHandleSend_UnknownProjectReturns404(t *testing.T) {
	engine := NewEngine("projectA", &stubAgent{}, []Platform{&stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}, "", LangEnglish)
	engine.interactiveStates["session-1"] = &interactiveState{
		platform: &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}},
		replyCtx: "reply-ctx",
	}

	api := &APIServer{engines: map[string]*Engine{"projectA": engine}}
	body, err := json.Marshal(SendRequest{
		Project:    "projectB", // typo; does NOT match the loaded engine
		SessionKey: "session-1",
		Message:    "hi",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleSend(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"projectB"`) {
		t.Errorf("body should mention the unknown project name, got: %s", rec.Body.String())
	}
}

// TestHandleSend_EmptyProjectFallsBackToSingleEngine documents the intended
// convenience behavior: when the caller omits project entirely AND only one
// engine is loaded, the API picks it automatically.
func TestHandleSend_EmptyProjectFallsBackToSingleEngine(t *testing.T) {
	engine := NewEngine("solo", &stubAgent{}, []Platform{&stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}, "", LangEnglish)
	engine.interactiveStates["session-1"] = &interactiveState{
		platform: &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}},
		replyCtx: "reply-ctx",
	}

	api := &APIServer{engines: map[string]*Engine{"solo": engine}}
	body, err := json.Marshal(SendRequest{
		// Project deliberately omitted.
		SessionKey: "session-1",
		Message:    "hi",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleSend(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleSend_EmptyProjectMultipleEnginesRequiresName ensures the API
// refuses to guess when more than one engine is loaded and the caller did
// not specify which one to send to.
func TestHandleSend_EmptyProjectMultipleEnginesRequiresName(t *testing.T) {
	engineA := NewEngine("a", &stubAgent{}, []Platform{&stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}, "", LangEnglish)
	engineB := NewEngine("b", &stubAgent{}, []Platform{&stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}, "", LangEnglish)
	api := &APIServer{engines: map[string]*Engine{"a": engineA, "b": engineB}}

	body, err := json.Marshal(SendRequest{
		SessionKey: "session-1",
		Message:    "hi",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleSend(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

type sendWorkDirAgent struct {
	name    string
	workDir string
	session AgentSession
}

func (a *sendWorkDirAgent) Name() string { return a.name }
func (a *sendWorkDirAgent) StartSession(_ context.Context, _ string) (AgentSession, error) {
	return a.session, nil
}
func (a *sendWorkDirAgent) ListSessions(_ context.Context) ([]AgentSessionInfo, error) {
	return nil, nil
}
func (a *sendWorkDirAgent) Stop() error { return nil }
func (a *sendWorkDirAgent) GetWorkDir() string {
	return a.workDir
}

func TestHandleSend_WorkDirStartsSideSession(t *testing.T) {
	agentName := "test-send-workdir-agent"
	baseDir := t.TempDir()
	targetDir := t.TempDir()
	sessionKey := "test:user1"
	var workspaceSession *resultAgentSession

	RegisterAgent(agentName, func(opts map[string]any) (Agent, error) {
		workDir, _ := opts["work_dir"].(string)
		workspaceSession = newResultAgentSession("agent result from " + workDir)
		return &sendWorkDirAgent{
			name:    agentName,
			workDir: workDir,
			session: workspaceSession,
		}, nil
	})

	platform := &stubCronReplyTargetPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "test"},
	}
	engine := NewEngine(
		"test",
		&sendWorkDirAgent{
			name:    agentName,
			workDir: baseDir,
			session: newResultAgentSession("base result"),
		},
		[]Platform{platform},
		filepath.Join(t.TempDir(), "sessions.json"),
		LangEnglish,
	)
	api := &APIServer{engines: map[string]*Engine{"test": engine}}

	body, err := json.Marshal(map[string]any{
		"project":     "test",
		"session_key": sessionKey,
		"message":     "please ask this person",
		"work_dir":    targetDir,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	api.handleSend(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	sent := platform.getSent()
	if len(sent) != 1 || sent[0] != "please ask this person" {
		t.Fatalf("platform sent = %#v, want direct send of request content", sent)
	}

	_, sessions, err := engine.getOrCreateWorkspaceAgent(targetDir)
	if err != nil {
		t.Fatalf("get workspace agent: %v", err)
	}
	list := sessions.ListSessions(sessionKey)
	if len(list) != 1 {
		t.Fatalf("workspace sessions len = %d, want 1", len(list))
	}
	if got := list[0].GetName(); got != "send" {
		t.Fatalf("side session name = %q, want send", got)
	}

	platform.clearSent()
	engine.handleMessage(platform, &Message{
		SessionKey: sessionKey,
		Platform:   "test",
		UserID:     "user1",
		UserName:   "Target",
		Content:    "human answer",
		ReplyCtx:   "reply-ctx",
	})
	sent = waitForPlatformSend(&platform.stubPlatformEngine, 1, 3*time.Second)
	if len(sent) == 0 || !strings.Contains(strings.Join(sent, "\n"), "agent result from "+targetDir) {
		t.Fatalf("platform sent after reply = %#v, want agent result from target work dir", sent)
	}
	if workspaceSession == nil || len(workspaceSession.sentPrompts) != 1 || !strings.Contains(workspaceSession.sentPrompts[0], "human answer") {
		t.Fatalf("workspace session prompts = %#v, want human reply prompt", workspaceSession)
	}
}
