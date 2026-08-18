package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const baseURL = "https://api.kinobox.tv/api"

type authResp struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

type player struct {
	Type      string `json:"type"`
	IframeURL string `json:"iframeUrl"`
	Quality   string `json:"quality"`
	Source    string `json:"source"`
}

func main() {
	username := "KirillZ"
	password := "7S9swMJPbr17@!"
	kpID := "258687" // Interstellar

	if len(os.Args) > 1 {
		kpID = os.Args[1]
	}

	client := &http.Client{Timeout: 15 * time.Second}

	// 1. Login
	fmt.Println("=== LOGIN ===")
	body, _ := json.Marshal(map[string]string{
		"username": username,
		"password": password,
	})
	req, _ := http.NewRequest("POST", baseURL+"/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Login error:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	fmt.Printf("Status: %d\n", resp.StatusCode)
	fmt.Printf("Body: %s\n\n", string(respBody))

	var auth authResp
	if err := json.Unmarshal(respBody, &auth); err != nil || auth.AccessToken == "" {
		fmt.Println("Failed to parse auth response or empty token")

		// Try register
		fmt.Println("=== TRYING REGISTER ===")
		regBody, _ := json.Marshal(map[string]string{
			"username": username,
			"password": password,
		})
		req2, _ := http.NewRequest("POST", baseURL+"/auth/register", bytes.NewReader(regBody))
		req2.Header.Set("Content-Type", "application/json")
		req2.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36")
		resp2, err := client.Do(req2)
		if err != nil {
			fmt.Println("Register error:", err)
			os.Exit(1)
		}
		defer resp2.Body.Close()
		regResp, _ := io.ReadAll(resp2.Body)
		fmt.Printf("Register status: %d\n", resp2.StatusCode)
		fmt.Printf("Register body: %s\n\n", string(regResp))

		if err := json.Unmarshal(regResp, &auth); err != nil || auth.AccessToken == "" {
			fmt.Println("Failed to get token from register either, trying without auth...")
		}
	}

	// 2. Get players
	fmt.Printf("=== PLAYERS for KP %s ===\n", kpID)
	req3, _ := http.NewRequest("GET", baseURL+"/players?kinopoisk="+kpID, nil)
	req3.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36")
	if auth.AccessToken != "" {
		req3.Header.Set("Authorization", "Bearer "+auth.AccessToken)
		fmt.Println("Using auth token:", auth.AccessToken[:20]+"...")
	} else {
		fmt.Println("No auth token, trying without...")
	}

	resp3, err := client.Do(req3)
	if err != nil {
		fmt.Println("Players error:", err)
		os.Exit(1)
	}
	defer resp3.Body.Close()
	playersBody, _ := io.ReadAll(resp3.Body)
	fmt.Printf("Status: %d\n", resp3.StatusCode)

	var players []player
	if err := json.Unmarshal(playersBody, &players); err != nil {
		fmt.Printf("Raw body: %s\n", string(playersBody))
		fmt.Println("Parse error:", err)
	} else {
		fmt.Printf("Found %d players:\n", len(players))
		for i, p := range players {
			fmt.Printf("  [%d] type=%s quality=%s source=%s\n      url=%s\n", i, p.Type, p.Quality, p.Source, p.IframeURL)
		}
	}

	// 3. Also try /api/players without auth to compare
	if auth.AccessToken != "" {
		fmt.Printf("\n=== PLAYERS WITHOUT AUTH (comparison) ===\n")
		req4, _ := http.NewRequest("GET", baseURL+"/players?kinopoisk="+kpID, nil)
		req4.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36")
		resp4, err := client.Do(req4)
		if err != nil {
			fmt.Println("No-auth players error:", err)
		} else {
			defer resp4.Body.Close()
			noAuthBody, _ := io.ReadAll(resp4.Body)
			fmt.Printf("Status: %d\n", resp4.StatusCode)
			fmt.Printf("Body: %s\n", string(noAuthBody))
		}
	}

	// 4. Try /api/me
	if auth.AccessToken != "" {
		fmt.Println("\n=== /api/auth/me ===")
		req5, _ := http.NewRequest("GET", baseURL+"/auth/me", nil)
		req5.Header.Set("Authorization", "Bearer "+auth.AccessToken)
		req5.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36")
		resp5, err := client.Do(req5)
		if err != nil {
			fmt.Println("Me error:", err)
		} else {
			defer resp5.Body.Close()
			meBody, _ := io.ReadAll(resp5.Body)
			fmt.Printf("Status: %d\n", resp5.StatusCode)
			fmt.Printf("Body: %s\n", string(meBody))
		}
	}
}
