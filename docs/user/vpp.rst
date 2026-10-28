VPP + FRR 模式
====================

这是面向实验验证的 **EVPN + 对称 IRB** 数据面。集成镜像包含 Agent、Redis、FRR、
VPP 和本地适配进程，原有 Linux 模式保持默认；已验收基线为 FRR 10.7.0 / VPP 26.06。
支持 IPv4/IPv6 VTEP、每个 VNI 一个原生多点接口、租户独立 FIB、BVI 网关、L3VNI、
Type-2 MAC/IP、Type-3 BUM、Type-5 前缀和远端 ARP/ND 抑制表同步。
暂不支持 EVPN 多归属、在线数据面切换。
这不是生产性能或高可用认证。

配置选择
--------

在 agent.conf 设置 **agent.dataplane** 为 **linux** 或 **vpp**，重启容器后生效。
VPP 模式要求 agent.frr=true。示例为 docker/vpp/agent.conf。
同一份持久化状态不能切换数据面，请为不同节点使用独立的状态和 Redis 目录并重新下发配置。

vpp.startupConfig 指定启动文件；vpp.apiSocket 必须与其中的 socksvr.socket-name 一致。
adapterSocket 是容器内权限 0600 的 Unix socket，不需要发布端口。

构建和隔离验收
--------------

在 Linux Docker 宿主机执行。VPP 源码需包含本次 l2_egress_flood API 和
extras/sdn-agent；未修改的 VPP 26.06 镜像不能替代它。

默认源码为 nanfengnan1/vpp 的 **sdn/v26.06 分支**，以及 nanfengnan1/frr 的
frr-10.7.0 tag。该 VPP 分支包含原生多点 VXLAN 与 SDN 适配器；上游 v26.06 tag
不能替代它。支持独立选择其他兼容 tag、分支或 commit，发布时推荐固定完整 commit。

.. code-block:: bash

   cd ~/program/github/sdn-agent
   # 默认 VPP_REF=sdn/v26.06，FRR_REF=frr-10.7.0
   BUILD_BASES=1 sh docker/build-vpp.sh

   # 分支有新提交时刷新源码构建缓存；使用自选标签保存新镜像
   BUILD_BASES=1 SOURCE_CACHE_BUST="$(date +%s)" \
     AGENT_IMAGE=alpine/sdn-agent:vpp-dev sh docker/build-vpp.sh

源码还在本地开发时，使用含 .git 目录的完整本地 clone，无需先提交；
linked worktree 和没有 Git 历史的源码压缩包不适用于本地构建入口：

.. code-block:: bash

   cd ~/program/github/sdn-agent
   VPP_SOURCE=/home/alexan/program/subversion/vpp \
   FRR_SOURCE=/home/alexan/program/subversion/frr \
   BUILD_BASES=1 sh docker/build-vpp.sh

   python3 lab/vpp_evpn.py
   python3 lab/vpp_irb.py
   SDN_VPP_UNDERLAY=6 SDN_VPP_WORKERS=2 python3 lab/vpp_evpn.py
   SDN_VPP_UNDERLAY=6 python3 lab/vpp_irb.py
   python3 lab/vpp_offload.py
   cat artifacts/vpp/integration.json
   cat artifacts/vpp/irb.json

默认构建生成 alpine/vpp:sdn-base、alpine/frr:sdn-base 和 alpine/sdn-agent:vpp-dev。
VPP_IMAGE / FRR_IMAGE / AGENT_IMAGE 可改输出名字；改镜像标签不会改变源码版本。
VPP_REF / FRR_REF 才选择远端源码版本，VPP_SOURCE / FRR_SOURCE 分别优先选择本地内容。
要验证 GitHub 分支时，确保没有设置 VPP_SOURCE / FRR_SOURCE，避免实际使用本地工作区。
已有新流程生成的匹配基础镜像时，设置 BUILD_BASES=0 即可只重建集成镜像，
不再要求 VPP_SOURCE。旧 VPP 基础镜像缺少随镜像打包的适配器时，需要先重建一次。
API JSON、vpp_papi 和适配器都从同一个 VPP 基础镜像复制，避免版本混用。
实际源码 commit、请求 ref、dirty 和未跟踪文件哈希分别在镜像的
/usr/share/vpp/source.json、/usr/share/frr/source.json。
此流程不创建 Git tag、不推送镜像。实现和构建细节见 :doc:`开发说明 </developer/vpp>`。

