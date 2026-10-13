# 项目开发协作指令

目标：在动手前找全受影响的调用链和契约，用最小的完整改动解决问题，减少审查返工。不要把“改动行数少”“编译通过”当作功能正确的证据。

本文件适用于整个仓库。进入子目录时还要读取该目录的 `AGENTS.md`，目前 CLI 有独立的 [cli/AGENTS.md](cli/AGENTS.md)。用户当前任务中的明确要求优先；本文中的路径用于定位，具体行为以当前实现、调用方和已确认的需求为准。

## 1. 工作方式与修改边界

- 始终使用简体中文，结论优先，每次给出一个最佳方案。说明具体行为、影响和验证结果，不堆砌术语。
- 开始先检查 `git status --short`，区分已有改动与本次改动，不覆盖、回滚或顺手整理他人的工作。
- 编写代码前，先阅读相关实现、调用方、配置、现有测试；简要说明方案、修改范围、涉及文件、关键变化和验证方式，获得用户批准后再修改。已批准范围内的实现细节不重复确认；影响行为的歧义必须先澄清。
- 优先使用 `rg` 查找符号及引用，按本次需求追踪调用链。无需为一个小改动通读全仓，但不能只看报错行、当前组件或单个处理函数。
- 只实现当前需求。优先复用职责一致的组件、接口和工具；不要为了少写几行制造抽象，也不要复制现有业务判断。
- 不顺手重构、全仓格式化、升级依赖，或新增无关功能、配置、文档、框架。接口变化所必需的调用方修改属于同一项改动，应在方案中说清。
- 未明确要求时，不增加兼容层、旧字段别名、双路径、自动回退或额外重试。已有 PostgreSQL/SQLite、Redis/无 Redis 等受支持实现是当前契约，不能当作“旧逻辑”删除。
- 不吞掉错误、不把失败伪装成空结果或成功、不用默认值掩盖无效输入。需要区分的状态应沿调用链保留到使用方。
- 默认不新增测试文件、用例、Mock 或测试依赖。经批准的行为或权限契约改变时，同步调整直接受影响的现有测试断言；不扩展为无关测试重构，不为保留过时断言而放宽业务行为。
- 未获本次任务明确许可，不使用浏览器或桌面自动化。不得输出或提交凭据、`.env`、密钥和运行数据；发现安全漏洞遵循 [SECURITY.md](SECURITY.md)，不在公开 Issue 中披露。
- 不自动提交、推送或创建 PR。用户要求提交时使用 `<类型>: <简明中文描述>`，只处理授权范围内的变更。要求创建 PR 时按用户格式及 [.github/pull_request_template.md](.github/pull_request_template.md) 说明实际变更、验证和注意事项，不虚构关联事项或验证证据。

## 2. 项目地图：先找对修改位置

WeKnora 是带知识库、检索问答和 Agent 能力的多空间应用。后端使用 Go、Gin、GORM、dig；前端使用 Vue 3、TypeScript、Pinia、TDesign、vue-i18n。文档解析同时涉及 Go 解析器和 Python gRPC 服务。

