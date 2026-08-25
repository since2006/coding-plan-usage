package wecom

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testBotToken  = "BotToken123"
	testBotAESKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
)

type botResponseCall struct {
	responseURL string
	content     string
}

type fakeBotResponder struct {
	calls chan botResponseCall
}

func (responder *fakeBotResponder) SendMarkdown(_ context.Context, responseURL, content string) error {
	responder.calls <- botResponseCall{responseURL: responseURL, content: content}
	return nil
}

func TestBotCryptorRoundTripAndSignature(t *testing.T) {
	cryptor, err := newBotCryptor(testBotToken, testBotAESKey)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cryptor.encrypt([]byte(`{"msgtype":"text"}`))
	if err != nil {
		t.Fatal(err)
	}
	signature := cryptor.signature("1700000000", "nonce", encrypted)
	if !cryptor.verify(signature, "1700000000", "nonce", encrypted) {
		t.Fatal("verify() = false, want true")
	}
	if cryptor.verify(signature, "1700000001", "nonce", encrypted) {
		t.Fatal("verify() accepted a changed timestamp")
	}
	plaintext, err := cryptor.decrypt(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(plaintext), `{"msgtype":"text"}`; got != want {
		t.Fatalf("decrypt() = %q, want %q", got, want)
	}
}

func TestBotHandlerVerifiesCallbackURL(t *testing.T) {
	handler, cryptor := newTestBotHandler(t, nil, func(context.Context) (string, error) {
		return "unused", nil
	})
	echo, err := cryptor.encrypt([]byte("verified"))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, signedBotURL(echo, cryptor, "1700000000", "nonce"), nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	if got, want := response.Body.String(), "verified"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestBotHandlerQueriesAndRepliesThroughResponseURLOnce(t *testing.T) {
	responder := &fakeBotResponder{calls: make(chan botResponseCall, 2)}
	var queryCalls atomic.Int32
	handler, cryptor := newTestBotHandler(t, responder, func(context.Context) (string, error) {
		queryCalls.Add(1)
		return "# Coding Plan 用量汇总", nil
	})
	callback := map[string]any{
		"msgid":        "message-1",
		"msgtype":      "text",
		"response_url": "https://qyapi.weixin.qq.com/cgi-bin/aibot/response?response_code=secret",
		"text": map[string]string{
			"content": "查询用量",
		},
	}
	requestBody, callbackURL := encryptedBotCallback(t, cryptor, callback)

	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodPost, callbackURL, bytes.NewReader(requestBody))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.Len() != 0 {
			t.Fatalf("attempt %d: status = %d, body = %q", attempt, response.Code, response.Body.String())
		}
	}

	select {
	case call := <-responder.calls:
		if call.content != "# Coding Plan 用量汇总" {
			t.Fatalf("content = %q", call.content)
		}
		if call.responseURL != callback["response_url"] {
			t.Fatalf("responseURL = %q", call.responseURL)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bot response")
	}
	select {
	case call := <-responder.calls:
		t.Fatalf("duplicate response = %+v", call)
	case <-time.After(50 * time.Millisecond):
	}
	if got := queryCalls.Load(); got != 1 {
		t.Fatalf("query calls = %d, want 1", got)
	}
	closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := handler.Close(closeContext); err != nil {
		t.Fatal(err)
	}
}

func TestBotHandlerReturnsEncryptedWelcomeMessage(t *testing.T) {
	handler, cryptor := newTestBotHandler(t, nil, func(context.Context) (string, error) {
		return "unused", nil
	})
	callback := map[string]any{
		"msgid":   "event-1",
		"msgtype": "event",
		"event": map[string]string{
			"eventtype": "enter_chat",
		},
	}
	requestBody, callbackURL := encryptedBotCallback(t, cryptor, callback)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, callbackURL, bytes.NewReader(requestBody)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	var reply encryptedReply
	if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if !cryptor.verify(reply.Signature, strconv.FormatInt(reply.Timestamp, 10), reply.Nonce, reply.Encrypted) {
		t.Fatal("welcome reply signature is invalid")
	}
	plaintext, err := cryptor.decrypt(reply.Encrypted)
	if err != nil {
		t.Fatal(err)
	}
	var message struct {
		MessageType string `json:"msgtype"`
		Text        struct {
			Content string `json:"content"`
		} `json:"text"`
	}
	if err := json.Unmarshal(plaintext, &message); err != nil {
		t.Fatal(err)
	}
	if message.MessageType != "text" || message.Text.Content == "" {
		t.Fatalf("welcome message = %+v", message)
	}
}

func TestBotHandlerRejectsInvalidSignature(t *testing.T) {
	handler, cryptor := newTestBotHandler(t, nil, func(context.Context) (string, error) {
		return "unused", nil
	})
	requestBody, callbackURL := encryptedBotCallback(t, cryptor, map[string]any{
		"msgid":        "message-1",
		"msgtype":      "text",
		"response_url": "https://example.invalid/response",
	})
	parsedURL, err := url.Parse(callbackURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsedURL.Query()
	query.Set("msg_signature", "invalid")
	parsedURL.RawQuery = query.Encode()
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, parsedURL.String(), bytes.NewReader(requestBody)))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestBotResponseClientSendsMarkdown(t *testing.T) {
	var requestPayload map[string]any
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost {
			t.Errorf("method = %s", request.Method)
		}
		raw, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		if err := json.Unmarshal(raw, &requestPayload); err != nil {
			t.Error(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"errcode":0,"errmsg":"ok"}`)),
		}, nil
	})}

	client := NewBotResponseClient(httpClient)
	if err := client.SendMarkdown(context.Background(), "https://qyapi.weixin.qq.com/response?code=secret", "usage"); err != nil {
		t.Fatal(err)
	}
	if requestPayload["msgtype"] != "markdown" {
		t.Fatalf("payload = %#v", requestPayload)
	}
	markdown, ok := requestPayload["markdown"].(map[string]any)
	if !ok || markdown["content"] != "usage" {
		t.Fatalf("markdown = %#v", requestPayload["markdown"])
	}
}

func newTestBotHandler(t *testing.T, responder BotResponder, query BotQuery) (*BotHandler, *botCryptor) {
	t.Helper()
	handler, err := NewBotHandler(testBotToken, testBotAESKey, query, responder, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	cryptor, err := newBotCryptor(testBotToken, testBotAESKey)
	if err != nil {
		t.Fatal(err)
	}
	return handler, cryptor
}

func encryptedBotCallback(t *testing.T, cryptor *botCryptor, callback map[string]any) ([]byte, string) {
	t.Helper()
	plaintext, err := json.Marshal(callback)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cryptor.encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(encryptedCallback{Encrypted: encrypted})
	if err != nil {
		t.Fatal(err)
	}
	return body, signedBotURL(encrypted, cryptor, "1700000000", "nonce")
}

func signedBotURL(encrypted string, cryptor *botCryptor, timestamp, nonce string) string {
	query := make(url.Values)
	query.Set("msg_signature", cryptor.signature(timestamp, nonce, encrypted))
	query.Set("timestamp", timestamp)
	query.Set("nonce", nonce)
	if encrypted != "" {
		query.Set("echostr", encrypted)
	}
	return BotCallbackPath + "?" + query.Encode()
}