2026-10-27 已从 GitHub 的 VPP 提交 194ee9f7d 与 FRR frr-10.7.0 完成镜像构建，
并通过 IPv4 / IPv6 底层各一组二层 EVPN 和对称 IRB 验收。测试镜像为
alpine/sdn-agent:vpp-github-test，具体 commit、镜像 ID 和验证边界见开发说明。
vpp_evpn.py / vpp_irb.py 可设置 SDN_VPP_IMAGE 选择镜像、SDN_VPP_OUTPUT
指定独立报告目录；未设置时沿用下方默认路径。

二层验收创建三个 Leaf、三个 PC 和独立 Docker 网络，使用 VPP AF_PACKET 软件接口，
通过真实 Agent 事务配置。它不使用物理网卡、PCI、hugepages 或对外端口，
结束后清理测试资源。SDN_VPP_KEEP=1 可保留现场，资源名为 sdn-vpp-it-时间戳；
integration.log 记录具体名称，隐藏临时令牌。IRB 测试使用 sdn-vpp-irb-时间戳，
日志为 irb.log，额外创建一个用于隔离验证的租户和 PC。

报告分别验证配置事务、Type-3 更新 BUM 成员、PC 连通、Type-2 双向同步、
BUM 撤销后保留单播、失败回滚与 EVPN 撤销。IRB 报告验证网关学习、跨子网
双向转发、IPv4/IPv6 Type-5 前缀安装/转发/撤销、重叠子网租户隔离、
原生 BVI 断开试验以及网关地址回滚。
status=passed 才表示本次全部检查通过；容器 healthy 只表示必要服务可用。
IPv6 underlay 报告在 artifacts/vpp/dual-ipv6；GSO 报告和抓包在 artifacts/vpp/offload。
完整 Web 查询联调的可选入口见 :doc:`/developer/vpp`。

独立启动空白节点
----------------

.. code-block:: bash

   cd ~/program/github/sdn-agent
   mkdir -p ~/sdn-vpp-node
   cp docker/vpp/agent.conf ~/sdn-vpp-node/agent.conf
   cp docker/vpp/startup.conf ~/sdn-vpp-node/startup.conf
   umask 077
   printf 'SDN_TOKEN=%s\n' "$(openssl rand -hex 32)" > ~/sdn-vpp-node/node.env

   docker run -d --name sdn-vpp-node \
     --cap-add NET_ADMIN --cap-add NET_RAW --cap-add SYS_ADMIN \
     --cap-add IPC_LOCK --cap-add SYS_NICE --ulimit memlock=-1:-1 \
     --device /dev/net/tun --device /dev/vhost-net --stop-timeout 210 \
     --env-file "$HOME/sdn-vpp-node/node.env" \
     -v "$HOME/sdn-vpp-node/agent.conf:/etc/sdn-agent/agent.conf:ro" \
     -v "$HOME/sdn-vpp-node/startup.conf:/etc/vpp/startup.conf:ro" \
     -v sdn-vpp-node-state:/var/lib/sdn-agent \
     -v sdn-vpp-node-redis:/var/lib/redis \
     -v sdn-vpp-node-frr:/etc/frr \
     alpine/sdn-agent:vpp-dev

   docker exec sdn-vpp-node sdnctl -insecure capabilities
   docker exec sdn-vpp-node vppctl show version

