package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Config defines when and where to send a notification.
type Config struct {
	Enabled   bool   `json:"enabled"`
	Type      string `json:"type"`       // "webhook", "email", or "telegram"
	Target    string `json:"target"`     // URL for webhook, email address for email, chat_id for telegram
	OnSuccess bool   `json:"on_success"`
	OnFailure bool   `json:"on_failure"`

	// SMTP settings (only for type "email")
	SMTPHost string `json:"smtp_host,omitempty"`
	SMTPPort int    `json:"smtp_port,omitempty"`
	SMTPUser string `json:"smtp_user,omitempty"`
	SMTPPass string `json:"smtp_pass,omitempty"`
	SMTPFrom string `json:"smtp_from,omitempty"`

	// Telegram settings (only for type "telegram")
	TelegramBotToken string `json:"telegram_bot_token,omitempty"`
}

// TaskInfo is a minimal view of a task for notification payloads.
type TaskInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Command string `json:"command"`
}

// ResultInfo is a minimal view of an execution result.
type ResultInfo struct {
	Success    bool   `json:"success"`
	Message    string `json:"message"`
	DurationMs int64  `json:"duration_ms"`
}

// webhookPayload is the JSON body sent to webhook targets.
type webhookPayload struct {
	Event     string     `json:"event"`
	Task      TaskInfo   `json:"task"`
	Result    ResultInfo `json:"result"`
	Timestamp int64      `json:"timestamp"`
}

// Send dispatches notifications for all matching configs.
// It runs asynchronously and logs errors rather than returning them.
func Send(configs []Config, task TaskInfo, result ResultInfo) {
	for _, c := range configs {
		if !c.Enabled {
			continue
		}
		if result.Success && !c.OnSuccess {
			continue
		}
		if !result.Success && !c.OnFailure {
			continue
		}
		go func(cfg Config) {
			if err := dispatch(cfg, task, result); err != nil {
				log.Printf("[zima-cron] notification error (%s -> %s): %v", cfg.Type, cfg.Target, err)
			}
		}(c)
	}
}

func dispatch(cfg Config, task TaskInfo, result ResultInfo) error {
	switch cfg.Type {
	case "webhook":
		return sendWebhook(cfg.Target, task, result)
	case "email":
		return sendEmail(cfg, task, result)
	case "telegram":
		return sendTelegram(cfg, task, result)
	default:
		return fmt.Errorf("unsupported notification type: %s", cfg.Type)
	}
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

func sendWebhook(url string, task TaskInfo, result ResultInfo) error {
	payload := webhookPayload{
		Event:     "task_completed",
		Task:      task,
		Result:    result,
		Timestamp: time.Now().Unix(),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("POST %s returned %d", url, resp.StatusCode)
	}
	return nil
}

func sendEmail(cfg Config, task TaskInfo, result ResultInfo) error {
	if cfg.SMTPHost == "" || cfg.Target == "" {
		return fmt.Errorf("email: smtp_host and target (email address) required")
	}
	port := cfg.SMTPPort
	if port == 0 {
		port = 587
	}
	from := cfg.SMTPFrom
	if from == "" {
		from = cfg.SMTPUser
	}

	status := "FAILED"
	if result.Success {
		status = "SUCCESS"
	}

	subject := fmt.Sprintf("[zima-cron] %s: %s", status, task.Name)
	body := buildEmailBody(task, result, status)

	msg := strings.Join([]string{
		"From: " + from,
		"To: " + cfg.Target,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=UTF-8",
		"",
		body,
	}, "\r\n")

	addr := cfg.SMTPHost + ":" + strconv.Itoa(port)
	var auth smtp.Auth
	if cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPass, cfg.SMTPHost)
	}

	if err := smtp.SendMail(addr, auth, from, []string{cfg.Target}, []byte(msg)); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	return nil
}

func sendTelegram(cfg Config, task TaskInfo, result ResultInfo) error {
	if cfg.TelegramBotToken == "" || cfg.Target == "" {
		return fmt.Errorf("telegram: bot_token and chat_id required")
	}
	status := "FAILED"
	emoji := "\u274c"
	if result.Success {
		status = "SUCCESS"
		emoji = "\u2705"
	}
	text := fmt.Sprintf("%s *%s — %s*\n\n`%s`\nDuration: %dms\n\n```\n%s\n```",
		emoji, status, escapeMarkdown(task.Name),
		escapeMarkdown(task.Command), result.DurationMs,
		escapeMarkdown(result.Message))
	return SendTelegramMessage(cfg.TelegramBotToken, cfg.Target, text)
}

// SendTelegramMessage sends a message via the Telegram Bot API.
// Exported so it can be used for test messages.
func SendTelegramMessage(botToken, chatID, text string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)
	payload := map[string]string{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "Markdown",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal telegram payload: %w", err)
	}
	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var errResp struct {
			Description string `json:"description"`
		}
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("telegram API %d: %s", resp.StatusCode, errResp.Description)
	}
	return nil
}

func escapeMarkdown(s string) string {
	r := strings.NewReplacer("*", "\\*", "_", "\\_", "`", "\\`", "[", "\\[")
	return r.Replace(s)
}

func buildEmailBody(task TaskInfo, result ResultInfo, status string) string {
	color := "#2ecc71"
	if !result.Success {
		color = "#ff5c7a"
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html><body style="font-family:sans-serif;background:#0b0e12;color:#e6eaf2;padding:24px">
<div style="max-width:600px;margin:0 auto;background:#12161c;border-radius:12px;padding:24px;border:1px solid #1c2330">
  <h2 style="margin:0 0 16px;color:%s">%s</h2>
  <table style="width:100%%;border-collapse:collapse;font-size:14px">
    <tr><td style="padding:8px 0;color:#93a1b5">Task</td><td style="padding:8px 0">%s</td></tr>
    <tr><td style="padding:8px 0;color:#93a1b5">Command</td><td style="padding:8px 0"><code>%s</code></td></tr>
    <tr><td style="padding:8px 0;color:#93a1b5">Duration</td><td style="padding:8px 0">%dms</td></tr>
    <tr><td style="padding:8px 0;color:#93a1b5">Output</td><td style="padding:8px 0"><pre style="white-space:pre-wrap;margin:0">%s</pre></td></tr>
  </table>
  <p style="color:#93a1b5;font-size:12px;margin:16px 0 0">zima-cron scheduler</p>
</div>
</body></html>`,
		color, status+" — "+task.Name, task.Name, task.Command, result.DurationMs,
		strings.ReplaceAll(result.Message, "<", "&lt;"))
}