| 修改主题 | 首先查看的位置 | 职责与边界 |
| --- | --- | --- |
| 启动与依赖 | [cmd/server](cmd/server/)、[cmd/desktop](cmd/desktop/)、[internal/container](internal/container/) | 服务端与桌面入口；`container.go` 装配服务、仓储、任务和外部依赖 |
| HTTP 接口与权限 | [internal/router](internal/router/)、[internal/middleware](internal/middleware/)、[internal/handler](internal/handler/) | 路由注册、认证授权、参数与响应；路由按 `routes_*.go` 拆分 |
| 业务与持久化 | [internal/application/service](internal/application/service/)、[internal/application/repository](internal/application/repository/)、[internal/types/interfaces](internal/types/interfaces/) | Service 组织业务，Repository 管理持久化，接口连接依赖；不要在普通 Handler 中另写业务或 SQL |
| 实体与接口字段 | [internal/types](internal/types/)、[internal/handler/dto](internal/handler/dto/) | 实体、任务负载和共享类型；DTO 承担对外字段筛选及凭据脱敏 |
| 问答与 Agent | [internal/handler/session](internal/handler/session/)、[chat_pipeline](internal/application/service/chat_pipeline/)、[internal/agent](internal/agent/)、[internal/models](internal/models/) | HTTP 问答入口、普通检索问答流水线、Agent 执行与工具、模型调用 |
| 文档处理 | [knowledge_process.go](internal/application/service/knowledge_process.go)、[knowledge_post_process.go](internal/application/service/knowledge_post_process.go)、[docparser](internal/infrastructure/docparser/)、[chunker](internal/infrastructure/chunker/)、[docreader](docreader/) | 解析、分块、索引、后处理；上传、重解析、数据源导入及临时附件有不同入口 |
| 检索后端 | [service/retriever](internal/application/service/retriever/)、[repository/retriever](internal/application/repository/retriever/)、[engine_factory.go](internal/container/engine_factory.go) | 检索编排、不同引擎的实现与装配，不能把一个引擎的能力当作全部实现的能力 |
| 异步任务 | [task.go](internal/router/task.go)、[sync_task.go](internal/router/sync_task.go)、[任务接口](internal/types/interfaces/task_queue.go) | Asynq 工作池、无 Redis 执行器、数据库待处理队列和死信记录 |
| 外部接入 | [internal/datasource](internal/datasource/)、[internal/im](internal/im/)、[internal/mcp](internal/mcp/)、[embed_channel.go](internal/handler/embed_channel.go) | 数据源连接器、IM 平台适配、MCP、嵌入式问答；复用核心业务，保留各入口身份边界 |
| 前端 | [frontend/src](frontend/src/) 下的 `views`、`components`、`api`、`stores`、`composables`、`utils` | 页面交互、公共组件、接口封装、共享状态、组合逻辑与工具 |
| 其他客户端 | [client](client/)、[cli](cli/)、[mcp-server](mcp-server/)、[packages/dsh-weknora](packages/dsh-weknora/)、[miniprogram](miniprogram/) | SDK、CLI 和外部消费方；HTTP、SSE 或字段变化时搜索实际受影响的客户端 |
| 配置、迁移与交付 | [internal/config](internal/config/)、[config](config/)、[migrations](migrations/)、[scripts](scripts/)、[.github/workflows](.github/workflows/) | 配置加载、数据库演进、开发脚本及真实 CI 检查；部署还涉及 Compose、Docker、Helm |

典型接口链路：前端 `api` → 路由与中间件 → Handler → Service → Repository/外部能力 → DTO → 前端状态。IM、后台任务和工具也会直接调用 Service，因此 HTTP 层的检查不能代替业务入口所需的权限和状态检查。

## 3. 动手前建立本次改动的契约

先用代码回答以下问题，再给方案；只检查本次相关的分支，不扩大需求：

1. **行为**：什么操作触发问题，原来怎样，现在应怎样？明确一个成功场景和最容易遗漏的失败或边界场景。
2. **来源与归属**：参数来自哪里，谁有权使用，数据属于哪个空间/用户/会话？不能仅凭客户端传来的 ID 推断权限。
3. **读写闭环**：字段从输入、转换、保存到再次读取和展示经过哪些位置？创建、编辑、清空、删除是否复用同一契约？
4. **其他入口**：同一 Service、类型或状态还被哪些 API、任务、工具、IM/Embed 或客户端使用？修改默认值尤其要查全部调用方。
5. **生命周期**：异步操作是否会被重复提交、取消、删除、切换对象或迟到的回调打断？已有机制如何处理？
6. **证据**：哪些现有测试描述了这个契约，哪些检查能发现本次回归，哪些环境无法验证？

| 本次改动触发条件 | 必须沿链路检查的联动点 |
| --- | --- |
| 增删接口字段、枚举或默认值 | 请求结构、JSON 标签、Service 合并逻辑、存储、响应 DTO、前端类型/表单、实际客户端、Swagger |
| 新增路由或改变可访问范围 | 路由 guards、API Key 路由能力声明、资源归属、Service 直接调用者、权限相关现有测试 |
| 修改实体、唯一性或状态字段 | GORM 定义、查询/更新条件、PostgreSQL 与 SQLite 迁移、软删除、并发写入、旧数据库升级 |
| 修改任务或文档处理 | 入队负载、两种执行器注册、状态推进、取消/删除、重解析、子任务计数及已有重试语义 |
| 修改聊天事件或引用 | 事件生产者、流缓存与续传、消息持久化、历史恢复、前端消费、SDK/CLI/IM/Embed 的实际使用路径 |
| 修改列表、编辑弹窗或共享状态 | 初次加载、空结果、编辑回显、连续点击、切换对象、迟到响应、缓存失效和失败后的交互状态 |
| 修改部署或配置 | 默认值、加载位置、能力检测、相关部署模板、CI 环境；只更新实际消费该配置的位置 |

