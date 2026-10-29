# SDN Controller 与 Agent 能力及组件设计

## 2026-10-29：Snort 外部基础配置

- ips.configFile 指定管理员只读挂载的 Lua，默认 /etc/sdn-agent/snort/base.lua。
- ips.autoReload 默认 false；sdn-snort-reload 手动校验重载，可选两次稳定轮询自动加载。
- 基础参数快照与 Web 托管规则组合；保持 ips 的其他选项，inline/rules/include 由 Agent 管理。
- 规则操作和基础重载串行，失败恢复上一份已接受快照；额外 include 不自动监控或快照。
- IPS_STATUS 与 Web 查询展示路径、待加载、错误和自动重载状态。
- 冷启动文件缺失或无效拒绝启动；宿主机挂载目录以支持编辑器的原子替换。
- 31 项隔离容器检查、12 项 Snort runtime 单测、25 项容器单测、相关 Go 测试、21 项 Web 单测/类型检查/构建通过。
- 默认基础配置与规则 include 拒绝经过实际 Snort 校验；两个项目 Sphinx 严格构建通过。
- 验收报告：配套 Agent 仓库 artifacts/ips/external-20261029-094908/verification.json。
- 软件 TAP 双栈测试；测试容器已清理，现有运行环境与 v1.0 镜像保持不变。代码未提交或推送。

## 2026-10-28：Snort IPS（Redis + confd）

本次实现采用 Agent 现有 Redis，不在 Agent 新增 etcd。Controller 保持原有存储。
新增 IPS_RULE / IPS_INTERFACE 和 IPS_STATUS / IPS_RULES，能力 snort-ips/v1。
Web 独立 IPS 页面复用候选、预览、事务提交、Undo 和实际查询。
confd 按次生成完整 Lua，Snort 校验后热重载，确认完成及代次后才接受配置；
失败恢复旧配置。接口使用 VPP 原生名称，IP feature arcs 不覆盖纯二层转发。
默认 IPS 关闭；VPP 镜像安装 Snort，启用后在 Agent 容器内运行。
已完成镜像构建和软件隔离验收：31 项 Python、20 项 Web 单测、三仓库 Go 测试、
20 项 IPS API/转发/重启检查、真实浏览器创建/查询/删除规则，以及 29 项 EVPN 双栈回归通过。
报告位于配套 Agent 仓库 artifacts/ips/redis-20261028-164849/verification.json。
浏览器首次因测试选择器未等待加载完成失败，修正后复测通过；原始记录保留。
未测试物理 NIC 或吞吐性能；未替换现有运行容器和 v1.0 镜像；没有提交或推送代码。


## 2026-10-28：VXLAN 外层 VRF 创建与修改

已实现配置字段 encapVrfId / protobuf encap_vrf_id，默认 0，与 BD/BVI 的内层租户 VRF 独立。
Web 提供创建字段、修改操作及查询列；Controller 按 vxlan-encap-vrf/v1 校验能力并生成 VXLAN SET；
Agent/适配器持久化、校验、执行与回滚；VPP 新增 vxlan_set_encap_vrf API 和
set vxlan tunnel <interface> encap-vrf-id <table-id> CLI。
保留接口/VNI/BD、远端 MAC/BUM，迁移外层收发 FIB；支持 IPv4/IPv6 和多点/固定目的接口。
Linux 控制镜像更新 lower device，不改变 tenant bridge master。状态文件 v4 兼容读取 v1/2/3。
Underlay/LCP、物理接口归属及路由仍由启动配置准备；不承诺无损切换。
组播目的/RX flow offload 的在线迁移明确拒绝。旧 VRF 必须在隧道切换后另行删除。
开发验收使用独立 :encap-vrf-test 镜像及隔离容器，不替换运行中的 fabric，不发布、不提交/推送代码。
详细操作和边界见相应用户及开发文档的“VXLAN 外层 VRF”章节。
默认 BGP 实例需能解析远端 VTEP；承载 VRF 独立时可由部署配置使用 FRR nexthop-vrf 泄漏真实路由。VXLAN SET 不自动修改这部分 Underlay 配置。

验收记录：45 项 Python 适配器测试、25 项 Docker/Python 测试、Agent/Controller/Gateway 全部 Go 测试、Web 18 项测试及类型检查/构建通过；真实浏览器验证提交显式 0 与查询列。原生 IPv4/IPv6 的多点/固定目的 CLI/API 在 0 和 2 worker 下通过；两个独立整栈实验（IPv4/IPv6 VTEP，内层双栈，0 worker）均通过 0→100→200→0、配置重载、失败修改和删除回滚。实验未使用物理网卡。汇总位于配套 Agent 的 artifacts/vpp/encap-vrf-20261028-143107/verification.json。测试容器与网络已清理。


更新日期：2026-10-28。

### 2026-10-27：默认使用 GitHub sdn/v26.06 并验证完整源码构建

`docker/build-vpp.sh` 和 `docker/vpp/Dockerfile.base` 的 Git 模式默认使用 `nanfengnan1/vpp` 的 `sdn/v26.06`；FRR 保持 `nanfengnan1/frr` 的 `frr-10.7.0`。仍支持单独覆盖 ref、固定 commit 或使用本地源码。首次构建设置 `BUILD_BASES=1`，移动分支更新后更换 `SOURCE_CACHE_BUST`。本记录取代下文早期“远端 VPP_REF 必填 / 构建待验证”的状态。

已在 10.8.126.103 从 GitHub 构建两个基础镜像和 `alpine/sdn-agent:vpp-github-test`，镜像 ID `ab0296d3525d280ae7636fffa0ead0f7ab2a7063cb28412e7b348db1e3dc9e17`。VPP 实际提交 `194ee9f7db669e9776dd7e0fd9845ef2d9dd8031`，FRR `87fe21fda92ce9e2ba3eaf2b0a327bf71ee183ef`，来源元数据均 `dirty=false`；`vpp -v` 的 dirty 后缀来自镜像内已记录的 Alpine 兼容补丁。PAPI、API JSON 和适配器均从同一 VPP 基础镜像复制，导入和必要 API 检查通过。

新增默认分支回归用例，扩展带斜杠的 Git 分支获取用例；Docker 单元测试 25 项通过，VPP Dockerfile BuildKit 静态检查无警告。`lab/vpp_evpn.py` / `lab/vpp_irb.py` 新增 `SDN_VPP_OUTPUT`，允许指定独立报告目录，保留旧实验记录。本次构建和验收日志保存在 `artifacts/vpp/github-sdn-v26.06-20261027-201038/`。

新镜像通过四组隔离软件组网：IPv4/IPv6 underlay 的三 Leaf EVPN 各 23 个布尔检查（每 Leaf 两个 worker），双 Leaf IRB 各 29 个布尔检查。包含内层双栈、MAC 移动、多点 API、Type-2/3/5、租户隔离、40 秒连续转发、失败回滚及撤销。全部 `sourceOverrides=false`，验证镜像自带适配器；本次未重复物理 VF 或硬件卸载验收。

Agent 全部 HTML 手册与 VPP 适配用户/开发/LCP 页面定向构建均通过 Sphinx 严格检查，已核对 VPP 正式导航入口。测试容器和网络已清理，测试前后原运行容器的名称、ID、镜像一致。未提交或推送 Git，也未覆盖 vpp-dev / v1.0 镜像标签。

### 2026-10-27：MCP 同步 VPP / EVPN 能力

MCP 保留六个默认只读与六个显式写工具，继续走既有 gRPC。初始化 Instructions、工具描述与 schema 增加 VPP composite、原生 src-only VXLAN、外层双栈、LCP 接口、BD/BVI/L3VNI 与 Router MAC 的语义；能力以实际 Agent 的 operations/backends、queries/families、features 为准。网络查询 source=vpp，协议查询仍来自 FRR；保留原生 records 的嵌套 remotes 和 camelCase 字段，不增加客户端任意选择后端的参数。

sdn_state 的 observation_error 非空，或 sdn_query 的 error 非空 / stale=true，返回 isError=true 与 FailedPrecondition，同时保留原始数据。身份/kind 校验先执行；不把恢复暂停、API 故障或不完整观测标为健康。startup.conf 外部挂载入口已写入 MCP 文档，但文件修改、CPU/内存/PCI、重启与切换数据面没有对应 MCP RPC，仍未实现。

`mcp/vpp_test.go` 新增 7 组测试，使用真实 Agent/Registry/VPP Go Driver 与模拟 Unix HTTP 适配器，覆盖双栈远端、分页指纹、L3VNI 候选、后端约束及异常证据；不启动 VPP 或 Agent worker，不执行宿主机网络命令。本次 go test ./...、go test -race ./mcp/...、go vet ./mcp/...、make build-mcp 和严格 HTML 文档构建通过；生成 bin/sdn-agent-mcp，测试记录在 artifacts/mcp/vpp-update/。未重建/部署容器或提交/推送代码，不等同真实网络转发验收。

### 2026-10-27：架构文档与可选择源码版本的构建入口

`docs/developer/vpp.rst` 增加 FRR/ZAPI/Netlink/LCP/Unix HTTP/Binary API 分层、Linux/VPP 对象映射、LCP 节点和 TAP/TUN 双向通信、完整对称 IRB 流程。参考 `/home/alexan/vxlan-s-evpn/docs/从零手动配置.txt`，明确业务 BD→BVI→租户 FIB→传输 BVI/BD→L3VNI；Leaf2 出口为 br20。区分参考文件 OSPF+Loopback 与已测静态 underlay/接口 VTEP，不扩大验收结论。

`docker/build-vpp.sh` 与两个基础 Dockerfile 支持 nanfengnan1/vpp、nanfengnan1/frr 的 tag/branch/commit，以及各自的本地工作区。该阶段远端 VPP_REF 必填（现已改为默认 sdn/v26.06，见最新记录），FRR_REF 默认 frr-10.7.0；BUILD_BASES 默认 0，首次设 1。基础镜像默认名改为 alpine/vpp:sdn-base、alpine/frr:sdn-base，最终仍为 alpine/sdn-agent:vpp-dev，名字与源码版本独立。该阶段仅完成脚本/文档校验；后续 GitHub 完整源码构建结果见最新记录。

VPP 基础镜像同时保存原生插件/API/PAPI/适配器，Agent 集成不再单独复制外部 VPP_SOURCE，防止版本漂移；旧基础镜像需先重建。构建前检查必要 API/文件，记录实际 commit、ref、dirty、未跟踪文件 SHA256。新版本选择不等于任意 VPP/FRR 组合兼容，已验收仍是本地 VPP26.06/FRR10.7.0。构建不部署、不改网卡、不提交或推送。VPP 仓库另有 `docs/usecases/sdn_agent.rst` 和 `docs/developer/extras/sdn_agent.rst`，分别面向使用者与适配开发者。

本次验证：Docker 单元测试 24 项通过（含新增构建输入/源码来源测试 15 项）；三个 Dockerfile 通过 BuildKit 静态检查，其中两个基础 Dockerfile 分别检查 Git 和 local 模式。Agent 完整 HTML 手册严格构建通过，VPP 新增两篇手册与 LCP 文档的定向严格构建通过，并核对原站点 toctree 入口。本地源码预检查读回真实 commit 与修改标记。FRR 原有 .dockerignore 会排除 .git，脚本通过独立的 frr_git/vpp_git 上下文保留历史；本地模式要求完整 clone，暂不支持 linked worktree。未执行完整镜像编译、部署、网卡操作或 Git 提交/推送。

