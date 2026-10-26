# sdn-agent 离线镜像包

本包用于 Linux/amd64。需要 Docker、Bash、Python 3、tar、xz 和 sha256sum。
版本、镜像 ID 与配套源码提交以本目录 `manifest.json` 为准。
`docs/html/index.html` 可离线打开；它包含完整用户手册和开发手册。

## 校验和加载

解压前，把下面 PACKAGE 改为下载文件的名称（去掉 `.tar.xz`）：

```bash
PACKAGE=sdn-agent-v1.0.1
sha256sum -c "$PACKAGE.tar.xz.sha256"
tar -xJf "$PACKAGE.tar.xz"
cd "$PACKAGE"
bash load-images.sh
```

外层 tar.xz 含文档，需要先解压。`load-images.sh` 校验包内文件后，对 `images/*.tar` 执行 docker load；它不启动容器。无需手工创建 Docker manifest.json。

以下首次启动命令均在解压目录、同一个 Bash 终端执行，具备 Docker 操作权限。
镜像版本从本包清单读取：

```bash
SDN_IMAGE="$(python3 -c 'import json; print(json.load(open("manifest.json"))["images"][0]["reference"])')"
```

## 首次启动 Agent

镜像包括 Agent、sdnctl、MCP、Redis 和 FRR。下面启动独立 Agent，gRPC 仅向宿主机 127.0.0.1:29091 开放。

```bash
set -eu
if [ ! -e agent.conf ]; then cp deploy/agent.conf agent.conf; fi
python3 - <<'PY'
import os, secrets
try:
    fd = os.open('agent.env', os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
except FileExistsError:
    print('保留已有 agent.env')
else:
    with os.fdopen(fd, 'w') as stream:
        stream.write('SDN_TOKEN=' + secrets.token_hex(24) + '\n')
PY
docker run -d --name sdn-agent-release --restart unless-stopped \
  --privileged --env-file agent.env \
  -p 127.0.0.1:29091:9090 \
  --mount "type=bind,src=$PWD/agent.conf,dst=/etc/sdn-agent/agent.conf,readonly" \
  -v sdn-agent-data:/var/lib/sdn-agent \
  -v sdn-agent-redis:/var/lib/redis \
  -v sdn-agent-frr:/etc/frr \
  "$SDN_IMAGE"
docker logs sdn-agent-release
docker exec sdn-agent-release sdnctl -addr 127.0.0.1:9090 -insecure capabilities
```

首次初始化需要时间；等待日志显示服务就绪后查询 capabilities。容器名已存在时先确认它的用途，不要重复创建。
此配置通过 privileged 在容器自己的网络命名空间管理 Linux/FRR，没有自动创建业务网线。

接入 Controller 前，修改 `agent.conf` 的 `agent.controller` 为可达的 Controller gRPC 地址，`agent.endpoint` 为 Controller 可回连的 Agent 地址，并使 agent.env 中 SDN_TOKEN 与 Controller 的 SDN_AGENT_TOKEN 一致。跨主机时调整发布端口的监听地址。修改环境文件后需按升级指南重建本组件容器并保留卷；单纯 docker restart 不会重新读取宿主机 env-file。
默认模板启用实验用 insecure 模式，TLS 和完整接入过程见 `docs/html/user/installation.html` 与 `configuration.html`。

MCP 使用镜像内 `sdn-agent-mcp`，接入说明见 `docs/html/user/mcp.html`。

## 停止、数据和升级

停止本页创建的容器：

```bash
docker stop sdn-agent-release
```

重新启动使用 `docker start sdn-agent-release`，不要再次随机生成凭据。
配置文件、环境文件及命名卷属于部署方，保留在本机，不要重新打包发布。
升级、备份和回退见 `docs/html/user/release.html`；不要通过删除数据卷完成普通停止或升级。
已有容器与新容器不能同时访问同一数据库目录。

包内 `VERIFICATION.md` 只描述该版本实际完成的验证。Controller/Agent 的持久记录不等于删除容器后自动重建全部 Linux 网络状态，业务接口和网络恢复应单独验证。

