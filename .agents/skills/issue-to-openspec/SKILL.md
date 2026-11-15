---
name: issue-to-openspec
description: Triage a GitHub issue, verify it against the code, turn it into an OpenSpec change, review the proposal for conflicts with existing specs and behavior, ask the user only for decisions they must make, and reply to the issue. Use whenever the user asks to investigate an issue, reproduce a bug report, draft an OpenSpec proposal from an issue, review a proposal for conflicts, or write an issue reply.
---

# Issue to OpenSpec

Turn an issue into a reviewed planning change, then reply to the issue only
when the user asks. This workflow stops at planning; implementation is a
separate apply step.

## Boundaries

- **Planning only.** Do not edit project code or start implementation. The
  request that starts this workflow authorizes planning artifacts only.
- **No outward actions without explicit instruction.** Do not post a comment,
  close/label an issue, commit, or push unless the user explicitly asks.
- **No secrets.** Issue replies, artifacts, and commit messages must not
  contain credentials, cookies, tokens, signed URLs, or unmasked logs.
- **Ask only what changes the plan.** Decide minor details yourself and record
  them as assumptions. Ask the user only for decisions that materially change
  scope, externally observable behavior, compatibility, or acceptance.

## Inputs

The user gives one of:

- an issue number (`#20`, `20`, or a URL);
- a description of a problem that should become an issue;
- a request to review an existing proposal for conflicts;
- a request to reply to an issue.

Derive a kebab-case change name from the issue (for example
`add-read-intent-policy`). If an active change already covers the topic, ask
whether to continue it or create a new one.

## Workflow

### 0. Ground yourself in the repository

Read the project instructions before touching anything:

- `AGENTS.md`
- `CONTEXT.md` (domain vocabulary)
- `docs/agents/issue-tracker.md`
- `docs/agents/domain.md`
- `openspec/config.yaml`

If the work touches a domain covered by a project skill, load it too:
`qrypt-driver-review`, `qrypt-cli-change`, `vfs-upload-recovery`, `qrypt-tests`.

Check the working tree and tooling:

```bash
git status --short
git branch --show-current
gh auth status
openspec context --json
```

If the user wants the work committed, remember the repo rule: run
`scripts/ci/check-all.sh` with the CI Go version before committing on `main`,
and format changed Go files with the module toolchain's `gofmt`. This workflow
does not commit by itself.

### 1. Inspect the issue

```bash
gh issue list --state open --limit 100
gh issue view <N> --json number,title,state,author,createdAt,labels,body,comments
```

Extract and write down:

- reproduction steps and environment/version;
- expected vs actual behavior;
- code references the reporter already found;
- the reporter's proposed fix, if any;
- whether it is a bug, feature request, duplicate, or a question.

Do not trust the reporter's root cause blindly; it is a hypothesis to verify.

### 2. Verify against the code

Read the referenced files and trace the actual call path. Use `rg` and the
Read tool, and cite `file:line` in your notes.

Check:

- Does the reported behavior still exist on the current branch?
- Is it intentional, documented, and covered by a spec?
- Is it a local symptom of a wider missing abstraction?
- Can it be reproduced with the smallest safe command or test?
  Use temp directories; never touch user data or real cloud accounts.

Classify the result:

| Result | What to do |
|---|---|
| Confirmed bug | Proceed to a change if behavior changes; otherwise propose a focused fix. |
| Expected behavior | Explain why, point to the spec/code, offer a workaround or a config/feature request path. |
| Feature request | Identify the capability and constraints; proceed to a change. |
| Duplicate | Link the canonical issue; do not create a second change. |
| Needs info | Ask for the missing reproduction detail; do not guess. |

If the fix is a pure refactor with no spec-level behavior change, say so and
skip the OpenSpec change (or use `skip_specs: true` only when justified).

### 3. Find the right capability

```bash
openspec list --specs
openspec show "<spec-id>" --type spec
```

Read every relevant spec **in full**, including scenarios. Do not rely on the
`--no-scenarios` summary when deciding whether behavior is already covered.

- Reuse an existing capability's exact path when the behavior belongs there.
- Introduce a new capability only for a genuinely new contract.
- Preserve the project's flat spec layout unless an existing domain level
  clearly applies.

### 4. Create the change and its artifacts

```bash
openspec new change "<kebab-name>"
openspec status --change "<kebab-name>" --json
```

Then, in dependency order, for each artifact:

```bash
openspec instructions <artifact-id> --change "<kebab-name>" --json
```

