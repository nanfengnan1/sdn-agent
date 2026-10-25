Protobuf、RPC 与错误约定
========================================

协议源文件是 ``api/agent/v1/agent.proto``，package 为 ``sdn.agent.v1``。
Go 生成文件随源码提交。当前消息使用静态类型化 oneof，不通过动态 Any 发布操作 schema。

操作消息
--------

.. code-block:: proto

   message Operation {
     string id = 1;
     OpCode code = 2;
     Action action = 3;
     repeated string depends_on = 4;
     oneof data {
       Network network = 10;
       Interface interface = 11;
       Address address = 12;
       Tenant tenant = 13;
       Bridge bridge = 14;
       // 其余类型以仓库中的 agent.proto 为准。
     }
   }

``code``、``action``、data 类型和参数要同时匹配，详细矩阵见
:doc:`操作目录 </user/operations>`。新增类型分配新的 enum 数值与字段 tag，
不得复用旧 tag；删除字段时保留编号。新增字段应考虑旧客户端忽略后的语义，
尤其区分 optional 字段未提供与显式 false / 0。

Capabilities 返回协议版本、操作码、全部 backend 名称及 rollback 标志。
它不动态列举全部字段约束、错误码或可执行 schema；客户端仍需匹配发布的协议。
新版本协议能被解码，不代表旧 Agent 已有对应 Driver。

Agent RPC
---------

.. list-table::
   :header-rows: 1
   :widths: 24 76

   * - RPC
     - 当前语义
   * - Begin
     - node_id、base_revision、request_id 创建 Candidate；版本必须匹配当前 Running。
   * - Stage
     - 按 operation.id 合并编辑候选，校验合并后的完整 DAG 和驱动；不执行后端。
   * - Commit
     - 冻结候选、保存计划 / 任务 / 事件 / 幂等收据，返回持久受理的 Task。
   * - Abort
     - 放弃未提交候选；不能取消已 Commit 的任务。
   * - RetryRollback
     - task_id、request_id、expected_sequence、expected_revision；受理保存的 Undo 重试。
   * - GetTask
     - 读取一个任务的持久化当前快照。
   * - WatchTasks
     - 重放 after_sequence 之后的事件，再跟随该任务直到终态；不是全节点广播接口。
   * - GetState
     - 节点 / 实例身份、管理状态、revision、running_json、实际 observations 和采集错误。
   * - GetCapabilities
     - 返回 op → 全部驱动；node_id 可为空以发现节点身份。

Stage 必须提交已经完整满足依赖的候选集合；不能先 Stage 一个依赖不存在的操作，
再期待下次补齐。Stage 可替换同 ID 的操作，不能编辑已经冻结的候选。

Controller 接口与身份
----------------------------------------

Register 上报 proposed_node_id、registration_id、instance_id、endpoint 和 capabilities，
返回确认的 node_id。Heartbeat 携带 node_id、instance_id、endpoint 和 revision。
registration_id 与最终 UUID 持久保存，instance_id 标识当前进程会话。

节点 ID 是寻址标识，不代替 token / TLS 认证。当前服务只实现部署级 token，
不具备设备硬件身份或按租户 RBAC。既有节点的 Controller 身份重分配会被拒绝，
防止旧日志套用到新节点。

幂等与重试
----------

Begin、Commit、RetryRollback 保存请求的确定性 Protobuf 摘要与结果引用。
同一节点内，同一幂等键与相同请求返回既有结果；内容不同返回 AlreadyExists。
已经冻结的同一个 transaction 再次 Commit 返回原任务，不重复执行。

RPC 超时不能证明没有受理。调用方应保留节点、事务、任务和请求 ID，
查询结果或重试原请求；不要重新规划成另一个创建事务。
RetryRollback 的重试还必须保留原 expected_sequence / expected_revision，
即使任务已经推进，也可通过原收据找到当前任务。

``sdnctl`` 给 Begin / Commit 的 request_id 分别加 ``/begin``、``/commit`` 后缀，
并让 commit / submit 等待终态。RetryRollback CLI 不等待终态。
``submit`` 每次先读取当前 revision，因此不应把重复整个 submit 当成跨版本的可靠重试机制。

事件游标与消息体
----------------

Task.sequence 单调递增，after_sequence 表示最后已处理的事件。
客户端断线后持久记录游标并恢复订阅，超过当前 sequence 的游标会被拒绝。
任务终态为 SUCCEEDED、FAILED_NO_CHANGE、FAILED_ROLLED_BACK、ROLLBACK_FAILED。
RetryRollback 可以让 ROLLBACK_FAILED 任务重新进入 ROLLING_BACK，历史事件不会被覆盖。

当前没有事件保留期与自动压缩，长期部署需考虑日志增长。
gRPC 默认接收消息上限为 4 MiB；大批量配置和大状态快照需要独立评估，
协议尚未提供通用分片或分页机制。拆分配置事务会同时改变其原子发布边界。

错误约定
--------

.. list-table::
   :header-rows: 1
   :widths: 32 68

   * - gRPC code
     - 含义 / 客户端动作
   * - Unauthenticated
     - 缺少或错误 token；检查凭据。
   * - PermissionDenied
     - node_id 与目标 Agent 不匹配。
   * - InvalidArgument
     - 参数、依赖、op / oneof、空事务或游标非法。
   * - NotFound
     - 事务或任务不存在。
   * - AlreadyExists
     - 幂等键对应其他请求，不能覆盖。
   * - FailedPrecondition
     - 版本 / 冻结状态冲突，或恢复基线、Undo、顺序不满足。
   * - Unavailable
     - RetryRollback 与 Worker 冲突时可返回；保留原请求稍后重试。
   * - Internal
     - 存储或服务错误；结果可能不确定，应查询任务与收据对应流程。

Task.error_code 使用 ``REVISION_CONFLICT``、``APPLY_FAILED``、``ROLLBACK_FAILED``；
Task.error、steps.error 和 steps.rollback_error 补充具体原因。
RPC 成功只说明该 RPC 完成，业务执行结果以任务状态为准。
