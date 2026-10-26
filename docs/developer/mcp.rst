MCP 适配层设计与扩展
============================================================

MCP 能力位于仓库根目录 mcp/，与 Agent 使用同一个 Go 模块。
它把 MCP 工具请求映射到公开 ``sdn.agent.v1.Agent`` gRPC 服务，
复用已有事务、版本、幂等、验证和恢复语义。用户接入与工具参数见 :doc:`/user/mcp`。

进程、数据与职责
------------------------------------------------------------

.. code-block:: text

   MCP client
       | initialize / tools/list / tools/call / cancellation
       v
   official MCP Go SDK, stdio transport
       |
   sdnmcp tools: schema -> strict decode -> node binding -> bounded RPC
       |
   generated AgentClient + existing authenticated TLS transport
       v
   Agent: validation -> candidate -> task journal -> adapters

MCP 进程只持有连接、启动选项和固定节点身份，没有候选或任务数据库。
命令入口负责凭据、TLS、生命周期和 stdout 协议通道；工具层负责参数与 RPC 映射。
入口限制单条 stdio 输入帧为 4 MiB，超限不继续读取该会话中的后续请求。
工具层另外限制 arguments 为 1 MiB、操作数组为 1000 项，并拒绝重复 JSON 字段。
Agent 是网络配置和任务状态的权威来源，MCP 不在本地模拟任务成功、不直接访问 Redis，
也不调用 shell、ip、vtysh 或读取调用方指定的文件。

当前 SDK 使用 ``github.com/modelcontextprotocol/go-sdk``，版本由根 go.mod 固定。
初始化、stdio JSON-RPC 和取消等协议机制由 SDK 承担；仅提供 tools 能力，
未增加 HTTP 服务、OAuth、MCP resources/prompts 或多节点自动编排。
MCP stdio 要求 stdout 只包含协议消息，诊断写 stderr；参见
`官方传输说明 <https://modelcontextprotocol.io/specification/2025-11-25/basic/transports>`_。

源码定位
------------------------------------------------------------

