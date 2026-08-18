package litesrc

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// flixcdnSolverURL is the Python Turnstile solver microservice endpoint.
const flixcdnSolverURL = "http://127.0.0.1:7880/solve"

// flixcdnSolverClient is a shared HTTP client for calling the solver.
var flixcdnSolverClient = &http.Client{Timeout: 60 * time.Second}

type flixcdnSolverRequest struct {
	PageURL     string // /show/<id> on the player host
	RefererURL  string // e.g. https://hdplayer.click/
	ID          int64  // payload id
	Translation string // translation id, optional
	Season      string // season number, optional (movies)
	Episode     string // episode number, optional (movies)
}

type flixcdnSolverResponse struct {
	FileString string `json:"file_string"`
	Error      string `json:"error"`
}

// flixcdnResolveViaBrowser calls the Python nodriver/uc solver to bypass Turnstile
// and POST /api/player/files for the actual stream descriptor.
func flixcdnResolveViaBrowser(ctx context.Context, req flixcdnSolverRequest) (string, bool) {
	log.Info().Str("url", req.PageURL).Str("t", req.Translation).
		Str("s", req.Season).Str("e", req.Episode).
		Msg("flixcdn: calling solver")

	q := url.Values{
		"url":     {req.PageURL},
		"referer": {req.RefererURL},
		"id":      {fmt.Sprintf("%d", req.ID)},
	}
	if req.Translation != "" {
		q.Set("translation", req.Translation)
	}
	if req.Season != "" {
		q.Set("season", req.Season)
	}
	if req.Episode != "" {
		q.Set("episode", req.Episode)
	}

	u := flixcdnSolverURL + "?" + q.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		log.Warn().Err(err).Msg("flixcdn: solver request build failed")
		return "", false
	}

	resp, err := flixcdnSolverClient.Do(httpReq)
	if err != nil {
		log.Warn().Err(err).Msg("flixcdn: solver request failed (is flixcdn_solver.py running?)")
		return "", false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Warn().Err(err).Msg("flixcdn: solver response read failed")
		return "", false
	}

	var result flixcdnSolverResponse
	if err := stdjson.Unmarshal(body, &result); err != nil {
		log.Warn().Err(err).Str("body", truncate(string(body), 200)).Msg("flixcdn: solver response parse failed")
		return "", false
	}

	if result.FileString == "" {
		log.Warn().Str("error", result.Error).Str("url", req.PageURL).Msg("flixcdn: solver returned empty file_string")
		return "", false
	}

	log.Info().Str("fileString", truncate(result.FileString, 100)).Msg("flixcdn: solver success")
	return result.FileString, true
}

// flixcdnExtractFileString extracts file_string from JSON response.
func flixcdnExtractFileString(body string) string {
	key := `"file_string":"`
	idx := strings.Index(body, key)
	if idx < 0 {
		return ""
	}
	start := idx + len(key)
	end := strings.Index(body[start:], `"`)
	if end < 0 {
		return ""
	}
	fs := body[start : start+end]
	fs = strings.ReplaceAll(fs, `\/`, `/`)
	return fs
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return fmt.Sprintf("%s...", s[:n])
}