Read the returned `context`, `rules`, `template`, `instruction`,
`resolvedOutputPath`, and dependencies. Read dependency artifacts from disk
again before writing. Inspect the relevant code before drafting, so scope,
approach, and tasks are grounded in the repository rather than generic.

Write the artifacts:

- `proposal.md`: why, what changes, new/modified capabilities, impact.
- `specs/<capability>/spec.md`: behavioral delta (`ADDED`, `MODIFIED`,
  `REMOVED`, `RENAMED`). Every requirement needs at least one `#### Scenario`.
- `design.md`: how, decisions, alternatives, risks, migration. Create only
  when the design warrants it.
- `tasks.md`: dependency-ordered `- [ ]` tasks, each with a verification.

Use the OpenSpec CLI's template; do not invent a different artifact format.
Do not copy the returned `context`/`rules` blocks into the artifact.

After each artifact, re-run:

```bash
openspec status --change "<kebab-name>" --json
```

Create every artifact reachable from `applyRequires` through the `requires`
edges. A `tasks` artifact reading `done` does not mean its dependencies exist;
use the dependency edges, not the status alone.

Validate:

```bash
openspec validate "<kebab-name>" --type change --strict
```

### 5. Review the proposal for conflicts

This is the step that prevents a plausible-looking proposal from fighting the
existing system. Use `references/conflict-review.md` as the checklist.

For each finding, record:

| Field | Content |
|---|---|
| Severity | hard conflict / spec gap / semantic inconsistency / scope gap |
| Finding | one sentence |
| Evidence | spec path and `file:line` |
| Impact | what breaks or becomes ambiguous |
| Resolution | concrete change to the artifacts, or a decision needed |
| User decision? | yes/no |

Fix everything you can answer yourself directly in the artifacts. Do not
silently narrow, defer, or reinterpret a requirement to make it fit. If a
finding needs a product/scope decision, move it to step 6.

Re-run `openspec validate --strict` after every batch of edits.

### 6. Ask the user one decision at a time

Use `AskUserQuestion` for decisions that are genuinely the user's:

- defaults that change user-visible behavior;
- whether a scope item is in this change or a follow-up;
- compatibility promises;
- config surface (user-tunable vs driver-owned);
- whether to include adjacent paths (for example direct driver reads);
- cancellation/error accounting semantics.

Rules:

- One decision per question; at most four questions per call.
- Give 2–4 mutually exclusive options, with the recommended option first and
  a short trade-off in each description.
- Keep the question about the decision, not about "is the plan okay".
- After each round, update the artifacts, re-validate, and summarize what
  changed. Do not carry an unanswered decision into implementation.

Minor choices (naming, internal helper shape, test layout) are yours; record
them as assumptions instead of asking.

### 7. Reply to the issue

Only after the user explicitly asks to post. Read
`references/issue-reply-template.md` first.

Before posting:

- confirm the issue number and repository;
- confirm the reply does not promise a version or date unless the user gave one;
- make the workaround accurate (do not suggest an action that does not work);
- check for secrets and internal-only details.

Post with a quoted heredoc so backticks are not expanded:

```bash
gh issue comment <N> --body-file - <<'EOF'
...
EOF
```

Then verify:

```bash
gh issue view <N> --json comments
```

Do not close the issue, add labels, or link a PR unless asked.

### 8. Hand off

Report:

- issue classification and the evidence;
- change name and path;
- artifacts created and their status;
- conflicts found and how they were resolved;
- any decisions still open;
- the next step (usually `/opsx:apply` when the user is ready to implement).

If no change was warranted, say why and what the user should do instead.

## Quality gates

Before declaring the workflow complete:

```bash
openspec validate "<kebab-name>" --type change --strict
git diff --check
git status --short
```

- Every artifact required by `openspec status` exists.
- Every requirement has at least one `#### Scenario`.
- The proposal's capability list matches the spec files created.
- No project code was edited.
- No outward action happened without explicit permission.

## Common mistakes

- Replying before verifying the reporter's root cause.
- Promising a version or date the user did not authorize.
- Treating "can this be a config option?" as a requirement to add user
  configuration; stability-sensitive defaults may belong to the driver.
- Asking the user a dozen open-ended questions instead of the two or three
  decisions that actually change the plan.
- Writing a `MUST` that the current driver contract cannot express.
- Adding a new requirement without checking the existing capability's spec.
- Posting with an unquoted heredoc and letting the shell expand backticks.
- Marking a task complete when only the code exists but its verification does
  not.

## References

- `references/conflict-review.md` — conflict categories, checklist, and
  evidence format.
- `references/issue-reply-template.md` — reply structure and tone rules.
- `scripts/collect-issue-context.sh` — read-only issue/spec/change context
  collector.