### 2026-10-27：华为 SR-IOV VF 容器实测

`lab/vpp_sriov_host.py` 显式接管测试 PF `0000:3b:00.0`，保存原驱动、override、VF autoprobe、容器和管理路由后，再停止 `vpp-26-06`。该 PF 当前提供 3 个 VF；PF 留在内核 `hisdk3`/`hinic3`，VF `0000:3b:00.2/.3/.4` 分别绑定 vfio-pci，分配给 Leaf1 / Spine / Leaf2，IOMMU group 为 142 / 143 / 144。管理网卡 eno4 不参与实验。默认测试结束自动删除实验容器并恢复原 PF 驱动和 VPP 容器；`--retain-host` 仅用于连续测试，最后必须显式 restore。

`lab/vpp_sriov.py` 使用 VF0 access VLAN 3101、VF1 trunk、VF2 access VLAN 3102；Spine 在一个 VF 上创建两个 VLAN 子接口。PC 为 network=none 容器，Leaf 的 VPP TAP 直接创建到 PC netns，另建 LCP 控制镜像供 Agent 管理。Docker bridge 仅用于管理，不承载 PC / VXLAN 业务。`vpp_sriov_evpn.py` 验证双栈底层、L2VNI、Type-2 对称 IRB、Spine 断链和恢复及原生查询；`vpp_sriov_offload.py` 验证双向 2 MiB TCP 和 Spine VF 接收侧 pcap 校验和。IPv4/IPv6 VTEP 分开运行，报告和抓包在 `artifacts/vpp/sriov/`。测试通过范围以各次 report.json 为准，不将脚本存在视为通过。

实测修复：Agent bundle 补齐 DPDK 的 libarchive / libatomic / isa-l / RDMA 运行依赖，并在构建时检查缺失动态库；VPP `driver.c` 登记 `net_hinic3`，`device.c` 在未知 PMD 创建 VLAN 子接口时检查空驱动指针，修复实际触发的 SIGSEGV。只更新开发镜像，不修改 v1.0。

本次验收通过：IPv4 VTEP 30 项、IPv6 VTEP 31 项；在 Spine VF RX 分别校验 7,023 / 7,084 个 VXLAN 报文，内层双栈 TCP 双向 2 MiB 哈希一致；三个节点的 Web Gateway/Controller 原生查询和 6 项真实浏览器检查通过。另通过 6 项宿主机保护/恢复单元测试、9 项 bundle 测试、32 项适配器测试。实验资源已清理，PF 恢复 vfio-pci、sriov_numvfs=0，原 vpp-26-06 健康运行，eno4 管理路由不变。结果索引为 `artifacts/vpp/sriov/validation.md`，不代表 Type-5 在物理 VF 上另做了验收。

该实验验证同一物理 NIC 的 VF / 硬件内部交换路径；不等同多台物理 Leaf/Spine 的外部线缆测试。当前 RX LRO 和硬件 VXLAN TSO 未验收，RSS 仅单 RX 队列，不宣称吞吐、硬件隧道卸载、多队列分流或 HA 达标。复现与恢复命令见 [VPP 开发文档](docs/developer/vpp.rst)。

### 2026-10-27：VPP + FRR EVPN / 对称 IRB 实验版

已实现 `agent.dataplane=linux|vpp`，重启生效；默认 Linux 不变。VPP 模式使用单一 composite Driver 管理 Linux 语义镜像与 VPP，不能同时注册独立 Linux Driver。状态绑定数据面，拒绝直接拿旧 Linux 日志切换 VPP。

本地适配器位于 VPP 仓库 `extras/sdn-agent`；使用 Binary API 配置 BD / 端口 / 隧道 / FDB，通过受管 Linux VXLAN FDB 接收 Zebra 有效远端状态，将 VPP 接入端口学习的 MAC 回送 Linux / FRR。周期核对约 1 秒，尚未采用事件驱动。

新增 `irb.py`：租户独立 FIB、BD/BVI 网关、L3VNI Router MAC、Type-2 MAC/IP 与 Type-5 选中路由、ECMP/next-hop 对象和远端 ARP/ND 抑制表。网关 MAC 的报文进入租户 FIB，普通二层报文查 BD；业务在 VPP 转发。Linux 镜像使用 tc 双向隔离，保留 carrier 供 Zebra 识别。

原生多点 VXLAN 已落到 `src/plugins/vxlan/multipoint.c`：每个 VNI 一个接口，CLI 只指定 src，v3 API 以 dst_address=0.0.0.0 表示无固定远端。`vxlan_mp_remote_add_del/dump` 独立维护 MAC→VTEP 和 BUM 成员，不产生逐远端接口。二层 BD/FDB 选择接口，接口按目的 MAC 选远端封装；跨网段先入口 BD/BVI→租户 FIB→出口 BVI/传输 BD，以 Router MAC 选择远端。外层报文查 underlay FIB。Type-3 撤销只删复制成员，所有远端撤销后仍保留 VNI 接口。旧固定 dst 模式和 `l2_egress_flood` API 保留。能力标识为 `vxlan-multipoint/v1`。

多点接口支持 IPv4/IPv6 VTEP 和以太网载荷（内层 IPv4/IPv6）；同一接口的远端须与本地 VTEP 同地址族。VXLAN API 2.3.0 新增 `vxlan_mp_remote_add_del_v2/vxlan_mp_remote_v2_dump`，旧 IPv4 API 保留。TCP GSO 在封装前软件分段；IPv6 外层 UDP 强制有效校验和，接收重新校验外层并清理旧标记。`lab/vpp_offload.py` 验证真实 TAP GSO、TCP 内容及抓包校验和，不能等同物理 NIC TSO/LRO 验收；保持 RX LRO 关闭。旧固定 src/dst RX flow 规则拒绝用于多点接口。

查询已贯通 sdn-web → sdn-controller → Agent → VPP：VXLAN 返回原生接口、地址族、MAC→远端及 BUM 列表；接口查询包含 BVI/VXLAN，支持租户、BD、接口、地址族和远端筛选。Web 展示 VPP 标识与可展开远端表，viewer 可读。Controller 接受双栈 VTEP/underlay 邻居，configureSystem 启用 IPv6 转发；Linux VXLAN 回读兼容 local6/remote6。能力增加 `vxlan-dualstack/v1` 和 `vxlan-software-gso/v1`。真实链路测试用 `lab/vpp_stack.py` 与 sdn-web 的 `tests/e2e/vpp-query.cjs`。

三 VTEP 验收由 `lab/vpp_evpn.py` 执行，原生 API/CLI 生命周期用例见 `lab/vpp_multipoint_api.py`；设置 `SDN_VPP_UNDERLAY=6` 验证 IPv6 外层。运行结果以 artifacts/vpp 的本次报告为准，不代表生产吞吐验收。

模型版本 2 兼容 L2 旧状态与 Undo，保存租户、绑定、BVI 属性、地址与 VNI；不保存动态 EVPN 学习结果。真实验收脚本为 `lab/vpp_evpn.py` 和 `lab/vpp_irb.py`，覆盖二层、对称 IRB、Type-5、租户隔离、撤销和回滚；报告以本次镜像运行输出为准。

镜像 `alpine/sdn-agent:vpp-dev` 集成 VPP 26.06、FRR 10.7.0、Agent、Redis 与适配器。代码、构建、真实隔离验收和冷启动限制见 [VPP 开发说明](docs/developer/vpp.rst) 和 [用户说明](docs/user/vpp.rst)。不修改或发布 v1.0 标签。

尚未实现或验收：多归属、租户内 BGP/OSPF 的 punt 通道、在线切换数据面、硬件 VXLAN TSO/RX LRO、全量冷启动 Running 重放、生产吞吐和 HA。第 7.3 节其余多后端动态启用设计仍未实现，不能把该提案的所有字段当成当前配置。

本文保留项目职责与长期设计约定。2026-10-24 已开始落地 Go 版本；当前实现范围与限制以 [README](README.md)、[协议](api/agent/v1/agent.proto)、[操作目录](docs/operations.md) 和 [事务说明](docs/transactions.md) 为准。下文未标为已实现的组件仍是目标设计，不表示已经交付。

