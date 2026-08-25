package wecom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	BotCallbackPath   = "/api/v1/vendor/wecom/bot/callback"
	maxCallbackBytes  = 64 << 10
	botQueryTimeout   = 30 * time.Second
	messageDedupeTime = 2 * time.Hour
)

type BotQuery func(ctx context.Context) (string, error)

type BotHandler struct {
	cryptor   *botCryptor
	query     BotQuery
	responder BotResponder
	logger    *slog.Logger
	now       func() time.Time

	workContext context.Context
	cancelWork  context.CancelFunc
	workers     sync.WaitGroup
	closeOnce   sync.Once

	seenMutex sync.Mutex
	seen      map[string]time.Time
}

type botCallbackMessage struct {
	MessageID   string `json:"msgid"`
	MessageType string `json:"msgtype"`
	ResponseURL string `json:"response_url"`
	Event       struct {
		EventType string `json:"eventtype"`
	} `json:"event"`
}

type encryptedCallback struct {
	Encrypted string `json:"encrypt"`
}

type encryptedReply struct {
	Encrypted string `json:"encrypt"`
	Signature string `json:"msgsignature"`
	Timestamp int64  `json:"timestamp"`
	Nonce     string `json:"nonce"`
}

func NewBotHandler(token, encodingAESKey string, query BotQuery, responder BotResponder, logger *slog.Logger) (*BotHandler, error) {
	cryptor, err := newBotCryptor(token, encodingAESKey)
	if err != nil {
		return nil, err
	}
	if query == nil {
		return nil, errors.New("企业微信智能机器人查询函数不能为空")
	}
	if responder == nil {
		responder = NewBotResponseClient(nil)
	}
	if logger == nil {
		logger = slog.Default()
	}
	workContext, cancelWork := context.WithCancel(context.Background())
	return &BotHandler{
		cryptor:     cryptor,
		query:       query,
		responder:   responder,
		logger:      logger,
		now:         time.Now,
		workContext: workContext,
		cancelWork:  cancelWork,
		seen:        make(map[string]time.Time),
	}, nil
}

func (handler *BotHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		handler.verifyURL(writer, request)
	case http.MethodPost:
		handler.receiveCallback(writer, request)
	default:
		writer.Header().Set("Allow", "GET, POST")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (handler *BotHandler) verifyURL(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	signature := query.Get("msg_signature")
	timestamp := query.Get("timestamp")
	nonce := query.Get("nonce")
	echo := query.Get("echostr")
	if !handler.cryptor.verify(signature, timestamp, nonce, echo) {
		http.Error(writer, "invalid signature", http.StatusUnauthorized)
		return
	}
	plaintext, err := handler.cryptor.decrypt(echo)
	if err != nil {
		http.Error(writer, "invalid encrypted payload", http.StatusUnauthorized)
		return
	}
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(plaintext)
}

func (handler *BotHandler) receiveCallback(writer http.ResponseWriter, request *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(request.Body, maxCallbackBytes+1))
	if err != nil {
		http.Error(writer, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(raw) > maxCallbackBytes {
		http.Error(writer, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	var envelope encryptedCallback
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Encrypted == "" {
		http.Error(writer, "invalid request body", http.StatusBadRequest)
		return
	}
	query := request.URL.Query()
	signature := query.Get("msg_signature")
	timestamp := query.Get("timestamp")
	nonce := query.Get("nonce")
	if !handler.cryptor.verify(signature, timestamp, nonce, envelope.Encrypted) {
		http.Error(writer, "invalid signature", http.StatusUnauthorized)
		return
	}
	plaintext, err := handler.cryptor.decrypt(envelope.Encrypted)
	if err != nil {
		http.Error(writer, "invalid encrypted payload", http.StatusUnauthorized)
		return
	}

	var message botCallbackMessage
	if err := json.Unmarshal(plaintext, &message); err != nil || message.MessageType == "" {
		http.Error(writer, "invalid callback message", http.StatusBadRequest)
		return
	}

	if message.MessageType == "event" && message.Event.EventType == "enter_chat" {
		handler.writeEncryptedReply(writer, nonce, map[string]any{
			"msgtype": "text",
			"text": map[string]string{
				"content": "发送任意消息即可查询 Coding Plan 最新用量。",
			},
		})
		return
	}
	if message.MessageType == "event" || message.MessageType == "stream" || message.ResponseURL == "" {
		writer.WriteHeader(http.StatusOK)
		return
	}
	if message.MessageID == "" {
		http.Error(writer, "callback message id is required", http.StatusBadRequest)
		return
	}
	if handler.markMessage(message.MessageID) {
		handler.startQuery(message.MessageID, message.ResponseURL)
	}
	writer.WriteHeader(http.StatusOK)
}

func (handler *BotHandler) writeEncryptedReply(writer http.ResponseWriter, nonce string, message any) {
	plaintext, err := json.Marshal(message)
	if err != nil {
		handler.logger.Error("编码企业微信智能机器人被动回复失败", "error", err)
		http.Error(writer, "failed to encode response", http.StatusInternalServerError)
		return
	}
	encrypted, err := handler.cryptor.encrypt(plaintext)
	if err != nil {
		handler.logger.Error("加密企业微信智能机器人被动回复失败", "error", err)
		http.Error(writer, "failed to encrypt response", http.StatusInternalServerError)
		return
	}
	timestamp := handler.now().Unix()
	timestampText := strconv.FormatInt(timestamp, 10)
	reply := encryptedReply{
		Encrypted: encrypted,
		Signature: handler.cryptor.signature(timestampText, nonce, encrypted),
		Timestamp: timestamp,
		Nonce:     nonce,
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(writer).Encode(reply); err != nil {
		handler.logger.Error("写入企业微信智能机器人被动回复失败", "error", err)
	}
}

func (handler *BotHandler) startQuery(messageID, responseURL string) {
	handler.workers.Add(1)
	go func() {
		defer handler.workers.Done()
		ctx, cancel := context.WithTimeout(handler.workContext, botQueryTimeout)
		defer cancel()

		content, err := handler.query(ctx)
		if err != nil {
			handler.logger.Error("企业微信智能机器人查询用量失败", "msgid", messageID, "error", err)
			content = "# Coding Plan 用量查询失败\n> 请稍后重试"
		}
		content = strings.TrimSpace(content)
		if content == "" {
			content = "# Coding Plan 用量查询失败\n> 暂无可展示的用量数据"
		}
		if err := handler.responder.SendMarkdown(ctx, responseURL, content); err != nil {
			handler.logger.Error("企业微信智能机器人回复失败", "msgid", messageID, "error", err)
		}
	}()
}

func (handler *BotHandler) markMessage(messageID string) bool {
	handler.seenMutex.Lock()
	defer handler.seenMutex.Unlock()
	now := handler.now()
	for seenID, seenAt := range handler.seen {
		if now.Sub(seenAt) >= messageDedupeTime {
			delete(handler.seen, seenID)
		}
	}
	if _, exists := handler.seen[messageID]; exists {
		return false
	}
	handler.seen[messageID] = now
	return true
}

func (handler *BotHandler) Close(ctx context.Context) error {
	handler.closeOnce.Do(handler.cancelWork)
	done := make(chan struct{})
	go func() {
		handler.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待企业微信智能机器人查询结束: %w", ctx.Err())
	}
}
