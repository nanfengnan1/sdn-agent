VPP 数据面适配
==============

本文描述本仓库与 nanfengnan1/vpp 的当前适配实现：FRR 负责协议与选路，
Linux 提供 FRR 可识别的控制面对象，VPP 执行业务转发，Agent 管理事务与查询。
已验证的源码基线为 VPP 26.06 / FRR 10.7.0；可选择构建 ref 不等于任意版本都兼容。
GitHub 构建默认选择 nanfengnan1/vpp 的 sdn/v26.06 分支；实际源码身份以镜像内
source.json 为准，构建与运行验证分别记录。

.. contents:: 本页导航
   :local:
   :depth: 3

实现分工
--------

.. code-block:: text

   Controller / Web / sdnctl
                | typed operation / transaction
           Agent Registry
             /         \
     VPP composite     FRR Driver
       |       |         | vtysh
       |       |         FRR / Zebra
       |       |           | Netlink
       |       +----- Linux VRF / bridge / VXLAN / LCP mirrors
       |                       ^ local MAC/IP | selected FDB/routes
       +---- Unix HTTP --------+-------------v
                     Python VPP adapter
                           | Binary API
               VPP BD / BVI / tenant FIB / VXLAN / FDB / neighbors

internal/adapters/vpp 是单一 composite Driver，内部复用 Linux 执行与 Undo。
VPP 模式不能另外注册 Linux Driver，避免重复写同一个资源。
FRR 的 EVPN / 租户 / Type-5 操作复用现有实现。
Controller / Web 共用 Agent 类型化协议；邻居保活扩展见 :doc:`vpp-keepalive`。Controller 接受双栈 VTEP 和 underlay 邻居，
configureSystem 启用 IPv6 转发；Web 按 Capabilities 显示查询地址族和 VPP 原生字段。
联调覆盖真实浏览器 → Gateway → Controller → Agent → VPP，viewer 查询无需配置权限。

VPP 仓库 extras/sdn-agent/adapter.py 和 irb.py 使用同一源码版本的 vpp_papi。
Unix socket 权限为 0600，配置 RPC 和周期核对串行执行。
它读取 Zebra 安装到受管 Linux VXLAN 的 FDB，不直接消费未选路的 BGP RIB。
零 MAC 项代表 BUM 成员，普通远端 MAC 提供单播引用。VPP 接入端口学习的 MAC
通过内核 bridge FDB 通知 Zebra，不把远端 MAC 反向通告成本地。

irb.py 管理 IPv4/IPv6 FIB、BVI、网关地址和邻居；将内核实际选中的租户路由
同步到自有 FIB source ``sdn-agent/evpn``，支持内联 ECMP 和 nexthop/group 对象。
本地 VPP 邻居必须同时匹配接入侧 FDB，才向 Zebra 发布 MAC/IP。
镜像邻居使用永久项，生命周期由 VPP 学习/老化和 MAC 迁移决定，避免镜像端口
无法探测导致 Linux NUD 反复撤销。远端邻居不得再次发布为本地。

L3VNI 使用标准以太网 VXLAN、Router MAC 和 BVI 完成对称 IRB，
不使用原生 VXLAN ``is_l3`` 代替 EVPN L3VNI。
每个 VNI 创建一个原生多点 VXLAN 接口，无固定 dst。Type-2 MAC/IP、
Router MAC 和 Type-3 只更新接口内的远端映射，不创建逐远端隧道或隐藏接口。
Linux 镜像接入口和 VXLAN 端口由 tc 双向丢包，priority 49150 由适配器保留。
解绑定释放对应过滤器；保留 carrier，避免 Zebra 将 VNI 误归到默认 VRF。
隔离验收会关闭 VPP BVI 并确认 Linux 镜像 up 时仍无法转发。

VPP 源码修改
------------

src/vnet/l2/l2.api、l2_api.c、l2_test.c 新增 l2_egress_flood API 和 VAT 入口，
L2 API 版本为 3.4.0。修改输出 flood class 时，按原 SHG 移除并重新插入 BD 成员，
更新复制列表和 flood_count；拒绝 BVI / multicast-master 转换，使用主线程 barrier。

src/plugins/vxlan 新增 multipoint.c，VXLAN API 版本为 2.3.0。
CLI 不提供 dst 或 v3 API 使用零地址（IPv4 为 0.0.0.0，IPv6 为 ::）创建多点接口。
固定 dst 的模式继续使用原有封装节点。多点模式支持 IPv4/IPv6 underlay 和
以太网载荷（含 IPv4/IPv6），拒绝 raw-IP is_l3。

vxlan_mp_remote_add_del_v2 管理两类独立表项：非零单播 MAC 到远端 IPv4/IPv6，
以及 bum=true、零 MAC 的复制成员。更新使用 worker barrier；MAC 替换后，
删除旧目的的请求不会撤销新目的。vxlan_mp_remote_v2_dump 提供双栈读回。
旧版 IPv4 API 保留兼容；IPv6 不出现在旧版 dump。适配器要求新版 API，
不能单独更新 Python 后继续使用缺少 v2 的原生插件。
适配器从 dump 恢复，先安装新 MAC 映射，再撤销旧映射。

