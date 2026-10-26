通过 MCP 接入 AI 客户端
============================================================

``sdn-agent-mcp`` 将 Agent 的结构化 gRPC 能力提供给支持 MCP 的 AI 客户端。
AI 可以发现工具、查询节点状态和任务；部署者显式启用写模式后，还能提交本机配置事务。
当前支持 stdio 传输，每个进程连接一个固定 Agent，不提供 HTTP MCP 服务或任意命令执行工具。
安装 Agent 本身见 :doc:`installation`，实现设计见 :doc:`/developer/mcp`。

.. contents:: 接入步骤
   :local:
   :depth: 2

工作方式与管理边界
------------------------------------------------------------

.. code-block:: text

   AI / MCP client
        | starts subprocess; stdin/stdout JSON-RPC
        v
   sdn-agent-mcp (default: read-only tools)
        | fixed endpoint + SDN_TOKEN + gRPC TLS
        v
   sdn-agent -> validation / transaction / journal -> Linux / FRR adapters

MCP 进程可以运行在能访问 Agent 的客户端主机上，也可以在 Agent 容器内通过 docker exec
启动。它不拥有独立的配置数据库；事务、任务、幂等记录和 Undo 仍由 Agent 保存。
关闭 AI 会话或停止 MCP 进程不会取消 Agent 已经受理的任务。

已由 Controller 纳管的节点应通过 Controller/Web 修改配置。
直接调用 Agent 写工具不会同步 Controller 的 Desired/Applied，可能造成节点 revision 漂移。
此类节点可使用默认只读 MCP 进行状态检查；下文写示例用于独立实验节点。
``-allow-writes`` 是本地 MCP 的工具暴露开关，不是 Agent 新增的 RBAC 权限体系。

构建、安装和客户端配置
------------------------------------------------------------

构建需要 Go 1.25+、模块依赖及可选 GNU Make；运行二进制不需要 Go、Node.js 或 Python。
源码在同一仓库的 mcp/，没有独立的 go.mod：

.. code-block:: bash
   :caption: Linux 构建机：sdn-agent 根目录

   go mod download
   make build-mcp
   ./bin/sdn-agent-mcp -h
   sudo install -m 0755 bin/sdn-agent-mcp /usr/local/bin/sdn-agent-mcp

``make build`` 也生成 ``bin/sdn-agent-mcp``。不使用 Make 时执行
``go build -o bin/sdn-agent-mcp ./mcp/cmd/sdn-agent-mcp``。
先启动具有正确身份、存储和数据面权限的 Agent，确认客户端可访问其 gRPC 地址。

MCP 客户端通常使用 command/args 启动子进程。以下是常见配置形状，
不是所有客户端都采用相同文件名或外层字段；按客户端自己的配置入口填写：

.. code-block:: json

   {
     "mcpServers": {
       "sdn-agent": {
         "command": "/usr/local/bin/sdn-agent-mcp",
         "args": ["-addr", "leaf1.example:9090", "-ca", "/etc/sdn-client/ca.pem"]
       }
     }
   }

``leaf1.example`` 和 CA 路径替换为实际配置。启动客户端的环境须已有与 Agent 匹配的
``SDN_TOKEN``，并允许客户端将它传给 MCP 子进程。令牌至少 16 字节，
不要把它交给模型作为工具参数，也不要提交包含真实凭据的客户端配置。
若客户端不继承环境，使用该客户端提供的环境变量或秘密管理设置。

.. list-table:: 程序参数
   :header-rows: 1
   :widths: 25 25 50

   * - 参数
     - 默认
     - 含义
   * - -addr
     - 127.0.0.1:9090
     - 固定的 Agent gRPC 地址
   * - -node
     - 空
     - 指定预期节点 UUID；空时通过能力查询发现并固定
   * - -ca
     - 空
     - 验证 Agent 服务证书的 CA 文件
   * - -insecure
     - false
     - 显式允许隔离实验中的明文 gRPC
   * - -timeout
     - 30s
     - 每次工具调用的超时预算，包含必要的节点发现和 RPC
   * - -allow-writes
     - false
     - 注册额外的事务写入与回滚重试工具

工具参数不能覆盖目标、UUID 或凭据。首次调用能力发现失败时，先排查地址、CA 和令牌，
不要将连接错误当作空状态。MCP 使用 stdin/stdout 收发协议消息；直接在终端启动后等待输入
是正常行为。调试日志使用 stderr，不能用 shell 的额外 echo 污染 stdout。
单条 stdio 输入消息上限为 4 MiB；超限会关闭会话，重新连接前先保留原事务恢复信息。

