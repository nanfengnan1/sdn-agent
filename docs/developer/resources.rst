资源协议与查询适配
==================

兼容性
------

``api/agent/v1/agent.proto`` 保留旧字段与枚举编号，增加 OSPF6_INSTANCE=23、
OSPF6_INTERFACE=24、BGP_AF_PEER=38、BGP_NETWORK=39、BGP_REDISTRIBUTE=40、
EVPN_TYPE5=41。``protocol_version`` 保持 1；扩展由 ``features`` 和 ``queries``
显式协商，不要求旧客户端理解未知字段。

``Driver`` 生命周期未改变：Validate → Prepare → Apply → Verify；失败时从持久化
Undo 反向补偿。每种新增 FRR 操作仍使用配置快照，并验证原始配置恢复。
L3VNI 绑定的 Prepare 检查现有 Linux 资源；不会代替调用者创建 VXLAN。

查询接口
--------

``backend.Querier`` 是可选接口，避免强迫只实现配置的后端支持全部观测能力。
``QueryCapabilities`` 声明种类、地址族和 OSPF 版本；``Query`` 返回
``[]map[string]any``。Agent 转成 Protobuf Struct，不把 CLI 字符串透传给 Web。

Linux 使用 ``ip -j -d link``、``ip -j address``、``ip -j -4/-6 route``、
``ip -j -4/-6 neigh`` 与 ``bridge -j fdb``。主从接口构成实际 VRF→BD→port 关系，
不能通过 VNI 数字或 RD 后缀猜测租户。Registry 合并 Linux 租户与 FRR
``show evpn vni json`` / ``show vrf vni json``，补充真实 L3VNI 及 SVI 信息。

FRR 查询选择经过校验的固定命令模板。NLRI 解析保留 RD、NLRI、routeType、paths；
Type-5 补充 IPv4/IPv6 prefix。原始结构化字段仍保留，避免隐藏路由属性。
``internal/adapters/frr/testdata`` 保存 FRR 10.2.2 采集的真实 JSON fixture。

错误与分页
----------

认证/节点不符为 PermissionDenied；无能力为 Unimplemented；非法参数为
InvalidArgument；采集/解析失败为 FailedPrecondition；分页结果变化为 Aborted。
采集失败不更新 Config/Running，也不生成成功的空响应。
Query 不锁住整个内核动态表，因此只能承诺带时间戳的实时观测。
分页令牌是按稳定字段排序后记录的 SHA-256 摘要与位置。
``query_fingerprint.go`` 明确列出不参与摘要的观测倒计时和计数，例如 Linux bridge 的
hello_timer/gc_timer；配置 ageing_time、hello_time、max_age、成员和路由路径仍参与。
返回记录保留原始 details。排序使用与摘要相同的稳定字段，避免计时器变化让记录跨页移动。
实际资源或路径变化后必须重启查询。Linux 6.8 真实接口 fixture 用于分页回归。

安全导入
--------

``VerifyResources`` 携带 node_id、expected_revision、operations。
Agent 先串行化执行锁及提交锁，再核对 Running 版本和任务终态。
有待执行任务或 ROLLBACK_FAILED 时拒绝导入验证。只接受 CREATE/SET/BIND 形式的
期望资源，对所有注册后端调用 Validate + Verify，不调用 Prepare/Apply/Rollback。
只有实际读回全部匹配才返回 verified=true；错误列明 op/backend。
Controller 可据此采用已存在对象，而不重复创建。读回通过后产生的新修改仍由后续
事务的版本控制处理，不能把一次 Verify 当作长期独占锁。

回归验证
--------

.. code-block:: bash

   go test -race ./...
   go vet ./...
   python3 -m unittest discover -s docker -p '*test.py'

真实 FRR 回归需要操作者预建独占、可删除的 ``sdn-agent-feature-*`` 容器，
启用 zebra/bgpd/ospfd/ospf6d，并准备测试接口和相关实例。普通测试不会执行此路径。
``SDN_AGENT_FRR_CONTAINER`` 指向该容器时，新增操作逐项 Apply/Verify/Rollback，
并调用实际 JSON 查询；不得指向已有业务容器。

.. code-block:: bash

   SDN_AGENT_FRR_CONTAINER=sdn-agent-feature-probe \
     go test ./internal/adapters/frr -run TestLiveFRRAddedOperations -v

2026-10-25 在独占 FRR 10.2.2 容器完成新增六种操作的实际变更与恢复验证。
测试资源已删除；证据与限制见仓库 ``docs/testing.md``。

IPv6 冷启动验收
------------------------------

IPv4 实验的业务 SVI 常设置 ``addrgenmode none``。扩展为 IPv6 时必须单独配置
业务 SVI 的链路本地地址。首次 UP 前可设置 ``INTERFACE SET addrgenmode=eui64``；
已 UP 且原来使用 none 的接口，仅改 mode 不一定补生成地址，应使用 ADDRESS 显式增加链路本地地址。
无编号 L3VNI SVI 保持原配置。全局地址和链路本地地址须完成 DAD。
不把预先 ping 网关作为跨 Leaf 验收前提，否则会掩盖邻居解析缺陷。

诊断时区分三步：转发包到达目标 Leaf、目标业务 SVI 发出 NS、终端返回 NA。
若目标邻居 INCOMPLETE/FAILED 且 SVI/接入口抓不到任何 NS，首先检查 SVI 链路本地地址；
不能据此直接归因于 VXLAN 邻居抑制或 MLD。

Linux 6.8 的 ``ndisc_solicit`` 在转发包源不是出接口本地地址时不使用该源地址，
``ndisc_send_ns`` 随后查找链路本地地址，失败则不发送 NS。
参考 `内核实现 <https://github.com/torvalds/linux/blob/v6.8/net/ipv6/ndisc.c>`_。

仅在明确授权的独占测试环境，删除两个业务 SVI 上对应测试主机的**指定全局 IPv6 邻居**，
确认旧 Type-2 状态撤销后，保留 Type-5 前缀可达性。随后直接从远端主机发包，
同时抓取目标接入口的 NS/NA 与 Echo，验证动态解析恢复。不要清空整个节点邻居表，
不要替换为静态邻居，不要通过预热绕过该步骤。