vxlan-mp-encap 按内层目的 MAC 选择 VTEP；未知单播、广播和组播按 BUM
列表复制。它生成 VXLAN/UDP/IPv4 或 IPv6 头，再进入 underlay FIB 的 ip4/ip6-lookup，
因此 underlay 路由变化无需重新创建端点。三层路径先经过 BVI 和租户 FIB，
用远端 Router MAC 经 L3 传输 BD 进入同一封装逻辑。
解封装先匹配固定 dst 隧道，再匹配本地地址、UDP 端口、FIB、VNI 的多点接口。
多点接收不以发送端 MAC 映射作为接收 ACL；需要的 underlay 访问控制应单独配置。

Type-3 撤销只删除对应 BUM 成员；Type-2 映射仍可转发。
所有远端撤销后接口仍保留，直到删除 VNI。旧 l2_egress_flood API 保留兼容，
多点复制不再依赖按远端接口切换 flood class。
多点接口为 SHG 1，禁止远端间反射与远端数据面学习。
远端 MAC 使用可移动动态项，避免静态项阻止主机迁移。

输出重新解析真实内层 Ethernet/VLAN/IP 头，软件完成校验和。TCP GSO 经原生分段
辅助函数生成普通帧再进行单播/BUM；IPv6 扩展头的传输偏移、长度和校验和另行修正。
src/plugins/tap/rx_node.c 补上 IPv6 扩展头遍历，保留 TAP 的 NEEDS_CSUM 请求；
否则 TCP SYN 的部分校验和会在进入 VXLAN 前丢失完成标记。
IPv4 外层 UDP checksum 为零，IPv6 外层校验和必须有效。输入清理外层 L4 标记并
重新验证 UDP，不能混用 NIC 的内外层校验结果。固定 IPv4 RX flow 规则不适用于多点，
因此明确拒绝；RX LRO、硬件 VXLAN TSO、DPDK 吞吐和大规模复制仍需专门验证。

BD ID 为设备名的稳定 24 位映射，与 VNI 分开，BD / 隧道使用 sdn-agent/ ownership tag。
不同 owner 的 ID / 隧道键冲突、抢占非受管 BD 端口都会失败。
VPP 接口索引每次通过 LCP pair 查询，不作为永久身份。

FRR、Linux、VPP 如何通信
------------------------

这套实现有三条不同的通道。Netlink 用于内核状态同步，TAP/TUN 用于报文，
Binary API 用于原生 VPP 对象操作；不能把这三者当作同一条通信链路。

.. list-table:: 通信边界
   :header-rows: 1
   :widths: 25 30 45

   * - 两端
     - 接口
     - 作用
   * - Controller → Agent
     - gRPC / typed operations
     - 配置租户、BD、VXLAN、协议；提交事务与查询运行状态。
   * - Agent → FRR
     - vtysh
     - 配置 BGP/EVPN、RD/RT、L3VNI 和显式 Type-5；回读配置和协议状态。
   * - bgpd ↔ Zebra
     - FRR 内部 ZAPI
     - bgpd 选路，Zebra 收集接口/VNI/邻居，并安装有效路由与 FDB。
   * - Zebra ↔ Linux 内核
     - Netlink
     - 实际 Linux 接口、bridge、VRF、VXLAN、邻居、路由和 FDB。
   * - Linux 内核 → linux_nl
     - Netlink 订阅与重同步
     - LCP 配对接口的地址、邻居、普通 IP 路由等；不是完整 EVPN 适配器。
   * - Agent → 本地适配器
     - Unix HTTP socket
     - 校验、下发、读回和 Undo；适配器另外按周期核对 EVPN 运行状态。
   * - 适配器 → VPP
     - vpp_papi / Binary API socket
     - 租户 FIB、BD/BVI、FDB、邻居、原生多点 VXLAN 及远端成员。
   * - Linux 网络栈 ↔ VPP
     - LCP 的 TAP/TUN 与 punt/xc 节点
     - BGP/OSPF 等控制报文和 Linux 本机流量；Netlink 不携带这些报文。

当前没有另外启动 FPM server，也没有把 Zebra 改成直接调用 VPP API。
虽然 FRR 构建启用了 FPM 能力，该编译选项不代表本方案在使用 FPM。
适配器读取受管 Linux 镜像上的实际选中状态，不能直接把所有 BGP RIB 候选
都装进 VPP。FDB、邻居和路由分别负责 MAC→VTEP、IP→MAC、前缀→下一跳。

.. code-block:: text

   本地 PC 发包
       -> VPP access FDB / BVI neighbor 学习
       -> adapter 发布本地主机到 Linux bridge FDB / neighbor
       -> Zebra -> bgpd -> EVPN Type-2 通告

   远端 EVPN 更新
       -> bgpd 选路 -> Zebra 安装 Linux FDB / neighbor / tenant route
       -> adapter / irb 核对有效状态
       -> VPP MAC-to-VTEP / BUM / tenant FIB / ARP-ND termination

   Linux underlay 配置或 FRR 安装的普通 IP 路由
       -> 内核 Netlink -> linux_nl -> VPP underlay FIB / interface / neighbor

