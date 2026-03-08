package notify

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// TelegramNotifier sends notifications via Telegram Bot API.
// Set TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID environment variables.
type TelegramNotifier struct {
	botToken string
	chatID   string
	client   *http.Client
}

// NewTelegramNotifier creates a notifier from environment variables.
// Returns nil if not configured (no-op, won't crash).
func NewTelegramNotifier() *TelegramNotifier {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	chatID := os.Getenv("TELEGRAM_CHAT_ID")
	if token == "" || chatID == "" {
		return nil
	}
	return &TelegramNotifier{
		botToken: token,
		chatID:   chatID,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Send sends a text message. Silently fails (logs warning, doesn't block trading).
func (t *TelegramNotifier) Send(ctx context.Context, message string) {
	if t == nil {
		return
	}
	go func() {
		if err := t.sendMessage(ctx, message); err != nil {
			log.Printf("[TELEGRAM] Send failed: %v", err)
		}
	}()
}

// Sendf sends a formatted message.
func (t *TelegramNotifier) Sendf(ctx context.Context, format string, args ...interface{}) {
	t.Send(ctx, fmt.Sprintf(format, args...))
}

// TradeAlert sends a formatted trade notification.
func (t *TelegramNotifier) TradeAlert(ctx context.Context, strategy, symbol, action string, amount float64, currency string, pnl float64, reason string) {
	var emoji string
	switch {
	case strings.Contains(strings.ToLower(action), "buy") || strings.Contains(strings.ToLower(action), "long"):
		emoji = "🟢"
	case strings.Contains(strings.ToLower(action), "sell") || strings.Contains(strings.ToLower(action), "short") || strings.Contains(strings.ToLower(action), "cover"):
		if pnl > 0 {
			emoji = "💰"
		} else if pnl < 0 {
			emoji = "🔴"
		} else {
			emoji = "🟡"
		}
	default:
		emoji = "📊"
	}

	msg := fmt.Sprintf("%s *%s* | %s\n%s %s %.2f\n",
		emoji, strategy, symbol, action, currency, amount)

	if pnl != 0 {
		pnlEmoji := "📈"
		if pnl < 0 {
			pnlEmoji = "📉"
		}
		msg += fmt.Sprintf("%s PnL: %s %.2f\n", pnlEmoji, currency, pnl)
	}
	if reason != "" {
		msg += fmt.Sprintf("Reason: %s", reason)
	}

	t.Send(ctx, msg)
}

// DailySummary sends a daily performance summary.
func (t *TelegramNotifier) DailySummary(ctx context.Context, summaries []string) {
	if len(summaries) == 0 {
		return
	}

	msg := "📋 *Daily Summary*\n" + strings.Join(summaries, "\n")
	t.Send(ctx, msg)
}

func (t *TelegramNotifier) sendMessage(ctx context.Context, text string) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.botToken)

	params := url.Values{}
	params.Set("chat_id", t.chatID)
	params.Set("text", text)
	params.Set("parse_mode", "Markdown")
	params.Set("disable_web_page_preview", "true")

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, strings.NewReader(params.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