## 4. 后端必须守住的项目契约

### 4.1 身份、空间与资源权限

- 区分 `Tenant`（工作空间）与 `Organization`（跨空间共享组织）。当前 Organization 成员关联的是空间，不是任意个人账号；不要把两套角色或成员关系混用。
- 区分当前操作空间、资源所属空间、会话所属空间和 Principal。共享知识库/智能体不会让源空间的全部模型、凭据、文件和会话自动对调用者开放。
- 优先阅读 [rbac.go](internal/router/rbac.go)、[kb_access.go](internal/middleware/kb_access.go)、[api_key_gate.go](internal/middleware/api_key_gate.go)。登录、角色、资源共享权限、API Key 能力和 KB 范围是不同检查，不能用“已登录”“管理员”或前端隐藏按钮替代其他检查。平台管理员、跨空间访问权、空间角色也不能相互推导。
- API Key 路由采用显式声明：新增可供 API Key 调用的接口，必须使用现有路由策略声明相应能力；不能只放宽 RBAC。公共 IM/Embed 入口保留自己的验签和身份解析。
- `Get...ByIDOnly` 一类无空间过滤的查询用于解析归属，不代表已授权。普通资源读写保留空间/所有者条件；共享访问按既有解析结果使用源空间，不通过移除过滤“修复”查不到数据。
- 会话和消息访问复用 [principal.go](internal/types/principal.go)、[context_helpers.go](internal/types/context_helpers.go) 与 Session Service 的所有者规则；不要只检查 `tenant_id`，也不要假定所有入口都具有普通 Web 用户 ID。
- 检索范围以服务端解析的 [SearchTargets](internal/types/search.go) 为依据。知识库、文档、标签限制必须在普通 RAG、Agent 工具、Wiki/图谱相关路径保持一致；空的受限文档集合不能变成检索整个知识库。

### 4.2 参数、更新与错误

- 修改字段时明确“未提供”“空字符串/空数组”“0/false”“显式清空”分别意味着什么。沿用该接口的指针、可选字段或更新列方式，不用真假判断把合法零值变成默认值。
- GORM 的结构体更新可能跳过零值，整行保存又可能覆盖并发修改。根据既有契约明确更新列；不要把旧对象整行写回作为局部更新的通用办法。
- 保留 `internal/errors` 及现有错误链，按契约区分输入错误、无权限、资源不存在和内部失败。不能把所有失败改成 500，也不能用空列表消除真实查询失败。
- 修改接口时核对实际响应层级与分页语义。前端 [request.ts](frontend/src/utils/request.ts) 已返回 HTTP 响应体，不要重复假设它仍是 AxiosResponse。
- 更改构造函数或接口后，查全部实现、调用点和 [container.go](internal/container/container.go) 注册及相关现有用例；dig 在运行时装配依赖，编译通过不代表注册齐全。

### 4.3 后台任务、取消与并发

- 任务入队使用 `interfaces.TaskEnqueuer`。有 Redis 时为 Asynq，无 Redis 时为 `SyncTaskExecutor`；后者虽然名为 Sync，实际会启动 goroutine，不能假设入队返回时任务已经完成。
- 新增或修改任务处理时，同时核对 [task.go](internal/router/task.go)、[sync_task.go](internal/router/sync_task.go)、`internal/types/task.go` 和实际入队位置。队列、超时、重试次数及任务 ID 要沿用所属任务的约定。
- 请求派生后台工作时，使用已有的上下文传递机制。新增 `ContextKey` 必须在 [context_clone.go](internal/types/context_clone.go) 明确是否随 `logger.CloneContext` 保留：遗漏限制可能扩大权限，盲目复制临时授权也可能越权。
- 队列负载不会自动继承 HTTP context；检查任务执行端如何恢复空间、发起者和后台标记。身份恢复与取消/超时是两回事，不随意用 `context.Background()` 替换有生命周期要求的上下文。
- 文档 `processing`、`finalizing`、完成、失败、取消和删除存在竞争。迟到任务不能复活已取消/删除数据，旧处理结果不能覆盖重解析或人工编辑后的版本。
- `pending_subtasks_count` 由 Repository 的原子状态方法维护；不要用读取后减一或整行保存替代 `SetFinalizing`、`FinalizeSubtask` 等已有操作。修改条件更新后检查 `RowsAffected` 的业务含义。
- 数据库写入、队列投递、索引和文件操作不是一个原子事务。按现有流程核对先后顺序、失败返回及清理责任；清队列不能只删除 Redis 记录而遗留业务状态或数据库 pending 操作。
- 只处理本次涉及的并发关系，优先使用现有事务、条件更新、锁和版本控制；不另造通用任务框架或增加无依据的重试。