Linux 控制面镜像不是业务旁路。受管 bridge/VXLAN 相关镜像采用 tc ingress/egress
priority 49150 隔离业务数据；LCP underlay 控制通道仍保留。
普通 TAP 不会因为名称为 vx50000 就向 Zebra 宣告自己是 VXLAN。
必须由适配器创建真实的 Linux VXLAN 控制对象，填写 VNI、本地 VTEP、bridge/VRF
归属等信息，再维护对应的 VPP 对象。LCP 本身不会自动镜像任意 BD/VRF/VXLAN。

Linux 对象与 VPP 对象映射
-------------------------

.. list-table:: 以 TENANT-A 为例
   :header-rows: 1
   :widths: 24 34 42

   * - Linux / Agent 对象
     - VPP 对象
     - 说明
   * - uplink / access0
     - LCP pair 对应的 phy sw_if_index
     - phy 表示实际 VPP 侧接口，也可以是软件 TAP，并非只能是物理网卡。
   * - TENANT-A，table 1001
     - IPv4/IPv6 tenant FIB，table ID 1001
     - 内层 IP 选路；两台 Leaf 的表是各自独立的实例。
   * - br10 / br20
     - 业务 BD + 网关 BVI
     - BD 交换以太帧，BVI 连接该 BD 与租户 FIB，承载网关 IP/MAC。
   * - br50000
     - 传输 BD + 无编号 BVI
     - BVI 使用本机 Router MAC，绑定租户 FIB；无需配置业务网关 IP。
   * - vx10010 / vx10020
     - 本地业务 L2VNI 的多点 VXLAN 接口
     - 挂接相应业务 BD，二层延伸取决于远端成员和 RT。
   * - vx50000
     - L3VNI 50000 的多点 VXLAN 接口
     - 挂接传输 BD，通过 Router MAC 选择远端 VTEP。

br50000 是逻辑设备名，50000 是该示例的 VNI；原生 BD ID 由稳定映射生成，
不能据此断定 VPP 数值 BD ID 也是 50000。table ID、VNI、BD ID、Linux ifindex、
VPP sw_if_index 是不同命名空间。接口索引会在重建后变化。

LCP 接口配对和节点
------------------

一个 TAP 型配对包含三个身份：VPP 实际接口、VPP 内部的 TAP 接口、Linux TAP netdev。
后两个是同一条主机通信通道的两侧，不能只凭 tap4096 等名字判断其用途。

.. code-block:: text

   Linux namespace                      VPP graph
   FRR / ping / Linux sockets
          |
   Linux uplink (ifindex)
          | TAP fd / vhost-net queues
   VPP host-side TAP (sw_if_index)
          | linux-cp-xc-* / punt / ARP features
   VPP vf0 / host-eth1 (phy sw_if_index)
          |
       VF / 外部链路

TAP 带 Ethernet 头；TUN 承载三层报文，使用 LCP 的 L3 路径。
pair 显式创建，不能只创建 Linux TAP 就期待 LCP 自动知道目标 VPP 端口。
本机 PC 接入的 TAP 则是另一条通道：VPP TAP 的 Linux 端位于 PC netns，
并非 FRR underlay 的镜像接口。实验还给该 VPP 接入口建立独立 LCP 控制镜像。

以下名称按本地 26.06 源码核对。LCP 处理报文的节点都是 INTERNAL；
Netlink process 是 PROCESS，用来等待控制事件，不属于逐包转发节点。

.. list-table:: 关键节点与职责
   :header-rows: 1
   :widths: 29 15 56

   * - 节点
     - 类型
     - 功能与方向
   * - dpdk-input / tap-input
     - INPUT
     - 分别从 DPDK RX 队列、TAP/vhost 队列收包；它们属于设备插件，不是 LCP 节点。
   * - linux-cp-xc-ip4 / linux-cp-xc-ip6
     - INTERNAL
     - TAP 收到 Linux 发出的 IP 包；依据 L2 rewrite 找配对接口的 adjacency，进入对应 IP output features。
   * - linux-cp-xc-l3-ip4 / linux-cp-xc-l3-ip6
     - INTERNAL
     - TUN 的三层出方向；P2P 使用配对 adjacency，P2MP 可进入对应 phy 的 FIB lookup；标记本地产生避免多减 TTL。
   * - linux-cp-xc-mpls
     - INTERNAL
     - 从主机向配对实际接口发送 MPLS；存在节点不代表本实验已验收 MPLS。
   * - linux-cp-punt
     - INTERNAL
     - 实际接口上的 punt 报文送到 host-side 接口；TAP 恢复 Ethernet 头，找不到 pair 时丢弃。
   * - linux-cp-punt-xc
     - INTERNAL
     - 按报文来自 phy 还是 host 查 pair，选择主机或实际接口，处理相应非普通 IP 通道。
   * - linux-cp-punt-l3
     - INTERNAL
     - ip4/ip6-punt feature 上为 TUN 调整 locally-originated 标记，再继续 punt redirect，避免额外 TTL 检查。
   * - linux-cp-arp-phy
     - INTERNAL
     - 实际接口 ARP feature；当前源码把 ARP reply 副本送主机，原包继续 feature 链，不能理解为复制全部业务帧。
   * - linux-cp-arp-host
     - INTERNAL
     - 主机发来的 ARP 经配对关系发往实际接口。
   * - linux-cp-netlink-process
     - PROCESS
     - 等待、批处理和重同步 Netlink 路由事件；不是收发业务帧的 TAP 节点。

