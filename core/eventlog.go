package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// EventLogger writes structured JSONL logs for operational debugging:
//   - turns.jsonl  — one entry per completed agent turn (who said what, bot replied what, how long, tokens)
//   - errors.jsonl — one entry per error/warning worth human attention
//
// Both files rotate by date: <dir>/<project>/<YYYY-MM-DD>/{turns,errors}.jsonl.
// Zero-value EventLogger is safe — all methods no-op.
type EventLogger struct {
	baseDir string // e.g. ~/.cc-connect/logs
	project string
	mu      sync.Mutex
}

// NewEventLogger creates a logger rooted at baseDir. baseDir="" disables all writes.
func NewEventLogger(baseDir, project string) *EventLogger {
	return &EventLogger{baseDir: baseDir, project: project}
}

// Package-level pointer used by AlertError so non-core packages (e.g. platform/*)
// can surface errors without threading an EventLogger through their APIs. The last
// Engine to initialise wins — safe because a cc-connect process usually hosts a
// single project; multi-project hosts will see alerts tagged with the wrong
// project name but the alert still fires.
var defaultEventLogger atomic.Pointer[EventLogger]

// SetDefaultEventLogger registers an EventLogger for package-level AlertError calls.
func SetDefaultEventLogger(el *EventLogger) {
	defaultEventLogger.Store(el)
}

// newEngineEventLogger creates a logger and also promotes it to the package default
// so platform packages can call AlertError without an explicit reference.
func newEngineEventLogger(baseDir, project string) *EventLogger {
	el := NewEventLogger(baseDir, project)
	SetDefaultEventLogger(el)
	return el
}

// AlertError records an error via the default event logger (if any). Safe to call
// from any package that can import core; returns silently when no logger is set.
func AlertError(category, summary string, fields map[string]any) {
	if el := defaultEventLogger.Load(); el != nil {
		el.WriteError(category, summary, fields)
	}
}

func (el *EventLogger) dayDir() (string, error) {
	if el == nil || el.baseDir == "" {
		return "", nil
	}
	day := time.Now().Format("2006-01-02")
	dir := filepath.Join(el.baseDir, el.project, day)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func (el *EventLogger) append(filename string, entry map[string]any) {
	if el == nil || el.baseDir == "" {
		return
	}
	el.mu.Lock()
	defer el.mu.Unlock()
	dir, err := el.dayDir()
	if err != nil || dir == "" {
		slog.Warn("eventlog: mkdir failed", "error", err)
		return
	}
	path := filepath.Join(dir, filename)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Warn("eventlog: open failed", "path", path, "error", err)
		return
	}
	defer f.Close()

	if _, ok := entry["ts"]; !ok {
		entry["ts"] = time.Now().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(entry)
	if err != nil {
		slog.Warn("eventlog: marshal failed", "error", err)
		return
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		slog.Warn("eventlog: write failed", "error", err)
	}
}

// TurnEntry captures one completed turn (user message in, bot reply out).
type TurnEntry struct {
	SessionKey      string
	SessionID       string
	AgentSessionID  string
	MsgID           string
	Platform        string
	UserID          string
	UserName        string
	UserMessage     string
	BotReply        string
	ToolCount       int
	InputTokens     int
	OutputTokens    int
	DurationMs      int64
}

func (el *EventLogger) WriteTurn(t TurnEntry) {
	el.append("turns.jsonl", map[string]any{
		"session_key":      t.SessionKey,
		"session_id":       t.SessionID,
		"agent_session_id": t.AgentSessionID,
		"msg_id":           t.MsgID,
		"platform":         t.Platform,
		"user_id":          t.UserID,
		"user_name":        t.UserName,
		"user_message":     truncate(t.UserMessage, 2000),
		"bot_reply":        truncate(t.BotReply, 4000),
		"tool_count":       t.ToolCount,
		"input_tokens":     t.InputTokens,
		"output_tokens":    t.OutputTokens,
		"duration_ms":      t.DurationMs,
	})
}

// WriteError records an issue worth a human looking at. Fields is free-form key/value
// context (e.g. session_key, url, http_code).
//
// If ERROR_ALERT_TG_TOKEN + ERROR_ALERT_TG_CHAT_ID env vars are set, the error is also
// forwarded to Telegram as a plaintext message (best-effort, async, non-blocking).
func (el *EventLogger) WriteError(category, summary string, fields map[string]any) {
	entry := map[string]any{
		"category": category,
		"summary":  truncate(summary, 2000),
	}
	for k, v := range fields {
		entry[k] = v
	}
	el.append("errors.jsonl", entry)

	if token := os.Getenv("ERROR_ALERT_TG_TOKEN"); token != "" {
		if chatID := os.Getenv("ERROR_ALERT_TG_CHAT_ID"); chatID != "" {
			go pushTelegramAlert(token, chatID, el.project, category, summary, fields)
		}
	}
}

func pushTelegramAlert(token, chatID, project, category, summary string, fields map[string]any) {
	var fb bytes.Buffer
	for k, v := range fields {
		fmt.Fprintf(&fb, "\n  %s: %v", k, v)
	}
	text := fmt.Sprintf("⚠️ [%s] %s\n%s%s", project, category, summary, fb.String())
	if len(text) > 3500 {
		text = text[:3500] + "…[truncated]"
	}

	body, _ := json.Marshal(map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_web_page_preview": true,
	})
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("eventlog: telegram alert POST failed", "error", err)
		return
	}
	resp.Body.Close()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…[truncated]"
}
