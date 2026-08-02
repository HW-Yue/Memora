# Agent 的 MSQL 边界与依赖注入

状态：方向性规格，作为内置 Agent 开工前的强制架构约束。

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

当前 `sdk/memora` 同时依赖 `internal/daemon` 和 `internal/ipc`，daemon 内的 Agent 反向导入
它会形成依赖环。因此应先抽出一个无运行时依赖的协议包：

```text
protocol/msql     Request、Envelope、版本常量、Validate；仅依赖标准库
sdk/memora        IPC Client，并复用或兼容别名 protocol/msql 类型
internal/msql     数据库执行实现
internal/agent    仅导入 protocol/msql，不导入 sdk/memora 或数据库内核
```

协议抽取不改变现有 wire format，也不允许协议包导入任何 `internal/*`。

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
	service *MSQLService
}

func (a *agentMSQLAdapter) ExecuteMSQL(
	ctx context.Context,
	req msql.Request,
) (msql.Envelope, error) {
	return a.service.ExecuteMSQL(ctx, req)
}

var _ agent.MSQLExecutor = (*agentMSQLAdapter)(nil)
```

IPC 的 `msql.execute` 与同进程 adapter 必须调用同一个 `MSQLService.ExecuteMSQL`。禁止 adapter
直接调用 Store、Catalog、Row、Router、索引或内部事务对象。

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

## 关联

- [MSQL](../query/msql.md)
- [可选内置 Agent Runtime](./embedded-agent-runtime.md)
- [内置评测 Agent 与外置 Hook](../development/evaluation-agent-observability.md)
- [F169 后开发计划](../planning/post-f169-development-plan.md)