具体 graph 还经过 ethernet-input、ip4/ip6-input、punt redirect、interface-output
和设备输出节点；feature 开关、TAP/TUN、是否 local/punt 会改变路径。
没有“全部 VF 包先送 Linux、再回 VPP”的固定流程。普通业务包直接由 VPP 查表转发。

.. code-block:: text

   # VPP：未受 Agent 管理的测试端口示例
   set interface state vf0 up
   lcp create vf0 host-if uplink
   lcp lcp-sync on
   show lcp

.. code-block:: bash

   # 同一节点的 Linux namespace
   ip link set uplink up
   ip addr add 10.0.1.2/30 dev uplink
   ip route add 10.255.0.12/32 via 10.0.1.1 dev uplink
   vppctl show interface address
   vppctl show ip fib
   vppctl show ip neighbors

Linux 发出的 BGP TCP179 报文走 uplink TAP → tap-input → linux-cp-xc-ip4/ip6
→ VF 输出；收到需交 Linux 的控制报文则经 local/punt 和配对 host-side TAP 进入内核。
准确的 punt 分类和插件组合需要现场 trace，不能认为任意报文都有默认 punt。
上游通用 LCP 文档建议由 Linux 处理 ARP/ND/ping；本适配器的租户 BVI 通道不同，
网关 ping 和业务邻居由 VPP 处理，因此不能照搬建议把所需 ping 插件全局关闭。
租户内部 BGP/OSPF 到 Linux 的 punt 通道尚未实现。

二层与跨网段报文流程
--------------------

同一 L2VNI 的通信
~~~~~~~~~~~~~~~~~

.. code-block:: text

   PC -> access TAP -> 业务 BD
      -> 目的 MAC 查 L2 FDB -> 多点 VXLAN 接口
      -> 目的 MAC 查远端 VTEP -> VXLAN 封装
      -> 外层 underlay FIB -> VF -> Spine IP 转发
      -> 远端 VF -> VXLAN 解封装 -> 业务 BD -> access TAP -> PC

已知单播用 MAC→VTEP 表；BUM 按 Type-3 成员复制。ARP/ND 抑制命中时可本地应答，
未命中按业务策略处理。业务帧目标为网关 MAC 时进入 BVI，转为下述租户路由流程。

不同网段的对称 IRB
~~~~~~~~~~~~~~~~~~

参考组网为 /home/alexan/vxlan-s-evpn/docs/从零手动配置.txt 第 10.5 节。
下表把该文件需要的配置列出，读者不需要访问这份本地文件才能理解拓扑。

.. list-table:: 精简 IRB 基准
   :header-rows: 1

   * - 项目
     - Leaf1
     - Leaf2
   * - PC / 网关
     - 192.168.10.11/24 / 192.168.10.254
     - 192.168.20.12/24 / 192.168.20.254
   * - 业务 BD / L2VNI
     - br10 / 10010
     - br20 / 10020
   * - 业务 L2 RT
     - 65000:10010
     - 65000:10020
   * - 租户 / FIB / L3VNI / L3 RT
     - TENANT-A / 1001 / 50000 / 65000:1
     - TENANT-A / 1001 / 50000 / 65000:1
   * - 参考文件 VTEP / Underlay
     - lo 10.255.0.11；uplink 10.0.1.2/30
     - lo 10.255.0.12；uplink 10.0.2.2/30
   * - Spine 对端地址
     - 10.0.1.1/30
     - 10.0.2.1/30

参考文件用 OSPF 发布 Loopback，Spine lo=10.255.0.1，AS65000 的 iBGP EVPN
通过 Loopback 建立，Spine 为 RR。Leaf1 无需创建远端 br20，Leaf2 无需创建 br10；
业务 L2 RT 不同，租户 L3 RT 相同。先让各 PC 访问网关，触发本地 MAC/IP 学习。
Type-2 导入对端 /32 主机路由即可跨网段转发；基础组网不要求启用 Type-5。

