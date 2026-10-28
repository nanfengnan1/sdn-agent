VPP 邻居保活实现
================

本功能是节点级开关，默认关闭，仅 VPP 数据面声明
``neighbor-keepalive/v1`` 能力。它维护已经学习到的本地动态 MAC/IP，
不负责发现从未发送过报文的主机，也不周期性重发 BGP Type-2。

.. contents:: 本页导航
   :local:
   :depth: 2

代码分层
--------

.. code-block:: text

   Web 二层网络 BD：邻居保活开关
             | candidate / plan / commit
   Controller nodeResources：resource/neighbor_keepalive
             | NEIGHBOR_KEEPALIVE SET { enabled: true / false }
   Agent VPP Driver：Validate / Prepare / Apply / Verify / Rollback
             | 本地 Unix RPC
   extras/sdn-agent/keepalive.py：策略、接入端口范围、查询
             | VPP Binary API
   neighbor_keepalive_plugin.so
             | 主线程周期进程：发现本地动态邻居、计时、发送探测
             v
   本地接入端口 -- 单播 ARP Request / IPv6 NS --> 主机
             ^                                  |
             +---- ARP Reply / NA / 有效学习 ----+
             | 原生 ARP/ND 校验及学习；正常 L2 学习
   VPP FDB + 邻居更新时间
             | 失败删除单个动态 IP 绑定
   irb.py -> Linux 控制面邻居 -> Zebra -> BGP Type-2 撤销

``api/agent/v1/agent.proto`` 增加 optional bool，确保关闭操作中的
``false`` 不会被当作未填写。VPP Driver 独立处理这个操作，不调用 Linux Driver。
Controller 使用节点单例身份，保留显式 false，并拒绝 DELETE；关闭使用 SET。
开关经过原有候选、验证、提交和 Undo，Linux 节点拒绝该操作。

``extras/sdn-agent/keepalive.py`` 只配置范围和读取状态，不负责发送报文。
范围由受管 BVI 与本地 LCP 接入口组成，排除 VXLAN、远端静态邻居及未受管接口。
适配进程暂停时，VPP 仍独立探测。适配器恢复后按实际状态撤销/重建控制面镜像。
隔离实验同时暂停 supervisor 的健康保护，才能持续观察原生调度；生产 supervisor
会在适配器持续失联时停止整套进程。因此该测试不代表容器已经具备进程故障容错。

代码入口
--------

VPP 原生实现的独立开发章节位于 VPP 仓库
``docs/developer/extras/neighbor_keepalive.rst``，已加入该仓库的开发文档目录。
下面路径除 Agent Driver 外均相对 VPP 仓库。

.. list-table:: 实现职责
   :header-rows: 1
   :widths: 40 60

   * - 入口
     - 职责
   * - Agent 的 ``internal/adapters/vpp/vpp.go``
     - 注册 VPP 专属操作和查询，保留显式 false，执行验证、Undo 与读回。
   * - ``extras/sdn-agent/keepalive.py``
     - ``reconcile`` 下发开关与 BVI/接入口范围；``failed`` / ``query`` 读取结果。
   * - ``src/plugins/neighbor_keepalive/neighbor_keepalive.c``
     - ``nk_process`` 主线程每轮约一秒扫描；``nk_learned`` 筛选动态邻居；``nk_send`` 构造探测报文。
   * - ``src/vnet/ip-neighbor/ip_neighbor_dp.c`` 与 ``ip_neighbor.c``
     - 接收线程通过 RPC 交给主线程学习/确认邻居，更新 ``ipn_time_last_updated``。
   * - ``src/vnet/ip6-nd/ip6_nd.c``
     - 无 TLLA solicited NA 的确认入口，要求已有动态绑定的接口/IP/MAC 匹配。
   * - ``extras/sdn-agent/irb.py``
     - ``sync_neighbors`` 发布健康绑定、撤销失败 IP，并处理 Linux/FRR 镜像通知。

原生状态机
----------

``src/plugins/neighbor_keepalive/neighbor_keepalive.c`` 在 VPP 主线程运行，
复用原生 ``ip_neighbor_walk`` 和 ``ipn_time_last_updated``。
ARP/ND 应答继续走原有协议校验、学习路径，不截获后丢包，也不自行修改 FDB 时间戳。
有效 ARP/ND 学习可确认存活；普通 IP 流量和另一个 IP 的应答不能替代该 IP 的确认。
这不是带随机挑战值的认证机制，适用与原生 ARP/ND 相同的受信任接入网络。

* 键为 BVI + 地址族 + IP；值保存 MAC、本地端口和最近原生学习时间。
  BVI 隔离租户上下文，同一 MAC 的不同 IP 单独维护。
