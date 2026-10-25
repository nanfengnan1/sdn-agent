# Alpine 源码镜像：Agent + Redis + FRR

从源码编译、安装 FRR 并运行 Agent 的完整步骤在 [部署手册](../docs/html/user/installation.html#agent-docker-deploy)；未生成 HTML 时可读 [RST 源文件](../docs/user/installation.rst)。本页给出 Docker 命令速查。

版本标签、镜像推送、离线分发及升级回退步骤见 [发布手册](../docs/user/release.rst)。

每个容器自带 Redis 和 FRR，由同一个启动器监督；无需独立 Redis 服务。`agent.conf`（严格 JSON）同时记录 Agent 与内置 Redis 设置，可只读挂载到 `/etc/sdn-agent/agent.conf` 覆盖镜像模板。完整字段、覆盖顺序与 nodeId 说明见 [配置手册](../docs/html/user/configuration.html)。

## 1. 准备源码并构建

需要 Linux Docker，源码目录为 `~/program/github/sdn-agent`。Dockerfile 位于本目录，构建上下文必须是项目的父目录；宿主机无需安装 Go 或 FRR。

```sh
cd ~/program/github
docker build -f sdn-agent/docker/Dockerfile -t sdn-agent:alpine .
docker image inspect sdn-agent:alpine --format '{{.Id}}'
docker run --rm sdn-agent:alpine sdn-agent -h
```

构建阶段默认 Go 1.25 / Alpine 3.22；运行阶段为 Alpine 3.22，通过 apk 安装 FRR、frr-pythontools（含 `/usr/lib/frr/frr-reload.py`）、iproute2 和抓包工具。只复制源码并在镜像内编译，不使用宿主机预编译二进制。

Redis 服务端与 CLI 从 `redis:7.4-alpine` 阶段复制，保持现有数据卷使用的版本系列；构建时检查两个程序可运行。`REDIS_IMAGE` 可用于固定已验证的 digest。容器健康检查跟随有效配置检查 Agent 端口、Redis PING 和 FRR 进程。

已验收的 Alpine 环境使用 FRR 10.2.2，后续包版本由构建仓库决定；更换版本需要重新回归。基础镜像可用 `--build-arg GO_IMAGE=... --build-arg ALPINE_IMAGE=...` 替换，发布时应固定验证过的 digest。

## 2. 启动独立测试节点

先把令牌占位符换为至少 16 字节的实际令牌；容器名及三个命名卷必须专用于该节点。

```sh
cd ~/program/github/sdn-agent
install -m 0600 docker/agent.conf ./agent.conf
# 按需要编辑 agent.conf；独立节点的 controller/endpoint 保持为空。
export SDN_TOKEN='<替换为实际的至少16字节Agent令牌>'
docker run -d --name sdna-doc-demo --privileged --stop-timeout 240 \
  -p 127.0.0.1:39090:9090 \
  -e SDN_TOKEN \
  --mount type=bind,src="$(pwd)/agent.conf",dst=/etc/sdn-agent/agent.conf,readonly \
  -v sdna-doc-demo-state:/var/lib/sdn-agent \
  -v sdna-doc-demo-redis:/var/lib/redis \
  -v sdna-doc-demo-frr:/etc/frr \
  sdn-agent:alpine
```

这是不连接 Controller 的独立节点，宿主机通过 `127.0.0.1:39090` 访问，容器内监听 `9090`。Agent 作用于容器的网络命名空间，不接管宿主机业务接口。本示例 privileged 和明文模式只用于隔离实验。

入口先校验配置，再等待内置 Redis、FRR 就绪，最后启动 Agent；默认启用 bgpd/ospfd/ospf6d，初始没有租户、业务地址和路由协议配置。模板令牌为空，可用 `SDN_TOKEN` 或外部文件中的 `agent.token` 提供。显式 CLI 覆盖同名应用设置；`SDN_TOKEN` 优先于文件令牌。

配置检查不会启动任何服务或输出 token：

```sh
docker run --rm -e SDN_TOKEN \
  --mount type=bind,src="$(pwd)/agent.conf",dst=/etc/sdn-agent/agent.conf,readonly \
  sdn-agent:alpine -check-config
```

不挂载时使用镜像模板；也可以传 `-config /other/agent.conf`。配置变更需要重启，不支持热加载。`-h` 和 `-check-config=true` 均不会启动 Redis/FRR。

## 3. 验证服务和配置业务

```sh
docker inspect sdna-doc-demo --format '{{.State.Status}} {{.State.Health.Status}}'
docker logs --tail 80 sdna-doc-demo
docker exec sdna-doc-demo vtysh -c 'show version'
docker exec sdna-doc-demo redis-cli ping
docker exec sdna-doc-demo sdnctl -insecure capabilities
docker exec sdna-doc-demo sdnctl -insecure state
```

等待 `running healthy`，并确认两次 RPC 成功、FRR 可查询、观测无采集错误。已安装宿主机 CLI 时可运行 `sdnctl -addr 127.0.0.1:39090 -insecure state`，当前 shell 需有相同的 `SDN_TOKEN`。

配置租户和 BD 见 [第一个 CLI 事务](../docs/html/user/quickstart.html)。首次新建环境的 revision 应为 0，但这不能用来证明一个已有节点没有人工配置。

## 4. 接入 Controller 或部署完整环境

`<Agent部署IP>`、`<Controller部署IP>`、`<部署服务器IP>` 都需要替换为实际地址。原生 Controller 默认监听 `19090`，Agent 默认 `9090`。

上面的容器只发布到 loopback，不能直接让另一台 Controller 通过宿主机 IP 回连。独立部署若让远端 Controller 访问，需要将发布地址改为 `<Agent部署IP>:39090:9090`，Agent 启动参数添加 `-endpoint '<Agent部署IP>:39090' -controller '<Controller部署IP>:19090'`；同时使用与 Controller `SDN_AGENT_TOKEN` 一致的令牌。TLS 部署还需挂载证书目录，并设置 `-cert/-key/-ca`，具体见 [TLS 接入说明](../docs/html/user/installation.html#agent-controller-connect)。

部署包含 Web、Controller、Spine、Leaf 的完整隔离实验，准备相邻的三个源码项目，在 Controller 仓库执行：

```sh
cd ~/program/github/sdn-controller
python3 docker/manage.py init
python3 docker/manage.py build
python3 docker/manage.py up
python3 docker/manage.py status
```

浏览器打开 `http://<部署服务器IP>:28080`。凭据来自该部署的 `docker/state/tokens.json`，与旧实验的文件或地址无关。Compose 管理网络内使用 `leaf1:9090`、`controller:19090` 这类容器 DNS，不使用浏览器入口地址注册 Agent。详见 [Controller Docker 指南](../../sdn-controller/docker/README.md)。

## 5. 停止与持久化

```sh
docker stop --time 240 sdna-doc-demo
docker start sdna-doc-demo
```

停止时先等待 Agent 退出/补偿，再停止 FRR 和 Redis；协议会话随之中断。`/var/lib/sdn-agent` 保存身份，`/var/lib/redis` 保存本节点 AOF/RDB，`/etc/frr` 保存 FRR 配置。默认 Redis 只绑定容器 loopback，AOF 开启且 `appendfsync always`，不发布 Redis 端口。

迁移原独立 Redis 时停止旧实例、备份，然后把旧 `/data` 卷挂到 `/var/lib/redis`，不能同时打开同一个卷。启动器只处理专属目录中的 Redis 文件，拒绝系统路径、与 Agent 身份目录重叠及无关文件；不会删除旧数据。任一必需进程失效会使整个容器失败退出。

每个节点必须使用独立身份/状态卷。容器网络命名空间重建后，手工 veth 和 Linux 配置不保证保留，当前没有完整自动重建能力；保留卷不等于业务网络自动恢复。停止和重启后都需重新检查实际网络，尤其不要把删除数据卷作为日常升级步骤。
