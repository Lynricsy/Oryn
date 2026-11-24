package orbdrt

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// makePlanStubBin 构造一个最小 POSIX sh RPC 替身，模拟 oryn-core 在 open_session 恢复计划模式（B/high，
// 原基线 A/medium）与普通模式，用来对完整 Oryn initFrames 做冷重启回归。
func makePlanStubBin(t *testing.T, mode string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "plan-stub-omp.sh")
	script := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "--version" ]; then
    echo "omp 19.0.1"
    exit 0
  fi
done

MODE=%q
PLAN=false
MODEL="claude-sonnet-4-5"
THINK="low"

printf '{"type":"ready","protocolVersion":2}\n'

while IFS= read -r line; do
  [ -z "$line" ] && continue
  id=$(printf '%%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  typ=$(printf '%%s' "$line" | sed -n 's/.*"type":"\([^"]*\)".*/\1/p')
  case "$typ" in
    negotiate_protocol)
      printf '{"type":"response","id":"%%s","command":"negotiate_protocol","success":true,"data":{"protocolVersion":2}}\n' "$id"
      ;;
    open_session)
      if [ "$MODE" = "plan" ]; then
        PLAN=true
        MODEL="claude-opus-4-6"
        THINK="high"
      else
        PLAN=false
        MODEL="claude-sonnet-4-5"
      fi
      printf '{"type":"response","id":"%%s","command":"open_session","success":true,"data":{"cancelled":false,"resumed":true,"sessionId":"s-plan","sessionFile":"/tmp/s-plan.jsonl"}}\n' "$id"
      ;;
    set_thinking_level)
      lvl=$(printf '%%s' "$line" | sed -n 's/.*"level":"\([^"]*\)".*/\1/p')
      [ -n "$lvl" ] && THINK="$lvl"
      printf '{"type":"response","id":"%%s","command":"set_thinking_level","success":true,"data":{"thinkingLevel":"%%s","thinkingConfigured":"%%s"}}\n' "$id" "$THINK" "$THINK"
      ;;
    set_plan_mode)
      case "$line" in
        *'"enabled":false'*)
          PLAN=false
          MODEL="claude-sonnet-4-5"
          THINK="medium"
          ;;
        *'"enabled":true'*)
          PLAN=true
          MODEL="claude-opus-4-6"
          THINK="high"
          ;;
      esac
      printf '{"type":"response","id":"%%s","command":"set_plan_mode","success":true,"data":{"enabled":%%s}}\n' "$id" "$PLAN"
      ;;
    get_state)
      printf '{"type":"response","id":"%%s","command":"get_state","success":true,"data":{"planModeEnabled":%%s,"model":{"provider":"anthropic","id":"%%s"},"thinkingLevel":"%%s","isStreaming":false,"isSettled":true}}\n' "$id" "$PLAN" "$MODEL" "$THINK"
      ;;
    *)
      printf '{"type":"response","id":"%%s","command":"%%s","success":true,"data":{}}\n' "$id" "$typ"
      ;;
  esac
done
`, mode)
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	return bin
}

// 完整冷重启 initFrames（与 internal/threads/agent.go initFrames 一致）：
// 包含带常驻基线 A 的 open_session 与带库里非计划态 medium 的 set_thinking_level。
func fullColdRestartInitFrames(thread string) []apigen.OmpFrame {
	return []apigen.OmpFrame{
		{"type": "open_session", "sessionDir": "~/.oryn/threads/" + thread, "provider": "anthropic", "modelId": "claude-sonnet-4-5"},
		{"type": "set_event_filter", "events": nil, "messageUpdates": "delta"},
		{"type": "set_ask_dialog", "enabled": true, "form": true},
		{"type": "set_subagent_subscription", "level": "progress"},
		{"type": "terminal_configure", "interactiveCommands": true, "agentTools": true},
		{"type": "set_thinking_level", "level": "medium"},
	}
}

func queryStubState(t *testing.T, st *link.Stream, fr *frameReader, id string) (planMode bool, modelID, thinking string) {
	t.Helper()
	sendCmd(t, st, fmt.Sprintf(`{"type":"get_state","id":%q}`, id))
	frames := fr.frameWith(t, "get_state "+id, responseID(id))
	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			PlanModeEnabled bool `json:"planModeEnabled"`
			Model           struct {
				ID string `json:"id"`
			} `json:"model"`
			ThinkingLevel string `json:"thinkingLevel"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(frames[len(frames)-1], &resp))
	require.True(t, resp.Success)
	return resp.Data.PlanModeEnabled, resp.Data.Model.ID, resp.Data.ThinkingLevel
}

