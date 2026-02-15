package upbit

import (
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func TestGenerateToken(t *testing.T) {
	c := NewClientWithKeys("test-access-key", "test-secret-key")

	tokenStr, err := c.generateToken()
	if err != nil {
		t.Fatalf("generateToken() error: %v", err)
	}

	if tokenStr == "" {
		t.Fatal("generateToken() returned empty string")
	}

	// Verify JWT structure (header.payload.signature)
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 JWT parts, got %d", len(parts))
	}

	// Parse and verify claims
	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			t.Fatalf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte("test-secret-key"), nil
	})
	if err != nil {
		t.Fatalf("jwt.Parse() error: %v", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatal("could not cast claims to MapClaims")
	}

	if claims["access_key"] != "test-access-key" {
		t.Errorf("expected access_key=test-access-key, got %v", claims["access_key"])
	}

	nonce, ok := claims["nonce"].(string)
	if !ok || nonce == "" {
		t.Error("expected non-empty nonce in claims")
	}
}

func TestGenerateTokenWithQuery(t *testing.T) {
	c := NewClientWithKeys("test-access-key", "test-secret-key")

	queryString := "market=KRW-BTC&side=bid&volume=0.01&price=50000000&ord_type=limit"
	tokenStr, err := c.generateTokenWithQuery(queryString)
	if err != nil {
		t.Fatalf("generateTokenWithQuery() error: %v", err)
	}

	if tokenStr == "" {
		t.Fatal("generateTokenWithQuery() returned empty string")
	}

	// Parse and verify claims include query_hash
	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
		return []byte("test-secret-key"), nil
	})
	if err != nil {
		t.Fatalf("jwt.Parse() error: %v", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatal("could not cast claims to MapClaims")
	}

	if claims["access_key"] != "test-access-key" {
		t.Errorf("expected access_key=test-access-key, got %v", claims["access_key"])
	}

	queryHash, ok := claims["query_hash"].(string)
	if !ok || queryHash == "" {
		t.Error("expected non-empty query_hash in claims")
	}

	alg, ok := claims["query_hash_alg"].(string)
	if !ok || alg != "SHA512" {
		t.Errorf("expected query_hash_alg=SHA512, got %v", alg)
	}
}

func TestMapOrderState(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"wait", "submitted"},
		{"watch", "submitted"},
		{"done", "filled"},
		{"cancel", "cancelled"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		got := mapOrderState(tt.input)
		if got != tt.expected {
			t.Errorf("mapOrderState(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestParseFloat(t *testing.T) {
	tests := []struct {
		input    string
		expected float64
	}{
		{"0", 0},
		{"100.5", 100.5},
		{"", 0},
		{"  42.0 ", 42.0},
		{"0.00000001", 0.00000001},
	}

	for _, tt := range tests {
		got := parseFloat(tt.input)
		if got != tt.expected {
			t.Errorf("parseFloat(%q) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}

func TestFormatVolume(t *testing.T) {
	tests := []struct {
		input    float64
		expected string
	}{
		{1.0, "1"},
		{0.001, "0.001"},
		{0.00000001, "0.00000001"},
		{100, "100"},
	}

	for _, tt := range tests {
		got := formatVolume(tt.input)
		if got != tt.expected {
			t.Errorf("formatVolume(%v) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestClientIsReady(t *testing.T) {
	// Empty keys
	c := NewClientWithKeys("", "")
	if c.IsReady() {
		t.Error("expected IsReady()=false with empty keys")
	}

	// With keys
	c = NewClientWithKeys("key", "secret")
	if !c.IsReady() {
		t.Error("expected IsReady()=true with keys set")
	}
}

func TestClientName(t *testing.T) {
	c := NewClient()
	if c.Name() != "upbit" {
		t.Errorf("expected Name()=upbit, got %s", c.Name())
	}
}