* reachable 到期后发送探测并进入 probing；默认重试间隔 5 秒，最多发 3 次。
  只有原生邻居学习时间变化才确认，发送请求、配置重放和 Linux 永久邻居均不续期。
* 三次探测无应答后进入 failed，删除对应动态邻居。保留同 MAC 的其他 IP 和 FDB，
  MAC 继续按正常 L2 学习/老化处理。失败项保留最多 300 秒用于查询。
* 有新的有效 ARP/ND 学习且 MAC 位于授权接入口时重新加入。
  不持续探测已经撤销的主机；恢复上线的主机需要重新发包，或由正常业务触发地址解析。
* 发送资源/源地址错误记录为 send-error，不伪造确认，不把未发送的请求计入三次失败。
  查询公开错误计数。最多跟踪 4096 个绑定，每秒最多发 512 个探测，超出容量计数可查。

探测使用 BVI 的源 IP/MAC，复用接入口的以太网 rewrite 后直接发到接入口，
不经过 VXLAN BUM 复制。IPv4 目的 MAC 为已知主机 MAC；IPv6 NS 的目的 IP
为已知邻居单播地址。未知地址解析仍由 VPP 原有 ARP/ND 机制负责。

VPP FDB 的时间戳粒度为一分钟。有效 MAC 老化为 T 秒时，探测间隔为
``(T - 60) / 2`` 秒，并提前最多 10% 分散探测；默认 T=300，对应约 108–120 秒。
开启时要求受管 BD 老化为 0 或不小于 120 秒；T=0 时使用约 135–150 秒。
不能直接假定在 3/4 T 时开始探测仍有完整的 1/4 T 可用。

例如 T=300 秒，假定该目标本轮的间隔为 120 秒，正常发送时的时序为：

.. code-block:: text

   t=0       有效 ARP/ND 学习，状态 reachable
   t=120     发第一次单播探测，状态 probing
   t=125     未确认，发第二次
   t=130     未确认，发第三次
   t=135     仍未确认，状态 failed，删除该动态 IP 邻居
      |
      +-- 任意一次有效学习 -> reachable，重新计算下次探测时间

上述时间需考虑约一秒的扫描周期、发送预算与发送错误；没有为每个邻居创建
独立线程。``nk_send`` 使用 BVI 源地址和已知主机 MAC，构造接入口 Ethernet
rewrite（含 VLAN 标签）后直接提交接口输出节点。应答通过正常 L2 学习刷新 MAC，
通过原生 ARP/ND 学习更新邻居时间，保活进程在下一轮观察并确认。

例如 MAC ``02:00:00:10:00:02`` 同时有 IPv4 ``192.168.20.12`` 和 IPv6
``2001:db8:20::12``：IPv4 三次无应答而 IPv6 继续应答时，只撤销 IPv4 绑定及其
MAC/IP Type-2，IPv6 和共享 MAC 保留。IPv6 的应答可以刷新该 MAC 的 L2 学习时间，
但不能替 IPv4 地址续期。

配置与运行状态
--------------

适配器状态文件 version 3 保存 ``neighborKeepalive.enabled``，可读取旧 version 1/2
并默认关闭。事务失败恢复旧开关，Verify 读回原生策略。
重复配置不重置邻居计时；关闭清除探测状态，不清空健康邻居和 MAC。
VPP 重启后的开关由已持久化的适配器模型重新配置；本地动态绑定需重新学习，
不能将以前的存活时间当作重启后的确认。完整网络冷启动依赖已有接口恢复机制。

Binary API 包括 ``neighbor_keepalive_set``、``neighbor_keepalive_status``、
``neighbor_keepalive_scope_add_del``、``neighbor_keepalive_scope_dump`` 和
``neighbor_keepalive_dump``。Agent 查询 ``NEIGHBOR_KEEPALIVE_STATUS`` 返回一条
policy 记录及绑定记录，可按 VRF、BD、接口、地址族和邻居地址筛选。
``irb.py`` 同步前读取 failed 集合，阻止残留邻居重新发布。
Linux 控制面镜像使用永久邻居，因此撤销时先将该 IP 标为 ``NUD_FAILED``，
再删除。FRR Zebra 会把永久邻居的直接删除通知交给 RFC 5549 特殊路径，
仅执行删除可能留下 EVPN Type-2；先发送失败状态可让 Zebra 正常撤销对应 IP。
这只影响适配器拥有的本地镜像项，不修改同 MAC 的其他 IP。
已发布项若被内核提前移除，适配器仍按发布记录发送失败通知；发现相同 IP 已被
新的 MAC 或远端所有者接管时，不覆盖新项。同步 bridge MAC 前先比较实际值，
避免重复设置相同 MAC 导致 Linux 清空永久邻居并产生无效的删除/重建循环。
运行表通过现有轮询查询；当前没有新增异步订阅事件接口。

