节点资源与结构化查询
========================================

独立配置对象
------------

Agent 使用同一组强类型 Protobuf 操作供直接 CLI 和 Controller 调用。
创建租户只创建 Linux VRF 和 FRR VRF，不创建 BD、VXLAN 或 L3VNI。
每个对象都有自己的生命周期；绑定操作与对象创建分开提交，也可以使用
``depends_on`` 在同一个事务中按依赖顺序执行。

.. list-table:: 配置职责
   :header-rows: 1
   :widths: 24 16 60

   * - 操作
     - 后端
     - 语义
   * - TENANT
     - Linux + FRR
     - CREATE / DELETE；name 和 table_id；删除前先解除相关对象。
   * - BD
     - Linux
     - CREATE / SET / DELETE；名称、MAC、MTU、STP、老化时间。
   * - ADDRESS
     - Linux
     - CREATE / DELETE；接口与 IPv4/IPv6 CIDR，也用于 BD 网关地址。
   * - BD_TENANT
     - Linux
     - BIND / UNBIND；把已存在的 BD 加入或移出已存在的 VRF。
   * - VXLAN
     - Linux
     - CREATE / DELETE；隧道名称、VNI、本地 VTEP、UDP 端口和 underlay 接口。
   * - BD_PORT
     - Linux
     - BIND / UNBIND / SET；添加、移除或调整成员。隧道加入 BD 也使用此操作。
   * - TENANT_L3VNI
     - FRR
     - BIND / SET / UNBIND；只配置 VRF 对应的 L3VNI。BIND/SET 先检查 Linux VRF、对应 VXLAN 及其桥接关系已经存在。
   * - INTERFACE / STATIC_ROUTE
     - Linux
     - 接口属性；IPv4/IPv6 静态路由创建、删除及读回。

``BD_PORT.bridge_port.neigh_suppress`` 对桥端口启用或禁用 ARP/ND 抑制。
通常设置在 VXLAN 端口上；它不是定时探测或 MAC 保活功能。
Controller 的 BD 级抑制意图会转换成成员端口配置。直接调用 Agent 时应列出
需要修改的端口，并将这些修改放入同一个事务。

只创建租户的示例文件 ``tenant.json``：

.. code-block:: json

   {
     "operations": [{
       "id": "tenant-a",
       "code": "TENANT",
       "action": "CREATE",
       "tenant": {"name": "TENANT-A", "table_id": 1001}
     }]
   }

.. code-block:: bash

   # SDN_TOKEN 使用部署时配置的访问凭据；参数必须位于子命令之前。
   sdnctl -addr AGENT_IP:9090 -insecure -file tenant.json submit
   sdnctl -addr AGENT_IP:9090 -insecure -kind TENANTS query
   sdnctl -addr AGENT_IP:9090 -insecure -kind BRIDGES -vrf TENANT-A query

生产部署使用 TLS 参数代替实验模式的 ``-insecure``。

BGP IPv4、IPv6 和 EVPN Type-5
----------------------------------------

现有 ``BGP_INSTANCE``、``BGP_NEIGHBOR``、``BGP_PEER_GROUP``、
``BGP_EVPN_PEER``、``BGP_EVPN_GLOBAL``、``EVPN_L2VNI``、
``EVPN_TENANT`` 保持兼容。邻居地址支持 IPv4 和 IPv6。
新增操作使用明确地址族，避免将邻居传输地址误认为 NLRI 的地址族。

.. list-table:: 新增 BGP 操作
   :header-rows: 1

   * - code / payload
     - 关键字段
     - 动作
   * - BGP_AF_PEER / peer
     - asn、vrf、name、family=IPV4/IPV6、active、rr_client
     - SET / DELETE
   * - BGP_NETWORK / bgp_network
     - asn、vrf、family、prefix
     - CREATE / SET / DELETE
   * - BGP_REDISTRIBUTE / bgp_redistribute
     - asn、vrf、family、protocol
     - CREATE / SET / DELETE
   * - EVPN_TYPE5 / bgp_type5
     - asn、vrf、family、enabled
     - SET / DELETE