### 4.4 数据库与多种运行模式

- 主业务数据库由 `internal/container/container.go` 选择 PostgreSQL 或 SQLite。仓库里的其他存储驱动不代表主业务表也支持该方言。
- PostgreSQL 版本迁移位于 [migrations/versioned](migrations/versioned/)，SQLite 位于 [migrations/sqlite](migrations/sqlite/)；两者编号独立。共同业务表变更必须核对两套 schema、索引和约束。
- 默认以新的版本迁移表达新 schema 变化，不能只改 GORM 字段、SQLite 初始化 SQL 或已执行过的旧迁移来完成升级。数据库版本迁移是已确认数据结构变更的一部分，不是额外兼容层；修复指定迁移本身的问题时，按确认范围处理。
- 阅读 [migration.go](internal/database/migration.go) 的实际执行方式。启用 dirty 恢复时可能重放 SQL，须检查已创建对象、已删除列、已搬迁数据和约束，避免部分执行后再次启动失败。
- 检查全新安装与现有数据库升级；不能用测试里的 `AutoMigrate` 成功证明生产迁移正确。SQLite 不支持 PostgreSQL 的所有 JSON、锁、索引和 DDL 语法。
- 唯一性变化同时核对数据库索引、GORM 标签和业务查询，尤其是软删除后的唯一性及 IM 私聊/群聊绑定。
- Lite、桌面、有/无 Redis 及可选解析引擎属于现有运行模式。修改共享能力时核对相关模式；不要假定 Redis、DocReader、某个向量库或某项部署能力必然存在。能力入口见 [deployment_capabilities.go](internal/handler/deployment_capabilities.go)。

### 4.5 文件、凭据与外部请求

- 模型、MCP、搜索服务和数据源响应使用已有 DTO，凭据走各自 `/credentials` 契约；其他配置可能使用 [secret.go](internal/types/secret.go) 的脱敏占位符。不要互相套用两种更新语义，也不要把 `***` 存成真实密钥。
- 列表、详情、创建/更新响应、错误和日志都可能泄露凭据；修改返回形状时查完整路径，不能只处理详情页。配置读取失败不能被当作“用户没有配置”。
- 外部 URL 复用 [security.go](internal/utils/security.go) 的 SSRF 校验及安全传输；数据源使用 [httpclient.go](internal/datasource/httpclient.go)。只在保存配置时校验 URL，不能替代重定向和实际连接时的保护。
- 文件路径、文件名和对象键使用现有校验及存储服务，不直接拼接用户输入。资源访问还要检查所属知识库、消息或 Embed 渠道，路径合法不等于调用者有权限。
- 文件持久化引用与可访问 URL 是两种数据。复用 [ResourceCatalog](internal/application/service/resource.go)、[internal/storageurl](internal/storageurl/) 和前端 [protectedFileAccess.ts](frontend/src/utils/protectedFileAccess.ts)，不要在每个入口重新拼文件链接，也不要把短期授权 URL 回写成持久化引用。

### 4.6 问答、工具与流式输出

- 同一回答会经历生成、事件发送、流缓存、消息保存和历史恢复。修改内容或状态时查这些阶段，以及停止、失败、续传后的表现，避免只修实时展示而历史仍错误。
- SSE 发送响应头之后不能再按普通 JSON 错误返回；参数与访问检查尽量在开始流之前完成，流内失败沿用现有事件协议。
- 模型临时句柄（`cN`、`dN`、`res://NNNN` 等）与持久 ID、`resource://` 引用严格区分。编码/解码统一走 [modelcontext.Registry](internal/modelcontext/README.md)，不得将临时句柄用于授权、持久化或直接交给界面。
- 修改内置工具参数时，联查工具定义、执行、注册、[tool_policy.go](internal/modelcontext/tool_policy.go) 和实际消费方。动态 MCP 工具字段语义由服务端定义，不能按字段名猜测并改写其 ID。
- 流式片段可能截断引用、文件句柄或 Markdown，复用已有流解码和重写器，不对每块文本分别进行完整文本替换。

