# 操作目录与适配器分工

`Operation = id + code + action + depends_on + typed oneof data`。参数使用 Protobuf 类型；可选字段具有 presence，未给字段不会被当作 false/0 覆盖。当前注册表在进程启动时固定注册 Go Driver，不支持任意远程代码 callback。

下表后端列描述默认 Linux 模式。选择 `agent.dataplane=vpp` 后，网络操作由 VPP composite Driver 同时管理 Linux 控制面镜像和 VPP 数据面，协议操作仍由 FRR 执行。支持租户、BD/BVI、L2VNI/L3VNI 和原生 src-only 多点 VXLAN；VXLAN 操作字段不变，每个 VNI 一个接口，远端由 EVPN 动态下发。VPP 模式的限制与操作流程见 [VPP 用户手册](user/vpp.rst)，以节点 Capabilities 为准。

| 编号 / code | data | 支持行为 | 后端 | 主要参数 |
|---|---|---|---|---|
| 1 NODE_NETWORK | network | SET | Linux | 白名单 sysctl 列表；optional 允许内核不存在对应项 |
| 2 INTERFACE | interface | SET | Linux | name、mtu、mac、up、addrgenmode |
| 3 ADDRESS | address | CREATE / DELETE | Linux | interface、带前缀长度的地址 |
| 4 TENANT | tenant | CREATE / DELETE | Linux + FRR | name、table_id；Linux VRF + FRR VRF 配置 |
| 5 BD | bridge | CREATE / SET / DELETE | Linux | name、stp、mtu、mac、ageing_time |
| 6 BD_PORT | bridge_port | BIND / UNBIND / SET | Linux | bridge、port、learning、neigh_suppress |
| 7 BD_TENANT | binding | BIND / UNBIND | Linux | bridge、tenant |
| 8 VXLAN | vxlan | CREATE / DELETE | Linux | name、vni、local、port、learning、underlay |
| 9 STATIC_ROUTE | route | CREATE / DELETE | Linux | prefix、gateway、interface、table |
| 10 NEIGHBOR_KEEPALIVE | neighbor_keepalive | SET | 仅 VPP | 必须显式填写 enabled；默认 false，关闭不清空健康邻居 |
| 20 OSPF_INSTANCE | ospf | CREATE / SET / DELETE | FRR | router_id、vrf |
| 21 OSPF_NETWORK | ospf_network | CREATE / SET / DELETE | FRR | prefix、area、vrf |
| 22 OSPF_INTERFACE | ospf_interface | SET / DELETE | FRR | name、vrf、area、passive、network_type、cost、hello_interval、dead_interval |
| 23 OSPF6_INSTANCE | ospf | CREATE / SET / DELETE | FRR | router_id、vrf |
| 24 OSPF6_INTERFACE | ospf6_interface | SET / DELETE | FRR | name、vrf、area、passive、network_type、cost、hello_interval、dead_interval |
| 30 BGP_INSTANCE | bgp | CREATE / SET / DELETE | FRR | asn、vrf、router_id、default_ipv4_unicast |
| 31 BGP_PEER_GROUP | peer | CREATE / SET / DELETE | FRR | asn、vrf、name、remote_as、update_source |
| 32 BGP_NEIGHBOR | peer | CREATE / SET / DELETE | FRR | asn、vrf、name（IPv4/IPv6）、remote_as / peer_group、update_source |
| 33 BGP_EVPN_PEER | peer | SET / DELETE | FRR | active、rr_client |
| 34 BGP_EVPN_GLOBAL | evpn_global | SET / DELETE | FRR | advertise_all_vni、advertise_svi_ip |
| 35 EVPN_L2VNI | evpn | CREATE / SET / DELETE | FRR | asn、vni、rd、import_rts、export_rts |
| 36 EVPN_TENANT | evpn | SET / DELETE | FRR | asn、vrf、rd、import_rts、export_rts |
| 37 TENANT_L3VNI | l3vni | BIND / UNBIND / SET | FRR | tenant、vni |
| 38 BGP_AF_PEER | peer | SET / DELETE | FRR | asn、vrf、name、family、active、rr_client |
| 39 BGP_NETWORK | bgp_network | CREATE / SET / DELETE | FRR | asn、vrf、family、prefix |
| 40 BGP_REDISTRIBUTE | bgp_redistribute | CREATE / SET / DELETE | FRR | asn、vrf、family、protocol |
| 41 EVPN_TYPE5 | bgp_type5 | SET / DELETE | FRR | asn、vrf、family、enabled |

Linux 创建实际 VXLAN 设备；FRR 的 `vni` 命令设置控制面关联，不创建 Linux VXLAN 接口。L2VNI 与 L3VNI 使用同一种 VXLAN 设备类型，角色由 bridge、VRF、FRR L3VNI 映射共同确定。L3-SVI 在当前实验中不配置 IP，只配置 Router MAC。

RT 列表是增删式配置：SET 添加指定 RT；EVPN_TENANT DELETE 移除指定 RD/RT；EVPN_L2VNI DELETE 删除该 VNI 的显式 BGP 配置，不删除内核隧道。FRR 可能把相同 import/export RT 输出为 `route-target both`，回读按语义比较。

`ageing_time` 使用 iproute2 bridge 的时钟 tick 单位，不应直接把秒数原样传入（常见 Linux USER_HZ=100）。MAC 老化计时与主动 ARP/ND 探测不同；本版只配置计时与邻居抑制，不实现保活服务。

## 依赖和删除

Controller 显式提供 `depends_on`。Agent 检查缺失依赖和循环，稳定拓扑排序后串行执行；同一个 op 的所有注册后端均参与。注册顺序 Linux → FRR，DELETE / UNBIND 通常反向执行。TENANT 删除是明确例外：FRR 要求内核 VRF 已 inactive，所以先解除全部引用、删除 Linux VRF，再删除 FRR VRF 配置。

```text
TENANT create → BD create → BD_TENANT bind
                         → ADDRESS create
VXLAN create → BD_PORT bind → INTERFACE up
TENANT + L3 bridge/VXLAN → TENANT_L3VNI bind
BGP instance → peer/group → EVPN activate → RD/RT
```

删除时先撤销 FRR 引用、解除端口/租户绑定、移除地址，再删除 VXLAN / BD / VRF。适配器拒绝删除仍有 master、成员或非 link-local 地址的设备。v1 面向自己创建的简单 bridge/VRF/VXLAN；复杂 VLAN-aware bridge、多播 VXLAN、外部元数据 VXLAN 等不在可完整恢复的删除范围内。

静态 MAC、VLAN-aware bridge、多归属 ESI、BFD、ACL、任意交换机 API 等不在当前协议能力中；未知 code 或不匹配的 oneof 会被拒绝，不能降级成原始 shell/CLI。Type-5、OSPFv3 与结构化查询示例见 [资源手册](user/resources.rst)。

参考：[FRR 10 EVPN 文档](https://docs.frrouting.org/en/stable-10.0/evpn.html)。本版实际兼容结果以固定实验镜像和回归报告为准。

VPP 独占查询 NEIGHBOR_KEEPALIVE_STATUS 返回节点策略和每 IP 探测状态，支持 IPv4/IPv6、VRF、BD、接口及邻居筛选。Linux 不声明此操作/查询。详见 [保活架构](developer/vpp-keepalive.rst)。