.. code-block:: text

   Leaf1 发往 Leaf2：
   PC1 -> TAP -> 业务 BD(br10) -> 网关 BVI
       -> TENANT-A FIB 查 192.168.20.12
       -> 传输 BVI -> 传输 BD(br50000)
       -> 远端 Router MAC 查 FDB -> VXLAN port(L3VNI 50000)
       -> Router MAC 查远端 VTEP -> 封装 VXLAN/UDP/IP
       -> underlay FIB 查外层目的 VTEP -> VF

   Spine：只查外层 IP 并转发，无租户 VRF，无 VXLAN 解封装。

   Leaf2 接收：
   VF -> VXLAN 解封装 -> 传输 BD(br50000)
      -> 命中本机 Router MAC -> 传输 BVI
      -> TENANT-A FIB 查内层目的 192.168.20.12
      -> 网关 BVI -> 业务 BD(br20) -> TAP -> PC2

BVI 是 BD 和租户路由表之间的连接点。远端方向先形成以 Router MAC 为目的的内层
以太帧，再从传输 BD 的 VXLAN 端口封装；接收方依靠本机 Router MAC 进入租户三层。
L3VNI 仍然承载 Ethernet，不是把 VXLAN 改成 raw-IP 隧道；创建时只给 src 也不意味着
线上报文没有 dst，dst 由动态远端表选择。内层与外层查的是两个不同用途的 FIB。

本次 VF 抓包与上述设计的关系
~~~~~~~~~~~~~~~~~~~~~~~~~~~~

已完成的 VF 验收使用静态 underlay 路由、接口地址 VTEP 192.0.2.0/192.0.2.2，
IPv6 运行使用 fd80:3101::2/fd80:3102::2；此外添加 br100/VNI10100 测二层延伸。
不能把这些结果写成已验收参考文件的 OSPF+Loopback 组合。

实际 Leaf1 FIB table1001 的 192.168.20.12/32 经 br50000 的 bvi0，
FDB 把 Leaf2 Router MAC 02:00:00:00:ff:02 指向 vx50000；Leaf2 的本地主机
192.168.20.12 经 br20 的 bvi1。Spine RX 抓包中，PC1↔PC2 的 3,330 个 IPv4
跨网段报文全部使用 VNI50000：外层 192.0.2.0↔192.0.2.2，内层
192.168.10.11↔192.168.20.12，内层 MAC 为两端 Router MAC。
关闭 Spine VF 子接口会中断业务，恢复后重新连通。完整边界见下方物理 VF 验收。

源码仓库、版本选择与镜像一致性
------------------------------

默认源码仓库为 https://github.com/nanfengnan1/vpp.git 和
https://github.com/nanfengnan1/frr.git，默认 ref 分别为 sdn/v26.06 与 frr-10.7.0。
Dockerfile 的 source-git 阶段在镜像构建环境中
获取指定 ref，保留 tag 历史用于 VPP 的 git describe；不修改宿主机工作区。
source-local 阶段使用显式 named build context，允许尚未提交的本地开发源码。

.. list-table:: 构建参数
   :header-rows: 1
   :widths: 28 30 42

   * - 参数
     - 默认值
     - 含义
   * - BUILD_BASES
     - 0
     - 1：先从源码构建 FRR/VPP，再集成 Agent；0：复用匹配的基础镜像。
   * - VPP_REPOSITORY / FRR_REPOSITORY
     - 上述 nanfengnan1 仓库
     - Git 来源；仅 source-git 使用，记录到来源元数据。
   * - VPP_REF
     - sdn/v26.06
     - 包含适配代码的 tag、分支或 commit；不能用未打补丁的上游 tag 替代。
   * - FRR_REF
     - frr-10.7.0
     - FRR 源码版本，支持 tag、分支或 commit。
   * - VPP_SOURCE / FRR_SOURCE
     - 空
     - 各自独立指定含 .git 目录的完整本地 clone，优先于远端 ref；不自动 checkout，不支持 linked worktree。
   * - VPP_IMAGE / FRR_IMAGE
     - alpine/vpp:sdn-base / alpine/frr:sdn-base
     - 生成或复用的基础镜像名，仅改标签不会改变源码版本。
   * - AGENT_IMAGE
     - alpine/sdn-agent:vpp-dev
     - 最终集成镜像名。
   * - ALPINE_VERSION / BUILD_JOBS
     - 3.22 / 12
     - 两个基础镜像一致的发行版版本、编译并发数；新组合需重新验收。
   * - SOURCE_CACHE_BUST
     - 0
     - 使用可移动分支时更改该值以重新抓取；可重复发布优先固定完整 commit。

推荐固定 commit：选择版本是选择源码，不能只把输出镜像改名为某个版本。
源码阶段检查 adapter.py、irb.py、多点 VXLAN v2 API、l2_egress_flood 等必要文件，
缺失则在编译前失败；该检查不是跨版本 ABI/依赖兼容认证。
脚本与 VPP 基础 Dockerfile 的默认分支一致；直接使用 Dockerfile 也会选择 sdn/v26.06。

