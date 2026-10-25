# sdn-agent

`sdn-agent` 是用 Go 编写的**本地数据面管理代理**。它接收 Controller 或 CLI 通过 Protobuf/gRPC 提交的配置事务，将统一网络操作交给适配器执行，负责配置下发、状态回读、失败回滚和任务恢复。

项目通过统一的 Driver 接口扩展不同数据面和执行后端。**当前实现 Linux 和 FRR 适配器**：Linux 适配器管理内核中的接口、地址、路由、桥域、VRF 和 VXLAN；FRR 适配器管理路由协议配置，配合数据面完成网络功能。开发新的数据面适配器时，可按[适配器扩展流程](docs/developer/adapters.rst)实现接口、注册能力并补齐测试。

默认 Docker 部署现在将 **Agent、Redis、FRR 集成在同一个容器**。使用 [`docker/agent.conf`](docker/agent.conf) 配置应用和内置 Redis，支持只读挂载到 `/etc/sdn-agent/agent.conf` 覆盖；原生 CLI 保持兼容。详见 [统一配置手册](docs/html/user/configuration.html) 与 [Docker 操作](docker/README.md)。

完整文档：[用户手册](docs/html/user/index.html) · [开发手册](docs/html/developer/index.html) · [文档源码与构建说明](docs/README.md)。执行 `make -C docs html` 生成 HTML，`make -C docs clean` 清理文档产物；首次使用先按构建说明安装文档依赖。