示例没有发布管理端口，CLI 在容器内读取 SDN_TOKEN。连接 Controller 时，
按 :doc:`configuration` 设置地址、节点可达 endpoint 和 TLS / token。
示例 insecure 适合受控实验网络，生产管理连接应启用 TLS。

接口与下发
----------

先通过 VPP 启动文件创建真实接口及 LCP pair，再下发 Agent 配置。
例如原始 eth1 对应 VPP host-eth1，Linux 镜像名为 access0：

.. code-block:: text

   create host-interface name eth1
   set interface state host-eth1 up
   lcp create host-eth1 host-if access0
   lcp lcp-sync on

Agent 操作使用 access0，适配器每次通过 LCP 查询实际 VPP 索引。
软件实验需迁移原始 Docker 地址到 LCP underlay 接口，完整示例见 lab/vpp_evpn.py。
不要将这些实验命令直接用于宿主机管理接口。

默认启动文件禁用 DPDK，不会接管物理网卡。使用 DPDK 时另行挂载经过验证的配置、
指定 VFIO 设备和 hugepages，先确保原 VPP 进程已释放目标 PCI。
两个进程不能共同使用同一块 PCI 设备。

Linux bridge / VXLAN 是 FRR 识别 VNI 和 MAC 的控制面对象，实际转发在 VPP。
Query 的 source=vpp 返回真实 VPP 状态；FRR 查询继续来自 FRR。
使用现有 TENANT、BD、BD_TENANT、ADDRESS、BD_PORT、VXLAN、TENANT_L3VNI、
STATIC_ROUTE 和 FRR EVPN 操作。VXLAN 端口设置 learning=false；需要远端
ARP/ND 抑制时设置 neigh_suppress=true。网关地址配置在 BD 上。
远端 VTEP 由 EVPN 学习，无需逐个手工创建隧道。

三层组网顺序
------------

1. 创建 TENANT，指定独立 table_id；创建业务 BD，使用 BD_TENANT 绑定租户。
2. 在业务 BD 配置网关 MAC、IP 和 up 状态；绑定接入口和对应 L2VNI 的 VXLAN。
3. 创建该租户专用的传输 BD 和 VXLAN（L3VNI），将传输 BD 绑定同一租户。
   传输 BD 配置 Router MAC 和 up 状态，可以不配置 IP。
4. TENANT_L3VNI 将租户关联到 L3VNI；配置默认实例 EVPN 邻居、
   advertise_all_vni / advertise_svi_ip、L2VNI 的 RT 和租户实例的 RD/RT。
5. 若需要发布前缀，在租户 BGP 实例导入路由并启用 EVPN_TYPE5。

完整可执行事务见 lab/vpp_irb.py。BD ID、VNI、租户 table_id 是不同标识。
同网段报文按目的 MAC 查 BD；目的 MAC 为网关的报文进入 BVI，再查租户 FIB。
远端下一跳通过 Router MAC、L3 传输 BD 和 L3VNI 完成 VXLAN 封装。
VXLAN 的 encap_vrf_id 是外层 underlay 路由表，不能当作内层租户 VRF。

当前每个 VNI 对应一个原生多点 VXLAN 接口，创建时仅指定本地 src，
不为每个远端创建隧道接口。EVPN Type-2 更新 MAC 到 VTEP 的映射，
Type-3 更新 BUM 复制成员；三层路由仍由租户 FIB 决定出口。
``VXLAN_TUNNELS`` 查询包含本地 VTEP、地址族、原生接口名/索引、multipoint、
remotes、remoteCount 和 bumCount。原生 dump 中 dst_address=0.0.0.0 或 ::
表示多点接口，不是实际报文目的地址。同一 VNI 的远端与本地 VTEP 必须属于同一地址族；
两种 underlay 都可承载内层 IPv4/IPv6。BGP router-id / RD 的 IPv4 字段仍使用 IPv4 标识。

手工实验可以在未由 Agent 管理的独立 VNI 上执行：

