package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

const telegramAPI = "https://api.telegram.org"

// tokenPattern is the shape of a bot token from @BotFather: "<bot id>:<secret>".
var tokenPattern = regexp.MustCompile(`^\d{5,}:[A-Za-z0-9_-]{30,}$`)

var tagPattern = regexp.MustCompile(`<[^>]*>?`)

// Telegram's limit is 4096 characters; keep room for the ellipsis.
const maxMessageRunes = 4000

// ValidTelegramToken reports whether token looks like a bot token.
func ValidTelegramToken(token string) bool { return tokenPattern.MatchString(token) }

// Telegram sends messages through a bot to one chat. The token is part of the
// request URL, so errors are rebuilt without it before they reach a log.
type Telegram struct {
	token  string
	chatID string
	base   string
	client *http.Client
}

// NewTelegram returns a notifier for the bot token and chat id from @BotFather
// and the chat the bot was added to.
func NewTelegram(token, chatID string) *Telegram {
	return &Telegram{
		token:  token,
		chatID: chatID,
		base:   telegramAPI,
		// The URL is fixed, so no SSRF guard is needed; no redirects are followed.
		client: &http.Client{
			Timeout: sendTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Name implements Notifier.
func (t *Telegram) Name() string { return "telegram" }

type telegramReply struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// Send implements Notifier.
func (t *Telegram) Send(ctx context.Context, m Message) error {
	text := m.Text
	if r := []rune(text); len(r) > maxMessageRunes {
		// Callers keep messages short; this only guards against a 400 that
		// would drop the alert. Cutting may break a tag, so send it as plain text.
		text = html.UnescapeString(tagPattern.ReplaceAllString(string(r[:maxMessageRunes]), "")) + "…"
		return t.send(ctx, text, "")
	}
	return t.send(ctx, text, "HTML")
}

func (t *Telegram) send(ctx context.Context, text, parseMode string) error {
	payload := map[string]any{
		"chat_id":                  t.chatID,
		"text":                     text,
		"disable_web_page_preview": true,
	}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return &Permanent{err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+"/bot"+t.token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return &Permanent{errors.New("telegram: bad request")}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		// *url.Error prints the URL, which holds the token: keep only the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("telegram: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var reply telegramReply
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&reply); err != nil {
		return fmt.Errorf("telegram: HTTP %d with an unreadable body", resp.StatusCode)
	}
	if reply.OK {
		return nil
	}
	err = fmt.Errorf("telegram: %d %s", reply.ErrorCode, reply.Description)
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return &RetryAfter{Wait: time.Duration(reply.Parameters.RetryAfter) * time.Second, Err: err}
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// Bad token, unknown chat, bot blocked or malformed text: retrying will not help.
		return &Permanent{err}
	default:
		return err
	}
}
