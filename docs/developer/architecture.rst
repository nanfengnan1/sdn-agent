分层、组件与请求时序
====================

当前 Agent 是一个 Go 进程，各层为逻辑边界。Controller 与 Agent 互为 gRPC 客户端和服务端，
FRR 自行参与 OSPF / BGP 协议，Agent 配置并观测 FRR。

Docker 部署单元另外包含 Redis 和 FRR 守护进程，由 tini + Python 启动器监督。
配置流程是读取 agent.conf → Go 合并 CLI/env 并校验 → 输出去掉 token 的服务参数 →
Redis/FRR 就绪 → Agent。配置未通过校验前不会创建运行目录或启动子服务。

这是默认 Linux 数据面的启动与分层。VPP 模式复用相同事务层，由 VPP composite
协调 Linux 控制面镜像与本地 Python/Binary API 适配器；集成镜像另外监督 VPP 和适配器。
FRR/Zebra、Netlink、LCP、EVPN 映射与二层/三层报文流程见 :doc:`vpp`。

分层架构
--------

.. code-block:: text

                       Controller / sdnctl
                               |
                        Protobuf + gRPC
                               |
   +------------------------------------------------------------+
   | Management / RPC                                           |
   | Server: Begin Stage Commit Abort RetryRollback              |
   |         GetTask WatchTasks GetState GetCapabilities         |
   | Client: Register Heartbeat -> Controller                    |
   +------------------------------------------------------------+
                               |
   +------------------------------------------------------------+
   | Candidate / Transaction                                    |
   | node check, request receipts, revision, candidate freeze    |
   | task creation, result queries, persisted event replay       |
   +------------------------------------------------------------+
                               |
   +------------------------------------------------------------+
   | Planning / Execution                                       |
   | depends_on -> DAG -> all matching backend steps             |
   | serial worker -> Prepare -> persist Undo -> Apply -> Verify |
   | success publish / failure reverse Undo / restart recovery   |
   +------------------------------------------------------------+
                               |
   +------------------------------------------------------------+
   | Registry / Driver                                          |
   | Validate Prepare Apply Verify Rollback Observe              |
   +------------------------------------------------------------+
                   |                             |
              Linux Driver                  FRR Driver
              ip / sysctl             vtysh / frr-reload.py
                   |                             |
              Linux kernel                  FRR daemons

       Redis or durable file Store spans the management layers.
       Storage does not participate in packet forwarding.

组件与代码
----------

.. list-table::
   :header-rows: 1
   :widths: 36 64

   * - 路径
     - 职责
   * - ``cmd/sdn-agent``
     - 参数、身份、进程锁、驱动注册、Store 选择、RPC 监听、注册与心跳。
   * - ``internal/config``
     - 严格 JSON、覆盖规则、端口/路径/TLS 校验和不含 token 的有效配置输出。
   * - ``docker/supervisor.py``
     - 容器内 Redis/FRR/Agent 就绪、健康、故障退出和按顺序停止。
   * - ``api/agent/v1/agent.proto``
     - 操作、参数、Candidate、Task、状态、能力及双方 RPC。
   * - ``internal/transport``
     - TLS / token、客户端连接和服务端拦截器。
   * - ``internal/agent/agent.go``
     - 事务 RPC、任务 Worker、事件、状态采集、正向执行与回滚。
   * - ``internal/agent/recovery.go``
     - RetryRollback 的基线、顺序、Undo、幂等和受理校验。
   * - ``internal/model``
     - 事务内部记录、Running、Step、Undo、拓扑排序与 ID。
   * - ``internal/backend``
     - Driver 接口、op → 驱动列表、计划展开、命令 Runner。
   * - ``internal/adapters/linux``
     - bridge / VRF / VXLAN 等内核对象和实际状态回读。
   * - ``internal/adapters/frr``
     - OSPF / BGP / EVPN、配置快照与验证。
   * - ``internal/adapters/vpp``
     - 复用 Linux 语义与 Undo，调用本地适配器下发、验证和查询 VPP；与独立 Linux Driver 互斥注册。
   * - VPP 仓库 ``extras/sdn-agent``
     - Python 适配器与 IRB，同步受管 Linux EVPN 状态、VPP BD/BVI/FIB/多点 VXLAN。
   * - ``internal/store``
     - Redis 与持久文件的 Load / Atomic / Keys。
   * - ``cmd/sdnctl``、``cmd/mock-controller``、``lab``
     - CLI、注册心跳模拟器、真实容器实验与回归入口。

一次提交的时序
--------------

.. code-block:: text

   Client          Agent RPC          Store            Worker         Driver
     | Begin(base)     |                 |                |              |
     |---------------->| validate base   |                |              |
     |                 |-- Candidate --->|                |              |
     |<-- transaction -|                 |                |              |
     | Stage(ops)      |                 |                |              |
     |---------------->| DAG + Validate  |                |              |
     |                 |-- Candidate --->|                |              |
     |<-- candidate ---|                 |                |              |
     | Commit          |                 |                |              |
     |---------------->| freeze + plan   |                |              |
     |                 |-- task/event -->|                |              |
     |<-- ACCEPTED ----|                 |                |              |
     |                 |-------------------------------> |              |
     |                 |                 |                |-- Prepare --->|
     |                 |                 |<-- Undo/save --|<-- old state -|
     |                 |                 |                |-- Apply ---->|
     |                 |                 |                |-- Verify --->|
     |                 |                 |<-- step save --|              |
     |                 |                 |                |  (repeat)    |
     |                 |                 |<-- atomic -----|              |
     |                 |                 | Running + SUCCEEDED + event   |
     | Watch/GetTask   |                 |                |              |
     |---------------->|--- read -------->|                |              |
     |<-- Task --------|                 |                |              |

Stage 不触发 Driver Apply；Commit 的响应也不等于配置完成。
任务在接受后脱离客户端连接，Client 超时不取消 Worker。
若某步失败，Worker 对所有 Started 步骤逆序 Rollback，并持久记录恢复进度。

状态查询和注册
--------------

.. code-block:: text

   Agent startup --> read identity --> Controller.Register
                       |                     |
                       +<-- assigned UUID ---+
                       |
                       +--> open node Store --> recover tasks --> serve RPC
                       +--> Heartbeat(node_id, instance_id, revision)

   Client.GetState --> Agent --> Store: Running
                         |
                         +--> Driver.Observe --> Linux / FRR actual state
                         |
                         +--> save Operational --> State + observation_error

当前 Observe 是 GetState 驱动的采集，不是通用 FPM / ZAPI 推送服务。
Controller 可聚合各节点报告，Agent 本身不建立全网拓扑。

并发与边界
----------

RPC 可以并发接入，但配置执行由单个 Worker 串行处理。
事务锁保护候选 / 受理状态，executionMu 协调 Worker 与 RetryRollback。
Store 提供相关记录的原子写入；它不能把外部 Linux / FRR 命令变成数据库事务。

当前没有跨主机节点身份 fencing、远程 Adapter client 管理、资源级自动 diff、
后台配置漂移修复器和主动邻居维护器。增加这些模块时必须明确它们与事务 Worker 的写入归属。
