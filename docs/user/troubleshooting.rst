观测与排错
==========

从任务结果开始，再检查实际状态。避免在 Agent 的事务执行期间人工修改同一 FRR 实例。

常用查询
--------

.. code-block:: bash
   :caption: CLI 客户端：设置目标地址、令牌和 CA 后运行

   bin/sdnctl -addr leaf1.example:9090 -ca ca.pem capabilities
   bin/sdnctl -addr leaf1.example:9090 -ca ca.pem state
   bin/sdnctl -addr leaf1.example:9090 -ca ca.pem -task '<任务ID>' task

``state`` 的 observations 是以字符串承载的原始 JSON，可先解析外层再解析具体值。
采集失败时检查 ``observation_error``；不能把部分数据或空数据当成完整正常状态。

.. list-table::
   :header-rows: 1
   :widths: 35 65

   * - observations key
     - 数据
   * - ``linux/links``、``linux/addresses``
     - 内核接口属性与地址。
   * - ``linux/routes``
     - 各路由表实际路由。
   * - ``linux/fdb``、``linux/neighbors``
     - bridge FDB 与 ARP / ND 邻居。
   * - ``frr/evpn``、``frr/bgp``
     - EVPN 路由表和 BGP EVPN 邻居摘要。
   * - ``frr/vni``、``frr/ospf``
     - FRR 的 VNI 分类与 OSPF 邻居。

启动和 RPC 问题
---------------

.. list-table::
   :header-rows: 1
   :widths: 30 70

   * - 现象
     - 检查方向
   * - Unauthenticated
     - 客户端与 Agent 的 SDN_TOKEN 是否一致、是否至少 16 字节；不要把 UUID 当 token。
   * - TLS 或地址连接失败
     - 检查监听、端口发布、证书 SAN / CA；TLS 与实验 insecure 模式必须一致。
   * - 初次注册失败
     - 首次启动必须能访问指定 Controller；核对 endpoint 是否能被 Controller 回连。
   * - another agent owns state-dir
     - 已有进程持有状态目录锁；检查进程归属，不要绕过锁启动第二个实例。
   * - PermissionDenied
     - 指定 node_id 与目标 Agent 不匹配；先重新查询 capabilities。
   * - InvalidArgument
     - 检查 op、action、oneof、字段、依赖是否完整；空事务和循环依赖会被拒绝。
   * - AlreadyExists
     - request_id 已用于不同请求；查询原任务，不能把同一 ID 改作其他操作。
   * - FailedPrecondition / REVISION_CONFLICT
     - 重新读取 Running；核对事务是否冻结、候选基线是否过期或恢复条件是否不满足。

任务和恢复问题
--------------

若 CLI 超时，先 GetTask。任务可能已成功，不能以客户端退出码判断服务端未执行。
若 Task 为 ``APPLY_FAILED``，查看每步的 backend、error，区分 Linux 命令、FRR 输出和回读失败。
``ROLLBACK_FAILED`` 要检查 ``rollback_error`` 并按 :doc:`transactions` 使用恢复入口。

``sdnctl commit`` 默认等待 3 分钟，长任务可在命令前增加 ``-timeout``。
已到终态的 watch 不会继续等待；恢复重试后可再次查询或从合适的 sequence 订阅。

.. code-block:: bash
   :caption: Linux 宿主机：完整 Docker 环境中的只读日志

   docker logs --tail 100 sdn-docker-leaf1
   docker exec sdn-docker-leaf1 vtysh -c 'show running-config'
   docker exec sdn-docker-leaf1 ip -d link show
   docker exec sdn-docker-leaf1 bridge fdb show

FRR 输出报错但退出码为零仍可能是失败；适配器会检查错误输出并回读。
Agent Commit 与 FRR 的 ``mgmt commit apply`` 不是同一层操作。
当前适配器以 ``vtysh -c`` 批处理应用配置，不额外发送该 mgmt 命令；
不要把交互会话中的行为直接套到此驱动。

EVPN 不通的检查顺序
-------------------

1. 检查实际接线、接口 UP、MTU 与 Underlay 地址，确认远端 VTEP loopback 路由可达。
2. 检查 OSPF 邻居和 BGP EVPN 会话。
3. 检查 bridge / VRF / VXLAN 主从关系，以及 FRR 对 50000 的 L3VNI 分类。
4. 检查 RD / RT 与导入导出范围；配置了租户 RD 不会改写另一个 L2VNI 的 RD。
5. 检查本地主机地址、网关、ARP 和 FDB；两端先 ping 本地网关，确认 Type-2 主机路由产生。
6. 对比远端 EVPN 路由、内核路由和 FDB，再进行双向连通性测试。

只有 Type-3、没有 Type-2 时，先确认主机确实发送流量并被接入 Leaf 学习。
动态 MAC / 邻居老化不等于 Agent 丢失 Running；本版不会周期性发送 ARP / ND 保活。

反馈问题时保留
--------------

记录软件版本、操作发生时间、节点 UUID、transaction_id、task_id、request_id、
提交的脱敏操作、任务完整步骤、state 和 FRR / Linux 查询。
明确是 Agent 进程重启还是容器 / 网络命名空间重建。
日志、Undo 和配置快照可能包含部署信息，提交问题前去除令牌、私钥和无关地址。
