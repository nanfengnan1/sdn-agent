# 事务、持久化与恢复

事务边界是单个 Agent 的全部注册后端。执行 `Prepare → 持久化 Undo + Started → Apply → Verify → 持久化步骤结果`，全部通过后一次性写入新 Running revision 和 SUCCEEDED 事件。失败步骤本身也进入回滚，因为报错前可能已经产生部分修改。

Linux Undo 保存旧字段和逆向命令，回滚后回读对应字段。FRR Undo 保存独占实例的 running-config，恢复时使用 `frr-reload.py` 并比较作用域化配置。新建 BGP peer 回滚按完整 neighbor 删除，避免先删 remote-as 后删除 update-source 被 FRR 拒绝。命令返回 0 之外还检查 FRR 错误输出和实际配置；不以 exit code 代替配置确认。

FRR 10.0.0 实验镜像在 `vtysh -c` 批处理模式下直接应用配置。本版不额外发送 `mgmt commit apply`：实测额外提交会报 `Commit requires lock on candidate and/or running DS`。这与 Agent 的 Commit RPC 是不同层面的提交；不能把一种版本/会话模式的行为推广到所有 FRR。

## 数据模型

所有 key 放在 `sdn/<node-id>/` 命名空间：

| key | 内容 |
|---|---|
| candidate/<transaction-id> | 候选 op 集合、base_revision、冻结标志 |
| running | revision、成功事务序列、成功提交的类型化操作日志 |
| operational | GetState 采集的实际后端快照；采集失败显式返回 observation_error |
| task/<task-id> | 计划、Undo、已开始/完成/恢复标志、任务结果 |
| event/<task-id>/<sequence> | 可重放任务事件 |
| begin/<request-id>、commit/<request-id> | 幂等收据与请求摘要 |
| rollback/<request-id> | 回滚重试的幂等收据和请求摘要 |

Running 采用已提交操作日志，按事务提交顺序表达配置；不是把 Operational 快照直接当作期望配置。当前前端接收命令级 patch，Controller 提供依赖，尚无任意完整期望配置到最小变更的自动 diff 引擎。失败任务不会更新 Running。冻结后的 Candidate 保留为事务审计记录，不再是可编辑候选。

Redis 同一 DB 的 MULTI/EXEC 原子更新 Running、Task 和事件。必须启用持久化并保障磁盘；协议不声称抵御 Redis 数据丢失。实验为 AOF always，开发文件 Store 使用 fsync + rename。每个 Agent 状态目录有 flock 防止本机重复进程；v1 不能让两个不同主机共享同一 node_id/Redis namespace 并并发执行。

## 断线和崩溃

管理断线不会取消已持久化 commit，也不会清除 Linux/FRR 配置。Agent 与 Controller 每 2 秒交互，RPC 失败后标记 DOWN 并尝试重新注册；恢复后 RUNNING。

Agent 重启扫描非终态任务：ACCEPTED 可以开始执行；APPLYING/ROLLING_BACK 依据持久化 Undo 回读并补偿，不盲目重放 Apply。Started 在调用后端之前持久化，所以“后端执行了但结果没记下来”的步骤仍会回滚。补偿遇到第一处失败就停止逆序执行，保留 ROLLBACK_FAILED 并暂停该节点后续配置，避免跨过失败步骤破坏恢复依赖。

运维修复临时故障后，可调用 `RetryRollback(node_id, task_id, request_id, expected_sequence, expected_revision)`。接口检查原任务、Running 基线、Undo、后端及恢复顺序，原子保存 ROLLING_BACK 事件和幂等收据后返回；Worker 继续执行未恢复步骤。重启或相同请求重试不会重放正向 Apply。只有全部 Undo 回读成功才成为 FAILED_ROLLED_BACK，解除该节点屏障；原失败事件继续保留。原正向事务不会自动继续，需要新的显式提交。

如果旧版本日志显示“较早步骤已经恢复，但它依赖的较晚步骤恢复失败”，接口拒绝自动重试，要求显式核对和恢复；不能直接修改日志掩盖失败。缺少 Undo、Running 基线已经改变也拒绝重试。数据读取失败时 Worker 停止本轮调度，不能跳过可能包含屏障的日志。

Running 更新发生在所有步骤完成之后，因此控制面配置具有单次成功发布边界，但 Linux/FRR 转发状态不具备跨命令原子可见性；执行和补偿过程中业务可能短暂受影响。动态邻居/FDB、协议会话和历史计时器不是静态配置快照的可恢复内容。

主机或网络命名空间重建后，Linux 设备可能丢失；当前不自动重放所有历史配置。此时需要 Controller 根据节点清单重新规划，而不是仅凭旧 Running revision 判断网络存在。

空 bridge 在最后一个端口离开后可能呈现全零自动 MAC，Linux 不允许显式设置这个地址。回滚这类 bridge 时恢复自动选择行为，随后由恢复的成员决定 MAC；显式配置的非零 bridge MAC 则按原值恢复。
