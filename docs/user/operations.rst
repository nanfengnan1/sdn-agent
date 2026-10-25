26 类操作与后端归属
========================================

每条操作由 ``id + code + action + depends_on + oneof data`` 组成。
``id`` 在候选事务内标识操作；Stage 中重复同一个 id 表示编辑该操作。
参数名和枚举来自 ``api/agent/v1/agent.proto``，JSON 接受 Protobuf 字段命名。

未给出的 ``optional`` 字段不会自动变成 false / 0；
但不是每个字段都有 presence，例如 VXLAN 的 ``learning`` 是普通 bool。
先查询 GetCapabilities，尤其是启动时使用了 ``-frr=false`` 的节点。

新增 OSPF6_INSTANCE、OSPF6_INTERFACE、BGP_AF_PEER、BGP_NETWORK、
BGP_REDISTRIBUTE、EVPN_TYPE5 的字段、动作和示例见 :doc:`resources`。

Linux 网络对象
--------------

.. list-table::
   :header-rows: 1
   :widths: 22 19 19 40

   * - code / 编号
     - data
     - action
     - 作用与主要字段
   * - NODE_NETWORK / 1
     - ``network``
     - SET
     - 白名单 sysctl 的 ``settings``；每项包含 key、value、optional。
   * - INTERFACE / 2
     - ``interface``
     - SET
     - 已存在接口的 name、mtu、mac、up、addrgenmode。
   * - ADDRESS / 3
     - ``address``
     - CREATE、DELETE
     - interface、带前缀长度的 prefix。
   * - TENANT / 4
     - ``tenant``
     - CREATE、DELETE
     - name、table_id；Linux 创建 VRF，FRR 驱动也配置 VRF。
   * - BD / 5
     - ``bridge``
     - CREATE、SET、DELETE
     - bridge 的 name、stp、mtu、mac、ageing_time。
   * - BD_PORT / 6
     - ``bridge_port``
     - BIND、UNBIND、SET
     - bridge、port、learning、neigh_suppress。
   * - BD_TENANT / 7
     - ``binding``
     - BIND、UNBIND
     - 将 bridge 加入或移出 tenant。
   * - VXLAN / 8
     - ``vxlan``
     - CREATE、DELETE
     - name、vni、local、port、learning、underlay。
   * - STATIC_ROUTE / 9
     - ``route``
     - CREATE、DELETE
     - prefix、gateway、interface、table。

除了 TENANT 还会参与 FRR 驱动，上述操作都由 Linux 驱动执行。
创建 VXLAN 不等于把它加入 bridge；使用单独的 BD_PORT 操作完成绑定。
接口启用和地址配置也使用各自操作。

FRR 路由协议与 EVPN
-------------------

.. list-table::
   :header-rows: 1
   :widths: 23 19 19 39

   * - code / 编号
     - data
     - action
     - 主要字段
   * - OSPF_INSTANCE / 20
     - ``ospf``
     - CREATE、SET、DELETE
     - router_id。
   * - OSPF_NETWORK / 21
     - ``ospf_network``
     - CREATE、SET、DELETE
     - prefix、area。
   * - OSPF_INTERFACE / 22
     - ``ospf_interface``
     - SET、DELETE
     - name、passive、network_type。
   * - BGP_INSTANCE / 30
     - ``bgp``
     - CREATE、SET、DELETE
     - asn、vrf、router_id、default_ipv4_unicast。
   * - BGP_PEER_GROUP / 31
     - ``peer``
     - CREATE、SET、DELETE
     - asn、vrf、name、remote_as、update_source。
   * - BGP_NEIGHBOR / 32
     - ``peer``
     - CREATE、SET、DELETE
     - asn、vrf、IPv4/IPv6 name、remote_as 或 peer_group、update_source。
   * - BGP_EVPN_PEER / 33
     - ``peer``
     - SET、DELETE
     - asn、vrf、name、active、rr_client。
   * - BGP_EVPN_GLOBAL / 34
     - ``evpn_global``
     - SET、DELETE
     - asn、advertise_all_vni、advertise_svi_ip。
   * - EVPN_L2VNI / 35
     - ``evpn``
     - CREATE、SET、DELETE
     - asn、vni、rd、import_rts、export_rts。
   * - EVPN_TENANT / 36
     - ``evpn``
     - SET、DELETE
     - asn、vrf、rd、import_rts、export_rts。
   * - TENANT_L3VNI / 37
     - ``l3vni``
     - BIND、UNBIND、SET
     - tenant、vni；设置 FRR VRF 与 L3VNI 的关系。

``TENANT_L3VNI`` 对应 FRR 的 ``vrf TENANT-A`` / ``vni 50000``。
它不会创建 Linux VXLAN；只有完成 Linux bridge / VRF / VXLAN 关系，
再建立 FRR 映射，才能把 VNI 解释为本租户的 L3VNI。

RT 列表是增删式操作：SET 添加指定 RT；EVPN_TENANT DELETE 移除指定 RD / RT。
EVPN_L2VNI DELETE 删除 VNI 的显式 BGP 配置，不删除内核 VXLAN。
FRR 把相同 import / export 输出为 ``route-target both`` 时，验证按配置语义比较。

依赖顺序
--------

.. code-block:: text

   TENANT CREATE ------> BD_TENANT BIND <------ BD CREATE
                                                  |
                                                  +--> ADDRESS CREATE
   VXLAN CREATE -------> BD_PORT BIND <--------- BD CREATE
                              |
                              +--> INTERFACE SET up
   Linux VRF + L3 bridge + L3 VXLAN --> TENANT_L3VNI BIND
   BGP_INSTANCE --> PEER/GROUP --> EVPN_PEER / EVPN_GLOBAL

这表示资源前提，实际请求仍需显式列出 ``depends_on``。
同一个 op 的所有注册后端都执行；创建通常按 Linux → FRR，删除 / 解绑通常反向。
TENANT 删除是例外：FRR 需要内核 VRF 已 inactive，因此先删除 Linux VRF，再移除 FRR VRF。

删除前必须先解除引用：移除 FRR 的租户 / VNI 引用、成员绑定、地址，
再删除 VXLAN、bridge、VRF。当前适配器会拒绝删除仍有 master、成员或非 link-local 地址的设备。
复杂 VLAN-aware bridge、多播或 external 元数据 VXLAN 不属于本版可完整恢复的删除范围。

MAC 老化与未实现能力
--------------------

``bridge.ageing_time`` 直接传给 iproute2 的 bridge 参数，单位为 ``clock_t``，
即秒数乘以 USER_HZ；常见 USER_HZ 为 100，此时 300 秒应表示为 30000。
iproute2 把该整数直接写入 IFLA_BR_AGEING_TIME；其单位见
`Linux bridge Netlink 文档 <https://docs.kernel.org/networking/bridge.html#bridge-netlink-attributes>`_，
解析过程见 `iproute2 的 iplink_bridge.c <https://github.com/iproute2/iproute2/blob/main/ip/iplink_bridge.c>`_。
不要把 USER_HZ 与内核调度用的 CONFIG_HZ 混为一谈。
``neigh_suppress`` 控制邻居抑制，均不等于主动探测或永不老化。

当前没有静态 MAC、主动 ARP / ND、ESI 多归属、BFD、ACL、
VPP 或交换机 API 操作。未知 code、错误 oneof、非法动作都会被拒绝，
不能用任意 shell / vtysh 字符串绕过能力边界。