**第一次安装软件：** [源码编译与安装](docs/html/user/installation.html#agent-source-install) · [Docker 构建与部署](docs/html/user/installation.html#agent-docker-deploy) · [连接 Controller](docs/html/user/installation.html#agent-controller-connect)。未生成 HTML 时可直接阅读 [安装手册源文件](docs/user/installation.rst)。手册含依赖检查、安装目录、令牌配置、启动/停止、systemd 模板、持久化和成功检查。

源码 Alpine 镜像（内含 FRR）见 [docker/README.md](docker/README.md)，Dockerfile 位于 `docker/Dockerfile`。完整隔离部署和空白手动测试环境见 [Controller Docker 指南](../sdn-controller/docker/README.md)。

交付版本请读 [版本发布、交付与升级](docs/user/release.rst)：包含二进制打包与校验、Docker 镜像分发、HTML 手册交付及升级回退边界。

本仓库包含可运行的 Agent、`sdnctl` 测试客户端、身份注册/心跳用的 `mock-controller`、真实 Docker 实验与回归测试。正式的全网 Controller 是独立项目；这里的 mock 不承担全网规划。

```text
 Controller Client ── Begin / Stage / Commit / GetTask / WatchTasks ──┐
 Controller Server ◀── Register / Heartbeat ─── Agent Client         │
                                                                   ▼
                                                           Agent gRPC Server
                                                                   │
                                                        Candidate / Validation
                                                                   │ commit
                                                        Task Journal + DAG
                                                                   │
                                                          Backend Registry
                                                          /              \
                                                 Linux Adapter        FRR Adapter
                                                 ip / sysctl          vtysh
                                                          \              /
                                                        Verify / Rollback
                                                                   │
                                                  Running + Operational + Events
                                                                   │
                                                    Redis（本节点命名空间）
```

## 源码编译与安装

需要 Linux、Go 1.25+ 和 Make；以下在项目源码目录执行。FRR、iproute2、sysctl 的运行依赖与准备方法见上面的安装手册。Agent 原生程序不会自动启动 FRR。

```bash
cd ~/program/github/sdn-agent
go mod download
make build
sudo install -m 0755 bin/sdn-agent /usr/local/bin/sdn-agent
sudo install -m 0755 bin/sdnctl /usr/local/bin/sdnctl
sudo install -d -m 0700 /etc/sdn-agent /var/lib/sdn-agent
```

独立节点的本机测试，在专属 Linux/FRR 环境中配置并启动：

```bash
export SDN_TOKEN='<替换为实际的至少16字节Agent令牌>'
sudo env "SDN_TOKEN=$SDN_TOKEN" /usr/local/bin/sdn-agent \
  -listen 127.0.0.1:9090 -state-dir /var/lib/sdn-agent -insecure
```

第二个终端设置相同 `SDN_TOKEN`，执行 `sdnctl -addr 127.0.0.1:9090 -insecure capabilities` 和 `state` 检查。接入 Controller、TLS、Redis、systemd 及 Docker 部署按完整手册操作；`<Agent部署IP>` / `<Controller部署IP>` 均替换为实际管理地址，Controller 默认 19090，Agent 默认 9090。

## 开发检查与独立实验

运行和测试环境为 Linux；需要 Go 1.25+。生成后的 `.pb.go` 已包含在仓库中，普通构建不需要安装 protoc。

```bash
go mod download
make build
make test
make race
go vet ./...
# 构建、全部自动测试、重新创建独立实验并完成网络回归
make regression
```

完整实验需要 Docker，以及 `quay.io/frrouting/frr:10.0.0`、`redis:7.4-alpine` 镜像。它创建独立的 `sdna-e2e-*` 容器和管理网络；实验网络通过额外 veth 连接，不修改已有 `sevpn-*` 容器。

上面是保留的旧回归脚手架，它的独立 Redis 用于兼容性测试。当前部署使用 `docker/Dockerfile` 的内置 Redis/FRR 镜像，以及 Controller 项目的 `docker/manage.py`，不需要额外 Redis 容器。

```bash
make lab
# 对保留的实验重新执行测试
python3 lab/run.py --verify-only
# 只清理带有本项目准确 ownership label 的实验容器和网络
make clean-lab
```

容器内的 spine、leaf1、leaf2 配置全部通过 gRPC 事务下发。veth 布线、FRR 守护进程启用和 PC 地址属于实验基础设施。为覆盖同一套适配器，实验也在 Spine 上运行 Agent；不要求未来所有商业 Spine 都部署 Agent。

```text
 pc1                  leaf1                spine/RR               leaf2                 pc2
192.168.10.11 ─ host10/br10 ─ uplink ─ swp1     swp2 ─ uplink ─ br20/host20 ─ 192.168.20.12
                      L2VNI 10010                            L2VNI 10020
                      TENANT-A ───────── L3VNI 50000 ──────── TENANT-A
                      10.255.0.11          10.255.0.1         10.255.0.12
```

查看运行中的实验：

```bash
docker exec sdna-e2e-leaf1 /workspace/bin/sdnctl -insecure state
docker exec sdna-e2e-leaf1 /workspace/bin/sdnctl -insecure capabilities
docker exec sdna-e2e-spine vtysh -c 'show bgp l2vpn evpn'
docker exec sdna-e2e-pc1 ping -c 1 192.168.10.254
docker exec sdna-e2e-pc2 ping -c 1 192.168.20.254
docker exec sdna-e2e-pc1 ping -c 3 192.168.20.12
```

`state` 的 `observations` 包含 FRR EVPN、VNI、BGP、OSPF，以及 Linux link/address/route/FDB/neigh 原始 JSON。Type-2/Type-3 描述 Overlay 信息；物理链路拓扑不能仅由它们推导。

默认实验只使用 Type-2 主机路由。两端先 ping 本地网关以产生 ARP 学习；主机长时间静默后可能需要重新触发。当前没有主动邻居保活；可通过新增 BGP_NETWORK/BGP_REDISTRIBUTE 与 EVPN_TYPE5 显式发布前缀，见[独立资源与查询手册](docs/user/resources.rst)。

## 配置操作

协议文件：[agent.proto](api/agent/v1/agent.proto)。完整 op 分工和参数见 [operations.md](docs/operations.md)。

一次事务的 JSON 示例：

```json
{
  "operations": [
    {"id":"tenant","code":"TENANT","action":"CREATE",
     "tenant":{"name":"TENANT-B","table_id":1002}},
    {"id":"bd","code":"BD","action":"CREATE","depends_on":["tenant"],
     "bridge":{"name":"br30","stp":false,"mtu":1500}},
    {"id":"bind","code":"BD_TENANT","action":"BIND","depends_on":["bd"],
     "binding":{"bridge":"br30","tenant":"TENANT-B"}}
  ]
}
```

保存为 `ops.json` 后，`sdnctl ... -file ops.json submit` 会完成 Begin → Stage → Commit → Watch。所有选项放在子命令前。也可以分别使用 `begin`、`stage`、`commit`、`abort`；详见 [protocol.md](docs/protocol.md)。

## 运行约束

Agent 需要所在网络命名空间的网络管理权限、iproute2/sysctl，以及访问 FRR VTY socket 的权限。v1 的 FRR 适配器必须独占管理该 FRR 实例，回滚使用完整配置快照和 `frr-reload.py`，不能与人工 CLI 或其他配置器并发修改。配置成功表示本地配置回读通过；路由协议收敛和业务可达由独立测试验证。

生产通信默认要求 TLS：服务端 `-cert/-key`，客户端 `-ca`；双方还需要 `SDN_TOKEN`（至少 16 字节）。`-insecure` 只用于显式选择的隔离实验。当前 token 是部署级权限，尚无按 Controller/租户划分的 RBAC 或设备证书身份体系。

Redis 的 Candidate、Running、Operational、Task 使用同一 Redis DB 内的不同 key 前缀，以便一次 MULTI/EXEC 原子发布。实验启用 `appendonly yes`、`appendfsync always`；生产需要同等级持久化配置。未设置 `-redis` 时使用 fsync 文件存储，方便开发测试。Agent 不运行本地 etcd。

上述文件默认适用于不加载配置的原生进程；Docker 默认从 agent.conf 选择内置 `127.0.0.1:6379`。`sdn-agent -config agent.conf -check-config` 可在写状态前验证设置并输出不含 token 的有效配置。node UUID 继续由首次注册协商和身份卷维护，不手填在 agent.conf 中。

详细设计：[agent.md](agent.md)、[事务和恢复](docs/transactions.md)、[测试系统](docs/testing.md)、[实测验收记录](docs/validation.md)、[待解决问题](docs/backlog.md)。回滚失败可通过 `RetryRollback` 重试已保存的 Undo，不能直接强制解除屏障；协议及 CLI 见 [protocol.md](docs/protocol.md)。

## 版本范围

v1 实现本实验需要的 Linux + FRR 配置链路。暂未实现 VPP/交换机插件、独立进程 Adapter 注册协议、FPM/ZAPI 接收器、ARP/ND 主动保活、全网拓扑聚合、资源级自动差异规划，以及主机重启后全部 Linux 配置自动重建。Agent 进程重启会恢复任务日志；管理断线不会主动清除网络配置。

运行数据、token、二进制和测试产物保存在被 Git 忽略的 `lab/state/`、`bin/`、`artifacts/`，不应作为源码提交。
