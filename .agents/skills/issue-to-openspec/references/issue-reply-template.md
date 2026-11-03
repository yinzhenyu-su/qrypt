# Issue reply template

Use this after verification and after the planning artifacts exist. Post only
when the user explicitly asks. Match the issue's language; qrypt issues are
usually Chinese.

## Structure

1. **Acknowledge and confirm** — thank the reporter for the reproduction or
   analysis, and say whether the report is confirmed, expected behavior, a
   feature request, or a duplicate.
2. **Root cause** — cite the real code path (`file:line`). Correct the
   reporter's hypothesis if it is only partly right. State whether the impact
   is bounded or unbounded.
3. **Fix direction** — what will change at the behavior/contract level. Explain
   why this shape was chosen, especially if the reporter suggested a narrower
   fix.
4. **Scope and compatibility** — what else is affected, what is explicitly not
   covered, and whether a user-visible default changes.
5. **Workaround** — only if accurate. Do not suggest an action that does not
   actually stop the problem.
6. **Status** — say what happens next (for example "implementation will update
   this issue"), but do not promise a version or date unless the user gave one.

## Chinese template

```markdown
感谢详细的复现和代码定位，确认这是真实问题，不是预期行为。

你定位的根因基本准确：<一句话说明实际调用链和 `file:line`>。因此
<可观察影响>，上限是 <有界数值/范围>，属于 <有界收敛/严重缺陷/预期行为>。

修复方向不是 <报告者建议的窄修复/新增配置>，而是 <实际方案>：<2–4 句说明
生命周期、所有权或契约层面的变化>。这样 <用户可观察的结果>，并且 <兼容性
或范围说明>。

你建议的 <报告者的最小修复> 方向是对的，但它只覆盖 <路径 A>，覆盖不了
<路径 B>；所以最终做成 <统一方案>。

<如果与另一个 issue/PR 相关：> 这块和 #N 是同一个 <子系统/改造> 的两面，
会一起处理。实现落地后我会在这个 issue 更新进展。

临时规避：<准确、可执行的规避方式；如果卸载挂载不能停止在途工作，要说明>。
```

## English template

```markdown
Thanks for the detailed reproduction and code pointers. Confirmed: this is a
real problem, not intended behavior.

The root cause is <call path and file:line>. The impact is <observable effect>,
bounded by <limit/range>.

The fix is not <narrow suggestion/config knob>; it is <actual direction>. This
makes <observable result> and keeps <compatibility/scope> intact.

Your suggested minimal fix is directionally right, but it only covers <path A>
and not <path B>, so we are doing <unified approach> instead.

This is the same <subsystem> work as #N and will be handled together. I will
update this issue when it lands.

Workaround: <accurate workaround>.
```

## For feature requests

- Confirm the use case and the current limitation.
- State the constraints (driver support, protocol, cache, compatibility).
- Describe the proposed behavior and what is explicitly out of scope.
- If the request needs a product decision, ask one focused question; do not
  promise a design in the same comment.

## For expected behavior

- Point to the spec or code that defines the behavior.
- Explain why it is intentional and what the user can do instead.
- If the behavior is surprising, treat that as a documentation gap and say you
  will improve the docs.
- Do not close the issue unless the user asks.

## For duplicates

- Link the canonical issue.
- Summarize what is already tracked there.
- Do not create a second change or promise separate work.

## Tone rules

- Be direct and technical; the reporter may have done real analysis.
- Acknowledge correct findings and correct wrong ones without dismissing them.
- Do not overclaim ("completely fixed", "no impact") unless verified.
- Do not promise versions or dates.
- Do not ask the reporter to fix the issue for you.
- Do not paste internal-only design documents, credentials, or customer data.
- If the fix changes a default, say so explicitly.
- If the workaround only reduces the impact, say that instead of implying it
  stops the problem.

## Before posting

- Confirm the issue number and repository.
- Re-read the issue and any existing comments; do not repeat a reply.
- Check the workaround against the current code.
- Check for secrets and internal-only details.
- Use a quoted heredoc so backticks and `$` are not expanded:

```bash
gh issue comment <N> --body-file - <<'EOF'
...
EOF
```

- Verify the posted comment with `gh issue view <N> --json comments`.
