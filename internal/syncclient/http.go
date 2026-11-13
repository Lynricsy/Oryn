package syncclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// HTTPRemote 经控制面 REST（`POST orbs/{id}/sync/prepare`、`GET orbs/{id}/sync/bundle`）实现 Remote。
type HTTPRemote struct {
	// BaseURL 是控制面地址（不带 /api/v1）；Token 是设备令牌；OrbID 是线程所在 orb。
	BaseURL, Token, OrbID string
	// Client 为 nil 时用 http.DefaultClient（截止时间由 ctx 控制）。
	Client *http.Client
}

// APIError 是控制面的错误体 {code, message}。
type APIError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string { return fmt.Sprintf("%s (%d %s)", e.Message, e.Status, e.Code) }

// ErrorCode 实现 Coded。
func (e *APIError) ErrorCode() string { return e.Code }

func (h *HTTPRemote) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(h.BaseURL, "/")+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+h.Token)
	c := h.Client
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		e := &APIError{Status: resp.StatusCode}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if json.Unmarshal(b, e) != nil || e.Message == "" {
			e.Message = strings.TrimSpace(string(b))
		}
		return nil, e
	}
	return resp, nil
}

// Prepare 实现 Remote。
func (h *HTTPRemote) Prepare(ctx context.Context, req apigen.OrbSyncPrepareRequest, background bool) (apigen.OrbSyncState, error) {
	var st apigen.OrbSyncState
	body, err := json.Marshal(req)
	if err != nil {
		return st, err
	}
	path := "/api/v1/orbs/" + url.PathEscape(h.OrbID) + "/sync/prepare"
	if background {
		path += "?bg=1"
	}
	resp, err := h.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return st, err
	}
	defer resp.Body.Close()
	return st, json.NewDecoder(resp.Body).Decode(&st)
}

// Bundle 实现 Remote。
func (h *HTTPRemote) Bundle(ctx context.Context, id string, w io.Writer) error {
	resp, err := h.do(ctx, http.MethodGet, "/api/v1/orbs/"+url.PathEscape(h.OrbID)+"/sync/bundle?id="+url.QueryEscape(id), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(w, resp.Body)
	return err
}
