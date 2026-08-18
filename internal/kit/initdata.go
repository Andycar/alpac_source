package kit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// TelegramUser represents the user extracted from Telegram WebApp initData.
type TelegramUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
	PhotoURL  string `json:"photo_url"`
}

// ValidateInitData validates the Telegram WebApp initData string using the bot token.
// Returns the authenticated user on success.
//
// Validation follows: https://core.telegram.org/bots/webapps#validating-data-received-via-the-mini-app
// 1. Parse initData as query string
// 2. Sort all key=value pairs (except "hash") alphabetically by key
// 3. Join them with "\n" to form data_check_string
// 4. secret_key = HMAC-SHA256("WebAppData", bot_token)
// 5. Verify HMAC-SHA256(secret_key, data_check_string) == hash
func ValidateInitData(initData, botToken string, maxAge time.Duration) (*TelegramUser, error) {
	if initData == "" {
		return nil, fmt.Errorf("empty initData")
	}
	if botToken == "" {
		return nil, fmt.Errorf("empty bot token")
	}

	vals, err := url.ParseQuery(initData)
	if err != nil {
		return nil, fmt.Errorf("invalid initData format: %w", err)
	}

	hash := vals.Get("hash")
	if hash == "" {
		return nil, fmt.Errorf("missing hash in initData")
	}

	// Build data-check-string: sorted key=value pairs (excluding "hash"), joined by "\n".
	var pairs []string
	for k, vv := range vals {
		if k == "hash" {
			continue
		}
		for _, v := range vv {
			pairs = append(pairs, k+"="+v)
		}
	}
	sort.Strings(pairs)
	dataCheckString := strings.Join(pairs, "\n")

	// secret_key = HMAC-SHA256("WebAppData", bot_token)
	secretKey := hmacSHA256([]byte("WebAppData"), []byte(botToken))

	// expected_hash = HMAC-SHA256(secret_key, data_check_string)
	expectedHash := hex.EncodeToString(hmacSHA256(secretKey, []byte(dataCheckString)))

	if !hmac.Equal([]byte(expectedHash), []byte(hash)) {
		return nil, fmt.Errorf("invalid hash")
	}

	// Check auth_date freshness if maxAge > 0.
	if maxAge > 0 {
		authDateStr := vals.Get("auth_date")
		if authDateStr != "" {
			var authDate int64
			if _, err := fmt.Sscanf(authDateStr, "%d", &authDate); err == nil {
				if time.Since(time.Unix(authDate, 0)) > maxAge {
					return nil, fmt.Errorf("initData expired (auth_date too old)")
				}
			}
		}
	}

	// Parse user field.
	userJSON := vals.Get("user")
	if userJSON == "" {
		return nil, fmt.Errorf("missing user in initData")
	}

	var user TelegramUser
	if err := json.Unmarshal([]byte(userJSON), &user); err != nil {
		return nil, fmt.Errorf("invalid user JSON: %w", err)
	}
	if user.ID == 0 {
		return nil, fmt.Errorf("user ID is zero")
	}

	return &user, nil
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}