.. list-table:: 实现入口
   :header-rows: 1
   :widths: 43 57

   * - 路径（相对仓库根）
     - 职责
   * - mcp/cmd/sdn-agent-mcp/main.go
     - CLI 参数、SDN_TOKEN、gRPC 连接、stdio 启动和退出
   * - mcp/server.go
     - NewServer、工具注册、固定节点、RPC 与结果/错误适配
   * - mcp/schema.go
     - 公开 Protobuf 字段、枚举和 Operation 的 MCP JSON schema
   * - ``mcp/*_test.go``、``mcp/cmd/sdn-agent-mcp/*_test.go``
     - MCP 协议调用、事务和边界、命令入口验证
   * - api/agent/v1/agent.proto
     - Agent 请求、响应、操作、查询能力和事务协议
   * - internal/transport/transport.go
     - Agent TLS 和 Bearer 凭据转发
   * - internal/agent/、internal/model/
     - Agent 事务、输入校验、任务状态及恢复语义
   * - Makefile、docker/Dockerfile
     - build-mcp / build 与镜像内 MCP 二进制

实现细节以当前源码为准。MCP 层新增业务能力前，先检查公开 Agent RPC 是否已经支持；
没有对应 RPC 的需求应在 Agent 协议和实现层完成，不能用 MCP 的通用执行入口绕过。

输入输出合同
------------------------------------------------------------

工具名称使用 ``sdn_`` 前缀，输入及 Protobuf 输出使用 snake_case。
schema 展示查询枚举、操作枚举、强类型 payload 和嵌套字段，
不能只给模型一个不透明 JSON 字符串并让它猜测结构。

* uint64 版本和序号只接受十进制字符串，避免 JSON/JavaScript 数字精度丢失。
* optional 字段保留 presence：省略、false 和 0 必须有不同处理。
* Operation oneof 只能选择一种 payload，code/action 和字段有效性继续由 Agent 校验。
* 拒绝未知字段和工具不接受的 node_id；目标地址、凭据、UUID 不由模型逐次选择。
* structuredContent 便于客户端程序消费，同时保留文本内容供只显示文本的客户端使用。
* gRPC/业务失败与 MCP 协议错误区分；工具错误标识 isError，任务终态另看 Task.state。

上游 gRPC 错误消息可能包含凭据或调用上下文，适配层仅传递状态 code 和固定说明。
已有结构化业务结果（例如验证错误列表、submit 的事务 ID）在可用时随错误保留。
Implementation.Version 当前为 dev；发布来源应通过二进制和源码构建记录追溯，
不能将此字段当作已实现的正式版本管理机制。

工具的 readOnlyHint、destructiveHint 等 annotations 仅帮助客户端展示与决策，
不能替代访问控制；MCP 官方也将其定义为提示信息，见
`工具合同 <https://modelcontextprotocol.io/specification/2025-11-25/server/tools>`_。
默认只读通过实际工具注册集合实现；不能仅把写工具标为危险而仍默认开放。
服务端 token 当前仍为部署级凭据，MCP 没有发明租户或用户级权限。

节点、超时和生命周期
------------------------------------------------------------

Options 中配置固定 NodeID，或首次通过 GetCapabilities 发现并固定节点。
一个进程始终面向同一地址和节点；不在工具输入中提供任意端点或替换凭据。
调用具有统一超时预算，MCP 取消应传播到 gRPC Context。
stdio 关闭或进程退出只停止适配层连接，不改变 Agent 已经持久受理的任务。

WatchTasks 是流式 RPC，MCP 工具将其转为有界批次：
通过 max_events、timeout_ms 和全局 Timeout 限制等待与返回数量。
返回事件、最后游标和终态/截断/等待超时标志，由调用方继续查询。
空流或 EOF 需要 GetTask 确认持久状态，不能当作 SUCCEEDED。
终态也可能是失败或回滚失败，返回后不得笼统解释为“网络配置完成”。

事务与幂等不变量
------------------------------------------------------------

sdn_begin / sdn_stage / sdn_commit 分别映射单个 RPC。
Begin、Commit 和 RetryRollback 要求调用方明确提供稳定 request_id；
MCP 不在重试时随机生成新的业务请求键。
Commit 返回持久受理的 Task 后立即结束工具调用，长期执行由 Agent 完成。

sdn_submit 是便利组合：先验证 schema、Protobuf 形状和完整依赖图，再按 Begin → Stage → Commit 调用。
基线 revision 来自调用方，两个幂等键从同一个 request_id 派生为 /begin 和 /commit。
成功结果包含 transaction 与 task；失败在可用时附带事务/任务 ID，帮助恢复原工作流。
不能在 Commit 结果未知时自动 Abort、自动切换新基线或重新创建意图。
重试同一 submit 时保持全部输入不变。

Stage 本身按 operation.id 增量合并，依赖可以指向候选中已有操作；
只有 submit 要求输入自成完整依赖图。submit 恢复已有候选时会比较操作集合，
冻结候选通过原 task_id 查询，不再次 Stage。一个 MCP 进程内串行推进 submit 组合，
不提供跨进程互斥；不要同时用手工 Stage 或其他进程编辑同一 submit 候选。

Stage 不等于配置下发；只读 VerifyResources 不等于 Apply。
RetryRollback 只重试原 Undo，遵守原任务 sequence、revision 和 Agent 回滚屏障。
Direct Agent 写入不会同步 Controller Applied；整网管理应在 Controller 一侧扩展入口。
详见 :doc:`execution-storage` 和 :doc:`/user/transactions`。

增加工具的步骤
------------------------------------------------------------

1. 明确现有 RPC、输入输出、读取或写入、事务副作用、超时和幂等语义。
   新的网络操作先按 :doc:`adapters` 补齐 Agent 的验证、执行、读回和回滚。
2. 在 mcp/ 注册命名工具和 schema。标明必填字段、枚举、数字表示和 optional 语义；
   不增加任意命令、文件路径或由模型指定目标地址的后门参数。
3. 实现 Context 传播、节点约束和严格 protojson 转换。
   写工具必须服从 AllowWrites，工具 annotations 与实际行为保持一致。
4. 为返回结果和失败定义清楚语义。长任务返回任务 ID 后供查询，
   不把 Agent 任务混同为 MCP SDK 自身的任务协议。
5. 用 MCP client 真实调用 initialize、tools/list、tools/call，
   测试合法和非法输入、权限模式、取消/超时、gRPC 错误与恢复 ID。
6. 更新本页、:doc:`/user/mcp` 和 mcp/README.md；若新增运行参数或依赖，
   同步命令帮助、构建和镜像文档。

测试与验收
------------------------------------------------------------

在仓库根目录执行：

.. code-block:: bash

   go test ./mcp/...
   make test-mcp
   go vet ./mcp/...
   make build-mcp
   make -C docs html

``make test-mcp`` 执行 MCP 包的 race 测试，需要 C 编译器和启用 CGO。

重点验证默认六个只读工具与写模式十二个工具的差异、全部 RPC 映射、optional false/0、
uint64 最大值、非法 oneof、固定节点、稳定请求键、submit 的丢响应恢复、
watch 游标/局部超时/EOF、认证转发及 stdout 无额外日志。
整体模块回归和适配器测试仍按 :doc:`testing` 执行。

Fake Agent 或内存 gRPC 证明 MCP 合同和调用链，不能证明真实 Linux/FRR 修改或转发。
实际客户端接入、目标平台二进制、TLS 链路和网络业务需要分别记录验证环境与结果。
不要将工具参数示例、历史实验报告或文档构建描述成已完成真实 AI 产品端到端验收。
