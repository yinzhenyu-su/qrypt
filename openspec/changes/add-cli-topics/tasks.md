## 1. Topic source and dispatch

- [ ] 1.1 Add an embedded topic source under `internal/cli` and a custom `help` command that resolves a command path first and a topic second; verify `qrypt help fs get` still prints command help and `qrypt help config` prints the topic
- [ ] 1.2 List the available topics when `qrypt help` is run with no argument, and fail with the topic list for an unknown topic; verify both paths with tests

## 2. Documentation single source

- [ ] 2.1 Align `docs/for-user/cli.md` with the embedded topic source; verify the documentation renders the same topic text
- [ ] 2.2 Add a CI drift check that fails when the topic source and the docs disagree; verify it passes in sync and fails when a topic is edited on one side only

## 3. Integration

- [ ] 3.1 Add a test that walks from the top-level help to a topic; verify it passes
- [ ] 3.2 Run the project CI check and the layered test scripts; verify they pass
