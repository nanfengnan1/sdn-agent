执行、存储与崩溃恢复
====================

事务边界是本节点一次 Commit 所涉及的全部驱动步骤。
外部配置不是数据库内的原子修改，因此实现采用先记录恢复信息、逐步配置、失败逆序补偿。

计划与 Worker
-------------

``model.Order`` 对显式 depends_on 拓扑排序，检查重复 ID、缺失依赖与循环；
无依赖的可执行操作按 ID 稳定排序。Registry 再把每个 op 展开为全部注册 Driver 的步骤，
形成 ``operation-id/backend-name`` 标识。

Worker 定期或由 kick 唤醒，按任务创建时间串行处理。
如果读取任何任务记录失败，本轮停止，不能跳过一个可能包含回滚屏障的未知记录。
存在 ROLLBACK_FAILED 时暂停后续任务。
执行前再次检查 Running revision，处理“受理时正确、排队后过期”的基线。

.. code-block:: text

   for each planned step:
       Prepare (query only)
       persist Undo + Started
       Apply
       Verify (actual readback)
       persist Done + step result

   all steps verified:
       atomic publish Running(revision + 1), Task(SUCCEEDED), Event

Prepare 失败时该步骤尚未标记 Started；Apply / Verify 失败时该步骤也进入回滚。
Started 必须先于后端修改落盘，以覆盖“命令已经执行，但结果未保存”的崩溃窗口。

持久化模型
----------

Redis 前缀为 ``sdn/<node-id>/``，同一 DB 内使用逻辑 key；
文件模式则将逻辑 key 存入该节点状态目录的 journal.json。

.. list-table::
   :header-rows: 1
   :widths: 35 65

   * - 逻辑 key
     - 内容
   * - ``candidate/<transaction-id>``
     - 操作、base_revision、冻结标志及关联任务。
   * - ``running``
     - revision、成功事务 ID 和已提交类型化操作日志。
   * - ``operational``
     - 最新 GetState 实际观测；错误单独显式呈现。
   * - ``task/<task-id>``
     - Task、Before 基线、展开计划、Undo、Started / Done / Restored。
   * - ``event/<task-id>/<sequence>``
     - 可重放任务快照。
   * - ``begin/<request-id>``、``commit/<request-id>``
     - 请求摘要和事务 / 任务收据。
   * - ``rollback/<request-id>``
     - RetryRollback 幂等摘要与原任务引用。

Store 接口只有 Load、Atomic、Keys。Redis 使用 MULTI / EXEC，
文件实现使用临时文件、fsync 和 rename，更新成功后才发布内存状态。
model.Op 的 JSON 处理保留 Protobuf oneof，不能用普通 JSON 反序列化丢弃具体参数类型。

Running 当前是操作日志，不是归一化资源树，也不是 Operational 的副本。
删除资源的事务同样追加成功操作；调用方不能简单按日志中出现过资源名称判断其仍然存在。
候选、任务、事件和幂等收据当前没有自动 GC。

Redis 连接成功不意味着持久化策略足够。部署需要配置 AOF、持久卷和磁盘保障；
当前实验使用 appendonly yes / appendfsync always。
本机 flock 只保护一个状态目录，不能支持两个主机共享节点 namespace 并发执行。

逆序补偿与屏障
--------------

回滚逆序遍历已 Started 的步骤，跳过已 Restored 的步骤。
Driver.Rollback 包含恢复和实际回读；成功后持久保存 Restored，
失败后保存 rollback_error 并立即停止，不继续恢复更早的依赖步骤。

.. code-block:: text

   Forward:   A --> B --> C --> D
   Undo:      D --> C(failed) STOP
   Retry:          C --> B --> A

   Do not restore B/A while C remains unrestored.

全部已开始步骤恢复后 Task 为 FAILED_ROLLED_BACK；未产生 Started 步骤时为 FAILED_NO_CHANGE。
任何补偿失败均为 ROLLBACK_FAILED，Running 不更新，后续配置保持阻挡。
Linux / FRR 的动态邻居、FDB 和协议会话不属于静态 Undo 可精确还原的内容。

RetryRollback 的受理检查
----------------------------------------

``internal/agent/recovery.go`` 按以下条件受理：

1. node_id、task_id、request_id 合法，幂等收据未与其他请求冲突。
2. Worker 执行锁可取得；否则返回 Unavailable，要求重试原请求。
3. 原任务为 ROLLBACK_FAILED 且 sequence 等于 expected_sequence。
4. 当前 Running revision 同时匹配 Before 基线和 expected_revision。
5. 至少存在未恢复的 Started 步骤，其 Undo 与 Driver 均可用。
6. 旧恢复历史未越过一个未恢复步骤继续恢复更早步骤。

通过后原子写入 ROLLING_BACK、递增事件和 rollback 收据，再唤醒 Worker。
它不执行 Apply，也不接收“人工确认已恢复”的强制成功参数。
完成恢复后解除屏障，保留原失败事件；新的正向配置必须另建事务。

进程重启与网络重建
------------------

ACCEPTED 可按原计划执行；APPLYING / ROLLING_BACK 使用持久 Undo 补偿。
即使所有后端修改已完成，只要最终 Running 成功发布未落盘，恢复仍选择补偿，
不会凭推测把任务标为成功。

管理连接断开不会取消持久任务。补偿使用独立的有限时长上下文，
避免客户端连接断开直接打断恢复。

这些保证依赖身份与日志保持一致，以及后端仍可访问。
重建容器或网络命名空间可能清空 Linux 对象；当前没有自动从 Running 日志重建整个网络。
新增重建能力需要资源化期望模型、依赖规划和重建后的实际核验，不能简单重放所有历史命令。