在容器内运行
------------------------------------------------------------

新构建的 Agent Docker 镜像包含 ``sdn-agent-mcp``。支持启动子进程的客户端可配置：

.. code-block:: json

   {
     "mcpServers": {
       "sdn-agent-container": {
         "command": "docker",
         "args": [
           "exec", "-i", "<目标Agent容器名>",
           "sdn-agent-mcp", "-addr", "127.0.0.1:9090", "-insecure"
         ]
       }
     }
   }

替换容器名，并确认客户端用户有访问该 Docker daemon 的权限。
保留 ``-i``，不要增加 ``-t``；TTY 会改变 stdio 协议传输。
容器内进程继承容器环境的 SDN_TOKEN，旧镜像没有新程序，需要重新构建并按原部署流程更新。
MCP 不读取 agent.conf 中的 token；仅通过文件配置凭据的部署应另外提供匹配的环境变量。
这个启动方式只创建 MCP 客户端进程，不需要重新启动 Agent。

工具清单与数据格式
------------------------------------------------------------

默认提供六个只读工具：

.. list-table:: 查询工具
   :header-rows: 1
   :widths: 25 43 32

   * - 工具
     - 主要参数
     - Agent 行为
   * - sdn_capabilities
     - 空对象
     - 查询节点、操作和查询能力
   * - sdn_state
     - 空对象
     - 查询 Running revision 和实际观测
   * - sdn_query
     - kind；可选 VRF、接口、地址族、分页等筛选
     - 结构化运行状态查询
   * - sdn_verify
     - expected_revision、operations
     - 按指定基线读回验证资源，不执行配置
   * - sdn_task
     - task_id
     - 查询原任务状态
   * - sdn_watch
     - task_id；可选 after_sequence、max_events、timeout_ms
     - 有界读取任务事件和后续游标

开启 ``-allow-writes`` 后再增加六个工具：

.. list-table:: 事务工具
   :header-rows: 1
   :widths: 25 43 32

   * - 工具
     - 必填参数
     - Agent 行为
   * - sdn_begin
     - base_revision、request_id
     - 建立候选
   * - sdn_stage
     - transaction_id、operations
     - 按操作 ID 合并到候选，并校验合并后的依赖图
   * - sdn_commit
     - transaction_id、request_id
     - 持久受理任务，立即返回 Task
   * - sdn_abort
     - transaction_id
     - 丢弃尚未提交候选
   * - sdn_retry_rollback
     - task_id、request_id、expected_sequence、expected_revision
     - 重试原任务保存的 Undo
   * - sdn_submit
     - base_revision、request_id、operations
     - 顺序完成 Begin、Stage、Commit，返回 transaction 与 task

先调用 MCP ``tools/list`` 获取实际 schema。字段使用 ``snake_case``，
所有 64 位版本、序号使用十进制字符串，例如 ``"base_revision":"0"``。
Operation 使用公开 Protobuf 的枚举和类型化 payload，不接受 shell 命令或本地文件路径。
optional 字段的未提供、false 和 0 含义不同；例如省略 mtu 不表示把 MTU 设置为 0。
合法工具调用的结果通过 structuredContent 和兼容的文本内容返回，
工具错误会标识 isError；仍需检查 Task.state 和验证结果中的业务状态。
每次工具 arguments 最多 1 MiB，操作数组最多 1000 项。sdn_verify 返回 verified=false 时
同时标记工具错误，验证信息仍保留；sdn_state 的 observation_error 和 sdn_query 的 error/stale
应由调用方单独检查，不能只判断 MCP 消息是否成功返回。
request_id、transaction_id、task_id 最长 256 个字符且不能全为空白。
gRPC 失败只返回状态 code 和通用 message，不把任意上游诊断字符串直接传给模型；
需要细节时按任务 ID 查询状态或查看 Agent 日志。

查询示例
------------------------------------------------------------

以下 JSON 是 MCP ``tools/call`` 的 params，客户端先完成 MCP 初始化再调用。
在提供工具调用界面的客户端中，分别填写 name 和 arguments 即可：

.. code-block:: json

   {"name":"sdn_capabilities","arguments":{}}

.. code-block:: json

   {"name":"sdn_state","arguments":{}}

.. code-block:: json

   {"name":"sdn_query","arguments":{"kind":"INTERFACES","page_size":100}}

先从 capabilities 确认节点声明了所需 kind、地址族和 OSPF 版本。
查询响应包含来源、采集时间、records、错误和分页信息；stale 或 error 不能当成正常空表。
继续查询时保持筛选不变并传回 next_page_token；实际数据变化导致游标失效时重新开始查询，
不能把旧页与新页静默拼接。具体查询分类见 :doc:`resources`。

