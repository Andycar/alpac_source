package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type clientConfig struct {
	BaseURL   string
	Session   string
	UserAgent string
	Timeout   time.Duration
	Retries   int
	Verbose   bool
}

type Client struct {
	cfg  clientConfig
	http *http.Client
	rng  *rand.Rand
}

func newClient(cfg clientConfig) *Client {
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.Timeout},
		rng:  rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Call делает один JsHttpRequest POST к /api.php.
//
// Формат: POST /api.php?format=ajax&JsHttpRequest={ts}-xml
// Body: action[0]=<method>&<extraParams>
// Cookie: PHPSESSID=...
//
// Возвращает HTTP-статус и сырое тело ответа (cp1251 → utf-8 декодирование
// не делаем здесь, поскольку API должен отдавать JSON в utf-8; если придёт
// cp1251 — нормализатор разберётся).
func (c *Client) Call(method string, params url.Values) (status int, body []byte, err error) {
	jhrID := c.jhrID()
	endpoint := fmt.Sprintf("%s/api.php?format=ajax&JsHttpRequest=%s", c.cfg.BaseURL, jhrID)

	payload := url.Values{}
	payload.Set("action[0]", method)
	for k, vs := range params {
		for _, v := range vs {
			payload.Add(k, v)
		}
	}

	for attempt := 0; attempt <= c.cfg.Retries; attempt++ {
		req, rerr := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(payload.Encode()))
		if rerr != nil {
			return 0, nil, rerr
		}
		req.Header.Set("User-Agent", c.cfg.UserAgent)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Accept-Language", "ru,en;q=0.9")
		req.Header.Set("Origin", c.cfg.BaseURL)
		req.Header.Set("Referer", c.cfg.BaseURL+"/")
		req.Header.Set("X-Requested-With", "JsHttpRequest")
		req.Header.Set("Cookie", "PHPSESSID="+c.cfg.Session)

		resp, derr := c.http.Do(req)
		if derr != nil {
			if attempt < c.cfg.Retries {
				c.backoff(attempt)
				continue
			}
			return 0, nil, derr
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= 500 && attempt < c.cfg.Retries {
			if c.cfg.Verbose {
				log.Printf("HTTP %d на %s — retry %d/%d", resp.StatusCode, method, attempt+1, c.cfg.Retries)
			}
			c.backoff(attempt)
			continue
		}
		return resp.StatusCode, b, nil
	}
	return 0, nil, fmt.Errorf("exhausted retries")
}

func (c *Client) jhrID() string {
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	rnd := strconv.Itoa(1000 + c.rng.Intn(9000))
	return ts + rnd + "-xml"
}

func (c *Client) backoff(attempt int) {
	d := time.Duration(1<<attempt) * 500 * time.Millisecond
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	time.Sleep(d)
}

// stripJHRWrapper отбрасывает JsHttpRequest wrapper (он ставит спереди
// "<script>...</script>" или подобный header чтобы XHR с разных origin
// мог проглотить). Если wrapper отсутствует — возвращает as-is.
func stripJHRWrapper(b []byte) []byte {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return b
	}
	// JsHttpRequest часто оборачивает в HTML script tag для cross-domain.
	// Структура: <script>parent.JsHttpRequest.____ts____({"..."}, true)</script>
	// или просто JSON.
	if bytes.HasPrefix(b, []byte("<script>")) {
		start := bytes.IndexByte(b, '(')
		end := bytes.LastIndexByte(b, ')')
		if start > 0 && end > start {
			inner := b[start+1 : end]
			if comma := bytes.LastIndexByte(inner, ','); comma > 0 {
				return bytes.TrimSpace(inner[:comma])
			}
			return bytes.TrimSpace(inner)
		}
	}
	return b
}
