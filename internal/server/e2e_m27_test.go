package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/browserrelay"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/orbdrt"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// TestE2EM27 端到端验证 M27 客户端浏览器中继（含真实 orynd + 真实 orbd + 真实 omp browser-relay 进程，
// 以及 Rev 1 要求的「第 30s 才批准 createTab」真实链路门禁）。
func TestE2EM27(t *testing.T) {
	realOmp, err := filepath.Abs("../../build/core/x64/omp")
	if err != nil {
		t.Fatalf("resolve real omp path: %v", err)
	}
	if _, statErr := os.Stat(realOmp); statErr != nil {
		t.Skipf("real Oryn Core binary not present at %s: %v", realOmp, statErr)
	}

	s := newStack(t, config.Idle{})
	ctx := context.Background()

	// 准备一条运行真实 orbd（指向真实 build/core/x64/omp）的工作线程与 orb
	orbToken := "oot_m27_e2e_token_0123456789abcdef"
	require.NoError(t, s.srv.Store.Q.CreateProvider(ctx, db.CreateProviderParams{
		ID: "prv_m27", Name: "m27", TokenHash: "hp_m27", CreatedAt: 1,
	}))
	require.NoError(t, s.srv.Store.Q.CreateOrb(ctx, db.CreateOrbParams{
		ID: "orb_m27", ProviderID: "prv_m27", Type: "default", Cpus: 2, MemoryMb: 1024, Image: "img",
		State: orbs.StateReady, StateChangedAt: 1, TokenHash: hashToken(orbToken), CreatedAt: 1,
	}))
	orbID := "orb_m27"
	require.NoError(t, s.srv.Store.Q.CreateThread(ctx, db.CreateThreadParams{
		Kind: "work", ID: "T-01932f6e-8d1a-7c3b-9f2e-000000000027", OrbID: &orbID,
		Title: "M27 本机浏览器测试", TitleSource: "auto", State: "idle", SessionDir: "/tmp/m27-sess",
		CreatedAt: 1, UpdatedAt: 1,
	}))
	const threadID = "T-01932f6e-8d1a-7c3b-9f2e-000000000027"

	orbdHome := t.TempDir()
	rt, err := orbdrt.New(orbdrt.Config{
		URL:                   s.http.URL,
		OrbID:                 orbID,
		Token:                 orbToken,
		Home:                  orbdHome,
		AgentBin:              realOmp,
		EgressAddr:            "127.0.0.1:0",
		SSHAddr:               "127.0.0.1:0",
		BrowserRelayIdleGrace: 300 * time.Millisecond,
	})
	require.NoError(t, err)
	rtCtx, rtCancel := context.WithCancel(ctx)
	rtDone := make(chan struct{})
	go func() {
		defer close(rtDone)
		_ = rt.Run(rtCtx)
	}()
	t.Cleanup(func() {
		rtCancel()
		rt.Shutdown(2 * time.Second)
		<-rtDone
	})

	// 等 orbd 连上并上报 browser-relay 特性
	require.Eventually(t, func() bool {
		return s.srv.Orbs.Connected(orbID) && s.srv.Orbs.HasFeature(orbID, orbs.FeatureBrowserRelay)
	}, 10*time.Second, 30*time.Millisecond, "orbd 应连上控制面并声明 browser-relay 特性")

	// ---- M27-1：配对 browser_extension 设备并校验鉴权 ----
	var pairStart apigen.DevicePairStartResponse
	postJSONNoAuth(t, s.http.URL+"/api/v1/devices/pair/start", apigen.DevicePairStartRequest{
		Name: "Chrome M27", Kind: apigen.BrowserExtension, Platform: "chrome",
	}, &pairStart, http.StatusOK)
	require.NotEmpty(t, pairStart.Code)
	require.NotEmpty(t, pairStart.Verifier)

	// 模拟所有者在网页批准配对
	dev, err := auth.ApprovePairing(ctx, s.srv.Store, pairStart.Code)
	require.NoError(t, err)
	assert.Equal(t, string(apigen.BrowserExtension), dev.Kind)

	var pairPoll apigen.DevicePairPollResponse
	postJSONNoAuth(t, s.http.URL+"/api/v1/devices/pair/poll", apigen.DevicePairPollRequest{
		Code: pairStart.Code, Verifier: pairStart.Verifier,
	}, &pairPoll, http.StatusOK)
	require.True(t, strings.HasPrefix(pairPoll.DeviceToken, "odt_"))
	// 普通 CLI 设备换来的票据不能连 /api/v1/browser-relay/ws（应回 403）
	var cliTicket apigen.WsTicket
	s.rest("POST", "/api/v1/ws-ticket", nil, &cliTicket, http.StatusOK)
	wsBase := "ws" + strings.TrimPrefix(s.http.URL, "http") + browserrelay.WSPath
	_, cliResp, cliErr := websocket.Dial(ctx, wsBase+"?ticket="+cliTicket.Ticket, nil)
	require.Error(t, cliErr)
	require.NotNil(t, cliResp)
	assert.Equal(t, http.StatusForbidden, cliResp.StatusCode)

	// 用 browser_extension 令牌换票据并连上 /api/v1/browser-relay/ws
	extTicket := issueWSTicketWithBearer(t, s.http.URL, pairPoll.DeviceToken)
	extWS, _, err := websocket.Dial(ctx, wsBase+"?ticket="+extTicket, nil)
	require.NoError(t, err)
	defer extWS.CloseNow()
	extWS.SetReadLimit(64 << 20)

	sendExt := func(v any) {
		b, mErr := json.Marshal(v)
		require.NoError(t, mErr)
		wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
		defer wcancel()
		require.NoError(t, extWS.Write(wctx, websocket.MessageText, b))
	}

	sendExt(map[string]any{
		"t":              "register",
		"instanceId":     "inst_m27",
		"browserSession": "bsess_m27",
		"browserVersion": "Chrome/136.0.0.0",
		"extVersion":     "0.2.0",
	})

	// ---- M27-4：验证绑定前线程前门（通过 agent.start 写出 browser-relay.yml）与 503 快速失败 ----
	// 直接绑定前先检查在线客户端列表
	require.Eventually(t, func() bool {
		var list apigen.BrowserRelayClientList
		s.rest("GET", "/api/v1/browser-relay/clients", nil, &list, http.StatusOK)
		return len(list.Clients) == 1 && list.Clients[0].DeviceID == dev.ID
	}, 5*time.Second, 30*time.Millisecond)

	// 构造运行本机策略语义的轻量模拟扩展处理器（验证 M27-2 与 M27-3 第 30s 才批准）
	var (
		extMu               sync.Mutex
		oneTimeCreateGrant  = map[string]bool{}
		pendingApprovalID   string
		createdTabCount     atomic.Int32
		removedCreatedCount atomic.Int32
		deniedUnauthorized  atomic.Bool
		relayFrontURLCh     = make(chan string, 1)
		approvalNotifiedCh  = make(chan struct{}, 1)
	)
	_ = relayFrontURLCh

	// 读取并驱动扩展消息循环
	extClosedCh := make(chan error, 1)
	go func() {
		for {
			_, raw, rerr := extWS.Read(ctx)
			if rerr != nil {
				extClosedCh <- rerr
				return
			}
			var msg struct {
				T        string          `json:"t"`
				ThreadID string          `json:"threadId"`
				Payload  json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(raw, &msg) != nil {
				continue
			}
			switch msg.T {
			case "session.open":
				// M27-2：本机有 tab 10（已共享）和 tab 20（未共享银行页），过滤后只发 tab 10！
				sendExt(map[string]any{
					"t":        "session.frame",
					"threadId": msg.ThreadID,
					"payload": map[string]any{
						"t":                     "hello",
						"instanceId":            "inst_m27",
						"browserSession":        "bsess_m27",
						"userAgent":             "Mozilla/5.0 Chrome/136.0.0.0",
						"browserVersion":        "Chrome/136.0.0.0",
						"discardedTabsProtocol": 1,
						"ompVersion":            "19.0.1",
						"tabs": []map[string]any{
							{
								"tabId": 10, "url": "https://app.example.com/home", "title": "Shared App",
								"active": true, "discarded": false, "windowId": 1, "pinned": false, "groupId": -1,
							},
						},
						"attachedTabIds": []int{},
					},
				})
				sendExt(map[string]any{
					"t":                "session.state",
					"threadId":         msg.ThreadID,
					"sharedTabCount":   1,
					"pendingApprovals": 0,
				})
			case "session.frame":
				var rpc struct {
					T      string `json:"t"`
					ID     int    `json:"id"`
					Op     string `json:"op"`
					TabID  int    `json:"tabId"`
					URL    string `json:"url"`
					Method string `json:"method"`
				}
				if json.Unmarshal(msg.Payload, &rpc) != nil || rpc.T != "rpc" {
					continue
				}
				switch rpc.Op {
				case "createTab":
					extMu.Lock()
					granted := oneTimeCreateGrant[rpc.URL]
					if granted {
						delete(oneTimeCreateGrant, rpc.URL)
					}
					extMu.Unlock()
					if !granted {
						// 非阻塞：立即回 ORYN_APPROVAL_REQUIRED，绝不挂起超过 bridge.ts 20s 超时
						extMu.Lock()
						pendingApprovalID = "apr_m27_1"
						extMu.Unlock()
						sendExt(map[string]any{
							"t":        "session.frame",
							"threadId": msg.ThreadID,
							"payload": map[string]any{
								"t":     "rpcResult",
								"id":    rpc.ID,
								"ok":    false,
								"error": "ORYN_APPROVAL_REQUIRED(apr_m27_1): 等待用户在 Chrome 扩展中确认新建标签页",
							},
						})
						sendExt(map[string]any{
							"t":                "session.state",
							"threadId":         msg.ThreadID,
							"sharedTabCount":   1,
							"pendingApprovals": 1,
						})
						select {
						case approvalNotifiedCh <- struct{}{}:
						default:
						}
					} else {
						n := createdTabCount.Add(1)
						newTabID := 30 + int(n)
						sendExt(map[string]any{
							"t":        "session.frame",
							"threadId": msg.ThreadID,
							"payload": map[string]any{
								"t":  "rpcResult",
								"id": rpc.ID,
								"ok": true,
								"result": map[string]any{
									"tab": map[string]any{
										"tabId": newTabID, "url": rpc.URL, "title": "New Page",
										"active": false, "discarded": false, "windowId": 1, "pinned": false, "groupId": -1,
									},
								},
							},
						})
					}
				case "removeTab":
					if rpc.TabID >= 30 {
						removedCreatedCount.Add(1)
					}
					sendExt(map[string]any{
						"t":        "session.frame",
						"threadId": msg.ThreadID,
						"payload":  map[string]any{"t": "rpcResult", "id": rpc.ID, "ok": true, "result": map[string]any{}},
					})
				case "attach":
					if rpc.TabID == 20 {
						deniedUnauthorized.Store(true)
						sendExt(map[string]any{
							"t":        "session.frame",
							"threadId": msg.ThreadID,
							"payload":  map[string]any{"t": "rpcResult", "id": rpc.ID, "ok": false, "error": "Tab 20 is not shared"},
						})
						continue
					}
					sendExt(map[string]any{
						"t":        "session.frame",
						"threadId": msg.ThreadID,
						"payload":  map[string]any{"t": "rpcResult", "id": rpc.ID, "ok": true, "result": map[string]any{}},
					})
				case "group", "ungroup", "detach", "activateTab":
					sendExt(map[string]any{
						"t":        "session.frame",
						"threadId": msg.ThreadID,
						"payload":  map[string]any{"t": "rpcResult", "id": rpc.ID, "ok": true, "result": map[string]any{}},
					})
				case "send":
					res := map[string]any{}
					if rpc.Method == "Target.getTargetInfo" {
						res = map[string]any{"targetInfo": map[string]any{"targetId": "t", "type": "page", "title": "Page", "url": "https://example.com"}}
					}
					sendExt(map[string]any{
						"t":        "session.frame",
						"threadId": msg.ThreadID,
						"payload":  map[string]any{"t": "rpcResult", "id": rpc.ID, "ok": true, "result": res},
					})
				}
			}
		}
	}()

	// 绑定线程 → 触发 orynd 打开 StreamBrowserRelay → orbd 懒启动真实 omp browser-relay！
	var boundState apigen.ThreadBrowserRelay
	s.rest("PUT", "/api/v1/threads/"+threadID+"/browser-relay", apigen.ThreadBrowserRelayBindRequest{
		DeviceID: dev.ID,
	}, &boundState, http.StatusOK)
	assert.True(t, boundState.Bound)
	assert.True(t, boundState.Online)

	// 读取 orbd 写出的线程级 browser-relay.yml，确认只写 browser.relayUrl（不写 browser.relay: true）并拿到前门地址
	overlayPath := filepath.Join(orbdHome, ".oryn", "threads", threadID, "browser-relay.yml")
	var relayURL string
	require.Eventually(t, func() bool {
		b, rerr := os.ReadFile(overlayPath)
		if rerr != nil {
			return false
		}
		txt := string(b)
		if strings.Contains(txt, "\n  relay:") {
			return false
		}
		for _, line := range strings.Split(txt, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "relayUrl:") {
				relayURL = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "relayUrl:")), `"`)
				return relayURL != ""
			}
		}
		return false
	}, 10*time.Second, 50*time.Millisecond, "应写出线程 browser-relay.yml")

	// ---- M27-2：等待真实 omp browser-relay 就绪并验证 /json/list 仅暴露已共享 Tab（tab 10）----
	var wsDebuggerURL string
	require.Eventually(t, func() bool {
		resp, gerr := http.Get(relayURL + "/json/version")
		if gerr != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var v map[string]any
		if json.NewDecoder(resp.Body).Decode(&v) != nil {
			return false
		}
		u, _ := v["webSocketDebuggerUrl"].(string)
		if u != "" {
			wsDebuggerURL = u
			return true
		}
		return false
	}, 15*time.Second, 100*time.Millisecond, "真实 omp browser-relay 应经 /ext hello 变为 ready(200)")

	listResp, err := http.Get(relayURL + "/json/list")
	require.NoError(t, err)
	listBody, err := io.ReadAll(listResp.Body)
	_ = listResp.Body.Close()
	require.NoError(t, err)
	assert.Contains(t, string(listBody), "https://app.example.com/home")
	assert.NotContains(t, string(listBody), "bank.secret.com", "未共享现有 Tab 绝不能暴露给 orb")

	// ---- M27-3：真实链路「第 30 秒才批准 createTab」门禁 ----
	cdpWS, _, err := websocket.Dial(ctx, wsDebuggerURL, nil)
	require.NoError(t, err)
	defer cdpWS.CloseNow()

	sendCDP := func(id int, method string, params map[string]any) map[string]any {
		reqBody, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
		require.NoError(t, cdpWS.Write(ctx, websocket.MessageText, reqBody))
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			rctx, rcancel := context.WithDeadline(ctx, deadline)
			_, b, rerr := cdpWS.Read(rctx)
			rcancel()
			require.NoError(t, rerr)
			var m map[string]any
			require.NoError(t, json.Unmarshal(b, &m))
			if gotID, ok := m["id"].(float64); ok && int(gotID) == id {
				return m
			}
		}
		t.Fatalf("CDP command %d (%s) timed out", id, method)
		return nil
	}

	// 第 0s：下游发起 Target.createTarget → 真实 bridge.ts 转 createTab → 扩展 <1s 立即返回 ORYN_APPROVAL_REQUIRED
	t0 := time.Now()
	firstRes := sendCDP(1, "Target.createTarget", map[string]any{"url": "https://example.com"})
	elapsedFirst := time.Since(t0)
	assert.Less(t, elapsedFirst, 5*time.Second, "首次未授权 createTab 必须立即返回而非阻塞 20s")
	errObj, ok := firstRes["error"].(map[string]any)
	require.True(t, ok, "首次未授权应返回 CDP 错误，实得: %v", firstRes)
	assert.Contains(t, errObj["message"], "ORYN_APPROVAL_REQUIRED(apr_m27_1)")
	assert.EqualValues(t, 0, createdTabCount.Load(), "拒绝/待审批时绝不提前建页")

	// 校验控制面 REST 接口实时看到 pendingApprovals == 1
	require.Eventually(t, func() bool {
		var cur apigen.ThreadBrowserRelay
		s.rest("GET", "/api/v1/threads/"+threadID+"/browser-relay", nil, &cur, http.StatusOK)
		return cur.PendingApprovals == 1 && cur.SharedTabCount == 1
	}, 3*time.Second, 30*time.Millisecond)

	// 刻意等待至第 30s（彻底跨过 bridge.ts:291 的 RPC_TIMEOUT_MS=20s 窗口）才由用户点「允许这次」
	waitApproval := 30 * time.Second
	if testing.Short() {
		waitApproval = 22 * time.Second
	}
	t.Logf("M27-3 门禁：刻意等待 %v（超过 bridge.ts 20s RPC 超时窗口）再模拟用户批准...", waitApproval)
	time.Sleep(waitApproval)

	// 用户在第 30s 点击「允许这次」：只写入单次放行票据并上报 session.approval_resolved，绝不事后执行过期旧 RPC
	extMu.Lock()
	oneTimeCreateGrant["https://example.com"] = true
	aprID := pendingApprovalID
	pendingApprovalID = ""
	extMu.Unlock()
	sendExt(map[string]any{
		"t":          "session.approval_resolved",
		"threadId":   threadID,
		"approvalId": aprID,
		"decision":   "approved_once",
		"summary":    "新建标签页 https://example.com",
	})
	sendExt(map[string]any{
		"t":                "session.state",
		"threadId":         threadID,
		"sharedTabCount":   1,
		"pendingApprovals": 0,
	})
	time.Sleep(300 * time.Millisecond)
	assert.EqualValues(t, 0, createdTabCount.Load(), "第 30s 批准瞬间绝不能事后幽灵执行过期旧 RPC")

	// 第 31s：agent 发起新一次重试 Target.createTarget → 命中单次放行票据，1s 内成功创建且绝不被 onLateResult 关掉！
	tRetry := time.Now()
	retryRes := sendCDP(2, "Target.createTarget", map[string]any{"url": "https://example.com"})
	assert.Less(t, time.Since(tRetry), 5*time.Second)
	resObj, ok := retryRes["result"].(map[string]any)
	require.True(t, ok, "第 30s 批准后的重试应成功创建目标，实得: %v", retryRes)
	assert.NotEmpty(t, resObj["targetId"])
	assert.EqualValues(t, 1, createdTabCount.Load())

	// 确认 bridge.ts 不会因迟到回调发 removeTab 关闭新建的标签页
	time.Sleep(1 * time.Second)
	assert.EqualValues(t, 0, removedCreatedCount.Load(), "批准后重试新建的 Tab 不应被 bridge.ts onLateResult 关闭")

	// 再次发起第三次 Target.createTarget → 单次票据已消费，应重新要求审批
	thirdRes := sendCDP(3, "Target.createTarget", map[string]any{"url": "https://example.com"})
	thirdErr, ok := thirdRes["error"].(map[string]any)
	require.True(t, ok, "单次票据消费后再次 createTab 应重新要求审批")
	assert.Contains(t, thirdErr["message"], "ORYN_APPROVAL_REQUIRED")

	// ---- M27-5：吊销设备 → 扩展 WS 收到 4401、绑定清空、前门恢复 503 ----
	s.rest("DELETE", "/api/v1/devices/"+dev.ID, nil, nil, http.StatusNoContent)
	select {
	case cerr := <-extClosedCh:
		assert.Equal(t, websocket.StatusCode(4401), websocket.CloseStatus(cerr), "吊销设备应以 4401 关闭扩展 WS")
	case <-time.After(5 * time.Second):
		t.Fatal("吊销设备后扩展 WS 未断开")
	}

	var afterRevoke apigen.ThreadBrowserRelay
	s.rest("GET", "/api/v1/threads/"+threadID+"/browser-relay", nil, &afterRevoke, http.StatusOK)
	assert.False(t, afterRevoke.Bound)
	assert.False(t, afterRevoke.Online)

	// orbd 空闲宽限期（300ms）后回收内部 omp browser-relay，前门恢复 503 快速失败
	require.Eventually(t, func() bool {
		resp, gerr := http.Get(relayURL + "/json/version")
		if gerr != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusServiceUnavailable
	}, 5*time.Second, 50*time.Millisecond, "设备吊销并回收后前门应恢复 503")
}

func hashToken(tok string) string {
	return ids.Hash(tok)
}

func postJSONNoAuth(t *testing.T, url string, body any, out any, wantStatus int) {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	resp, err := http.Post(url, "application/json", strings.NewReader(string(b)))
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, wantStatus, resp.StatusCode, "POST %s: %s", url, string(raw))
	if out != nil {
		require.NoError(t, json.Unmarshal(raw, out))
	}
}

func issueWSTicketWithBearer(t *testing.T, baseURL, bearer string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/ws-ticket", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var tk apigen.WsTicket
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tk))
	return tk.Ticket
}
