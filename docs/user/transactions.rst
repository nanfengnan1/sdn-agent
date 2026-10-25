事务、持久化与恢复
==================

.. contents:: 本页内容
   :local:
   :depth: 2

Candidate、Running、Operational
-------------------------------

.. list-table::
   :header-rows: 1
   :widths: 24 38 38

   * - 数据
     - 保存什么
     - 如何理解
   * - Candidate
     - 事务的操作集合、基础版本、冻结标志
     - Stage 只是编辑；Commit 后冻结并保留审计记录。
   * - Running
     - 成功事务和类型化操作日志、revision
     - 记录成功发布的配置，不是当前内核对象的完整快照。
   * - Operational
     - GetState 采集的 Linux / FRR 实际状态
     - 可能与 Running 不一致；采集错误单独返回。
   * - Task / Events
     - 执行计划、Undo、步骤进度、结果和序号
     - 回答是否受理、执行到哪、能否恢复；通知不是唯一结果来源。

这些是同一个存储中的逻辑区域，不是必须启动三个 Redis 实例。
Agent 不运行本地 etcd。Redis 使用同 DB 的原子事务发布相关记录；
文件模式使用 fsync 和 rename。两种方式都要保护状态目录、存储和备份的一致性。

默认 Docker 部署把 Redis 与 Agent、FRR 放在同一个容器内，
``agent.conf`` 和三个持久卷分别管理运行参数、身份、Redis 数据及 FRR 配置，见 :doc:`configuration`。
原生 CLI 和保留的旧实验脚手架仍支持已有外部 Redis / 文件模式。

任务状态
--------

.. code-block:: text

   Begin -> Stage -> Commit -> ACCEPTED -> APPLYING -> SUCCEEDED
                               |            |
                               |            +-> ROLLING_BACK
                               |                    |
                               |                    +-> FAILED_ROLLED_BACK
                               |                    +-> ROLLBACK_FAILED
                               +-> FAILED_NO_CHANGE

``ACCEPTED`` 是任务已经持久受理；``SUCCEEDED`` 才表示所有驱动的 Apply / Verify 成功。
即使成功，协议邻居建立和业务连通仍需独立检查。
多个事务基于同一个 revision 排队时，前一个成功后，后一个可能以
``FAILED_NO_CHANGE / REVISION_CONFLICT`` 结束，需要读取新状态后重新规划。

失败步骤本身也会回滚，因为后端可能在返回错误前修改了一部分配置。
Undo 按执行步骤逆序运行，**遇到第一处恢复失败立即停止**，避免破坏更早步骤的恢复前提。
``ROLLBACK_FAILED`` 会阻挡该节点后续任务；此时 Running 版本不变不代表实际网络已经复原。

断线后的结果查询
----------------

.. code-block:: bash
   :caption: CLI 客户端：替换目标地址、CA 和任务 ID

   TASK_ID='<已受理任务ID>'
   bin/sdnctl -addr leaf1.example:9090 -ca ca.pem -task "$TASK_ID" task
   bin/sdnctl -addr leaf1.example:9090 -ca ca.pem \
     -task "$TASK_ID" -after 12 watch

``after`` 是上次已处理的事件 sequence，初次订阅可以省略。
订阅断开或 CLI 超时不会取消已受理任务；查询原任务后继续订阅。
不要因为响应丢失而新建一个相同配置事务。

若游标已消费终态事件，watch 可能收到空流；CLI 会查询原任务确认结果，
不会重复打印已经消费的事件。流提前结束且任务尚未完成、任务查询失败，
或任务处于非 SUCCEEDED 终态时，CLI 返回非零退出码。

Agent 与 Controller 的管理状态为 INIT / RUNNING / DOWN。
心跳失败只反映管理链路，不会主动清除 Linux / FRR 配置，也不直接撤销 EVPN 路由。

回滚失败后的 RetryRollback
--------------------------

先修复导致 Undo 失败的具体原因，例如恢复 FRR 进程或访问权限，并保存原错误证据。
已由 Controller 纳管的节点优先通过 Controller 的恢复入口，保持双方任务记录一致。
独立节点可以用下面的 Agent CLI。

.. code-block:: bash
   :caption: CLI 客户端：只对确认的 ROLLBACK_FAILED 任务使用

   TASK_ID='<失败任务ID>'
   bin/sdnctl -addr leaf1.example:9090 -ca ca.pem -task "$TASK_ID" task
   bin/sdnctl -addr leaf1.example:9090 -ca ca.pem state
   SEQUENCE='<上一次task查询返回的sequence>'
   REVISION='<上一次state查询返回的revision>'
   bin/sdnctl -addr leaf1.example:9090 -ca ca.pem \
     -task "$TASK_ID" -sequence "$SEQUENCE" -revision "$REVISION" \
     -request-id repair-001 retry-rollback
   bin/sdnctl -addr leaf1.example:9090 -ca ca.pem -task "$TASK_ID" watch

``retry-rollback`` 只等待持久受理，随后用 task / watch 查看结果。
接口检查任务状态、sequence、Running revision、已保存 Undo、驱动和逆序恢复历史。
它只继续未完成的 Undo，不重新执行正向 Apply，也没有“强制标记成功”的选项。

恢复完成后原任务变为 ``FAILED_ROLLED_BACK`` 并解除节点屏障。
CLI 的 watch 对所有非 SUCCEEDED 终态都会返回非零退出码，因此恢复成功仍需按
``FAILED_ROLLED_BACK`` 判断。原失败事件继续保留；若仍要实施原配置，需要新的显式事务。

响应丢失后重试必须保留原 request-id、sequence 和 revision。
若 Running 基线改变、Undo 缺失，或旧日志曾越过失败步骤继续恢复，接口会拒绝自动重试。
不要直接编辑数据库删除屏障。

重启的边界
----------

.. list-table::
   :header-rows: 1
   :widths: 30 70

   * - 场景
     - 当前行为
   * - Agent 进程重启，网络命名空间仍在
     - 使用持久身份；ACCEPTED 可继续调度，APPLYING / ROLLING_BACK 根据 Undo 补偿。
   * - Agent 已 Apply，尚未保存结果时崩溃
     - Started 与 Undo 已提前落盘，重启后保守回滚，不盲目重放正向命令。
   * - Controller 暂时断开
     - 已受理任务继续执行，配置保留；重连后查询结果。
   * - 主机或容器网络命名空间重建
     - Linux 对象可能丢失；当前没有自动重放所有 Running 历史的完整重建器。
   * - 日志、身份或 Redis 数据丢失
     - 无法把旧 revision 作为恢复证明；需按节点实际状态重新核对与规划。

事务提供配置成功的单次发布边界，不提供 Linux / FRR 多条命令的原子转发可见性。
执行与补偿期间可能影响流量；动态 FDB、邻居、协议会话和计时器不会从静态配置快照原样恢复。