.. code-block:: text

   create vxlan tunnel src 192.0.2.1 vni 10100
   set interface l2 bridge vxlan_tunnel0 100 1
   set vxlan remote vxlan_tunnel0 remote 192.0.2.2 mac 02:00:00:00:00:02
   l2fib add 02:00:00:00:00:02 100 vxlan_tunnel0
   set vxlan remote vxlan_tunnel0 remote 192.0.2.2 bum
   show vxlan tunnel

接口名以创建命令返回值为准；仍需创建 BD、配置 underlay 和接入口。
由 Agent 管理的设备通过 typed operations 配置，不要手工修改其动态表项。
Linux 镜像端口保留 carrier，使用 tc ingress/egress priority 49150 丢弃业务报文。
不要移除这些过滤器或将镜像当作业务接口使用。网关 ping 由 VPP ping 插件处理；
租户内 BGP/OSPF 会话或 Linux 应用到 BVI 的 punt 通道尚未实现。

Web 查询与卸载边界
------------------

升级匹配的 Agent、Controller、Web 镜像后，在“资源工作台”选择 VPP 节点。
“隧道 → VXLAN → 查询运行状态”显示 VPP 标识，支持 IPv4/IPv6 地址族、VRF、BD、
接口和远端 VTEP 筛选。展开行可看到目的 MAC 对应的单播远端及 BUM 复制成员。
“接口”包含 LCP 接口、原生 BVI 与 VXLAN；FDB、租户路由和邻居查询读取真实 VPP 表。
viewer 可以查询，不能修改。FRR 协议查询继续读取 FRR。

当前多点封装使用 **软件校验和与 TCP GSO 分段**：进入封装前完成内层校验和，
IPv6 外层 UDP 必须有有效校验和；IPv4 外层 UDP 可为零。解封装重新校验外层 UDP，
清理不再适用的外层校验和标记，不把网卡提供的内层结果直接当作外层校验成功。
``show errors`` 中的 ``TCP GSO packets segmented`` 可确认分段路径被执行。

这不表示已实现硬件 VXLAN TSO、硬件自动解封装或 RX LRO。原生多点接口拒绝使用
旧版固定 src/dst 的 RX flow offload 规则。DPDK 应保持 RX LRO 关闭；物理网卡上的
校验和元数据、分段、RSS 和性能仍需结合实际驱动、固件及对端抓包单独验收。
本次 TAP GSO + AF_PACKET 测试验证的是软件回退的报文正确性，不能代替物理 NIC 验收。

另有华为 HiNIC3 的真实 VF 容器实验：两个 Leaf 各用一个 VF，Spine 使用一个 VF
及两个 VLAN 子接口，PC 直接连接 VPP TAP。测试同时检查 VF 底层双栈路由、
EVPN 二层与跨网段转发、Spine 断链恢复，以及接收侧报文校验和。
运行方式和恢复命令见 :doc:`开发文档 </developer/vpp>` 的“华为物理 VF 实验”。
结果以 artifacts/vpp/sriov 中对应 report.json 为准；网卡内部 VF 交换不等于
多台设备的外部线缆测试，也不表示硬件 TSO/LRO 或 RSS 多队列已验收。

恢复边界和排错
--------------

持久化包括 Agent 事务日志、FRR 配置、stateDir/vpp.json 中的受管模型。
模型版本 2 保存租户、BD/BVI 属性、绑定、VNI 和地址，兼容版本 1 的 L2 状态及 Undo。
适配进程重启后重新读取 VPP 和内核转发表，远端状态继续以 FRR 的有效选路为准。
VPP / 容器重建时，必须由启动配置先恢复接口、LCP pair、underlay 地址和路由，
再恢复受管镜像、租户和地址并等待 EVPN 收敛。当前没有全量 Running 重放，
静态路由仍应由启动配置或重新下发恢复，手工 VPP CLI 不会自动跨命名空间恢复。
中断事务保留恢复屏障，按 :doc:`transactions` 处理 Undo / RetryRollback。

