<!--
移植自 can1357/oh-my-pi python/robomp，MIT 许可（Copyright (c) 2026 Stencil Labs, Inc.）。
来源：src/prompts/question_autoclose_suffix.md，以及 src/host_tools.py 中 gh_post_comment 的自动关闭排期语义。
robomp 由宿主在评论末尾自动追加后缀；Oryn 中由 agent 自行追加，并在发帖后调用 github_autoclose 排期；本注释不属于提示词正文。
-->
# Question auto-close

When the issue is classified `question` and your answer comment on #{{number}} is posted, the issue closes automatically unless its author objects.

1. Applies only to the answer comment on the originating issue #{{number}}, and only while it is not waiting for reporter details (`needs_info`) or already closed. Never on pull requests, never on other classes.
2. End the answer comment with exactly this suffix (after one blank line):

   ```
   ---
   If this didn't solve your issue, react 👎 on this comment and I'll keep it open.
   Otherwise I'll auto-close in {{autoclose_hours}} hours.
   ```

3. Immediately after posting, before any other comment on the issue, call `github_autoclose(number={{number}})`. Do NOT pass `hours`: Oryn applies the rule's configured window, which is the one stated in the suffix. It schedules the close against your most recent comment on the issue, so it must be the answer you just posted; it fails if the issue is already closed or you have not commented on it yet.
4. When the window ends, only a 👎 from the issue author on that answer comment keeps the issue open (other users' reactions do not count); otherwise it is closed as completed. The close is cancelled earlier by any new comment from someone other than you, by an external close, or when the task ends. Do not close the issue yourself.
