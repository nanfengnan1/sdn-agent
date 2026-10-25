# gRPC 协议与客户端

协议唯一来源是 [`api/agent/v1/agent.proto`](../api/agent/v1/agent.proto)，版本为 `sdn.agent.v1`。未知 protobuf 字段不等于未知 op 能力可执行；先查询 GetCapabilities。新增内置 op 分配新的 enum 数值和 oneof tag，既有 tag 不复用。

| RPC | 行为 |
|---|---|
| Begin | 指定 node_id、base_revision、request_id，创建候选事务 |
| Stage | 按 op.id 更新候选操作；不执行后端配置 |
| Commit | 校验并持久化任务后返回 ACCEPTED；并非执行成功 |
| Abort | 丢弃未提交事务；不能取消已接受的 commit |
| RetryRollback | 核对 task sequence 和 Running revision，持久受理对原 Undo 的重试；不是强制解锁 |
| GetTask | 查询任务的权威持久化结果 |
| WatchTasks | after_sequence 之后按序重放，并继续订阅直到终态 |
| GetState | Running revision/已提交操作，以及实际 Operational 快照 |
| GetCapabilities | 支持的 op → 全部后端映射、协议版本；node_id 为空可发现节点身份 |
| Controller.Register | 用持久化 registration_id 幂等协商 node_id、上报能力和 endpoint |
| Controller.Heartbeat | 以 node_id、instance_id 上报在线与 revision |

Stage 采用批量操作模式，依赖必须已在当前候选事务中。相同 op.id 表示编辑该候选 op；已 commit 后不可修改。幂等键在当前节点内有效，同一 request_id 配不同请求会报 AlreadyExists。Commit 后重复提交相同 transaction 会返回原 task_id。

身份由本地 `identity.json` 持久化。UUID 是标识，不是密码。mock-controller 在首次 UUID 冲突时分配新 UUID；后续相同 registration_id 返回既有 UUID。Agent 已有日志时若 Controller 改分 node_id，会停止而不是把旧运行状态绑定到另一个节点。克隆整个身份目录需要外部运维处理，当前注册机制不是设备硬件身份认证。

```bash
export SDN_TOKEN='<部署时生成的至少16字节token>'
bin/sdnctl -addr leaf1.example:9090 -ca ca.pem capabilities
bin/sdnctl -addr leaf1.example:9090 -ca ca.pem begin
bin/sdnctl -addr leaf1.example:9090 -ca ca.pem -transaction <id> -file ops.json stage
bin/sdnctl -addr leaf1.example:9090 -ca ca.pem -transaction <id> -request-id change-001 commit
bin/sdnctl -addr leaf1.example:9090 -ca ca.pem -task <task-id> task
bin/sdnctl -addr leaf1.example:9090 -ca ca.pem -task <task-id> -after 12 watch
bin/sdnctl -addr leaf1.example:9090 -ca ca.pem -task <task-id> -sequence <当前sequence> -revision <Running版本> -request-id repair-001 retry-rollback
```

CLI 的 commit/submit 等待终态；底层 Commit RPC 立即返回持久化受理结果。CLI 超时/断开不会取消已受理任务，可用 task/watch 接续。`sdnctl submit` 是便利组合命令；需要可靠重试整个工作流时，应保存 transaction_id 并重试 Commit，而不是把新的 Begin 当作旧事务重试。

`retry-rollback` 返回受理结果后使用 task/watch 查询，重试必须保留原 request-id、sequence 和 revision。已纳管节点优先通过 Controller 的 [恢复接口](../../sdn-controller/docs/recovery.md) 操作，使两端任务状态一起更新；新增 RPC 为兼容扩展，恢复需要升级 Agent 与 Controller。旧版本可能返回 Unimplemented。

## 错误与状态

| 位置 | 代码 | 含义 |
|---|---|---|
| gRPC | Unauthenticated | token 错误或缺失 |
| gRPC | PermissionDenied | node_id 不匹配 |
| gRPC | InvalidArgument | 参数、op、依赖、空事务或事件游标非法 |
| gRPC | NotFound | 事务/任务不存在 |
| gRPC | AlreadyExists | 幂等键对应不同请求 |
| gRPC | FailedPrecondition | revision 冲突或候选已冻结 |
| gRPC | Internal | 存储/服务错误；应查询任务核实是否已受理 |
| Task.error_code | REVISION_CONFLICT | 排队期间其他事务更新了 Running |
| Task.error_code | APPLY_FAILED | prepare/apply/verify 失败，见 error 和 steps |
| Task.error_code | ROLLBACK_FAILED | 补偿或回读失败，节点暂停后续配置 |

任务状态为 ACCEPTED → APPLYING → SUCCEEDED，或进入 ROLLING_BACK → FAILED_ROLLED_BACK / ROLLBACK_FAILED；未产生修改时可为 FAILED_NO_CHANGE。管理状态只有 INIT / RUNNING / DOWN，不能用 DOWN 推导业务路由已经撤销。

事件无过期清理策略，当前为实验阶段持久日志。批量请求限制为 4 MiB；大型配置需拆分事务。无跨节点原子提交承诺。
