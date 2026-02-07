package kis

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	BaseURL = "https://openapi.koreainvestment.com:9443"
)

// tokenCache 토큰 캐시 파일 구조
type tokenCache struct {
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
	AppKey      string    `json:"app_key"` // 다른 계정 구분용
}

// TokenManager OAuth 토큰 관리
type TokenManager struct {
	creds     Credentials
	client    *http.Client
	cacheFile string

	mu          sync.RWMutex
	accessToken string
	expiresAt   time.Time
}

// NewTokenManager 토큰 매니저 생성
func NewTokenManager(creds Credentials) *TokenManager {
	// 토큰 캐시 파일 경로 (AppKey별 분리)
	homeDir, _ := os.UserHomeDir()
	hash := sha256.Sum256([]byte(creds.AppKey))
	suffix := hex.EncodeToString(hash[:4])
	cacheFile := filepath.Join(homeDir, fmt.Sprintf(".kis_token_%s.json", suffix))

	tm := &TokenManager{
		creds:     creds,
		client:    &http.Client{Timeout: 10 * time.Second},
		cacheFile: cacheFile,
	}

	// 캐시된 토큰 로드 시도
	tm.loadCachedToken()

	return tm
}

// loadCachedToken 캐시된 토큰 로드
func (tm *TokenManager) loadCachedToken() {
	data, err := os.ReadFile(tm.cacheFile)
	if err != nil {
		return
	}

	var cache tokenCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return
	}

	// 같은 AppKey인지 확인
	if cache.AppKey != tm.creds.AppKey {
		return
	}

	// 만료 확인 (5분 여유)
	if time.Now().Add(5 * time.Minute).Before(cache.ExpiresAt) {
		tm.accessToken = cache.AccessToken
		tm.expiresAt = cache.ExpiresAt
		fmt.Printf("[KIS] Using cached token (expires: %s)\n", tm.expiresAt.Format("2006-01-02 15:04:05"))
	}
}

// saveCachedToken 토큰 캐시 저장
func (tm *TokenManager) saveCachedToken() error {
	cache := tokenCache{
		AccessToken: tm.accessToken,
		ExpiresAt:   tm.expiresAt,
		AppKey:      tm.creds.AppKey,
	}

	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cache: %w", err)
	}

	if err := os.WriteFile(tm.cacheFile, data, 0600); err != nil {
		return fmt.Errorf("write cache file %s: %w", tm.cacheFile, err)
	}

	return nil
}

// GetCacheFile 캐시 파일 경로 반환 (디버깅용)
func (tm *TokenManager) GetCacheFile() string {
	return tm.cacheFile
}

// GetToken 유효한 토큰 반환 (자동 갱신)
func (tm *TokenManager) GetToken(ctx context.Context) (string, error) {
	tm.mu.RLock()
	if tm.accessToken != "" && time.Now().Add(5*time.Minute).Before(tm.expiresAt) {
		token := tm.accessToken
		tm.mu.RUnlock()
		return token, nil
	}
	tm.mu.RUnlock()

	return tm.refreshToken(ctx)
}

// refreshToken 토큰 갱신
func (tm *TokenManager) refreshToken(ctx context.Context) (string, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Double-check
	if tm.accessToken != "" && time.Now().Add(5*time.Minute).Before(tm.expiresAt) {
		return tm.accessToken, nil
	}

	url := BaseURL + "/oauth2/tokenP"
	reqBody := tokenRequest{
		GrantType: "client_credentials",
		AppKey:    tm.creds.AppKey,
		AppSecret: tm.creds.AppSecret,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := tm.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token request failed: %d - %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("empty access token in response: %s", string(body))
	}

	tm.accessToken = tokenResp.AccessToken
	tm.expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	// 토큰 캐시 저장
	if err := tm.saveCachedToken(); err != nil {
		fmt.Fprintf(os.Stderr, "[KIS] Warning: failed to cache token: %v\n", err)
	} else {
		fmt.Printf("[KIS] Token cached to %s (expires: %s)\n", tm.cacheFile, tm.expiresAt.Format("2006-01-02 15:04:05"))
	}

	return tm.accessToken, nil
}

// Invalidate 토큰 무효화 (재발급 강제)
func (tm *TokenManager) Invalidate() {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.accessToken = ""
	tm.expiresAt = time.Time{}
}