### 4.7 数据源、IM 与解析扩展

- 数据源按 [Connector/StreamHandler](internal/datasource/connector.go) 契约实现并在容器注册。`Emit` 的布尔返回决定该项能否推进游标；`Checkpoint` 必须是完整可恢复快照，不能只是增量，也不能把尚未成功处理的项标记为已完成。
- 数据源地址、账号或资源范围变化时核对游标归属、全量/增量同步及删除基线。连接失败、单项抓取失败和远端明确删除不能混为一种状态。
- IM 渠道是系统管理的机器人配置，用户权限来自有效绑定。修改绑定或消息路由时联查 [binding.go](internal/im/binding.go)、[service.go](internal/im/service.go)、平台 adapter 和 schema；区分私聊、群、成员及话题，回复目标与会话隔离范围都要保持正确。
- IM 渠道禁用、凭据变更、解绑和并发删除涉及数据库与运行中连接/请求；不能只改一处状态或只依赖启动时的权限检查。
- 解析能力从 [engine_registry.go](internal/infrastructure/docparser/engine_registry.go) 及 Python `docreader/parser` 查起，能力展示、选择和实际解析需一致。Go 本地引擎与 Python 远端引擎的注册机制不同，不另建硬编码能力清单。

## 5. 前端改动的闭环

- 修改前读取 [frontend/package.json](frontend/package.json)。沿用现有 Vue API、TypeScript、TDesign 和两空格风格，组件样式添加 `scoped`；优先查 `components/settings` 等已有公共组件。
- 普通请求走 `src/api` 和 `utils/request.ts`；流式请求另走 [api/chat/streame.ts](frontend/src/api/chat/streame.ts)。认证、语言或空间请求头变化必须检查这两条路径及受保护文件请求；Embed 凭据不能被普通 JWT 或选中空间覆盖。
- 权限展示使用 `stores/auth.ts`、`config/settingsAccess.ts` 和部署能力配置的现有判断。界面可见性不承担后端授权。
- 表单同时检查创建、编辑回显、取消重开、合法空值和提交失败。异步加载期间，确认按钮与提交函数的状态应一致，避免重复创建、保存尚未加载完的配置或把临时草稿当正式资源。
- 修改共享列表或配置后，检查列表、详情、侧栏计数及资源缓存是否同步。注销、切换账号/空间/编辑对象后，旧请求不能回写当前状态；已有 [versionedRequest.ts](frontend/src/stores/versionedRequest.ts) 可处理对应的缓存读写竞态，按需复用。
- 新增/修改文案同步检查 `zh-CN`、`en-US`、`ru-RU`、`ko-KR` 的键、插值和动态键路径，运行 `npm run check-i18n`。不要靠全量重生成语言包修复一个键。
- Markdown、引用、附件和受保护图片复用 `utils/chatMarkdownRenderer.ts`、`utils/security.ts` 及对应 Composable。不要绕过 DOMPurify，或用新的正则清洗破坏引用和文件句柄。
- 涉及流、定时器、监听和 Blob URL 时，核对切换会话/卸载时的清理及旧回调隔离，避免后台继续更新已经失效的页面。

## 6. 验证：按影响选择，不把命令堆满

先运行受影响包或现有用例，再运行相关模块检查。不要因全仓已有失败就跳过本次验证，也不要反复运行已通过且不受后续改动影响的检查。

工具版本以各模块清单和 CI 为准：根模块/CLI 使用 Go 1.26，前端 CI 使用 Node 24、npm；`client/go.mod` 独立声明 Go 版本。前端 CI 使用 `package-lock.json`，不要因同时存在 pnpm 文件就切换包管理器。Python 使用对应项目 `.venv` 或 `uv run --project ...`，不使用全局环境代替项目依赖。

