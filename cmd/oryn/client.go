package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// credentials 保存在 ~/.config/oryn/credentials.json（0600）。
type credentials struct {
	URL      string `json:"url"`
	Token    string `json:"token"`
	DeviceID string `json:"deviceId,omitempty"`
}

func credentialsPath() (string, error) {
	if p := os.Getenv("ORYN_CREDENTIALS"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "oryn", "credentials.json"), nil
}

func loadCredentials() (credentials, error) {
	var c credentials
	// orb 内或自动化场景可直接用环境变量
	if u, t := os.Getenv("ORYN_API_URL"), os.Getenv("ORYN_API_TOKEN"); u != "" && t != "" {
		return credentials{URL: u, Token: t}, nil
	}
	p, err := credentialsPath()
	if err != nil {
		return c, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, errors.New("尚未登录：先运行 `oryn login --url <服务器地址>`")
		}
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", p, err)
	}
	return c, nil
}

func saveCredentials(c credentials) error {
	p, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	// 只收紧默认的专用目录（~/.config/oryn）；ORYN_CREDENTIALS 指向的任意位置，其父目录可能是共享目录
	// （如 /tmp），不能改它的权限。凭据文件本身总是以 0600 写入。
	if os.Getenv("ORYN_CREDENTIALS") == "" {
		_ = os.Chmod(filepath.Dir(p), 0o700)
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	// 先写 0600 临时文件再改名：已有文件的宽松权限不会沿用
	tmp, err := os.CreateTemp(filepath.Dir(p), ".credentials-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// apiClient 是对控制面 REST API 的最小封装。
type apiClient struct {
	base  string
	token string
	http  *http.Client
}

// apiError 对应错误体 {code, message}。
type apiError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s (%d %s)", e.Message, e.Status, e.Code)
}

func newClient() (*apiClient, error) {
	c, err := loadCredentials()
	if err != nil {
		return nil, err
	}
	return &apiClient{base: strings.TrimSuffix(c.URL, "/"), token: c.Token, http: &http.Client{Timeout: 5 * time.Minute}}, nil
}

// do 发送请求；body 为 nil、[]byte/io.Reader（原样）或任意值（JSON 编码）；out 非 nil 时解码 JSON 响应。
func (c *apiClient) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case io.Reader:
		rd, ct = b, "application/octet-stream"
	case []byte:
		rd, ct = bytes.NewReader(b), "application/octet-stream"
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			return err
		}
		rd, ct = bytes.NewReader(buf), "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		e := &apiError{Status: resp.StatusCode}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if json.Unmarshal(b, e) != nil || e.Message == "" {
			e.Message = strings.TrimSpace(string(b))
		}
		return e
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// printJSON 以缩进 JSON 打印到 stdout。
func printJSON(v any) error { return printJSONTo(os.Stdout, v) }

// printJSONTo 以缩进 JSON 打印到 w。
func printJSONTo(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