独立节点的配置事务
------------------------------------------------------------

下面只展示独立实验节点的调用顺序。先启用写模式，读取 sdn_state 的 revision，
把示例中的 ``"0"`` 替换成真实版本，并保存本次意图的稳定 request_id。
不要因为版本冲突就自动改用新版本重提旧意图，应先重新核对实际配置和变更目标。

.. code-block:: json

   {"name":"sdn_begin","arguments":{"base_revision":"0","request_id":"ai-lab-001-begin"}}

保存返回的 transaction.id，替换下面的占位符：

.. code-block:: json

   {
     "name":"sdn_stage",
     "arguments":{
       "transaction_id":"<上一步返回的事务ID>",
       "operations":[
         {"id":"tenant","code":"TENANT","action":"CREATE",
          "tenant":{"name":"ai-vrf","table_id":1001}},
         {"id":"bridge","code":"BD","action":"CREATE","depends_on":["tenant"],
          "bridge":{"name":"ai-br","stp":false,"mtu":1500}},
         {"id":"binding","code":"BD_TENANT","action":"BIND","depends_on":["bridge"],
          "binding":{"bridge":"ai-br","tenant":"ai-vrf"}}
       ]
     }
   }

Stage 通过表示候选校验通过，还没有配置 Linux/FRR。确认目标与依赖后提交：

.. code-block:: json

   {"name":"sdn_commit","arguments":{"transaction_id":"<原事务ID>","request_id":"ai-lab-001-commit"}}

Commit 返回 ACCEPTED 等任务状态后，保存 task.id 并查询：

.. code-block:: json

   {"name":"sdn_task","arguments":{"task_id":"<返回的任务ID>"}}

.. code-block:: json

   {"name":"sdn_watch","arguments":{"task_id":"<原任务ID>","after_sequence":"0","max_events":100,"timeout_ms":1000}}

``sdn_watch`` 默认最多返回 100 个事件、等待 1000ms；max_events 可选 1..256，
timeout_ms 可选 1..30000，同时受进程 -timeout 限制。
响应带 events、after_sequence、terminal、truncated 和 timed_out，可能附带 task。
局部等待超时是有界观察结果，不表示配置失败；保存返回游标继续调用或查询 sdn_task。
terminal 只说明任务已到终态，仍需区分 SUCCEEDED、FAILED_ROLLED_BACK 和 ROLLBACK_FAILED。
流提前结束时会查询原任务核实，不能将 EOF 当作成功。

``sdn_submit`` 适用于已形成完整操作集合的同一流程：传入 base_revision、request_id
和上面相同的 operations，一次得到 transaction 与 task。
它使用 request_id 后附 ``/begin``、``/commit`` 作为两个 Agent 幂等键，
不自动读取新 revision，不等待网络收敛，也不自动撤销结果未知的 Commit。
回复丢失后重复原完整请求，不改变 request_id、base_revision 或 operations。

失败、重启与恢复
------------------------------------------------------------

.. list-table:: 结果与处理
   :header-rows: 1
   :widths: 35 65

   * - 现象
     - 处理
   * - 工具不存在或写工具未列出
     - 核对名称与 -allow-writes；需要写入时更新客户端启动参数并重启 MCP 进程
   * - InvalidArgument / schema 错误
     - 对照 tools/list 修正枚举、必填字段、64 位字符串和 Operation payload
   * - Unauthenticated / Unavailable
     - 检查 MCP 子进程的令牌环境、CA、Agent 地址与服务状态
   * - Commit / submit 超时或响应丢失
     - 保留原请求键和已知事务/任务 ID；查原 Task 或重试原请求，不建立新意图
   * - 版本冲突
     - 重新读取状态并审阅差异后重新规划，不能直接绕过版本检查
   * - ROLLBACK_FAILED
     - 先修复后端故障；使用原任务、明确 sequence/revision 和稳定 request_id 重试 Undo

sdn_retry_rollback 的语义与 CLI 相同，见 :doc:`transactions`。
它不能强制解锁、替换丢失 Undo，或把失败任务标为成功。
MCP 进程本身不保存会话恢复数据库；客户端应保留业务请求键、事务 ID、任务 ID 和游标。
重启后重新连接同一 Agent 身份并查询原任务，不生成空状态假定原配置消失。

MCP 适配测试验证协议、字段、认证转发和任务调用链；真实 Linux/FRR 配置、
协议收敛和业务转发仍需要独立验证。工具调用成功不构成这些验收证据。
