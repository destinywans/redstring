package v3admin

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// FeishuMFAReminderConfig is a dedicated, published Feishu bot application.
// It must have permission to send direct messages to every intended user.
type FeishuMFAReminderConfig struct {
	AppID     string
	AppSecret string
}

type FeishuAPIMFAReminderSender struct {
	appID     string
	appSecret string
	client    *http.Client
	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func NewFeishuAPIMFAReminderSender(config FeishuMFAReminderConfig) (*FeishuAPIMFAReminderSender, error) {
	if config.AppID == "" || config.AppSecret == "" ||
		strings.TrimSpace(config.AppID) != config.AppID ||
		strings.ContainsAny(config.AppID+config.AppSecret, "\r\n") {
		return nil, errMFAReminderUnavailable
	}
	return &FeishuAPIMFAReminderSender{
		appID: config.AppID, appSecret: config.AppSecret,
		client: &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}, nil
}

func (s *FeishuAPIMFAReminderSender) tenantAccessToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && time.Now().Before(s.expiresAt) {
		return s.token, nil
	}
	body, err := json.Marshal(map[string]string{"app_id": s.appID, "app_secret": s.appSecret})
	if err != nil {
		return "", errMFAReminderUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", errMFAReminderUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return "", errMFAReminderUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", errMFAReminderUnavailable
	}
	var response struct {
		Code   int    `json:"code"`
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 8192)).Decode(&response) != nil ||
		response.Code != 0 || response.Token == "" || response.Expire <= 60 ||
		strings.ContainsAny(response.Token, "\r\n") {
		return "", errMFAReminderUnavailable
	}
	s.token = response.Token
	s.expiresAt = time.Now().Add(time.Duration(response.Expire-60) * time.Second)
	return response.Token, nil
}

func (s *FeishuAPIMFAReminderSender) SendMFAReminder(ctx context.Context, message FeishuMFAReminder) (FeishuMFAReminderReceipt, error) {
	var empty FeishuMFAReminderReceipt
	// Store IDs are 128 random bits as hex. Convert them deterministically to
	// Feishu's documented UUID-shaped idempotency key.
	raw, parseErr := hex.DecodeString(message.DeliveryID)
	if s == nil || !feishuOpenIDPattern.MatchString(message.OpenID) ||
		message.Environment == "" || parseErr != nil || len(raw) != 16 {
		return empty, errMFAReminderUnavailable
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	uuid := fmt.Sprintf("%x-%x-%x-%x-%x", raw[:4], raw[4:6], raw[6:8], raw[8:10], raw[10:])
	token, err := s.tenantAccessToken(ctx)
	if err != nil {
		return empty, err
	}
	content, err := json.Marshal(map[string]string{"text": "AIOps Canvas: your account has not completed the required two-step verification. Please open the trusted product sign-in page and complete enrollment. Contact your administrator if you need help."})
	if err != nil {
		return empty, errMFAReminderUnavailable
	}
	requestBody, err := json.Marshal(map[string]string{
		"receive_id": message.OpenID, "msg_type": "text", "content": string(content),
		// The durable local attempt ID is carried to Feishu for correlation.
		"uuid": uuid,
	})
	if err != nil {
		return empty, errMFAReminderUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://open.feishu.cn/open-apis/im/v1/messages?receive_id_type=open_id", bytes.NewReader(requestBody))
	if err != nil {
		return empty, errMFAReminderUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Accept", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return empty, errMFAReminderUncertain
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return empty, errMFAReminderUncertain
	}
	var response struct {
		Code int `json:"code"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 8192)).Decode(&response) != nil ||
		response.Code != 0 || response.Data.MessageID == "" {
		return empty, errMFAReminderUncertain
	}
	return FeishuMFAReminderReceipt{MessageID: response.Data.MessageID}, nil
}
