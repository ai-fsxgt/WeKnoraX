# Repository Guidelines

## Project Structure & Module Organization

The backend entry is `cmd/server/`; core code is in `internal/`, configuration in `config/`, and migrations in `migrations/`. The CLI (`cli/`) and SDK (`client/`) are separate Go modules. Vue 3 code lives in `frontend/src/`, organized into components, composables, stores, views, and assets. Parsing code is in `docreader/`; deployment resources are in `docker/`, `helm/`, and the root Compose files. Put documentation in `docs/` or `website-docs/` and shared fixtures in `testdata/`.

## Build, Test, and Development Commands

Use Go 1.26, Node 24, npm, Make, and Docker with Compose. Run `cp .env.example .env`, then `make dev-start`; launch `make dev-app` and `make dev-frontend` in separate terminals. Stop services with `make dev-stop`.

- `make build`: compile the server to `./WeKnora`.
- `make test`: run tests for the root Go module.
- `make lint`: run Go lint checks.
- `make -C cli test`: test the CLI module.
- `cd client && go test ./...`: test the Go SDK.
- `cd frontend && npm ci && npm test`: install locked dependencies and test the frontend.
- `cd frontend && npm run type-check && npm run build`: validate and build the frontend.

## 私有构建机约束

在私有构建机（`self-hosted` Runner）上执行的 CI 工作流和构建脚本，只能使用构建机已提供的工具。禁止直接依赖未提供的 `jq`、`gh` 等命令；优先使用 Bash 内置能力或项目已有的 GitHub Actions 完成相应操作。

## Coding Style & Naming Conventions

Format Go files with `gofmt`; CI also checks `gofumpt`, `govet`, `revive`, and a 120-character line limit. Use `PascalCase` for exported Go identifiers, `camelCase` for unexported identifiers, and lowercase package names. In Vue/TypeScript, follow surrounding two-space formatting, name components `PascalCase.vue`, and composables `useSomething`. Keep component styles scoped and reuse existing components, stores, and utilities.

## Testing Guidelines

Name Go tests `*_test.go` and frontend tests `*.test.ts`. Keep tests beside their source. Run focused tests first, then the relevant module suite. There is no global coverage threshold, but changed behavior should be tested. Document infrastructure-dependent failures in the pull request.

When an approved behavior or permission contract changes, update the directly affected existing test assertions in the same change. Do not leave a known failing test or broaden production behavior merely to satisfy a stale expectation. This does not authorize unrelated test refactors or new test files.

## Commit & Pull Request Guidelines

Use Conventional Commits, optionally with a scope: `feat(chat): add source filter`, `fix: handle empty upload`, or `docs: clarify setup`. Keep commits focused. Pull requests must explain the change, link issues with `Fixes #123` when applicable, list validation commands, and note failures. Complete `.github/pull_request_template.md`, call out breaking changes, and include screenshots or recordings for UI changes.

## Browser and Desktop Automation

Do not use browser automation or desktop UI automation for access, operations, or debugging unless the user has explicitly approved it for the current task.

## Security & Configuration

Copy `.env.example` for local configuration and never commit `.env`, credentials, keys, or generated data. Report vulnerabilities privately through GitHub Security Advisories rather than public issues.

## PR 审查流程（Codex 自动审查）

PR 每次推送新提交后，Codex 会自动审查。用户要求“关注 PR 审查”时，按以下流程循环处理，**直到 Codex 在 PR 上给出 👍 反应才算可以合并**。

1. **等待结果**：用 `gh` 轮询 PR 的 reviews、行内评论和 Codex 汇总评论，不要凭空猜测结果。
2. **逐条判断**每条审查意见：
    - **需要修复**：确实存在 bug、会引发错误，或违反本文件中的规则。
    - **过度审查**：针对假设场景的防御性逻辑、与本次需求无关的重构或优化、个人风格偏好，或与本文件“禁止过度设计”的原则冲突。不修改，但要说明理由。
3. **处理**：
    - 有需要修复的意见：用最小改动修复，运行相关 lint / 类型检查，只提交相关文件，推送后会自动触发下一轮审查。
    - 全部是过度审查：不提交任何文件，在 PR 上评论 `@codex review` 手动触发下一轮审查。
4. **汇报**：每轮结束后向用户简要说明哪些意见修复了、哪些判定为过度审查，以及理由。
5. **结束**：出现 👍 后停止，告知用户可以合并。