验证方法
--------

.. code-block:: bash

   python3 -m unittest discover -s /path/to/vpp/extras/sdn-agent -p 'test_*.py'
   go test ./...
   SDN_VPP_IMAGE=alpine/sdn-agent:keepalive-test \
   SDN_CONTROLLER_IMAGE=alpine/sdn-controller:keepalive-test \
   SDN_WEB_IMAGE=alpine/sdn-web:keepalive-test \
   SDN_VPP_KEEPALIVE=1 SDN_VPP_STACK=1 \
   SDN_VPP_OUTPUT=artifacts/vpp/keepalive/irb4 python3 lab/vpp_irb.py

增加 ``SDN_VPP_UNDERLAY=6`` 可测试 IPv6 VTEP；内层一直同时测试 IPv4/IPv6。
仅运行双栈 IRB 基线及保活验收时，使用 ``SDN_VPP_KEEPALIVE_ONLY=1``；
增加 ``SDN_VPP_WORKERS=2`` 可验证工作线程中的 NA 经主线程 RPC 完成确认。
实验使用隔离 Docker 网络和 VPP 软件端口，不接管宿主机物理网卡。
每次结果以输出目录的 irb.json、irb.log 和 keepalive-idle.json 为准；
单元测试通过不代表物理网卡、规模和性能验收通过。
测试 PC 禁用 RA 并移除 Docker 的 IPv6 默认网关，只经过实验 Leaf；删除和恢复
PC 地址时也恢复实验回程路由。真实浏览器入口为 Web 仓库的
tests/e2e/vpp-keepalive.cjs，使用隔离实验生成的 stack-access.json。

维护机制参考 `RFC 9161 第 3.5 节 <https://www.rfc-editor.org/rfc/rfc9161.html#section-3.5>`_。


IPv6 单播 NA 确认
-----------------

Linux 对单播 NS 的应答可以不携带 Target Link-Layer Address 选项。
ip6-nd 的原路径只在带选项时学习；本次在通过原有校验的 solicited NA
上补充确认路径，通过主线程 RPC 查找相同接口/IP 的已有动态邻居，
仅在以太网源 MAC 匹配时刷新时间。不会从无选项 NA 创建邻居、变更 MAC
或刷新静态/远端项；非 solicited 或错误源 MAC 的报文不能续期。
涉及 src/vnet/ip6-nd/ip6_nd.c 与 ip-neighbor 的 confirm/confirm_dp 接口。
构建预检会检查这项适配，不能只复制 Python 文件替代原生更新。
集成测试从 PC 接收端抓取无 TLLA 的真实 NA，并注入错误源 MAC 的 NA 验证拒绝续期。

原生诊断命令：vppctl show neighbor-keepalive。
协议依据见 `RFC 4861 第 7.2.5 节 <https://www.rfc-editor.org/rfc/rfc4861.html#section-7.2.5>`_。

回归记录（2026-10-27）
--------------------------------

40 项 VPP Python 适配测试、25 项容器/构建测试、17 项 Web 单元测试通过；
Agent、Controller、Gateway 的 Go 测试和 Agent 关键模块 race 检查通过。
真实浏览器验证了显式 false 的候选操作以及双栈运行状态查询。

完整 IRB 回归复测通过 29 项检查，包括 Type-2/Type-5、双栈转发、租户隔离、
资源清理与事务回滚。首次回归曾发生 IPv6 Type-5 到新地址的转发超时，
相同镜像复测未重现，根因尚未定位，不能将复测通过写成该偶发问题已修复。
原始失败与复测报告分别保存在 artifacts/vpp/keepalive/irb-regression
和 irb-regression-r2；后续需要继续跟踪收敛稳定性。

最终保活专项通过：IPv4 VTEP、2 个工作线程和 Controller/Web 联调共 34 项；
IPv6 VTEP 共 32 项。两组均验证单播 ARP/NS、195 秒以上静默存活（MAC 老化
设为 120 秒）、IPv4/IPv6 分别失联撤销及恢复、共享 MAC 保留、错误源 MAC 的
无 TLLA NA 不续期、Undo 恢复，以及关闭开关后双栈跨网段通信。
报告位于 artifacts/vpp/keepalive/final-accept4 和 final-accept6；
validation.json 汇总镜像 ID、测试结果和验证范围。
测试镜像使用 alpine/sdn-agent:keepalive-test、alpine/sdn-controller:keepalive-test
和 alpine/sdn-web:keepalive-test，未覆盖发布标签，未提交或推送代码。