// 冷重启回归：完整 initFrames 含库里的 set_thinking_level(medium)，
// 启动器检测到 open_session 恢复进计划态时必须跳过该初始化帧，保持 B/high；
// 退出计划模式回 A/medium；后续用户手动 set_thinking_level 语义不受影响。
func TestColdRestartInitSkipsThinkingInPlanMode(t *testing.T) {
	cp := newFakeCP(t)
	startRuntime(t, cp, func(c *Config) {
		c.AgentBin = makePlanStubBin(t, "plan")
	})
	cp.ctlNow(t)

	const thread = "T-plan-cold-restart"
	var res apigen.LinkCtlAgentStartResult
	require.NoError(t, cp.call(t, "agent.start", apigen.LinkCtlAgentStartParams{
		ThreadID:   thread,
		SessionDir: "~/.oryn/threads/" + thread,
		InitFrames: fullColdRestartInitFrames(thread),
	}, &res))
	st := waitAgentStream(t, cp)
	fr := readFrames(st)
	cp.waitNotif(t, 0, "agent.status", statusIs(thread, "ready"))

	// 1. 完整冷启动 initFrames 跑完后：计划模式仍保持 B/high（未被 init 的 medium 覆盖）
	planOn, modelID, thinking := queryStubState(t, st, fr, "s1")
	assert.True(t, planOn)
	assert.Equal(t, "claude-opus-4-6", modelID)
	assert.Equal(t, "high", thinking, "冷启动初始化帧的 set_thinking_level(medium) 不得覆盖 Core 恢复的计划思考级别")

	// 2. 用户手动发 set_thinking_level 语义照常生效
	sendCmd(t, st, `{"type":"set_thinking_level","id":"u1","level":"low"}`)
	fr.frameWith(t, "manual set_thinking_level", responseID("u1"))
	planOn, modelID, thinking = queryStubState(t, st, fr, "s2")
	assert.True(t, planOn)
	assert.Equal(t, "claude-opus-4-6", modelID)
	assert.Equal(t, "low", thinking, "用户手动设置思考级别不受初始化跳过逻辑影响")

	// 3. 退出计划模式：恢复原基线 A/medium
	sendCmd(t, st, `{"type":"set_plan_mode","id":"e1","enabled":false}`)
	fr.frameWith(t, "exit plan mode", responseID("e1"))
	planOn, modelID, thinking = queryStubState(t, st, fr, "s3")
	assert.False(t, planOn)
	assert.Equal(t, "claude-sonnet-4-5", modelID)
	assert.Equal(t, "medium", thinking)
}

// 对照组：非计划态冷重启，完整 initFrames 的 set_thinking_level(medium) 照常执行。
func TestColdRestartInitAppliesThinkingOutsidePlanMode(t *testing.T) {
	cp := newFakeCP(t)
	startRuntime(t, cp, func(c *Config) {
		c.AgentBin = makePlanStubBin(t, "normal")
	})
	cp.ctlNow(t)

	const thread = "T-normal-cold-restart"
	var res apigen.LinkCtlAgentStartResult
	require.NoError(t, cp.call(t, "agent.start", apigen.LinkCtlAgentStartParams{
		ThreadID:   thread,
		SessionDir: "~/.oryn/threads/" + thread,
		InitFrames: fullColdRestartInitFrames(thread),
	}, &res))
	st := waitAgentStream(t, cp)
	fr := readFrames(st)
	cp.waitNotif(t, 0, "agent.status", statusIs(thread, "ready"))

	planOn, modelID, thinking := queryStubState(t, st, fr, "n1")
	assert.False(t, planOn)
	assert.Equal(t, "claude-sonnet-4-5", modelID)
	assert.Equal(t, "medium", thinking, "非计划态冷启动应照常应用初始化帧的 set_thinking_level")
}