.. code-block:: bash

   # 使用远端默认分支，避免环境中的本地源码路径覆盖 Git 模式。
   cd ~/program/github/sdn-agent
   unset VPP_SOURCE FRR_SOURCE
   VPP_REF=sdn/v26.06 \
   FRR_REF=frr-10.7.0 BUILD_BASES=1 \
     sh docker/build-vpp.sh

   # 分支更新后重新抓取，或将 VPP_REF 替换为要发布的完整 commit。
   BUILD_BASES=1 SOURCE_CACHE_BUST="$(date +%s)" sh docker/build-vpp.sh

   # 尚未提交时：使用两个本地工作区，不切换分支、不修改代码。
   VPP_SOURCE=/home/alexan/program/subversion/vpp \
   FRR_SOURCE=/home/alexan/program/subversion/frr BUILD_BASES=1 \
     sh docker/build-vpp.sh

   # 已用新流程生成两个基础镜像后，仅重建 Agent。
   BUILD_BASES=0 VPP_IMAGE=alpine/vpp:sdn-base FRR_IMAGE=alpine/frr:sdn-base \
     sh docker/build-vpp.sh

直接使用 Dockerfile 时，对应参数为 VPP_SOURCE_MODE=git|local、
FRR_SOURCE_MODE=git|local，以及同名 REPOSITORY / REF / ALPINE_VERSION / BUILD_JOBS。
local 模式还需 --build-context vpp_source=/path 和 vpp_git=/path/.git；
FRR 对应 frr_source 和 frr_git。脚本自动传入这两类上下文，避免源码自己的
.dockerignore 排除 .git 后丢失版本历史；不要修改 FRR 上游忽略规则。
需要 Docker BuildKit 的 named context 和多阶段构建支持。

VPP 基础镜像同时打包原生库、API JSON、匹配的 vpp_papi 与 extras/sdn-agent。
Agent Dockerfile 从这个基础镜像复制整组文件，不再另读 VPP_SOURCE，避免分支移动或
不同 checkout 造成 API 与 Python 代码错配。旧基础镜像不含这些文件时，需先重建基础镜像。
元数据在 /usr/share/vpp/source.json 和 /usr/share/frr/source.json，记录实际 commit、
请求的 ref、dirty 标志、未跟踪文件 SHA256；source-commit 和源码差异另行保存。
本地 ref 字段只作记录，实际使用的是本地工作区内容。完整复现仍需保存/提交修改源码。

.. code-block:: bash

   docker run --rm --entrypoint cat alpine/sdn-agent:vpp-dev /usr/share/vpp/source.json
   docker run --rm --entrypoint cat alpine/sdn-agent:vpp-dev /usr/share/frr/source.json
   docker run --rm --entrypoint vpp alpine/sdn-agent:vpp-dev -v

构建入口不会创建 tag、提交或推送仓库，也不会重启容器或修改物理网卡。

2026-10-27 GitHub 分支构建记录
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

在 Linux Docker 宿主机 10.8.126.103 完成 Git 模式的两个基础镜像及 Agent 集成构建，
没有使用 VPP_SOURCE / FRR_SOURCE 本地源码覆盖。实际来源为：

.. list-table:: 本次构建的源码版本
   :header-rows: 1
   :widths: 15 20 65

   * - 组件
     - 请求 ref
     - 实际 commit
   * - nanfengnan1/vpp
     - sdn/v26.06
     - 194ee9f7db669e9776dd7e0fd9845ef2d9dd8031
   * - nanfengnan1/frr
     - frr-10.7.0
     - 87fe21fda92ce9e2ba3eaf2b0a327bf71ee183ef

输出 alpine/vpp:sdn-base、alpine/frr:sdn-base 和 alpine/sdn-agent:vpp-github-test。
集成镜像 ID 为 ab0296d3525d280ae7636fffa0ead0f7ab2a7063cb28412e7b348db1e3dc9e17。
来源元数据均 dirty=false；vpp -v 带 dirty 后缀是因为构建内应用了 Alpine 兼容补丁，
补丁保存在 /usr/share/vpp/alpine-compat.patch，不代表使用了宿主机未提交源码。
PAPI / adapter 导入与原生多点 API 定义检查通过。

该镜像完成四组隔离软件验收，均未挂载宿主机适配脚本：IPv4 / IPv6 underlay
各运行一次三 Leaf EVPN（每 Leaf 两个 worker）和双 Leaf 对称 IRB。
二层报告各 23 个布尔检查、IRB 报告各 29 个布尔检查通过，覆盖原生多点 API、
双栈主机通信、MAC 移动、Type-2/3/5、租户隔离、回滚和撤销；
IRB 包含连续 40 秒转发零丢包检查。本次没有重复物理 VF、硬件卸载或吞吐验收。
另有 25 项 Docker 单元测试及 VPP Dockerfile BuildKit 静态检查通过。

构建和实验报告位于 artifacts/vpp/github-sdn-v26.06-20261027-201038/。
后续分支移动不能沿用这个提交的验收结论；发布时固定以上 commit 或重新验证新提交。

事务和恢复
----------

