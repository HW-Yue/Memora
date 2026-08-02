# Agent 的 MSQL 边界与依赖注入

状态：已落地的架构约束；F175a–F175c 已完成中立协议、共享 Service、Agent port 与依赖守卫。

## 决定

内置 Agent 是独立模块，但第一阶段不要求独立进程或独立可执行文件。它与 daemon
可以运行在同一进程，代码依赖仍必须单向隔离：

```text
cmd / daemon composition root
          │
          ├── engine-backed MSQL service
          └── agent.Runtime
                    │
                    └── agent.MSQLExecutor
```

Agent 对 Memora 的唯一能力端口是版本化 MSQL。所有发现、读取、写入、建模和资料吸收
结果都必须形成 MSQL 请求，并经过同一套 Lexer、Parser、Binder、Policy、事务和执行器。
同进程调用只省去本地 socket 与重复 JSON 编解码，不允许缩短数据库语义链路。

## Go 中的注入方式

Go 没有内建依赖注入容器，但接口、构造函数和显式组装本身就是依赖注入。Memora 采用：

- consumer-owned interface：接口由 Agent 按实际需要定义；
- constructor injection：必需依赖通过 `New` 一次性传入；
- explicit composition root：只在 daemon 启动层连接具体实现；
- narrow dependency：下层节点只接收自己使用的最小接口。

第一版不采用全局变量、`init` 注册表、service locator、`map[string]any` 容器或反射式 DI。

## 中立协议包

`sdk/memora` 的 Client 仍依赖 daemon/IPC；daemon 内的 Agent 反向导入 SDK 会形成依赖环。
F175a 已抽出无运行时依赖的协议包：

```text
protocol/msql     Request、Envelope、版本常量、Validate；仅依赖标准库
sdk/memora        IPC Client，并复用或兼容别名 protocol/msql 类型
internal/msql     数据库执行实现
internal/agent    仅导入 protocol/msql，不导入 sdk/memora 或数据库内核
```

协议抽取未改变现有 wire format；SDK 公共类型现在是 protocol aliases，静态测试禁止协议包
生产文件导入任何非标准库。F175b 已将依赖和 Session 生命周期收敛到实例级共享 Service。

## Agent 拥有端口

接口放在使用方 `internal/agent`，而不是数据库实现方：

```go
type MSQLExecutor interface {
	ExecuteMSQL(context.Context, msql.Request) (msql.Envelope, error)
}

type Dependencies struct {
	MSQL     MSQLExecutor
	Provider Provider
	Jobs     JobStore
	Events   EventSink
	Trace    TraceSink
	Clock    Clock
	IDs      IDGenerator
}

func New(deps Dependencies) (*Runtime, error)
```

`New` 必须验证所有必需依赖，返回可测试、无隐藏全局状态的 Runtime。`Dependencies` 只服务
顶层构造；解析器、编排节点和查询节点继续接收更窄的接口，不能把它当 service locator 逐层传递。

`Provider`、任务状态、资料暂存、事件流、Trace、时钟和 ID 生成器可以由 Agent 自己拥有。
它们不能直接读取或修改用户数据库，任务恢复后产生的数据库动作仍只能走 `MSQLExecutor`。

## Composition root

daemon 启动层是唯一同时认识 Agent 与数据库实现的位置，例如：

```go
type agentMSQLAdapter struct {
	session *msqlservice.Session
}

func (a *agentMSQLAdapter) ExecuteMSQL(
	ctx context.Context,
	req msql.Request,
) (msql.Envelope, error) {
	return a.session.ExecuteMSQL(ctx, req)
}

var _ agent.MSQLExecutor = (*agentMSQLAdapter)(nil)
```

composition root 从同一个 `msqlservice.Service` 为 IPC connection 和 Agent run 打开独立 Session。
IPC 的可信兼容 adapter 与同进程的不可信 protocol adapter 最终调用同一个 Batch core；后者必须先
验证 `protocol/msql.Request`，且不能触发旧 CLI 的零 StatementInput 模式。禁止 adapter 直接调用
Store、Catalog、Row、Router、索引或内部事务对象。

## 依赖方向

允许：

- `internal/agent` → `protocol/msql`；
- Agent 子包 → Agent 自己的窄接口；
- composition root → Agent 和 MSQL service；
- 外部 Agent → `sdk/memora` → IPC `msql.execute`。

禁止：

- `internal/agent` → `internal/store|catalog|row|router|index|daemon`；
- Agent 直接打开 Instance、Page、WAL 或数据库文件；
- 为了性能增加绕开 Parser、Policy 或事务的“内部快速路径”；
- 让数据库内核依赖 Agent、模型 Provider、Eino 或文档解析实现。

## 会话与并发契约

每个数据库实例只创建一个共享的 `MSQLService`，IPC handler 与内置 Agent adapter 必须复用它，
不能各自创建带独立锁状态的数据库服务。每个 IPC 连接和 Agent run 拥有独立逻辑 Session；
同一 Session 内请求串行，不同 Session 可以并发并由事务、MVCC、revision 和 Store 锁解决冲突。

Agent 的一次工具调用提交完整 MSQL batch；需要原子性的 `BEGIN` 到 `COMMIT` 必须处于同一次调用。
等待模型、文档解析或用户确认时不得持有数据库事务。取消、超时或 Session 关闭必须回滚未完成事务；
只有明确标记为可重试且提交结果已知未成功的请求才能自动重试，结果未知时禁止盲目重放写入。

## 第三方 DI 工具

Fx 能提供运行时依赖图和生命周期管理，但第一版对象图不复杂，引入反射容器会隐藏组装关系并
扩大二进制与调试面。Google Wire 已进入归档维护状态，也不作为新架构基础。若未来 Provider
插件数量导致手工组装成为可测量的维护问题，再以 ADR 重新评估；当前不预留框架耦合。

## 强制证据

实现该边界时至少需要：

- import allowlist 测试，阻止 Agent 导入数据库内核；
- fake `MSQLExecutor` 的 Runtime 单元测试，证明 Agent 只提交版本化 MSQL；
- 同一请求经同进程 adapter 与 IPC 得到等价 Envelope 的 parity 测试；
- context 取消、超时、并发和 race 测试；
- Agent 测试不得打开真实 Instance 来绕过 fake 端口；
- `sdk/memora` 协议类型兼容测试，证明协议抽取不破坏外部调用方。

F175c 已提供 `internal/agent.Runtime` 的显式构造注入、`internal/agent/agenttest` scripted fake，
并在 CI 中解析 Agent 全树 import AST。生产代码只能导入标准库与 `protocol/msql`；测试只能额外
导入 Agent 自有包，因此无法通过测试便利性绕开边界打开 Instance。

## 关联

- [MSQL](../query/msql.md)
- [可选内置 Agent Runtime](./embedded-agent-runtime.md)
- [内置评测 Agent 与外置 Hook](../development/evaluation-agent-observability.md)
- [F169 后开发计划](../planning/post-f169-development-plan.md)