.. code-block:: bash

   docker logs --tail 100 sdn-vpp-node
   docker exec sdn-vpp-node sdnctl -insecure -kind VXLAN_TUNNELS query
   docker exec sdn-vpp-node sdnctl -insecure -kind VXLAN_TUNNELS -family IPV6 query
   docker exec sdn-vpp-node sdnctl -insecure -kind ROUTES -family IPV6 -vrf TENANT-A query
   docker exec sdn-vpp-node sdnctl -insecure -kind FDB query
   docker exec sdn-vpp-node vtysh -c 'show bgp l2vpn evpn summary'
   docker exec sdn-vpp-node vtysh -c 'show evpn vni'
   docker exec sdn-vpp-node bridge -j fdb show
   docker exec sdn-vpp-node vppctl show l2fib
   docker exec sdn-vpp-node vppctl show ip fib table 1001
   docker exec sdn-vpp-node vppctl show ip neighbors

没有 Type-3 或 Type-2 引用时，多点接口仍存在，但 remotes 为空。
适配器约每秒全量核对，
不是无延迟的事件同步。VPP / 适配进程退出会导致整个 bundle 停止，
防止 Agent 在缺少必要后端时继续执行配置。

邻居保活开关
------------

VPP 节点可以开启本地动态邻居保活，默认关闭。Linux 数据面不支持。
在 Web 的“二层网络 BD”中选择“邻居保活（节点开关）”，开启后加入候选，
预览并提交。它作用于本节点受管接入 BD 的 IPv4/IPv6 邻居，
不是 ARP/ND 抑制，也不是 BGP 会话 keepalive。

也可通过 sdnctl 提交下面的操作文件：

.. code-block:: json

   {
     "operations": [{
       "id": "keepalive",
       "code": "NEIGHBOR_KEEPALIVE",
       "action": "SET",
       "neighbor_keepalive": {"enabled": true}
     }]
   }

.. code-block:: bash

   docker cp keepalive.json sdn-vpp-node:/tmp/keepalive.json
   docker exec sdn-vpp-node sdnctl -insecure -file /tmp/keepalive.json submit
   docker exec sdn-vpp-node sdnctl -insecure -kind NEIGHBOR_KEEPALIVE_STATUS query
   docker exec sdn-vpp-node sdnctl -insecure -kind NEIGHBOR_KEEPALIVE_STATUS -family IPV6 query

关闭时将 enabled 改为 false，重新提交。关闭不清空健康邻居；它们继续正常老化。
Controller 已管理的节点应通过 Web/Controller 提交，以免产生配置 revision 漂移。

VPP 向已知本地主机发送单播 ARP Request / IPv6 NS；只有有效邻居学习才续期。
三次无应答后撤销对应 IP 绑定，FRR 随之撤销 MAC/IP Type-2，
同 MAC 的其他健康 IP 不受影响。失败后主机重新发包可触发恢复。
仅有 MAC、没有 IP 绑定的设备以及从未发包的主机不在此功能范围内。

受管 BD 的 MAC 老化时间需要为 0 或 >=120 秒，默认 300 秒无需修改。
最多跟踪 4096 个绑定，查询中的 send_errors / capacity_errors 应为 0；
非零时需要检查 BVI 源地址、接入口状态或规模。

默认容器 startup.conf 已加载插件。使用自己挂载的配置时，plugins 内还需要：

.. code-block:: text

   plugin neighbor_keepalive_plugin.so { enable }

必须一起更新 VPP 原生插件、Python 适配器和 Agent 镜像。
配置写入既有 VPP 状态目录；重启恢复依赖挂载保留该目录及恢复接入口，
动态主机邻居仍需要重新学习。架构及验证方法见 :doc:`../developer/vpp-keepalive`。