``protocol`` 支持 ``connected``、``static``、IPv4 的 ``ospf`` 和 IPv6 的
``ospf6``。Type-5 发布需要同时满足：租户 L3VNI 已建立、相关前缀已进入该 VRF
的 BGP RIB、该 VRF 已显式启用对应地址族的 Type-5 发布。
仅设置 ``enabled=true`` 不会凭空产生前缀。

.. code-block:: json

   {
     "operations": [
       {"id":"v6-route", "code":"BGP_NETWORK", "action":"SET",
        "bgp_network":{"asn":65000,"vrf":"TENANT-A","family":"IPV6","prefix":"2001:db8:10::/64"}},
       {"id":"v6-type5", "code":"EVPN_TYPE5", "action":"SET", "depends_on":["v6-route"],
        "bgp_type5":{"asn":65000,"vrf":"TENANT-A","family":"IPV6","enabled":true}}
     ]
   }

OSPFv2 与 OSPFv3
----------------

``OSPF_INSTANCE`` 和 ``OSPF_NETWORK`` 保持兼容，增加可选 ``vrf``。
``OSPF_INTERFACE`` 增加可选 VRF、area、cost、hello_interval、dead_interval。
同一 OSPFv2 实例的 ``network`` 模式与接口 area 模式遵循 FRR 自身的互斥规则；
错误会触发事务失败及补偿，不能混用后期待 Agent 隐式删除旧配置。

OSPFv3 使用 ``OSPF6_INSTANCE`` 携带 ``ospf`` 对象；router_id 仍为 IPv4 格式的
32 位标识。``OSPF6_INTERFACE`` 携带 ``ospf6_interface``，支持接口名称、VRF、
area、passive、network_type、cost、hello_interval、dead_interval。
FRR 10.2 的接口配置为 ``interface NAME`` 下的 ``ipv6 ospf6 area AREA``。

镜像运行和监督 ``ospf6d``，持久化的旧 ``daemons`` 文件也会启用它。
Agent 不会默认关闭 IPv6。需要 IPv6 三层转发时通过 ``NODE_NETWORK`` 设置
``net.ipv6.conf.all.forwarding=1``；旧实验显式禁用 IPv6 的请求仍保持原语义。

IPv6 业务网关必须具备链路本地地址
------------------------------------------------------------

为 BD 配置 IPv6 网关时，除了全局地址，还必须为该业务 SVI 保留可用的链路本地地址。
IPv4 参考实验对 br10/br20 设置的 ``addrgenmode none`` 不能原样用于 IPv6 网关。
可以在首次启用接口之前配置 ``addrgenmode=eui64`` 自动生成地址。
对于已经 UP 且过去设置为 none 的接口，仅修改该模式不保证补生成地址；
应显式配置适合该链路的链路本地地址，确认它与全局地址均完成 DAD，之后验证业务：

.. code-block:: json

   {
     "operations": [
       {"id":"br10-ipv6-mode", "code":"INTERFACE", "action":"SET",
        "interface":{"name":"br10","addrgenmode":"eui64"}},
       {"id":"br10-link-local", "code":"ADDRESS", "action":"CREATE",
        "depends_on":["br10-ipv6-mode"],
        "address":{"interface":"br10","prefix":"fe80::ff:fe00:10/64"}},
       {"id":"br10-ipv6-address", "code":"ADDRESS", "action":"CREATE",
        "depends_on":["br10-ipv6-mode"],
        "address":{"interface":"br10","prefix":"2001:db8:10::fe/64"}}
     ]
   }

可用 ``sdnctl -kind ADDRESSES -interface br10 -family IPV6 query`` 查看地址，
确认既有全局地址，也有 ``fe80::`` 链路本地地址，且 ``details`` 没有 tentative/dadfailed 标志。
实际部署的链路本地前缀范围为 ``fe80::/10``；EUI-64 自动生成的接口地址通常显示 /64。
上述示例对应 br10 的 MAC ``02:00:00:00:00:10``。其他接口应使用合适且不冲突的地址。