| 变更范围 | 已有检查与注意事项 |
| --- | --- |
| Go 业务代码 | 对改动文件运行 `gofmt`；运行受影响包的 `go test`、`go vet`，依赖/装配变化再检查 `go build ./cmd/server`；相关 CI 见 [.github/workflows/app.yml](.github/workflows/app.yml) |
| Go Lint | `make lint` 使用 golangci-lint；配置见 [.golangci.yml](.golangci.yml)，包含 gofumpt、govet、revive、120 字符行宽。PR CI 按新增问题检查；比较基准必须是本次真实基线，不靠禁用规则消除告警 |
| 根 Go 模块全量 | `make test` 等价于 `go test -v ./...`；包含 DocReader 客户端测试，未运行 gRPC 服务时部分用例会跳过，不能据此声称集成验证通过。App CI 排除 `docreader/` 包，由独立 DocReader 工作流验证 |
| CLI / SDK | `make -C cli test`、`make -C cli lint`；SDK 在 `client/` 执行 `go test ./...`。根目录测试不覆盖这两个嵌套 Go 模块；SDK 改动须检查通过本地 `replace` 使用它的 CLI |
| 前端 | 依赖未安装时在 `frontend/` 执行 `npm ci`；仓库根目录运行 `bash scripts/verify_frontend_pr.sh`，依次执行测试、类型检查、构建。`npm run build` 本身不做类型检查，也没有 `npm run lint` 脚本 |
| DocReader | 根目录执行 `uv run --project docreader python -m unittest discover -s docreader/tests -p 'test_*.py' -v`；Go 客户端检查为 `go test ./docreader/client ./docreader/proto`，先按 [.github/workflows/docreader.yml](.github/workflows/docreader.yml) 准备依赖和 gRPC 服务 |
| 可选构建能力 | AnyDoc 需先构建 Rust 库并使用 `anydoc` 标签，见 [.github/workflows/anydoc.yml](.github/workflows/anydoc.yml)；Lite 构建使用 CGO 和 `sqlite_fts5`。默认 Go 构建不能证明这些路径可用 |
| 其他客户端 | 在 `packages/dsh-weknora/` 执行 `npm test`、`npm run typecheck`；在 `miniprogram/` 执行 `npm test`；Python MCP 的依赖和 unittest 命令以 [.github/workflows/mcp-server.yml](.github/workflows/mcp-server.yml) 为准 |
| 仅文档 | 核对路径、符号、命令与当前代码一致，运行 `git diff --check`；不为文档修改启动服务或执行无关构建 |

- 涉及 Go/SQLite 编译时注意 CGO 和 SQLite 头文件；App CI 有专门的头文件准备步骤。遇到环境缺失应记录实际原因，不修改业务逻辑绕过编译条件。
- 确实需要运行项目时，使用 `make dev-start`，再分别运行 `make dev-app`、`make dev-frontend`；配置从 `.env.example` 初始化时不覆盖已有 `.env`。检查结束不清理用户原有服务、数据库或 Docker 卷。
- **自托管 Runner**：只使用构建机已提供的工具；不得直接假设存在 `jq`、`gh` 等命令，优先用 Bash 内置能力或已有 GitHub Actions。
- Swagger 源头为 Handler 注解，通过 `make docs` 生成；检查 `docs/docs.go`、`docs/swagger.json`、`docs/swagger.yaml` 一致，控制无关生成差异。Proto 源头为 `docreader/proto/docreader.proto`，使用现有生成脚本，并确认 Go/Python 两端实际生成，不能只改生成文件。

## 7. 提交结果前审查一次完整改动

审查 `git diff` 的整体行为，而不是只重读刚改的几行：

1. **需求是否闭合**：原始操作现在能否走完输入、保存、重读和展示；是否只消除了表象？
2. **边界是否守住**：本次涉及的空间、所有者、角色、共享范围、凭据和合法零值是否保留？
3. **状态是否一致**：本次涉及的取消、失败、重复操作、并发写入和迟到结果是否破坏已有状态？
4. **调用方是否完整**：接口、事件、schema 或默认值变化是否漏掉实际消费者、另一种受支持实现或直接受影响的现有断言？
5. **改动是否克制**：是否出现重复判断、无用抽象、无依据回退、无关文件或为了测试而加入的生产分支？
6. **证据是否足够**：检查结果是否覆盖本次风险；`git diff --check` 是否通过；未验证的行为是否明确说明？

收到审查意见后，先依据触发条件、调用路径和当前契约确认问题，再在职责所属位置修根因；同时搜索相同受影响路径。不要逐条堆补丁，也不要为了让审查通过而擅自扩大范围。发现方案需要改变行为或权限时，说明差异并重新确认。

完成后简要报告：完成内容、修改文件、关键实现变化、实际执行的检查及结果、尚未验证的部分。环境阻塞、既有失败和本次引入的问题要区分，不能宣称未运行的检查通过，也不能仅凭“看起来与本次无关”认定为既有失败。