Prepare 保存 Linux Undo 和期望模型；Agent 持久日志后才 Apply。
Apply 修改 Linux 镜像和 VPP，Verify 读回两侧；失败反向恢复，
任一侧失败保留回滚失败状态。适配器第一次写入前原子保存 desired / previous 并 fsync，
重启遇到中断写入时暂停周期核对，等待 Agent 使用原 Undo 恢复。
状态版本 2 可读取版本 1 的 L2 模型与历史 Undo。BVI 使用 ownership tag，
租户 FIB 使用名称标识，拒绝复用其他 owner 的表；删除时先撤销自有路由、
移除 BVI/地址绑定，再删除空闲租户 FIB。
TENANT_L3VNI 由 composite 与 FRR 共同处理，其 Linux 部分没有重复写命令。

FRR 10.7 的 ``% Configuration applied with notes:`` 是成功提交提示。
执行器只放行这一精确提示，继续检查后续错误行，并读回实际配置。

internal/agent/binding.go 将状态绑定到 linux 或 vpp，
拒绝带旧配置切换后端，也拒绝未完成事务缺少原执行后端。
主机重建的完整恢复边界见 :doc:`用户说明 </user/vpp>`。

构建和测试
----------

docker/build-vpp.sh 支持 Git ref 与本地源码上下文；BUILD_BASES=1 依次使用
docker/vpp/Dockerfile.frr、Dockerfile.base 构建基础镜像，
最后使用 docker/Dockerfile.vpp 集成。基础镜像默认均为同架构 Alpine 3.22，
不能混入 glibc 动态库。默认输出 vpp-dev 开发标签，不覆盖 v1.0。

VPP 构建读取分支声明的 DPDK 版本和校验和，包含 musl 插件加载修正，
关闭依赖 glibc 的 VCL。排除 cnxk / dpaa mempool 驱动以保留 VPP mempool ops 槽位，
检查注册失败；libdaq 为 3.0.21。基础镜像记录源码提交与工作区 patch，
发布时还应保留这两个仓库的修改源码。

.. code-block:: bash

   # Agent 仓库
   go test ./...
   go vet ./...
   python3 -m unittest discover -s docker -p '*_test.py'
   python3 -m unittest discover -s lab -p 'test_vpp_sriov.py'
   python3 lab/vpp_evpn.py
   # 三 VTEP，同一接口转发到两个远端，并发 worker 下更新远端成员
   SDN_VPP_WORKERS=2 python3 lab/vpp_evpn.py
   python3 lab/vpp_irb.py
   SDN_VPP_UNDERLAY=6 SDN_VPP_WORKERS=2 python3 lab/vpp_evpn.py
   SDN_VPP_UNDERLAY=6 python3 lab/vpp_irb.py
   python3 lab/vpp_offload.py

   # VPP 仓库
   python3 -m unittest discover -s extras/sdn-agent -p 'test_*.py'

集成验收需要 Linux Docker、/dev/net/tun、/dev/vhost-net 和已构建镜像。
vpp_evpn.py / vpp_irb.py 可用 SDN_VPP_IMAGE 选择待验收镜像，
SDN_VPP_OUTPUT 指定独立报告目录，避免覆盖以前的实验结果。例如：

.. code-block:: bash

   SDN_VPP_IMAGE=alpine/sdn-agent:vpp-github-test \
   SDN_VPP_OUTPUT="$PWD/artifacts/vpp/github-check/evpn4" \
   python3 lab/vpp_evpn.py

开发可设置 SDN_VPP_SOURCE 挂载适配脚本，最终验收必须去掉覆盖，
确保检查实际打进镜像的代码。该测试不代表物理网卡吞吐、HA 或规模验证。
vpp_offload.py 创建真实 GSO TAP，验证双向 2 MiB TCP 内容、分段计数、
内外层抓包校验和及 UDP 错包拒绝，不依赖 Docker 内核 VXLAN 转发。

完整 Web 联调需三个仓库位于同级目录。先构建配套镜像：

.. code-block:: bash

   cd ~/program/github
   docker build -f sdn-controller/docker/Dockerfile -t alpine/sdn-controller:vpp-dev .
   docker build -f sdn-web/docker/Dockerfile -t alpine/sdn-web:vpp-dev .
   cd sdn-agent
   SDN_VPP_STACK=1 SDN_VPP_BROWSER=1 SDN_VPP_UNDERLAY=6 SDN_VPP_WORKERS=2 \
     python3 lab/vpp_evpn.py

该模式临时发布随机 Web 端口，并在 stack-access.json 写入权限 0600 的临时测试令牌。
看到 READY 后，另一终端设置 SDN_TEST_ACCESS 指向该文件，运行
sdn-web/tests/e2e/vpp-query.cjs。安装 playwright-core，使用 PLAYWRIGHT_MODULE
指定模块路径、CHROME_BIN 指定本机 Chrome；跨主机时把 access 文件的 url 改为可达地址。
浏览器测试结果写回同目录，实验脚本继续完成撤销测试并清理容器与临时令牌。
不需要浏览器时省略 SDN_VPP_BROWSER，仍会验证真实 HTTP/gRPC 查询链路。

当前每秒全量核对，事件驱动同步、批量 API、多归属、租户协议 punt 通道和
全量冷启动恢复仍待实现。ARP/ND 主动保活见 :doc:`vpp-keepalive`；物理 NIC 性能和大规模收敛仍需专项验证。