2026-10-26 新增：[7.3 后端按配置启用与资源归属](#73-后端按配置启用与资源归属设计提案尚未实现) 为设计提案，尚未实现；其中的配置字段不能用于当前版本。

## 0. 当前实现映射

### 2026-10-25：独立资源与结构化 Query

保持原 op 编号和协议版本，增加 OSPFv3、BGP IPv4/IPv6 unicast、显式 EVPN Type-5 发布及 `Query` / `VerifyResources` RPC。新增能力通过 `Capabilities.queries` 与 `features` 协商；只安装 Linux 驱动时不会宣称 FRR 查询可用。

```text
Controller / sdnctl
  ├─ Begin → Stage(typed operations + depends_on) → Commit → Task/Watch
  │    └─ Candidate → Registry.Plan → Prepare/Undo journal
  │         → Linux / FRR Apply → Verify → Running revision
  │         └─ 失败：反向补偿，读回恢复结果
  ├─ Query(kind + VRF/BD/interface/AF + page token)
  │    └─ Querier → Linux ip/bridge JSON 或 FRR show JSON
  │         → records + source + 采集时间 + Running revision
  └─ VerifyResources(expected_revision + present desired operations)
       └─ 执行锁/提交锁 → 版本与任务屏障 → 全后端只读 Validate/Verify
            → verified + errors；不执行配置
```

VRF、BD、VXLAN 和它们的绑定分别建模。L3VNI 绑定前必须已有对应 Linux VRF、桥与 VXLAN 关联。TENANTS 查询合并 Linux 与 FRR 实际状态，保留未绑定资源。ARP/ND 抑制是桥端口能力，不能等同于主动探测或 MAC 保活。

Query 不返回裸 CLI 字符串，不把后端错误当作空表。分页令牌绑定结果摘要；动态数据变化返回 `Aborted`，不承诺冻结快照。EVPN_ROUTES 是全局 EVPN RIB，Type-5 可按 route_type/prefix 查询；租户关联由 EVPN_VNI/TENANTS 提供。

OSPFv3 使用 `ospf6d`，镜像监督其存活；运行旧持久化 daemons 配置时也确保该进程启用。IPv6 地址、邻居和路由可查询，Agent 不默认关闭 IPv6。

操作示例、查询和限制见 [用户资源手册](docs/user/resources.rst)；字段兼容、只读导入验证和测试见 [开发资源手册](docs/developer/resources.rst)。

阅读入口：当前分层架构见 [3.3](#33-当前实现的分层架构)，内部组件及代码映射见 [3.4](#34-当前实现的内部架构与代码映射)，配置、注册心跳、状态查询及重启恢复时序见 [9.6](#96-当前-go-实现的请求时序)。第 3.1 节保留长期目标拓扑，不能将其中全部组件理解为已实现。

- `cmd/sdn-agent`：双向 gRPC 身份注册/心跳、Agent 服务、进程锁、TLS/token 配置。
- `internal/agent`：Candidate、commit、串行 DAG 执行、全部后端参与、任务查询/事件重放、失败补偿和进程重启恢复。
- `internal/agent/recovery.go`：带 sequence/revision 校验及幂等收据的 RetryRollback；按逆依赖顺序恢复原 Undo，验证成功后解除节点屏障。主机重建后配置恢复仍见 [待办](docs/backlog.md)。
- `internal/adapters/linux`：bridge、VRF、VXLAN、接口/地址、绑定、sysctl 和静态路由。
- `internal/adapters/frr`：OSPF、BGP 基础与 EVPN、L3VNI、配置回读与独占实例快照回滚。
- `internal/store`：Redis 同 DB 内逻辑命名空间和开发用持久文件；Running 当前使用已成功提交的类型化操作日志。
- `cmd/mock-controller` / `cmd/sdnctl` / `lab/run.py`：模拟 Controller、真实 RPC 客户端、隔离 Spine/Leaf 实验。
- 当前协议使用静态 `oneof` 类型；不是动态 Any schema 分发。当前后端为进程内 Driver；远程 Adapter client 管理、FPM/ZAPI、主动 ARP/ND、全网拓扑聚合、资源级 diff 和本机 etcd 尚未实现。

## 1. 已确认的范围与行为

- `sdn-controller` 是独立项目，管理全网 Leaf 和 Spine；`sdn-agent` 是独立项目，维护本地 Leaf。
- 支持多个控制面来源，以及 Linux、VPP、交换机、FRR 配置等不同执行后端。
- Agent 与 Controller 使用 gRPC 通信，双方分别提供 Client 和 Server；op 及其参数通过 Protobuf 定义。
- Agent 首次启动生成候选 UUID，与 Controller 确认身份。首次注册发生 ID 冲突时，由 Controller 分配未占用的 UUID；后续管理消息携带最终 `node_id`。
- 节点管理状态只保留 `INIT`、`RUNNING`、`DOWN`，暂不增加复杂的节点状态机或负载状态机。
- Redis 使用 Candidate、Running、Operational 三类逻辑数据库保存本地信息，并持久保存事务与任务记录。
- 本节点一个 op 的所有注册实现都参与执行，并按依赖关系安排顺序，不从多个注册实现中任意选择一个。
- 配置修改先进入 Candidate；客户端显式调用 `commit` 后，服务器开始事务执行。失败时回滚已经产生的配置修改。
- 一次 commit 的事务范围是单节点的全部相关后端。跨节点变更由 Controller 协调，第一版不承诺多节点原子提交。
- 使用任务 ID、状态订阅和结果查询获取执行结果。受理成功与执行成功分别确认。
- Agent 已持久受理 commit 后，即使与 Controller 断连，也继续执行；执行失败才进入回滚，重连后可查询和补报结果。
- 保留 FRR EVPN Type-2、Type-3 学习与拓扑上报能力；FRR 继续负责分布式路由协议。

当前已通过显式 `depends_on` 生成 DAG，拓扑排序后串行执行；正式协议已落地为 [agent.proto](api/agent/v1/agent.proto) 中的静态 `oneof`、操作码、RPC 和错误结果字段。下文的动态扩展容器、资源级差异计算等仍是后续设计，不代表当前协议支持这些能力。

组件首先是逻辑模块，不要求每个模块都是一个进程。自有进程间接口可以采用 gRPC，外部 FRR、Linux、VPP 等继续使用其实际支持的接口。

## 2. sdn-controller 的能力

| 组件 | 能力与职责 | 主要输出 |
| --- | --- | --- |
| North API | 向网管、CLI 和自动化系统提供配置、查询、拓扑与任务接口；预留 REST、NETCONF 等协议适配 | 统一的资源请求 |
| Global Resource Manager | 管理租户、网络、策略、资源归属；维护 VNI、RT、地址等分配和校验规则 | 全局期望配置 |
| Node Inventory | 管理 Leaf/Spine 身份、角色、接口、执行后端、能力、连接状态和软件版本 | 节点清单与能力视图 |
| Global Planner | 决定资源部署在哪些节点；将全网资源转换成各节点期望配置；协调跨节点依赖与执行进度 | 带版本的节点配置 |
| Southbound Manager | 根据节点接入方式选择 Agent 通道或设备管理适配器 | 节点配置交付和状态接入 |
| Agent Gateway | Controller gRPC Server 接受注册、能力和状态上报；Controller gRPC Client 调用 Agent 的事务、订阅和查询服务 | 配置事务与状态消息 |
| Spine Device Adapters | Agent 仅部署在 Leaf 时，通过 Spine 支持的管理接口配置和查询 Spine | Spine 配置结果与观测 |
| Topology Service | 合并配置、EVPN、接口、LLDP、FDB、邻居等信息，形成物理、Overlay、主机位置视图 | 全网拓扑与数据来源 |
| Task / Health Service | 跟踪配置是否接收、执行和生效；呈现失败原因、受影响节点和过期观测 | 任务结果、告警与健康状态 |
| Storage / Coordination | 使用 etcd 保存全局配置、节点登记、资源分配、版本和协调信息 | 可恢复的全局状态 |

Controller 负责配置哪些路由协议、网络和策略；动态 EVPN 选路与路由交换在此设计中继续由 FRR 执行。

管理节点离线与路由不可达是不同的事件。Agent 心跳超时应标记管理状态和观测新鲜度，不能直接当作 EVPN 路由撤销依据。

## 3. sdn-agent 的组件职责

本表及 3.1 节描述目标职责划分，包含尚未实现的组件。当前 Go 实现将多个职责合并在一个 Agent 进程中，具体边界见 3.3、3.4 节。

| 分组 | 组件 | 职责 |
| --- | --- | --- |
| 节点管理 | Identity / Session Manager | 身份持久化、注册、能力协商、节点状态和管理会话恢复 |
| 北向接入 | Agent gRPC Server | 接收事务编辑、commit、结果查询和状态订阅请求 |
| 北向接入 | Agent gRPC Client | 调用 Controller 的注册、能力报告、心跳和状态上报服务 |
| Front Adapter | Protocol / Resource API | 解码 Protobuf，校验身份、op 与 data 类型、资源归属和版本，转换内部资源模型 |
| Front Adapter | Candidate Manager | 管理每个事务的候选配置及基础版本；提交前不修改后端 |
| Front Adapter | Transaction / Task Manager | 管理 commit、锁、持久化任务、执行进度、提交、回滚、订阅与恢复 |
| Front Adapter | Planner / Executor | 计算配置差异，展开全部后端步骤，建立 DAG，按顺序调用并核对结果 |
| Backend Adapter | Client Manager | 按配置文件或 CLI 接入后端，维护 client、通道、连接状态与重连 |
| Backend Adapter | Op Registry | 维护 op 到全部注册实现的映射及其能力、依赖、生命周期接口 |
| Backend Adapter | Backend Drivers | 将统一资源操作转换为 Linux、VPP、交换机、FRR 等后端 API |
| 本地观测 | Service Adapters / Event Normalizer | 接收或采集外部控制面信息，规范化来源、网络上下文和事件序号 |
| 本地观测 | Local Observers / Reconciler | 采集实际状态，对比 Running 与 Operational，发现偏差并按明确恢复策略处理 |
| 本地维护 | Neighbor Maintenance | 按已提交策略维护本地接入主机的 ARP/ND 探测 |
| 状态输出 | State Reporter / Task Events | 提供状态快照、增量和可恢复的任务状态通知 |
| 本地存储 | Redis Storage | Candidate、Running、Operational，以及事务、任务、步骤日志和身份记录 |

### 3.1 组件拓扑（目标设计）

```text
                         sdn-controller
                gRPC Client          gRPC Server
                     |                    ^
       edit / commit / query / watch      | register / report / heartbeat
                     v                    |
             Agent gRPC Server     Agent gRPC Client
                     |                    ^
                     v                    |
             Front Adapter          State Reporter
             Protocol / Resource API      ^
                     |                    |
                     v              Operational DB
             Candidate Manager            ^
                     |                    |
                     v            Local Observers / Service Adapters
               Candidate DB               ^
                     |                    |
                     | commit      Backends / FRR / FPM / Others
                     v
             Transaction / Task Manager <----> Task Store
                     |
                     v
             Planner / DAG Executor
                     |
                     v
             Backend Adapter Registry <---- Client Manager
                     |
                     +------ Linux Driver ------> Linux
                     +------ VPP Driver --------> VPP
                     +------ Switch Driver -----> Switch
                     +------ FRR Driver --------> FRR

     All required steps verified -> commit Running DB
     Failure -> reverse dependency rollback -> verify result
     Running DB <-> Local Reconciler <-> Operational DB
     Task Store -> Agent Server: WatchTasks / GetTask
```

图中控制面学习数据保存在 Operational 的独立来源空间，例如 `operational/learned/`，不会自动覆盖配置。Local Observers 负责回读实际对象；Task Store 保存任务结果，通知只是结果的传递方式。

Neighbor Maintenance 从 Running 读取已提交策略，从 Operational 获取探测对象；结果依靠后端实际事件或查询确认。发送探测报文本身不能作为主机仍然存活的证据。

### 3.2 第一版需要覆盖的资源能力

| 资源 | 期望支持的操作 |
| --- | --- |
| Bridge Domain | 创建、删除、查询和修改 BD；维护成员接口 |
| 网关接口 | 为 BD 对应的 SVI/BVI 配置 IPv4/IPv6 地址，并管理租户归属 |
| L2VNI | BD 与 L2VNI 的绑定、解除和状态查询 |
| VXLAN | 创建、删除、查询隧道；明确其 L2/L3 使用上下文 |
| Tenant / VRF | 创建、删除、查询租户与 VRF；维护租户与二层网络的关联 |
| L3VNI | 配置租户与 L3VNI 的关联；由具体后端落实需要的对象 |
| 静态 MAC | 增加、删除、查询静态 MAC 转发表项 |
| 邻居与 MAC 策略 | 配置 MAC 保活、ARP/ND 探测和 ARP 抑制；按后端能力落实 |
| FRR BGP / EVPN | 基础 BGP、邻居、EVPN 地址族、RD/RT、VRF-VNI 关联等配置与查询 |
| 网络感知 | 采集并报告 EVPN Type-2/Type-3、接口、FDB、邻居和执行状态 |

这张表定义目标接口范围。当前已有 Linux / FRR 后端和 VPP EVPN / IRB 实验版；具体操作以 [操作目录](docs/operations.md) 与 [VPP 限制](docs/user/vpp.rst) 为准。交换机、通用静态 MAC 操作等仍待扩展；VPP 主动 ARP/ND 保活见下文实现说明。不支持的操作必须明确返回能力错误。

### 3.3 当前实现的分层架构

以下各层都是同一个 `sdn-agent` Go 进程内的逻辑分层。Controller 是远端管理方，Linux 内核和 FRR 是实际执行目标；存储贯穿各层，不参与网络转发。

```text
                         sdn-controller / sdnctl
                                      |
                         gRPC + Protobuf + token/TLS
                                      |
  +------------------------------------------------------------------+
  | L1  Management / RPC                                             |
  | Agent Server: Begin / Stage / Commit / Abort                      |
  |               GetTask / WatchTasks / GetState / GetCapabilities   |
  | Agent Client: Register / Heartbeat -> Controller Server           |
  +------------------------------------------------------------------+
                                      |
  +------------------------------------------------------------------+
  | L2  Candidate / Transaction                                      |
  | Node check / request dedup / revision check / candidate freeze    |
  | Task receipt / persistent events / query and replay               |
  +------------------------------------------------------------------+
                                      |
  +------------------------------------------------------------------+
  | L3  Planning / Execution                                         |
  | Explicit depends_on -> DAG -> all registered backend steps        |
  | Serial worker: Prepare -> save Undo -> Apply -> Verify            |
  | Success: publish Running; failure/recovery: reverse rollback      |
  +------------------------------------------------------------------+
                                      |
  +------------------------------------------------------------------+
  | L4  Backend Registry / Driver                                    |
  | op -> []Driver; Validate / Prepare / Apply / Verify / Rollback     |
  | Observe is called by GetState; drivers are in-process Go objects  |
  +------------------------------------------------------------------+
                   |                                  |
           Linux Driver                         FRR Driver
           ip / sysctl                     vtysh / frr-reload.py
                   |                                  |
              Linux kernel                         FRR daemons

  Shared persistence: Store interface -> Redis OR local File store
  Candidate | Running | Operational | Task / Undo / Events / Receipts
  Process lifecycle: identity.json / local lock / INIT-RUNNING-DOWN
```

这里的规划输入是 Controller 已经生成的操作及依赖，不是完整资源配置的自动 diff。Running 当前保存成功提交的操作日志及 revision，不是已经归一化的资源快照。`Driver` 通过启动代码注册，没有远程 Adapter 连接管理或独立的 SAI 服务进程；周期 Reconciler、事件型 Service Adapter 和主动邻居维护也尚未实现。

### 3.4 当前实现的内部架构与代码映射

```text
                         cmd/sdn-agent
              identity + lock + startup + shutdown
                     |                    |
                     |                    +--> ControllerClient
                     |                         Register / Heartbeat
                     v
              transport + Agent gRPC Server
                     |
      +--------------+----------------------+------------------+
      |              |                      |                  |
  Begin/Stage     Commit                GetTask/WatchTasks   GetState
      |              |                      |                  |
      v              v                      v                  v
  Candidate      Task + frozen          persisted          Registry.Observe
      |          Candidate + event      task/events            |
      |              |                                         v
      +--> Plan <----+                                   Driver.Observe
      |              |                                         |
      |           wake worker                           Linux / FRR
      |              |                                         |
      |              v                                         v
      |        serial execute/rollback                   Operational
      |              |
      |         Registry.Driver(backend_id)
      |              |
      |        Prepare / Apply / Verify / Rollback
      |              |
      |         Linux / FRR drivers
      |              |
      +--------------+--------------------> Store
                     |                      Redis / File
             success publishes Running
```

图中 Store 是共享持久化接口：Candidate、Task、Running 和 Operational 都通过它读写；不是把每类记录部署成一个独立 Redis 服务。启动后 Worker 扫描持久化任务恢复执行或补偿，见 9.6.7。

| 当前职责 | 代码入口 | 实现边界 |
| --- | --- | --- |
| 进程装配、身份、注册与心跳 | [cmd/sdn-agent/main.go](cmd/sdn-agent/main.go) | 本地身份文件与进程锁；注册携带能力；心跳更新管理状态 |
| gRPC 鉴权与传输 | [internal/transport/transport.go](internal/transport/transport.go)、[agent.proto](api/agent/v1/agent.proto) | 静态协议；token 与 TLS/实验用 insecure 参数 |
| 候选、任务、执行、查询与恢复 | [internal/agent/agent.go](internal/agent/agent.go) | RPC 与单个后台 Worker；任务订阅读取持久化事件 |
| 操作依赖与持久化记录模型 | [internal/model/model.go](internal/model/model.go) | 操作 DAG、步骤、Undo、Candidate、Running 操作日志 |
| 后端注册、计划展开与能力 | [internal/backend/backend.go](internal/backend/backend.go) | `op -> []Driver`；调用全部注册实现；不动态加载远程客户端 |
| Linux 执行及回读 | [internal/adapters/linux/linux.go](internal/adapters/linux/linux.go) | VRF、bridge、VXLAN、接口、地址、静态路由等 |
| FRR 执行及回读 | [internal/adapters/frr/frr.go](internal/adapters/frr/frr.go) | OSPF/BGP/EVPN；配置校验与专用 FRR 实例快照回滚 |
| 本地持久化 | [internal/store/store.go](internal/store/store.go) | Redis 同库键空间或 File；原子保存任务、事件和成功 Running |
| 开发用 Controller | [internal/controller/controller.go](internal/controller/controller.go) | Register/Heartbeat 模拟实现，不是独立 `sdn-controller` 项目 |

当前 `GetState` 按请求采集后端，随后保存 Operational；没有独立周期采集线程，也没有标准化拓扑聚合。能力来自 Driver 的静态操作列表，尚未交换动态 schema 或完整参数限制。注册、能力与心跳的实际调用方向见 9.6.5；状态读取路径见 9.6.6。

## 4. 节点身份与三态管理

### 4.1 身份字段与首次注册

| 标识 | 含义 | 生命周期 |
| --- | --- | --- |
| `node_id` | Controller 确认的节点永久 UUID | 正常重启、名称和地址变化时保持不变 |
| `registration_id` | 首次注册流程的幂等标识 | 首次注册重试时保持不变 |
| `instance_id` | 当前 Agent 运行实例 | 每次 Agent 启动重新生成 |
| `node_name` | 展示名称 | 可以修改，不作为永久身份 |
| `backend_id` | 节点内的稳定后端实例标识 | 后端重连时保持不变 |
| `client_id` | 当前后端连接实例标识 | 连接重建时可以变化 |

首次注册流程：

1. Agent 生成候选 UUID 和 registration_id，保存到本节点独立的持久化目录。
2. Agent 使用注册凭据向 Controller 申请登记。
3. Controller 原子检查并登记身份。候选 node_id 冲突时，生成新 UUID 并再次检查占用。
4. Controller 保存 registration_id 与最终 node_id 的映射，重复注册请求返回相同结果。
5. Agent 持久化最终 node_id 和身份信息，后续管理消息使用最终身份。

已登记节点重连需要验证原身份，不因为冲突就自动换 UUID。容器公共镜像不能内置一个供所有节点共用的身份文件。UUID 是标识，认证凭据及其与 node_id 的绑定需要另行校验。

### 4.2 节点状态

以下是目标语义；当前代码以 Heartbeat 成功进入 `RUNNING`，Register 成功并不单独触发该迁移，独立运行模式除外。具体时序与身份校验边界见 9.6.5。

```text
INIT --身份确认、能力协商成功--> RUNNING
RUNNING --管理会话失效或保活超时--> DOWN
DOWN --重连并重新确认身份、能力--> RUNNING
```

- Agent 进程启动时进入 INIT，节点身份仍从持久化存储恢复。
- RUNNING 表示管理关系建立，不表示所有事务都已成功。
- DOWN 表示管理连接不可用，不触发删除 Running、清空转发表或停止 FRR。
- Controller 和 Agent 根据各自观测维护状态，网络分区时不要求双方瞬时一致。
- 两个方向的 gRPC 通道属于同一管理关系。建议任一必要方向持续不可用或会话心跳超时即判定 DOWN；具体超时和探测规则待定。
- 节点状态字段使用 `node_state`，已提交配置使用 `running_config`，避免混淆。
- 任务执行与回滚状态独立记录，不扩展节点的三态模型。

## 5. gRPC、Protobuf 与能力协商

### 5.1 双向 Client / Server

| 调用方向 | 接口建议 | 语义 |
| --- | --- | --- |
| Agent Client -> Controller Server | RegisterNode | 首次注册或恢复身份，确认协议版本及服务地址 |
| Agent Client -> Controller Server | ReportCapabilities | 上报 op、消息模型版本、功能和限制 |
| Agent Client -> Controller Server | Heartbeat / ReportState | 上报节点健康、状态快照和增量 |
| Controller Client -> Agent Server | BeginTransaction | 建立带基础版本的候选事务 |
| Controller Client -> Agent Server | StageOperations | 向该事务添加或修改配置操作 |
| Controller Client -> Agent Server | Commit | 冻结候选配置并提交执行任务，返回 task_id |
| Controller Client -> Agent Server | AbortTransaction | 放弃尚未提交的候选事务 |
| Controller Client -> Agent Server | GetTask / WatchTasks | 查询结果，或通过服务端流订阅任务变化 |
| Controller Client -> Agent Server | GetOperational / GetRunning | 查询实际状态或已提交配置 |

双方分别维护 gRPC Server 和 Client，提供可达地址并验证身份。gRPC 采用 HTTP/2，消息采用 Protobuf；建议启用 TLS，节点认证与证书或其他凭据的具体引导流程待定。

已提交事务不能通过 AbortTransaction 当作普通候选配置直接删除。第一版不默认提供任意阶段的任务取消；如增加取消接口，必须经过事务恢复流程。

### 5.2 公共消息与操作模型

建议公共消息携带 `protocol_version`、`message_id`、`correlation_id`、`node_id`、`instance_id` 和具体业务消息。首次注册尚无最终身份时，node_id 可以为空，候选 ID 放在注册参数中。

关键标识分别承担不同职责：

- `request_id`：一次业务请求的稳定幂等键，重试时不变。
- `transaction_id`：候选配置事务的标识，关联多次 StageOperations 和一次有效提交。
- `task_id`：commit 后后台执行任务的标识，用于订阅、查询和恢复。
- `op_id`：事务内某次具体操作的标识，同一个 op_code 可以出现多次。
- `step_id`：执行计划中的具体后端步骤标识。

建议 Operation 的逻辑结构为：

```text
Operation
  op_id
  op_code
  schema_version
  resource_id
  depends_on[]
  data
```

op、请求参数、结果和公共错误码通过版本化 `.proto` 定义。建议使用统一操作外层加具体类型的 Protobuf 参数，插件扩展容器可采用 `google.protobuf.Any`；Agent 必须验证 op_code、消息类型和版本是否匹配。

Any 只提供类型标识和编码内容，不自动安装 schema 或业务处理器。Controller 和对应 Adapter 仍需要兼容的消息定义及实现。未知 op 或不支持的语义在执行前拒绝，不能静默忽略。字段编号、操作编号、错误码须保持稳定，废弃编号不复用。

### 5.3 能力与错误

| 内容 | 定义位置 |
| --- | --- |
| op 含义、data 格式、公共错误码 | 共享的版本化协议模型 |
| 本节点各后端支持哪些 op 和模型版本 | Agent 能力报告 |
| IPv6、ARP 抑制、批量大小、资源限制等 | op / 后端能力属性 |
| 校验、回读、幂等、回滚支持情况 | 后端操作生命周期能力 |
| 后端当前是否在线、可用 | Operational 状态 |

一个后端不必实现全部 op。对已注册的某个 op，全部注册实现都参与执行；对外可用能力还需要满足这些实现的参数约束和事务要求，不能简单取能力并集。能力变化后重新上报，已经受理的事务使用其冻结的参与集合。

建议公共错误至少区分 INVALID_ARGUMENT、UNSUPPORTED_OPERATION、UNSUPPORTED_SCHEMA、REVISION_CONFLICT、DEPENDENCY_ERROR、BACKEND_UNAVAILABLE、ROLLBACK_UNSUPPORTED、APPLY_FAILED、VERIFY_FAILED、RESULT_UNKNOWN 和 ROLLBACK_FAILED。正式编号及完整错误目录后续在 `.proto` 中定义。

错误结果包含公共错误码、后端原始错误码、backend_id、op_id、step_id、描述、是否可重试、是否部分生效，以及回滚结果。gRPC 调用状态与后台任务结果分别表达；RPC 超时不能直接解释为配置未执行。

## 6. 本地数据库与持久化记录

| 存储 | 内容 | 主要写入方 |
| --- | --- | --- |
| Candidate DB | 按 transaction_id 隔离的候选配置；基础 Running 版本、修改集、操作归属和冻结信息 | Candidate Manager |
| Running DB | 最后成功提交的配置基线、版本及资源归属 | Transaction Manager 在提交完成时写入 |
| Operational DB | 实际配置对象、接口/FDB/邻居、学习信息、后端连接、健康状态及配置偏差 | Local Observers、Service Adapters、执行模块 |
| Task Store | 持久化任务、请求去重、计划、参与后端、步骤结果、原配置和回滚信息、事件序号 | Transaction / Task Manager |

前三项是三类逻辑数据库，Task Store 是额外的持久化记录空间，可以放在同一 Redis 实例的独立键空间中，无需新增数据库服务。具体使用逻辑 DB 还是键前缀在实现时确定。

Candidate 保存规范化的候选资源模型或基于明确版本的修改集，不能只把原始 RPC 入库后直接与 Running 做字符串比较。不同事务不能修改同一份无归属的 Candidate。

学习信息建议在 Operational 中按来源隔离，并保留 node_id、网络上下文、来源、时间、序号、增加/删除语义和新鲜度。事务回滚不恢复或覆盖动态学习的 MAC、邻居和路由历史。

Running 记录已提交基线；Operational 记录实际状态。执行或回滚途中两者可能存在差异，必须如实呈现，不得用 Running 未变化来证明后端没有变化。

Task 与冻结的 Candidate 在返回 commit 已受理前必须可靠保存，定义一致写入和重启恢复规则。Redis 持久化策略必须与受理承诺匹配，不能只依赖内存写入或 Pub/Sub。

最终任务结果和 Running 新版本需要一致发布，或通过持久化日志保证崩溃后可恢复。活动事务、去重记录及恢复所需信息不能提前过期；完成后的保留时间另行配置。

配置下发核对 Candidate 与 Running；本地恢复还需要核对 Running 与 Operational，否则后端重启丢失对象时可能无法发现。恢复操作只处理本项目拥有的资源，并避开正在提交或尚未恢复完成的事务。

## 7. Backend Adapter 与 op 注册

### 7.1 注册和连接

```text
op_code -> [
  op_obj {
    backend_id, client_id, schema_version, capabilities,
    dependencies,
    validate, prepare, apply, verify, rollback
  },
  ...
]
```

Client Manager 通过配置文件或 CLI 接入后端并维护连接，Registry 管理后端提供的操作实现。`backend_id` 表示稳定后端身份，`client_id` 表示本次连接；不能用易变 socket 身份替代稳定的资源归属。

当前同一个 op 的所有注册实现全部参与；7.3 节提出改为按资源归属选择后端及显式协同集合，尚未实现。目标设计要求 commit 时固定本次参与集合及注册版本，后续后端加入不改变正在执行的事务。后端断线应保留其登记并标记不可用，不能通过移除登记将其静默排除。缺少实现或必需后端不可用时，不能把配置当作成功的空操作。

callback 是 Agent 内的函数或 RPC 代理，不跨网络传递函数对象。独立后端进程通过接口暴露操作，Agent 中的代理负责转换并发送；Linux、VPP、FRR 等原生接口由具体 Driver 适配。

### 7.2 生命周期接口

| 接口 | 职责 |
| --- | --- |
| validate | 检查参数、依赖、消息版本、后端能力和资源限制 |
| prepare | 查询原配置、生成持久化恢复信息；如需预留资源，预留必须可撤销 |
| apply | 执行配置，并返回能关联到具体步骤的结果 |
| verify | 查询或等待后端确认配置已落实 |
| rollback | 恢复本事务修改前的配置，并核对恢复结果 |

调用上下文应包含 transaction_id、task_id、op_id、step_id、backend_id、幂等标识及资源身份。具体签名后续定义。

同步接口返回 0 可以表示该步骤执行完成；如果后端仅受理请求，应返回 PENDING 并提供查询或通知机制，不能直接当作成功提交。后端调用超时应记录结果不确定并回读，不能盲目重复 apply。

所有参与配置事务的实现必须支持所需的恢复和核对能力。无法提供回滚保证的 op 应在执行前拒绝进入本类事务，不静默降级成尽力执行。

这里的 SAI 指项目内部统一操作抽象。交换机 Driver 可对接 OCP SAI 或厂商 SDK。FRR 是控制面组件，其配置接口可以使用同一执行抽象；FRR 配置 Driver 与 FRR 状态采集 Adapter 分别承担配置和观测。

### 7.3 后端按配置启用与资源归属（设计提案，尚未实现）

**状态：通用机制仍为设计提案；2026-10-27 已落地启动时选择 linux / vpp 的特定实现。** 实际字段是 `agent.dataplane`，VPP 模式使用 composite Driver。下面的通用后端开关、动态资源归属和配置热加载草案尚未实现。

#### 7.3.1 现状与设计目标

| 项目 | 当前实现 | 待实现设计 |
| --- | --- | --- |
| 后端启用 | Linux 固定注册；FRR 由 `agent.frr` / `-frr` 控制，默认启用 | 通过统一后端配置选择启用的 Driver，并校验依赖与就绪状态 |
| 操作分发 | `Registry.Plan` 对同一 `OpCode` 调用所有已注册实现 | 按资源归属选择执行后端；需要协作时显式展开多个后端步骤 |
| 能力声明 | 汇总已注册 Driver 的操作与查询能力 | 只发布已启用、就绪且依赖完整的操作与查询能力 |
| 查询与核对 | Query 通常按后端名称排序后选首个支持者，TENANTS 有 Linux/FRR 合并逻辑；Verify 遍历该操作的注册实现 | Query、Verify、Observe 与写入使用同一套归属和协作规则 |
| 配置生效 | 启动时读取配置，没有运行时后端热切换 | 第一阶段重启生效；运行时重载作为后续独立阶段 |
| 重启恢复 | 加载 Running，恢复持久化任务；任务步骤已保存 `Backend` | 启动前校验历史任务、Undo 和资源归属所需后端，防止禁用或换绑破坏恢复 |

当前并不是将全部配置广播给所有后端：接口、地址、Bridge、VXLAN、静态路由主要由 Linux 执行；BGP、OSPF、EVPN 协议配置由 FRR 执行；启用 FRR 时，TENANT/VRF 涉及 Linux 与 FRR 协作。启动也不是向所有后端重放整个 Running。问题在于将来 Linux、VPP 等同时声明同一操作时，现有注册机制会让它们全部执行，不能表达资源应由谁管理。

因此本方案同时解决“启用哪些后端”和“每项操作由哪些后端负责”。Linux、VPP、OVS 属于可选数据面方向；FRR 是路由协议后端，常与数据面协作。仅增加 VPP/OVS 开关或 Driver 注册，不会自动完成它们与 FRR 的路由下发和事件反馈集成。

#### 7.3.2 配置草案与兼容性

以下仅为拟议配置片段，字段位置与最终 schema 在实现时确定。**当前配置解析器不支持 `backends`、`bindings`，会拒绝这些未知字段；不可直接写入当前 `agent.conf` 使用。**

```json
{
  "backends": {
    "linux": {"enabled": true},
    "frr": {"enabled": true}
  },
  "bindings": {
    "dataplane": "linux",
    "routing_protocol": "frr"
  }
}
```

- `backends` 控制后端是否加载、检查其依赖和纳入能力集合；`bindings` 指定角色对应的后端。只声明支持某个操作，不应自动取得该资源的写入权。
- 第一阶段拟限制为一个默认数据面，FRR 按需启用；禁用 FRR 时不得仍将其绑定为路由协议后端。多数据面并存时的逐资源绑定、稳定资源标识和迁移流程留待后续设计。
- 绑定必须引用已启用且实现相应能力的后端。未知后端、角色不匹配、依赖缺失、归属冲突应明确报错，不按注册顺序选择，也不静默回退。
- 拟兼容现有 `agent.frr` / `-frr` 和默认 Linux + FRR 行为；引入新配置时需明确旧开关的映射规则。建议新旧字段发生冲突时报错，禁止静默覆盖；该兼容规则也尚未实现。

#### 7.3.3 规划、执行与观测的一致性

拟采用以下处理流程：

```text
读取并校验配置 -> 解析后端与角色绑定 -> 检查依赖和就绪状态
  -> 加载持久化状态并检查恢复依赖 -> 固定本次启动的注册表与选择规则
  -> 注册有效能力 -> 接收配置、查询及恢复请求

操作 + 资源归属 -> 选择后端及协同集合 -> 生成持久化步骤
  -> Prepare / Apply / Verify -> 成功提交或按原步骤逆序补偿
```

这里的启动顺序是目标流程，需调整当前启动实现；能力发布前必须完成依赖校验。后端不可用时不能宣称对应操作可执行，也不能将“未执行”当作成功。

单后端资源只交给其所属后端；TENANT/VRF 等跨后端资源由明确的协作规则生成参与集合和步骤依赖。创建、删除及回滚顺序需分别定义，保留现有 Linux/FRR VRF 删除的依赖约束，不能简单依赖注册顺序或统一反转。必需参与者缺失时，应在规划阶段拒绝整个操作。

Stage 与 Commit 必须使用一致的绑定依据，并在 Commit 时重新校验能力及依赖。Query、VerifyResources 与 Observe 使用相同归属规则；跨后端聚合应保留来源并报告采集失败，不把后端缺失当成空结果或核对成功。Capabilities 同步过滤操作、查询和相关功能声明；Controller 需更新节点能力并重新校验受影响的计划。

容器启动器和健康检查也要依据同一份有效配置决定是否管理 FRR 等服务。原生部署仍按其部署方式管理外部进程，不能把“加载适配器”等同于“Agent 已负责启动外部服务”。

#### 7.3.4 持久化、禁用与重启恢复

任务步骤已有 `Backend` 字段，执行和回滚按该后端名称寻找 Driver。改变配置后，不能把旧步骤或 Undo 转交给另一个后端；仅关闭驱动却保留未完成任务，会使执行或补偿缺少实现。

- 在放行新写入前检查待执行、执行中、补偿中以及 `ROLLBACK_FAILED` 等需要恢复的记录。若所需后端被禁用或身份不匹配，应阻止以新配置进入正常写入状态，给出任务及缺失后端信息，要求先恢复所需配置并完成处理。
- 对已有受管资源，不能把变更默认绑定理解为资源迁移。需补充稳定的资源归属记录及历史记录兼容策略；存在未处理资源时，应拒绝无迁移流程的禁用或换绑。当前 Running 操作日志不能直接视为已完成的资源归属模型。
- 禁用后端不等于删除其配置，也不自动清理接口、VRF、VXLAN 或路由。资源清理应在原后端可用时通过明确事务执行，迁移则需要独立的可核对流程。
- 以上约束同样适用于修改配置后重启进程，并非只有运行时热切换才需要检查。恢复既有事务与主机重建后重新下发全部期望配置是不同能力，本方案不将后者视为已实现。

#### 7.3.5 通用机制分阶段实施与验收（尚未实现）

| 阶段 | 拟实现内容 | 验收重点 |
| --- | --- | --- |
| 1：启动时选择 | 通用后端配置、旧配置兼容、默认角色绑定、依赖校验；配置修改后重启生效 | Linux 单独运行及 Linux + FRR 行为；未知或矛盾配置拒绝；禁用后端不加载、不被调用 |
| 2：统一归属与能力 | 操作选择、显式协同依赖、Query/Verify/Observe 统一、能力与容器服务同步 | 同一操作不因多个 Driver 注册而重复下发；协作操作全部必需步骤参与；能力与实际可用功能一致 |
| 3：变更与恢复约束 | 持久化归属及配置版本、历史数据兼容、启动恢复依赖检查 | 待执行任务、崩溃补偿和失败回滚不被换绑；有受管资源时禁止隐式迁移；不自动删除网络配置 |
| 4：运行时重载 | 独立设计配置代次、事务隔离、原子切换及 Controller 能力同步 | 重载失败保留旧配置；新旧事务不混用后端；旧计划失效或重校验；恢复链仍可执行 |

阶段 1–3 共同组成可交付的启动时配置选择能力，不能在恢复校验缺失时允许危险的禁用或换绑。阶段 4 延后：事务需要固定配置代次和参与后端；切换前隔离新写入并检查候选、执行及回滚任务，满足条件后原子替换不可变注册表。旧事务不能被新配置重新解释。

预计涉及 [配置解析](internal/config/config.go)、[启动与注册](cmd/sdn-agent/main.go)、[后端规划和能力](internal/backend/backend.go)、[查询与核对](internal/backend/query.go)、[任务执行与恢复](internal/agent/agent.go)、[恢复接口](internal/agent/recovery.go)、[持久化模型](internal/model/model.go) 和 [容器启动器](docker/supervisor.py)，并需同步 Controller 的能力消费及用户、开发文档。验收用例需覆盖重复操作实现、共享资源依赖、禁用后查询、后端不可用、历史任务恢复及旧配置兼容；本节列出的用例是待办，不表示已编写或已通过。

## 8. 依赖计划与执行顺序

建议使用有向无环图 DAG。依赖来自资源模型、操作显式依赖和 Adapter 声明，不能使用 callback 的注册先后代替业务依赖。

两类依赖需要同时表达：

- 操作之间：例如先创建 BD，再添加 BD 成员。
- 同一操作的后端之间：例如先建立底层对象，再执行依赖该对象的 FRR 配置。

```text
A --------+
          |
          v
          C -----> D
          ^
          |
B --------+
```

A、B 必须完成后才能执行 C，C 完成后才能执行 D。第一版拓扑排序后串行执行，例如 A、B、C、D；后续是否并行另行决定。

每个步骤使用独立 step_id，关联 transaction_id、op_id、backend_id、资源和执行阶段。同一个 op_code 处理多个资源时必须展开成不同的实例。跨步骤需要的后端对象标识也纳入执行记录。

执行前检查：

1. 参数和基础配置版本是否匹配。
2. 全部注册实现是否完整、可用并满足事务能力要求。
3. 依赖是否存在，是否存在循环或互相冲突的资源修改。
4. 计划中的执行顺序及回滚路径是否明确。

计划、参与集合和恢复所需信息持久保存。删除类操作需要按资源生命周期建立依赖，不能直接沿用创建顺序。回滚依据已完成步骤的依赖逆序执行，并处理失败步骤可能产生的部分修改。

## 9. commit、回滚与任务恢复

### 9.1 客户端交互

```text
BeginTransaction(base_revision) -> transaction_id
       |
StageOperations(transaction_id, operations)  [可多次]
       |
Candidate: 暂存配置，不修改后端
       |
Commit(transaction_id, request_id) -> task_id / ACCEPTED
       |
WatchTasks(task_id, after_sequence) / GetTask(task_id)
```

一次事务可以包含一个或多个配置 op，全部相关后端参与同一次单节点提交。Controller 负责跨节点的任务编排和结果汇总，但单节点成功不代表整个网络的多节点任务已完成。

查询、注册和心跳不属于配置提交。动态控制面信息遵循独立的所有权与转发表集成路径，不能因用户配置事务回滚而被整体恢复成旧快照。其他非配置动作的具体接口边界后续定义。

### 9.2 服务端处理

1. 校验身份、请求格式和事务归属，进行请求去重；冻结本次 Candidate、参与后端集合及注册版本。
2. 一致保存任务、冻结的候选版本、参与集合和恢复所需入口信息，再返回 task_id 与 ACCEPTED。
3. 获取本节点配置提交锁，并再次检查 base_revision 是否仍与 Running 一致；不一致时拒绝执行，不自动覆盖新配置。
4. 生成计划，检查全部后端、依赖和可回滚性；执行 validate / prepare，保存原配置和撤销信息。
5. 按 DAG 顺序执行，调用前记录步骤意图，调用后记录结果，核对后端实际状态。
6. 全部步骤执行并验证成功后，发布 Running 新版本和成功结果。
7. 任一步骤失败，停止后续 apply，核对该步骤是否已产生部分效果，按依赖逆序回滚已产生的修改。
8. 核对恢复结果，保存最终任务结果及 Operational；清理策略不能删除仍用于恢复的信息。

第一版同一节点配置提交串行执行。可以保留多个隔离的候选事务，但后提交者必须通过版本检查，不能用旧 Candidate 覆盖新 Running。

预检查需要结合 Candidate 和计划中的待创建资源判断依赖，不能要求所有未来依赖已经存在于后端；实际 apply 前再确认前置步骤已经完成。

如果 preflight 或 prepare 失败，也需要释放已经产生的预留资源。未对配置产生修改的失败不必调用不存在的配置撤销步骤，但仍要记录任务和清理结果。

### 9.3 回滚语义

- 新建对象：仅撤销本事务实际创建的对象；事前已经存在的对象不能被误删。
- 修改对象：恢复修改前的属性和关联。
- 删除对象：保存足以重建受管配置的信息，并按恢复依赖重新创建。
- 回滚范围只覆盖本事务拥有的配置，不恢复无关人工配置、动态学习状态或整个后端数据库快照。
- 返回错误或调用超时的步骤也可能已经部分生效，需要先检查后端，不能只撤销明确返回成功的步骤。
- 操作和回滚需要幂等，确保进程崩溃后能够继续核对和恢复。

这种方案是多后端补偿事务：执行过程中可能短暂部分生效，回滚恢复配置也不等于恢复期间丢失的业务报文。若要求转发面中间状态不可见，必须由后端提供预配置和原子激活能力，单靠 Agent 顺序调用无法保证。

回滚自身可能失败。此时 Running 仍保留上一个提交基线，Operational 记录实际偏差；保留任务、恢复信息并阻止受影响资源上的冲突变更，不能声称后端已经恢复。

### 9.4 任务状态与查询

任务状态与节点三态独立。建议的任务状态包括：

| 状态 | 含义 |
| --- | --- |
| ACCEPTED | commit 已持久受理，等待执行 |
| VALIDATING | 核对版本、能力、依赖和恢复信息 |
| APPLYING / VERIFYING | 正在执行或确认后端结果 |
| RECOVERING | 重启或结果不确定后，正在核对与恢复 |
| ROLLING_BACK | 正在撤销已产生的修改 |
| SUCCEEDED | 全部相关后端已验证，Running 已提交 |
| FAILED_NO_CHANGE | 提交失败，确认没有遗留配置修改 |
| FAILED_ROLLED_BACK | 提交失败，已确认恢复原配置 |
| ROLLBACK_FAILED | 提交失败且恢复未完成，保留恢复记录和资源保护 |

GetTask 返回最终或当前状态、配置版本、逐后端结果、错误、回滚结果及最后事件序号。WatchTasks 可按序号恢复；历史过期后的快照与重新同步机制属于目标设计，当前尚未实现事件过期清理，持久事件缺失时返回读取错误。

通知来自持久化任务记录，不依赖一次性 Pub/Sub 投递。结果查询是可靠获取当前结果的入口，订阅用于及时通知。

### 9.5 幂等、崩溃与断连

- 相同 request_id 和相同请求返回原 task_id；相同 ID 携带不同内容返回冲突。
- 对已冻结事务重复 commit 不重新执行；返回已有任务或明确的事务状态。
- commit 响应丢失时，Controller 使用同一 request_id 重试或按 transaction_id 查询。
- Task Manager 拥有后台任务生命周期，提交 RPC 结束、超时或管理连接断开不取消已经持久受理的任务。
- Controller 断连后继续执行已受理事务；执行失败才回滚。重连后通过 task_id 查询或按事件序号补报。
- 未 commit 的候选事务不产生后端配置，其断连后的保留期限和过期清理另行配置。
- 当前 Agent 重启后扫描持久化任务：`ACCEPTED` 重新检查 revision 后执行；其余非终态任务进入逆序补偿，依据 `Started`、`Restored` 和 Undo 恢复已开始的步骤，不盲目重放全部命令。
- 即使后端已经完成、但 Running 与成功任务尚未原子发布，当前实现也采用补偿策略；“重新验证后补发成功版本”属于后续可选恢复策略，尚未实现。
- 当前 Worker 在节点内串行执行；持久化记录中存在 `ROLLBACK_FAILED` 时阻止该节点后续任务。可通过 RetryRollback 重试并验证 Undo；尚无按资源细分的恢复锁，也没有直接强制解锁接口。

节点身份恢复与事务恢复分别处理：重新协商进入 RUNNING 不自动把遗留任务标记为成功。

### 9.6 当前 Go 实现的请求时序

本节描述当前 Go 代码的实际调用流程：9.6.1～9.6.4 是配置提交、执行、回滚和结果读取；9.6.5～9.6.7 补充启动注册、状态采集和重启恢复。图中的 Redis 也可以替换为同一 Store 接口下的 File 存储。

配置流程以创建租户 `TENANT-A` 为例，假设 Controller 已完成身份协商，并已获得 `node_id` 和当前配置版本 `revision`。

同一个 op 会展开成两个执行步骤：

```text
TENANT / CREATE
    |
    +-- 1. Linux Driver：创建内核 VRF
    |
    +-- 2. FRR Driver：创建 FRR VRF 配置
```

#### 9.6.1 创建候选、暂存配置与提交

```text
 Controller           Agent RPC             Registry                Redis
     |                    |                     |                      |
     |-- Begin ---------->|                     |                      |
     |   node_id          |-- 读取 Running.revision ------------------->|
     |   base_revision    |<-------------------------------------------|
     |   request_id       | 校验身份、版本、幂等键                       |
     |                    |-- 保存 Candidate + 幂等记录 --------------->|
     |<-- transaction_id -|                     |                      |
     |                    |                     |                      |
     |-- Stage ---------->|                     |                      |
     |   transaction_id   |-- Plan(ops) -------->|                      |
     |   operations[]     |                     | 检查依赖、拓扑排序    |
     |                    |                     | 查找全部注册 Driver  |
     |                    |                     | 调用 Driver.Validate |
     |                    |<-- 校验结果 --------|                      |
     |                    |-- 保存 Candidate ------------------------->|
     |<-- 暂存成功 -------|                     |                      |
     |                    |                     |                      |
     |-- Commit --------->|                     |                      |
     |   transaction_id   |-- 再次 Plan -------->|                      |
     |   request_id       |<-- 执行计划 --------|                      |
     |                    | 再次检查 Running.revision                   |
     |                    |                     |                      |
     |                    |-- 原子保存 ------------------------------->|
     |                    |   冻结 Candidate                           |
     |                    |   Task + 执行计划 + ACCEPTED 事件           |
     |                    |   Commit 幂等记录                          |
     |                    |<-- 持久化成功 -----------------------------|
     |                    |                     |                      |
     |                    | 唤醒后台 Worker                            |
     |<-- task_id --------|                     |                      |
     |    ACCEPTED        |                     |                      |
     |                    |                     |                      |
```

Begin、Stage 不修改网络；Commit 返回 `ACCEPTED` 表示已持久化受理。后台 Worker 的执行与 Commit RPC 的返回可以并发。图中省略了部分存储读取和异常返回；身份校验实际在各 RPC 入口执行，幂等重试可能直接返回既有事务或任务。

#### 9.6.2 后台执行与成功提交

```text
   Worker                 Redis                Driver             Linux / FRR
      |                     |                     |                     |
      |-- 读取 Task/Plan -->|                     |                     |
      |<--------------------|                     |                     |
      | 再次检查 revision                         |                     |
      |-- 保存 APPLYING --->|                     |                     |
      |                     |                     |                     |
      |  按计划串行执行：先 Linux step，再 FRR step                      |
      |                     |                     |                     |
      |-- Prepare -------------------------------->|                     |
      |                     |                     |-- 查询旧配置 ------>|
      |                     |                     |<-- 当前配置 --------|
      |<-- Undo：回滚所需的旧值/快照 ---------------|                     |
      |                     |                     |                     |
      |-- 持久化 Undo ----->|                     |                     |
      |   Started=true      |                     |                     |
      |<-- 保存成功 --------|                     |                     |
      |                     |                     |                     |
      |-- Apply ---------------------------------->|                     |
      |                     |                     |-- ip / vtysh ------>|
      |                     |                     |<-- 执行结果 --------|
      |<-- 返回结果 -------------------------------|                     |
      |                     |                     |                     |
      |-- Verify --------------------------------->|                     |
      |                     |                     |-- 回读实际配置 ---->|
      |                     |                     |<-- 实际配置 --------|
      |<-- 校验通过/失败 --------------------------|                     |
      |                     |                     |                     |
      |-- 保存步骤结果 ---->|                     |                     |
      |   + 新事件          |                     |                     |
      |                     |                     |                     |
      |  重复上述流程，直到全部 step 完成                                |
      |                     |                     |                     |
      |-- 原子保存 -------->|                     |                     |
      |   更新 Running      |                     |                     |
      |   revision + 1      |                     |                     |
      |   Task=SUCCEEDED    |                     |                     |
      |   成功事件          |                     |                     |
      |<-- 保存成功 --------|                     |                     |
```

每个 step 执行前，Worker 通过 `Registry.Driver(backend_id)` 找到对应 Driver。`Undo` 和 `Started=true` 在 Apply 之前持久化，保证后端已经修改、但步骤结果尚未保存时，重启恢复仍能找到补偿信息。Prepare、Apply、Verify 任一环节失败都会进入回滚；执行前发现 revision 冲突则直接记录 `FAILED_NO_CHANGE`。

#### 9.6.3 失败与逆序回滚

```text
   Worker                 Redis                 Drivers
      |                     |                      |
      |-- ROLLING_BACK ---->|                      |
      |                     |                      |
      |-- Rollback(Undo) ------------------------->|
      |   按执行计划逆序，恢复所有已开始的步骤      |
      |                     |                      |
      |                     |      恢复配置 + 回读确认
      |<-- 每一步回滚结果 ------------------------|
      |-- 保存步骤结果 ---->|                      |
      |                     |                      |
      |-- 保存任务终态 ---->|                      |
      |                     |                      |
      |   未开始修改：FAILED_NO_CHANGE             |
      |   回滚成功：  FAILED_ROLLED_BACK           |
      |   回滚失败：  ROLLBACK_FAILED              |
      |                     |                      |
      |   Running 保留旧版本                       |
      |   ROLLBACK_FAILED 会阻止后续任务执行        |
```

发生部分修改后报错的那一步也参与回滚；尚未开始的步骤跳过，已确认恢复的步骤在恢复流程中跳过。创建租户的示例中，若 FRR step 失败，会先恢复 FRR，再恢复 Linux。当前代码以 `Started` 标志判断是否存在需要补偿的步骤。

#### 9.6.4 事件订阅与结果查询

```text
 Controller              Agent RPC                  Redis
     |                       |                         |
     |-- WatchTasks -------->|                         |
     |   task_id             |-- 读取持久化事件 ------>|
     |   after_sequence      |<-- 按 sequence 返回 ----|
     |<== gRPC 事件流 =======|                         |
     |    ACCEPTED           |                         |
     |    APPLYING           |                         |
     |    步骤执行进度       |                         |
     |    SUCCEEDED / 失败态 |                         |
     |                       |                         |
     |-- GetTask(task_id) -->|-- 读取 Task ----------->|
     |<-- 当前任务结果 ------|<------------------------|
```

订阅与后台执行并行，可以在任务执行中或完成后发起。断开后使用 `after_sequence` 继续读取已持久化事件；客户端断线不会取消已受理任务。每次步骤状态更新会生成新的事件序号，事件携带任务快照。当前 `WatchTasks` 每轮读取任务和缺失事件，无新事件时等待 50 ms 后重查，终态事件发送完毕后关闭流；没有使用 Redis Pub/Sub，也未实现历史事件过期后的快照重同步协议。

当前 Registry 和 Driver 都是 Agent 进程内的 Go 调用；Controller 与 Agent 之间使用 gRPC。对应代码：

- [internal/agent/agent.go](internal/agent/agent.go)：Begin、Stage、Commit、Worker、执行、回滚、订阅与查询。
- [internal/backend/backend.go](internal/backend/backend.go)：Driver 接口、op 注册映射、执行计划。
- [internal/store/store.go](internal/store/store.go)：持久化读取与原子更新。
- [cmd/sdnctl/main.go](cmd/sdnctl/main.go)：客户端组合调用 Begin → Stage → Commit → WatchTasks。

#### 9.6.5 启动、注册与双向管理通信

```text
 Agent main/client       Identity / Store        Controller Server
        |                       |                       |
        |-- lock + load/create->|                       |
        |   persist node_id + registration_id           |
        |   create new instance_id                      |
        |                       |                       |
        |-- Register ---------------------------------->|
        |   proposed_node_id / registration_id          |
        |   instance_id / endpoint / capabilities       |
        |<-- assigned node_id --------------------------|
        |-- persist identity -->|                       |
        |                       |                       |
        |  create Store + Agent(INIT); start Worker      |
        |  start Agent gRPC Server                      |
        |                       |                       |
        |-- Heartbeat(node_id, instance_id, revision) -->|
        |<-- OK / error --------------------------------|
        |  OK: RUNNING; error: DOWN + retry Register     |
        |  re-register OK: wait for next heartbeat OK    |
        |                       |                       |
        |  Agent gRPC Server <--- Controller gRPC Client |
        |                 Begin / Stage / Commit        |
        |                 GetState / GetTask / WatchTasks|
```

当前首次 Register 在 Agent gRPC 服务开始监听之前，Controller 收到 endpoint 后不能立即假定 Agent 已经可调用。首次注册失败会退出；已分配身份的节点在 Controller 暂不可达时，可以使用持久化身份启动并恢复本地任务。已有身份被重新分配成其他 node_id 时会拒绝关联旧任务日志。

心跳协程启动即发起一次调用，随后使用 2 秒 ticker，单次 Heartbeat 和重注册超时均为 2 秒。成功心跳设置 `RUNNING`；失败设置 `DOWN` 并尝试重新注册；重注册成功本身不将状态改回 `RUNNING`。不配置 Controller 地址的独立运行模式直接设置 `RUNNING`。管理断连不删除配置，也不取消已持久受理的任务。

开发用 Controller 校验 Heartbeat 的 `node_id + instance_id`，拒绝被新实例替换的旧实例心跳；但相同 `registration_id` 再次 Register 会更新当前实例。配置 RPC 只检查目标 node_id，并经过传输鉴权，尚未携带管理会话代次；本地进程锁也仅保护同一状态目录。因此当前机制不能当作完整的跨主机实例隔离或控制器写入租约。能力随 Register 上报，Controller 也可调用 GetCapabilities 查询；尚无独立 ReportCapabilities/ReportState RPC。

#### 9.6.6 查询实际状态与更新 Operational

```text
 Controller        Agent.GetState          Registry / Drivers       Store
     |                   |                         |                   |
     |-- GetState ------>|                         |                   |
     |   node_id         |-- load Running ---------------------------->|
     |                   |<-- revision + operation log ----------------|
     |                   | read node_state / instance_id               |
     |                   |                         |                   |
     |                   |-- Observe ------------->|                   |
     |                   |                         | query Linux / FRR |
     |                   |<-- observations/errors --|                   |
     |                   |                         |                   |
     |                   |-- save operational snapshot --------------->|
     |                   |<-- result ----------------------------------|
     |<-- State ---------|                         |                   |
     | running_json / observations / observation_error                 |
```

本流程在每次 GetState 请求中同步执行，不是读取一个持续更新的缓存。`observations` 使用后端前缀区分原始查询结果；采集失败可返回部分结果及 `observation_error`，保存 Operational 失败也通过该字段报告。读取 Running 本身失败则返回 RPC 错误。Running 和各后端依次读取，不构成跨后端的原子时点快照；调用方应结合任务状态判断正在变更的节点。

当前没有独立周期 Observer、FPM/ZAPI 事件接入或由 Operational 自动生成配置的 Reconciler。后续增加这些组件时，应保持“已提交配置、真实观测和任务结果”三者的边界。

#### 9.6.7 进程重启与持久化任务恢复

```text
 Agent startup          Worker                  Store              Drivers
       |                   |                       |                    |
       |-- Start --------->|-- scan task/* ------->|                    |
       |                   |<-- records + plans ---|                    |
       |                   |                       |                    |
       |                   | any ROLLBACK_FAILED?  |                    |
       |                   |   yes: block this node's task execution    |
       |                   |                       |                    |
       |                   | sort unfinished tasks by creation time     |
       |                   |                       |                    |
       |                   | ACCEPTED: recheck revision -> execute      |
       |                   |   mismatch -> FAILED_NO_CHANGE             |
       |                   |                       |                    |
       |                   | other unfinished state:                   |
       |                   |-- persist ROLLING_BACK>|                    |
       |                   |-- reverse Rollback(saved Undo) ----------->|
       |                   |   only Started && !Restored steps          |
       |                   |<-- restore + verification result ----------|
       |                   |-- save step/event --->|                    |
       |                   |-- save terminal state>|                    |
       |                   |   Running stays at previous committed rev  |
```

Worker 在启动唤醒、Commit 唤醒或 1 秒定时扫描时读取任务记录，恢复逻辑复用 9.6.3 的补偿实现。已经原子发布 `SUCCEEDED + Running` 的任务是终态，不再次执行；未完成发布的任务即使最后一个后端步骤已经成功，也仍按未完成事务补偿。回滚过程中再次重启时，已持久记录 `Restored` 的步骤跳过，其余已开始步骤继续恢复。`ROLLBACK_FAILED` 不自动转成成功，也不会因 Controller 重连而解除阻塞。

## 10. Controller etcd 与全局信息

etcd 在本设计中用于全局配置和分布式协调。建议第一版只在 Controller 侧部署 etcd，Agent 通过 gRPC 同步本节点所需信息并保存在 Redis。Agent 是否需要独立 etcd 暂不作为必需组件。

需要全局维护的信息包括：

| 信息 | 用途 |
| --- | --- |
| 节点身份、注册映射、能力、服务地址 | 唯一登记、接管和连接管理 |
| 租户、网络、节点部署关系 | 规划跨节点网络 |
| VNI、地址、RT 等资源分配与约束 | 避免冲突并保持跨节点策略一致 |
| 全网配置与各节点目标版本 | 跟踪期望配置和部署进度 |
| 跨节点任务、依赖与完成结果 | 协调单节点事务组成的全网变更 |
| Controller 管理权和接管代次 | 多 Controller 场景下拒绝旧管理者写入 |

etcd 采用集群选举和多数派提交。不能把 Controller 固定视为 etcd 主库、每个 Agent 视为普通从库；独立 etcd 集群也不会自动形成这种主从同步关系。

高频 MAC、邻居和原始观测先在本地处理，Controller 按需建立拓扑索引，不要求逐条写入 etcd。全局配置同步、协议路由交换和观测汇总是不同的数据路径。

## 11. FRR、FPM 与数据面边界

推荐的职责链路：

```text
配置：Controller -> Agent -> FRR 配置 Adapter / 数据面 Adapter
路由：Leaf FRR <-> Spine RR <-> Leaf FRR
观测：FRR / 数据面 -> Agent -> Controller 拓扑服务
```

- FRR 负责 BGP EVPN 路由交换、选路和撤销。
- FPM 是 Zebra 转发信息导出接口，不等同于 ZAPI，也不能直接当作完整 EVPN NLRI 数据源。
- 获取 Type-2、Type-3 需要独立的 FRR EVPN 采集能力；具体接口及增量方式按目标 FRR 版本验证。
- FPM 监听、主动查询和其他订阅方式可以并存，Service Adapter 不强制采用同一种连接方向。
- 同一后端、同一类资源必须明确写入负责人。Linux 中由 FRR 管理的动态路由，不由 Agent 再从拓扑视图重复安装。
- VPP 通过受管内核 FDB/路由/邻居与 Binary API 完成 EVPN / IRB 同步；交换机后端仍需单独实现路由编程和本地学习反馈；统一配置 Adapter 本身不等于完成全部协议集成。
- 远端 EVPN 安装的 MAC/邻居不能被当成本地新学习的主机再次通告。
- 默认只探测本节点直接接入且属于已授权策略范围的主机，不由每个 Leaf 周期性探测全网远端主机。

## 12. 网络拓扑的含义

| 视图 | 需要的信息 | 可表达的关系 |
| --- | --- | --- |
| 配置视图 | Controller 期望配置 | 租户、网络、节点部署和预期成员 |
| 物理拓扑 | LLDP、接口、设备身份与配置 | Leaf/Spine 的端口连接及状态 |
| Overlay 拓扑 | EVPN 路由及本地 VRF/VNI/RT 映射 | 已观测的 VTEP、网络成员和路由可达关系 |
| 主机位置 | Type-2、FDB、ARP/ND、接入口信息 | MAC/IP、所属网络、本地接入口或远端 VTEP |
| 执行状态 | 后端实际对象和配置执行结果 | 哪些网络对象已经在节点落实 |

Type-2 的 IP 信息可能缺失，未被学习或通告的静默主机也不会自动出现在路由视图中。Type-3 可以提供参与某个二层网络的 VTEP 信息，不能据此还原 Leaf 到 Spine 的物理布线。

关联数据时需要使用网络上下文和显式映射，不能把 RD 直接当租户标识，也不能假设 RT 后缀始终等于 VNI。拓扑记录应保留来源、新鲜度和不完整状态。

## 13. 尚待确定的实现项

以下事项不影响已确认的单节点事务范围、全后端依赖执行、显式 commit、失败回滚、断连继续和节点三态：

- Linux/FRR 已实现后继续扩展哪些后端，以及新增操作如何保存原值、验证和回滚。
- 通用后端开关、显式资源归属与协同分发、运行时重载仍待实现，详见 [7.3 设计提案](#73-后端按配置启用与资源归属设计提案尚未实现)；特定 linux / vpp 启动选择与恢复依赖校验已实现。
- 当前静态 `.proto` 的版本兼容规则、完整错误目录和未来 Any 类型注册方式。
- 在已实现 `depends_on` 和 Driver 生命周期接口之上，扩展步骤输出引用、自动资源依赖及验证标准。
- 在已实现 Redis/File 持久化与任务补偿恢复之上，确定候选事务、事件和最终任务记录的保留期限。
- 注册凭据、节点证书与身份绑定方式；gRPC 地址发现和管理会话超时参数。
- 未提交事务过期策略，以及其他非配置动作和未来任务取消接口的语义。
- 本地人工配置冲突处理；Running 与 Operational 偏差的自动修复边界。
- Type-2/Type-3 采集接口、首版是否同时实现物理拓扑采集，以及动态转发表的写入归属。
- Spine 使用远程设备管理 Adapter，还是后续增加 `role=spine` 的 Agent。
- Python 的实际使用范围；统一 SAI 是否需要映射 OCP SAI；SVI/BVI 等具体 API 命名。
- Agent 本地是否存在需要独立 etcd 的场景，目前建议不引入。

## 14. 参考资料

- [OVN Architecture](https://www.ovn.org/support/dist-docs/ovn-architecture.7.html)：集中逻辑配置转换、本地执行，以及配置进度确认。
- [OpenDaylight Controller Developer Guide](https://docs.opendaylight.org/projects/controller/en/latest/dev-guide.html)：配置状态与运行状态的模型划分。
- [SONiC Architecture](https://github.com/sonic-net/SONiC/wiki/Architecture)：设备内部 Redis、SWSS、Orchagent 与适配层协作。
- [FRR EVPN](https://docs.frrouting.org/en/latest/evpn.html)：EVPN、VRF、VNI 与路由目标等概念。
- [FRR Zebra FIB Push / FPM](https://docs.frrouting.org/en/latest/zebra.html#zebra-fib-push-interface)：转发信息导出和 FPM 接口。
- [gRPC Core Concepts](https://grpc.io/docs/what-is-grpc/core-concepts/)：服务、消息模型、请求响应和流式接口。
- [gRPC Retry](https://grpc.io/docs/guides/retry/)：调用重试与业务幂等的边界。
- [Protobuf Language Guide](https://protobuf.dev/programming-guides/proto3/)：消息定义、字段演进和 Any。
- [NETCONF RFC 6241](https://www.rfc-editor.org/rfc/rfc6241.html#section-8.3)：Candidate 与 commit 语义。
- [NMDA RFC 8342](https://www.rfc-editor.org/rfc/rfc8342.html#section-5.3)：实际应用配置与 Operational 状态。
- [etcd FAQ](https://etcd.io/docs/v3.6/faq/)：集群选举、复制和多数派提交。
- [Redis Pub/Sub](https://redis.io/docs/latest/develop/pubsub/#delivery-semantics)：订阅断连时的消息交付限制。

上述资料用于参考职责和语义。本文的 Protobuf 操作、单节点多后端补偿事务、数据库分工及组件接口属于本项目设计，不表示已经实现 NETCONF 协议或满足所有后端的原子提交能力。

## VPP 邻居保活（已实现，验收以实验报告为准）

- 节点级 NEIGHBOR_KEEPALIVE / SET，optional enabled；默认 false，仅 VPP 注册，能力 neighbor-keepalive/v1。
- VPP src/plugins/neighbor_keepalive 原生主线程进程负责单播 ARP/NS，标准 ARP/ND 学习更新时间是确认依据。Python keepalive.py 只管理开关、BVI/本地接入口范围与查询。
- 只维护本地动态 MAC/IP，不将远端 EVPN、静态邻居、纯 MAC 项作为探测对象。每 IP 独立三次重试，失败撤销该邻居，不删除共用 MAC。
- MAC 分钟粒度预留 60 秒，间隔 (age-60)/2 并提前抖动；启用要求 age=0 或 >=120 秒。固定重试 5 秒，最多 3 次，4096 绑定/512 探测每秒上限。
- Controller 使用 resource/neighbor_keepalive 单例、原有候选与提交；Web 仅支持节点显示开关与 NEIGHBOR_KEEPALIVE_STATUS 查询。
- 状态 version 3，兼容旧状态默认关闭；Undo 恢复开关。irb.py 对 failed 绑定过滤后再发布。没有新增异步事件 API，也不依赖周期 BGP 重通告。
- 撤销 Linux 永久镜像邻居前先发 NUD_FAILED，防止 FRR 的永久项删除分支漏撤 Type-2；发布记录覆盖内核提前清理的情况，避让新 MAC/远端项。bridge MAC 只在变化时设置，避免每次同步清空邻居。
- 详见 docs/developer/vpp-keepalive.rst、docs/user/vpp.rst；隔离容器测试入口 SDN_VPP_KEEPALIVE=1 SDN_VPP_STACK=1 python3 lab/vpp_irb.py。不可把软件端口测试当成物理 NIC 或规模性能验收。
- 2026-10-27 最终软件验收：IPv4 VTEP + 2 workers + Controller/Web 34 项、IPv6 VTEP 32 项、完整 IRB 复测 29 项通过；40 项 Python、25 项容器、17 项 Web 单测及 Go 测试通过。见 artifacts/vpp/keepalive/validation.json；首次 IPv6 Type-5 超时复测未重现，仍保留跟踪记录。