VXLAN 外层 VRF：创建与在线修改
--------------------------------

VPP 节点支持为 VXLAN 设置外层路由表，并在创建后修改。
外层 VRF 用于到远端 VTEP 的封装路由及接收隧道匹配；内层租户 VRF 仍由
BD/BVI 与 TENANT 的关系决定。修改外层表不改变 VNI、原生接口编号或 BD 绑定。

使用前先准备同地址族的 VPP 路由表、Linux VRF 控制映射以及到远端 VTEP 的
底层路由。通过 Controller 管理时，非零外层表必须先以 TENANT 资源创建；
这个承载 VRF 可以没有业务 BD 和 L3VNI。表 ID 0 表示 VPP 默认表，不能把
Linux main 的数字 254 填进此字段。253、254、255 和 4294967295 被拒绝。

Web 中选择支持 ``vxlan-encap-vrf/v1`` 的节点，进入“隧道 / VXLAN”：

1. 创建 VXLAN 时可以填写“外层 VRF 表 ID”。
2. 对已有隧道选择“修改 VXLAN 外层 VRF”，填写名称及目标表 ID。
3. 加入候选、预览计划后提交；回到默认表时明确填写 0。
4. 运行查询的“外层 VRF 表 ID”显示 VPP 实际值。

Agent / Controller 资源操作示例：

.. code-block:: json

   {
     "code": "VXLAN",
     "action": "SET",
     "vxlan": {"name": "vx50000", "encapVrfId": 100}
   }

CREATE 使用相同的可选字段，同时提供 local、vni 等创建参数。
SET 只修改 encapVrfId 和可选的 underlay；不接受 local、vni、port 或 learning
的身份修改。已有 underlay 留空表示保留，不表示清除。显式指定的 underlay
必须是 LCP 接口，而且 Linux 与 VPP 两侧均已属于目标外层表。
省略 underlay 时，非零表使用对应 Linux VRF 作为控制镜像的底层设备；
VXLAN 的 bridge master 仍然是原有租户 BD。

此操作不会替用户迁移物理网卡、调整 BGP 管理连接或创建远端 VTEP 路由。
先完成承载网络准备，再修改隧道；切换可能短暂中断通信。要删除旧承载 VRF，
先单独提交所有隧道的切换，再删除 VRF；仍被引用的表不能删除。

需要同时更新 VPP 插件/API/适配器、Agent、Controller 和 Web。
旧节点或 Linux 数据面不显示修改入口；Linux 后端拒绝非零 encapVrfId。
设备配置由适配器持久化，重新加载时仍需先恢复启动配置管理的 Underlay/LCP。

FRR 下一跳解析也需要承载可达性
----------------------------------------

当 BGP EVPN 实例仍在 default VRF，而数据 Underlay 已迁移到 OUTER100，
仅在 VPP 表 100 中存在路由还不够：默认 BGP 实例也必须解析远端 VTEP，
否则 EVPN 路由可能已接收但无 valid/best 标记，进而撤销租户路由和远端 MAC。

可按部署设计向控制面 VRF 泄漏真实承载路由。例如远端 VTEP
192.0.2.2 已经可以通过 OUTER100 中的 uplink 直连访问时，在 FRR 配置：

.. code-block:: text

   ip route 192.0.2.2/32 uplink nexthop-vrf OUTER100

IPv6 对应使用 ipv6 route <VTEP>/128 uplink nexthop-vrf OUTER100。
这是一条经真实 Underlay 出口解析的路由，不能用黑洞或伪造直连路由来让
EVPN 强行选路。使用其他 IGP/BGP 泄漏方案时同样验证 show ip nht / show ipv6 nht。
接口移动、该路由泄漏及其启动恢复由承载网络配置负责，
VXLAN SET 不自动配置它们。当前 Agent 的 STATIC_ROUTE 资源没有 nexthop-vrf 字段；
可在部署时的 FRR 配置文件中维护此项。
