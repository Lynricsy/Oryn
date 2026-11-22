package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// `oryn follow add`（M26）：主人在工作线程里让 agent「跟进」某件要等的事时，agent 经 orbd 本地控制口转成
// ctl follow.add 登记一个公网跟进话题；控制面核对 --quote 是主人在本线程里的原话。

var followCmd = &cobra.Command{
	Use:   "follow",
	Short: "全局助手的跟进话题（orb 内）",
}

var followAddCmd = &cobra.Command{
	Use:   "add",
	Short: "登记一个由全局助手定期查公开信息的跟进话题",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		title, _ := cmd.Flags().GetString("title")
		question, _ := cmd.Flags().GetString("question")
		quote, _ := cmd.Flags().GetString("quote")
		kind, _ := cmd.Flags().GetString("kind")
		asJSON, _ := cmd.Flags().GetBool("json")
		res, err := followAdd(cmd.Context(), title, question, quote, kind)
		if err != nil {
			return err
		}
		if asJSON {
			return printJSONTo(cmd.OutOrStdout(), res)
		}
		fmt.Fprintln(cmd.OutOrStdout(), res.TopicID)
		return nil
	},
}

// followAdd 以调用方线程的身份登记跟进话题；kind 为空时由控制面取 question。
func followAdd(ctx context.Context, title, question, quote, kind string) (apigen.LinkCtlFollowAddResult, error) {
	if !inOrb() {
		return apigen.LinkCtlFollowAddResult{}, usageErr("oryn follow add only works inside an orb thread")
	}
	caller, err := callerThread()
	if err != nil {
		return apigen.LinkCtlFollowAddResult{}, err
	}
	for _, f := range []struct{ name, v string }{{"title", title}, {"question", question}, {"quote", quote}} {
		if strings.TrimSpace(f.v) == "" {
			return apigen.LinkCtlFollowAddResult{}, usageErr("--%s 不能为空", f.name)
		}
	}
	p := apigen.LinkCtlFollowAddParams{ThreadID: caller, Title: title, Question: question, Quote: quote}
	if kind != "" {
		k := apigen.ResearchTopicKind(kind)
		if !k.Valid() {
			return apigen.LinkCtlFollowAddResult{}, usageErr("--kind 只能是 question 或 interest")
		}
		p.Kind = &k
	}
	var out apigen.LinkCtlFollowAddResult
	if err := sockCall(ctx, orbsock.MethodFollowAdd, p, &out, nil); err != nil {
		return apigen.LinkCtlFollowAddResult{}, sockErr(err)
	}
	return out, nil
}

func init() {
	followAddCmd.Flags().String("title", "", "一句话标题")
	followAddCmd.Flags().String("question", "", "要查的公开信息")
	followAddCmd.Flags().String("quote", "", "主人在本线程里让你跟进的原话")
	followAddCmd.Flags().String("kind", "", "question（问题跟进，缺省）或 interest（长期关注）")
	followAddCmd.Flags().Bool("json", false, "以 JSON 输出结果")
	followCmd.AddCommand(followAddCmd)
	root.AddCommand(followCmd)
}
