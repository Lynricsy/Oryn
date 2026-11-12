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
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "登录到 Oryn 控制面（设备配对，或直接写入设备令牌）",
	Long: `发起设备配对：打印网页批准地址与配对码，在已登录的浏览器里批准后自动取得设备令牌。
令牌写入 ~/.config/oryn/credentials.json（0600）。--token 直接写入已有的 odt_ 令牌。`,
	Args: cobra.NoArgs,
	RunE: runLogin,
}

var loginOpts struct {
	url   string
	token string
	name  string
}

func init() {
	f := loginCmd.Flags()
	f.StringVar(&loginOpts.url, "url", "", "控制面地址，如 https://oryn.example.com（必填）")
	f.StringVar(&loginOpts.token, "token", "", "直接使用已有的设备令牌 odt_…")
	f.StringVar(&loginOpts.name, "name", "", "设备名（默认取主机名）")
	_ = loginCmd.MarkFlagRequired("url")
	root.AddCommand(loginCmd)
}

func runLogin(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	base := strings.TrimSuffix(loginOpts.url, "/")
	if loginOpts.token != "" {
		return saveVerified(ctx, base, loginOpts.token)
	}
	name := loginOpts.name
	if name == "" {
		h, err := os.Hostname()
		if err != nil || h == "" {
			h = "oryn-cli"
		}
		name = h
	}
	anon := &apiClient{base: base, http: &http.Client{Timeout: 30 * time.Second}}
	var start apigen.DevicePairStartResponse
	if err := anon.do(ctx, http.MethodPost, "/api/v1/devices/pair/start", apigen.DevicePairStartRequest{
		Name: name, Kind: apigen.Cli, Platform: runtime.GOOS + "-" + runtime.GOARCH,
	}, &start); err != nil {
		return fmt.Errorf("发起配对: %w", err)
	}
	fmt.Fprintf(os.Stderr, "在已登录的浏览器中打开以下地址并批准配对：\n\n  %s\n\n配对码：%s（%s 前有效）\n等待批准…\n",
		start.VerifyURL, start.Code, time.UnixMilli(start.ExpiresAt).Local().Format("15:04:05"))
	interval := time.Duration(max(start.Interval, 1)) * time.Second
	deadline := time.UnixMilli(start.ExpiresAt)
	for {
		status, body, err := rawPost(ctx, anon, "/api/v1/devices/pair/poll", apigen.DevicePairPollRequest{Code: start.Code, Verifier: start.Verifier})
		if err != nil {
			return err
		}
		switch {
		case status == http.StatusOK:
			var res apigen.DevicePairPollResponse
			if err := json.Unmarshal(body, &res); err != nil {
				return fmt.Errorf("解析配对结果: %w", err)
			}
			if err := saveCredentials(credentials{URL: base, Token: res.DeviceToken, DeviceID: res.Device.ID}); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "已登录，设备 %s（%s）\n", res.Device.Name, res.Device.ID)
			return nil
		case status == http.StatusAccepted:
		default:
			e := &apiError{Status: status}
			if json.Unmarshal(body, e) != nil || e.Message == "" {
				e.Message = strings.TrimSpace(string(body))
			}
			return fmt.Errorf("配对失败: %w", e)
		}
		if time.Now().After(deadline) {
			return errors.New("配对码已过期，请重新运行 oryn login")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// saveVerified 用 GET me 校验令牌后写入凭据。
func saveVerified(ctx context.Context, base, token string) error {
	c := &apiClient{base: base, token: token, http: &http.Client{Timeout: 30 * time.Second}}
	var me apigen.Me
	if err := c.do(ctx, http.MethodGet, "/api/v1/me", nil, &me); err != nil {
		return fmt.Errorf("校验令牌: %w", err)
	}
	cred := credentials{URL: base, Token: token}
	if me.Auth.DeviceID != nil {
		cred.DeviceID = *me.Auth.DeviceID
	}
	if err := saveCredentials(cred); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "已登录", base)
	return nil
}

// rawPost 发送 JSON POST 并返回状态码与原始响应体（轮询需要区分 200 / 202）。
func rawPost(ctx context.Context, c *apiClient, path string, body any) (int, []byte, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(buf))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, err
}