在 Linux 6.8 中，转发报文触发邻居解析时，内核需要出接口的链路本地地址作为 NS 源。
缺少它会导致目标邻居停留在 INCOMPLETE/FAILED，并且接入口抓不到 NS。
主机先 ping 网关可能让网关被动学到主机邻居，从而暂时恢复；这不是修复方法，
邻居再次老化后仍可能失败。验收应覆盖没有主机预热的双向跨 Leaf 通信。

该要求针对承载 IPv6 网关的业务 SVI。EVPN 对称 IRB 的无编号 L3VNI SVI
（例如 br50000）继续保持无 IP 和 ``addrgenmode none``，不要把业务 SVI 的设置
批量应用到 L3VNI SVI。

源码依据：`Linux v6.8 邻居发现实现 <https://github.com/torvalds/linux/blob/v6.8/net/ipv6/ndisc.c>`_
中的 ``ndisc_solicit`` 与 ``ndisc_send_ns``。

结构化查询
----------

.. code-block:: bash

   sdnctl -addr AGENT_IP:9090 -insecure -kind PORTS -bridge br10 query
   sdnctl -addr AGENT_IP:9090 -insecure -kind FDB -bridge br10 query
   sdnctl -addr AGENT_IP:9090 -insecure -kind NEIGHBORS -vrf TENANT-A -family IPV6 query
   sdnctl -addr AGENT_IP:9090 -insecure -kind ROUTES -vrf TENANT-A -family IPV6 query
   sdnctl -addr AGENT_IP:9090 -insecure -kind VXLAN_TUNNELS query
   sdnctl -addr AGENT_IP:9090 -insecure -kind BGP_ROUTES -vrf TENANT-A -family IPV6 query
   sdnctl -addr AGENT_IP:9090 -insecure -kind EVPN_ROUTES -route-type 5 query
   sdnctl -addr AGENT_IP:9090 -insecure -kind OSPF_NEIGHBORS -ospf-version 3 query

查询返回 ``records`` 结构化对象数组以及 node_id、source、collected_unix_ms、
revision、total_count、next_page_token。配置版本是采集时已提交的 Running 版本，
不代表内核所有动态表具有原子快照。
Linux 空表返回空数组；命令失败、FRR warning、JSON 损坏、接口不存在均返回明确 RPC
错误，不会伪装成空表。Agent 实时查询不返回缓存，``stale`` 默认为 false。

分页默认 200 条、最多 1000 条。后续页携带 ``-page-token``，其他过滤字段保持相同。
令牌绑定记录稳定字段的摘要；接口/路由集合、配置或转发路径变化时返回 ``Aborted``，
应重新从第一页开始。已知桥倒计时、协议 uptime、流量计数等不参与分页排序或摘要，
原始观测字段仍完整返回。分页不是冻结的历史快照。

查询种类包括 TENANTS、BRIDGES、INTERFACES、ADDRESSES、PORTS、ROUTES、FDB、
NEIGHBORS、VXLAN_TUNNELS、BGP_SUMMARY、BGP_NEIGHBORS、BGP_ROUTES、EVPN_ROUTES、
EVPN_VNI，以及 OSPF_SUMMARY/INTERFACES/NEIGHBORS/DATABASE/ROUTES。
以 ``capabilities`` 返回的 ``queries`` 为该节点实际支持集合。
EVPN_ROUTES 查询全局 EVPN RIB，支持 route_type/prefix；它不接受 VRF 过滤。
EVPN_VNI 与 TENANTS 提供实际 VRF/VNI 关联，未绑定桥、隧道仍出现在各自列表中。

参考：`FRR EVPN <https://docs.frrouting.org/en/stable-10.1/evpn.html>`_、
`FRR OSPFv3 <https://docs.frrouting.org/en/stable-10.1/ospf6d.html>`_。