华为物理 VF 实验
----------------

这组脚本会改变指定测试网卡的 PCI 绑定，先确认 PF 没有承担管理连接。
它针对 10.8.126.103 上的华为 HiNIC3，不能直接套用到任意服务器。
测试 PF 为 ``0000:3b:00.0``，当前 ``sriov_totalvfs=3``；管理口为 eno4，
PCI 地址 ``0000:18:00.3``。PF 留在内核驱动提供 VF mailbox / MAC / VLAN / trust，
三个 VF 各自绑定 vfio-pci 并传入独立容器。该厂商驱动包注册的 PCI 驱动名
是 hisdk3，hinic3 是网络上层模块；不能把模块名直接当成 PCI driver_override。

.. code-block:: text

   PC1 -- VPP TAP -- Leaf1 (VF 0000:3b:00.2, access VLAN 3101)
                             |
                    NIC internal switching
                             |
                      Spine (VF 0000:3b:00.3)
                       vf0.3101 / vf0.3102
                             |
                    NIC internal switching
                             |
   PC2 -- VPP TAP -- Leaf2 (VF 0000:3b:00.4, access VLAN 3102)

每个 PC 有两个 VPP TAP 端口，分别测试公共 L2VNI 和不同网段的对称 IRB。
PC 使用 Docker network=none；TAP 的 Linux 端直接创建在 PC 的 netns。
Leaf / Spine 的 Docker 网络只负责管理；业务必须经过 VF 和 VPP。
两个 VLAN 需要网卡内部 VF 转发支持，不以“能创建 VF”代替连通验证。
本机默认 CPU 为 main/worker 18/20、22/24、26/28，使用 1G hugepage；
换机器前按 NUMA、可用 CPU、IOMMU 分组和 hugepage 情况调整脚本配置。

先按上文构建匹配的 VPP 与 Agent 镜像，再执行：

.. code-block:: bash

   cd ~/program/github/sdn-agent
   python3 lab/vpp_sriov_host.py prepare artifacts/vpp/sriov/host-state.json \
     --pci 0000:3b:00.0 --owner vpp-26-06
   python3 lab/vpp_sriov.py --offload --vtep-family 4 \
     --out artifacts/vpp/sriov/ipv4

   # 上一次测试默认已恢复 PF 和原容器，IPv6 测试需重新准备。
   python3 lab/vpp_sriov_host.py prepare artifacts/vpp/sriov/host-state.json
   python3 lab/vpp_sriov.py --offload --vtep-family 6 --stack \
     --out artifacts/vpp/sriov/ipv6

--offload 包含 EVPN/TAP 测试；--stack 需要配套 Controller/Web 开发镜像，
检查三个物理 VF 节点的真实 HTTP/gRPC 查询链路。设置 SDN_VPP_BROWSER=1
可在 READY 时执行上文浏览器用例。临时令牌只保存在受限文件中，结束后删除。
省略 --evpn / --offload 时，仅做底层 VF 的双栈邻接与跨 Spine 路由探测。

脚本退出时删除其创建的容器和管理网络，然后恢复原 PF 绑定、autoprobe、
NetworkManager 管理状态和原 VPP 容器。原本已有 VF 时拒绝接管。
--retain-host 用于连续实验，仅保留已准备的 PF/VF，不保留实验容器；
此模式结束或进程被强制终止后，确认没有本次实验容器占用 VF，再执行：

.. code-block:: bash

   python3 lab/vpp_sriov_host.py restore artifacts/vpp/sriov/host-state.json
   docker exec vpp-26-06 vppctl show hardware-interfaces
   ip route get 10.8.126.1

结果目录包含 report.json、命令日志、原生硬件/接口/FIB/trace、Agent 查询、
Spine VF RX 的 spine-vf-rx.pcap；抓包校验包括 VLAN 解包、内层 TCP、IPv4 头和
IPv6 外层 UDP。测试同时要求分段计数和双向 2 MiB 内容哈希通过。
关闭 Spine VF 子接口必须中断业务，恢复后必须重新连通。

硬件测试发现并修复了两项启动问题：集成镜像缺少 DPDK 动态依赖；
VPP 的 net_hinic3 驱动描述缺失，创建 VLAN 子接口时解引用空指针。
镜像构建现在检查 DPDK 共享库依赖，VPP 增加驱动描述及空指针保护。
这组验收覆盖同一 NIC 内部 VF 转发；不代表外部物理链路、硬件 VXLAN TSO、
RX LRO、RSS 多队列或生产性能通过。

2026-10-27 本机验证结果：IPv4 VTEP 30 项、IPv6 VTEP 31 项通过，
Spine RX 分别校验 7,023 / 7,084 个 VXLAN 报文；真实浏览器 6 项查询通过。
物理实验验证 Type-2/Type-3 和对称 IRB；Type-5 的验收仍见软件实验报告。
详细结果索引为 artifacts/vpp/sriov/validation.md。
