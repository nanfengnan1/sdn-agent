编译、安装与部署
========================================

第一次部署按本页选择一种方式：已有 Linux / FRR 节点使用 :ref:`agent-source-install`；
希望同时获得 Agent 与 FRR 运行环境使用 :ref:`agent-docker-deploy`。
两种方式均需要配置令牌，安装成功后再阅读 :doc:`quickstart` 提交业务配置。
交付二进制、发布镜像、分发手册以及升级回退流程见 :doc:`release`。

.. contents:: 本页步骤
   :local:
   :depth: 2

先确定部署地址与模式
----------------------------------------

本文的 ``<Agent部署IP>``、``<Controller部署IP>`` 和 ``<部署服务器IP>`` 都是占位符，
必须换成实际部署地址。没有固定的实验服务器地址。

.. list-table:: 地址的用途
   :header-rows: 1
   :widths: 25 35 40

   * - 地址
     - 使用位置
     - 含义
   * - ``127.0.0.1:9090``
     - 原生 Agent 所在主机的 CLI
     - 仅本机；不能让另一台服务器通过它回连
   * - ``<Agent部署IP>:9090``
     - Controller / 远端 CLI
     - Agent 的管理入口，与 ``-endpoint`` 和证书 SAN 一致
   * - ``<Controller部署IP>:19090``
     - Agent ``-controller``
     - 独立 Controller 的 gRPC 入口；如果做了端口映射，使用映射后的端口
   * - ``leaf1:9090`` / ``controller:19090``
     - 同一 Compose 管理网络内
     - 容器 DNS；不能直接当作浏览器或外部主机可解析的地址
   * - ``http://<部署服务器IP>:28080``
     - 完整 Docker 环境的浏览器
     - Web 入口；Agent 自身没有 Web 页面

独立 Agent 不传 ``-controller``，可以通过 ``sdnctl`` 直接管理本地事务。
接入 Controller 时，Agent 的 ``SDN_TOKEN`` 必须与 Controller 的 ``SDN_AGENT_TOKEN`` 一致，
且 ``-endpoint`` 必须能被 Controller 解析并访问。Controller 默认 19090，Agent 默认 9090；
不要把这两个端口交换。

.. _agent-source-install:

方式一：源码编译并安装到 Linux 节点
------------------------------------------------------------

1. 准备源码和运行依赖
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

将源码放到目标编译机的目录，以下使用 ``~/program/github/sdn-agent``。
源码来自自己的仓库或交付包；本文不假定存在一个公开下载仓库。
构建需要 Linux、Go 1.25+、GNU Make；生成的 Protobuf Go 文件已提交，普通安装不需要 protoc。
编译机与部署机架构应一致；下面直接在部署节点上构建。

.. code-block:: bash
   :caption: Linux 编译/部署节点

   export SDN_AGENT_SRC="$HOME/program/github/sdn-agent"
   cd "$SDN_AGENT_SRC"
   go version
   make --version
   test -f go.mod

原生运行需要 ``ip``、``bridge``、``sysctl``、正在运行的 FRR、``vtysh`` 和
``/usr/lib/frr/frr-reload.py``。使用节点发行版的包管理器安装对应 iproute2、procps、
FRR 和 FRR Python 工具；不同发行版的 FRR 包版本可能不同，要先核对目标版本。
本项目的 Alpine 镜像已把这些依赖集中在 Dockerfile 中安装；
不希望自行准备原生 FRR 时，直接使用 :ref:`agent-docker-deploy`。

已有验收覆盖独立实验的 FRR 10.0.0 镜像，以及 Alpine 源码镜像中的 FRR 10.2.2。
发行版默认包不一定等于这些版本；更换版本需重新验证 vtysh 输出、配置回读和失败恢复。

.. code-block:: bash
   :caption: Linux 部署节点：检查运行依赖

   command -v ip bridge sysctl vtysh
   test -f /usr/lib/frr/frr-reload.py
   sudo vtysh -c 'show version'
   sudo vtysh -c 'show running-config'

FRR 的 zebra、bgpd、ospfd 及相应版本依赖的守护进程应已启动。
检查 ``/etc/frr/daemons`` 和发行版 FRR 服务状态；bgpd / ospfd 是本实验需要的协议进程。
Agent 原生二进制不会替你安装或启动 FRR，也不会给已有 FRR 自动创建基础配置。

Agent 必须独占管理对应 FRR 实例，因为回滚使用实例配置快照。
以下原生部署用于专属节点；不要直接把正在由其他系统管理的宿主机 FRR 当成实验实例。

2. 编译二进制
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

.. code-block:: bash
   :caption: Linux 编译/部署节点

   cd "$SDN_AGENT_SRC"
   go mod download
   make build
   ./bin/sdn-agent -h
   ./bin/sdnctl -h
   test -x bin/sdn-agent
   test -x bin/sdnctl

``make build`` 使用 ``CGO_ENABLED=0``，生成 ``bin/sdn-agent``、``bin/sdnctl`` 和
``bin/mock-controller``。前两个分别是服务和客户端；mock-controller 只模拟注册/心跳，
不替代独立的 sdn-controller。仅构建不要求启动 Docker、Redis 或 FRR。

需要先验证源码时可执行 ``make test``；完整 race / 网络回归的前置条件和影响见
:doc:`开发测试指南 </developer/testing>`，它们不是每次安装的启动步骤。

3. 安装程序、配置目录和状态目录
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

.. code-block:: bash
   :caption: Linux 部署节点：仍在源码目录执行

   cd "$SDN_AGENT_SRC"
   sudo install -m 0755 bin/sdn-agent /usr/local/bin/sdn-agent
   sudo install -m 0755 bin/sdnctl /usr/local/bin/sdnctl
   sudo install -d -m 0700 /etc/sdn-agent /var/lib/sdn-agent
   /usr/local/bin/sdn-agent -h
   /usr/local/bin/sdnctl -h

.. list-table:: 安装后的文件位置
   :header-rows: 1
   :widths: 40 60

   * - 位置
     - 内容
   * - ``/usr/local/bin/sdn-agent`` / ``sdnctl``
     - 服务端与客户端程序
   * - ``/etc/sdn-agent/agent.env``
     - 本文约定的令牌文件，由 shell / systemd 加载；不是 Agent 原生配置文件
   * - ``/etc/sdn-agent/tls/``
     - 使用 TLS 时放置部署证书、私钥和 CA
   * - ``/var/lib/sdn-agent/``
     - 身份、进程锁及文件模式任务日志；升级程序时保留
   * - ``/etc/frr/``
     - FRR 自己的配置；由 FRR 安装包或管理员准备

当前 Agent 支持 ``-config /path/to/agent.conf`` 的严格 JSON 配置，也保留 CLI 和
``SDN_TOKEN`` 覆盖。配置文件可以记录 Agent 与内置 Redis 参数，详见 :doc:`configuration`。
项目仍没有 ``make install`` 或自动安装 systemd 服务的脚本。

4. 配置令牌并以前台方式启动
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

在专用节点上创建 ``/etc/sdn-agent/agent.env``，内容如下。
替换占位符为自己生成的至少 16 字节令牌；接入 Controller 时使用它配置的 Agent 令牌。
可以用 ``sudoedit /etc/sdn-agent/agent.env`` 编辑。

.. code-block:: text
   :caption: /etc/sdn-agent/agent.env

   SDN_TOKEN='<替换为实际的至少16字节Agent令牌>'

.. code-block:: bash
   :caption: Linux 部署节点

   sudo chmod 0600 /etc/sdn-agent/agent.env
   sudo sh -c 'set -a; . /etc/sdn-agent/agent.env; set +a; exec /usr/local/bin/sdn-agent -listen 127.0.0.1:9090 -state-dir /var/lib/sdn-agent -insecure'

这是本机独立测试方式，使用文件持久化且仅监听 loopback；明确的 ``-insecure`` 表示不使用 TLS，
不会免除 token 检查。日志应出现 ``node_id=... instance_id=... listen=127.0.0.1:9090``。
保持当前终端运行，在第二个终端检查：

.. code-block:: bash
   :caption: 同一 Linux 节点的第二个终端

   sudo sh -c 'set -a; . /etc/sdn-agent/agent.env; set +a; exec /usr/local/bin/sdnctl -addr 127.0.0.1:9090 -insecure capabilities'
   sudo sh -c 'set -a; . /etc/sdn-agent/agent.env; set +a; exec /usr/local/bin/sdnctl -addr 127.0.0.1:9090 -insecure state'

成功标志是可查询 UUID、协议版本、支持操作及状态；Linux / FRR 的观测没有采集错误。
独立运行时节点应为 ``RUNNING``。初始空环境的 Running revision 为 0，
但 revision 为 0 本身不能证明 FRR / Linux 没有人工配置。
前台按 Ctrl+C 停止 Agent；它不会自动删除已配置的网络。

5. 可选：交给 systemd 管理
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

适用于有 systemd 的专用 Linux 节点。先停止上述前台进程，确认 FRR 已由它自己的服务管理。
创建 ``/etc/systemd/system/sdn-agent.service``：
可以用 ``sudoedit /etc/systemd/system/sdn-agent.service`` 编辑。

.. code-block:: ini

   [Unit]
   Description=SDN local network agent
   After=network-online.target frr.service
   Wants=network-online.target

   [Service]
   Type=simple
   User=root
   EnvironmentFile=/etc/sdn-agent/agent.env
   ExecStart=/usr/local/bin/sdn-agent -listen 127.0.0.1:9090 -state-dir /var/lib/sdn-agent -insecure
   Restart=on-failure
   RestartSec=3

   [Install]
   WantedBy=multi-user.target

.. code-block:: bash
   :caption: Linux 部署节点

   sudo systemctl daemon-reload
   sudo systemctl enable --now sdn-agent
   sudo systemctl status sdn-agent --no-pager
   sudo journalctl -u sdn-agent -n 80 --no-pager

此模板与前台的“本机独立测试”参数相同。
分布式接入时按下一节替换 ``ExecStart`` 的地址和 TLS 参数，再执行 daemon-reload 和 restart。
停止使用 ``sudo systemctl stop sdn-agent``；修改二进制前先停服务，安装新程序后再启动。
状态目录和已有身份必须保留，不能同时运行两份 Agent 使用同一目录。

.. _agent-controller-connect:

接入 Controller 与 TLS
------------------------------------------------------------

Controller 需先启动，且已配置与 Agent 一致的 Agent token。
在 ``/etc/sdn-agent/tls`` 放置已签发的 Agent 证书、私钥和用于验证 Controller 的 CA。
证书 SAN 必须包含客户端实际连接的 IP 或域名；不要把文档中的占位符原样写进证书。
这里假定 Agent 与 Controller 证书由同一受信 CA 签发，分别配置双方的 CA 信任。

停止本机独立实例后，在 Agent 节点启动：

.. code-block:: bash
   :caption: Linux Agent 节点：替换两个管理 IP；本例使用文件存储

   export SDN_TOKEN='<与Controller的SDN_AGENT_TOKEN一致的令牌>'
   sudo env "SDN_TOKEN=$SDN_TOKEN" /usr/local/bin/sdn-agent \
     -listen :9090 \
     -endpoint '<Agent部署IP>:9090' \
     -controller '<Controller部署IP>:19090' \
     -state-dir /var/lib/sdn-agent \
     -cert /etc/sdn-agent/tls/server.pem \
     -key /etc/sdn-agent/tls/server-key.pem \
     -ca /etc/sdn-agent/tls/ca.pem

Controller 出站也需要用 ``-agent-ca`` 信任 Agent 证书。
在 CLI 客户端使用同一令牌和 CA 验证 Agent：

.. code-block:: bash

   export SDN_TOKEN='<与目标Agent匹配的令牌>'
   sdnctl -addr '<Agent部署IP>:9090' -ca ca.pem capabilities
   sdnctl -addr '<Agent部署IP>:9090' -ca ca.pem state

隔离实验若暂不部署 TLS，双方均显式选择 ``-insecure``，省略证书参数；
Agent 的 listen / endpoint 仍必须满足 Controller 回连要求。
防火墙和端口映射应允许管理连接，不涉及业务 VXLAN 端口的自动开放。

连接正常后，Controller 节点列表应出现对应 UUID、endpoint 和 RUNNING 状态，
还需要在 Controller / Web 显式纳管，才可执行全网配置。
首次注册失败且没有已确认身份时 Agent 会启动失败；已有身份可在 Controller 离线时启动，
详见 :doc:`transactions`。UUID 用于寻址，不代替 token / TLS 认证。

文件存储与 Redis 的选择
------------------------------------------------------------

原生二进制不加载配置文件且不传 ``-redis`` 时使用 ``/var/lib/sdn-agent/journal.json``，身份仍保存在同目录的
``identity.json``。先用文件模式完成单节点测试即可，不需要安装 Redis 或 etcd。

新的默认 Agent 镜像已经集成 Redis，由启动器在同一个容器内拉起；
agent.conf 统一设置地址、数据目录和 AOF，见 :doc:`configuration`。
不需要另外部署 Redis 容器。原生兼容模式仍可用 ``-redis`` 连接预先准备的 Redis，
但 Go 二进制不会替原生系统管理 Redis 进程；Redis 密码/TLS 当前未实现。

文件与 Redis 是两种不同日志存储，切换参数不会迁移已有任务和 Running 数据；
不要对已受管节点直接切换到空数据库。Redis 模式仍需保留 Agent 身份目录。
每个节点使用独立身份/状态目录；本项目没有本地 etcd 或 Redis 主从配置自动管理功能。

.. _agent-docker-deploy:

方式二：从源码构建并部署 Docker 镜像
------------------------------------------------------------

1. 构建镜像
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

需要 Linux Docker 和可用的源码目录。此方式不要求宿主机安装 Go 或 FRR，
Dockerfile 的构建阶段编译 Agent，运行层包含 Redis 7.4、FRR、frr-pythontools、iproute2 等依赖。
构建上下文是 ``sdn-agent`` 的父目录：

.. code-block:: bash
   :caption: Linux Docker 宿主机

   cd ~/program/github
   docker build -f sdn-agent/docker/Dockerfile -t sdn-agent:alpine .
   docker image inspect sdn-agent:alpine --format '{{.Id}}'
   docker run --rm sdn-agent:alpine sdn-agent -h

默认构建镜像为 Go 1.25 / Alpine 3.22，运行镜像为 Alpine 3.22。
镜像包含 ``sdn-agent``、``sdnctl``、Redis 和 FRR；Redis 来自 redis:7.4-alpine 阶段，
FRR 包版本以实际构建仓库为准。默认统一配置为 /etc/sdn-agent/agent.conf。

2. 启动独立节点
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

以下节点专用于本手册的 CLI 示例，未连接 Controller。先替换令牌占位符，
并保证名称未被其他环境使用；三个命名卷专属于这个节点。
需要从容器外覆盖 agent.conf 时，按 :doc:`configuration` 增加只读 bind mount。

.. code-block:: bash
   :caption: Linux Docker 宿主机

   export SDN_TOKEN='<替换为实际的至少16字节Agent令牌>'
   docker run -d --name sdna-doc-demo --privileged --stop-timeout 240 \
     -p 127.0.0.1:39090:9090 \
     -e SDN_TOKEN \
     -v sdna-doc-demo-state:/var/lib/sdn-agent \
     -v sdna-doc-demo-redis:/var/lib/redis \
     -v sdna-doc-demo-frr:/etc/frr \
     sdn-agent:alpine

``39090`` 是本机映射端口，``9090`` 是容器内端口。
Agent 管理的是容器网络命名空间，不是宿主机业务接口。
此例的 privileged 与明文模式用于隔离实验；完整项目目前没有已验收的生产权限裁剪模板。

3. 检查、停止与再次启动
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

.. code-block:: bash
   :caption: Linux Docker 宿主机

   docker inspect sdna-doc-demo --format '{{.State.Status}} {{.State.Health.Status}}'
   docker logs --tail 80 sdna-doc-demo
   docker exec sdna-doc-demo vtysh -c 'show version'
   docker exec sdna-doc-demo redis-cli ping
   docker exec sdna-doc-demo sdnctl -insecure capabilities
   docker exec sdna-doc-demo sdnctl -insecure state

等待状态为 running / healthy，再确认 capabilities 和 state 返回正常。
容器入口先校验配置，再等待本地 Redis 和 FRR 就绪，然后启动 Agent；默认启用 bgpd / ospfd，
不预置租户、业务地址或 BGP / OSPF 配置。
已在宿主机安装 CLI 时，也可用 ``sdnctl -addr 127.0.0.1:39090 -insecure state`` 查询。

.. code-block:: bash
   :caption: 只操作上述独立测试节点

   docker stop --time 240 sdna-doc-demo
   docker start sdna-doc-demo

stop 会先停止 Agent，再停止 FRR 和内置 Redis；FRR 协议会话会断开。
命名卷保留身份、Redis 日志和 FRR 配置，
但容器网络命名空间重建可能丢失手工 veth 或 Linux 对象，当前不自动完整重建业务网络。
重启后必须重新检查状态，不能把卷仍在理解为无损业务恢复。

4. 部署完整 Controller / Web / Spine / Leaf 环境
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

需要相邻的 ``sdn-controller``、``sdn-web`` 源码及 Docker Compose。
在 Controller 项目执行：

.. code-block:: bash
   :caption: Linux Docker 宿主机

   cd ~/program/github/sdn-controller
   python3 docker/manage.py init
   python3 docker/manage.py build
   python3 docker/manage.py up
   python3 docker/manage.py status

浏览器使用 ``http://<部署服务器IP>:28080``，以该部署的
``sdn-controller/docker/state/tokens.json`` 中 admin / viewer 字段登录。
管理脚本为每个 Agent 挂载专属持久卷与 agent.conf，每个 Agent 内运行自己的 Redis，
并使用容器 DNS 连接 Controller；
不会自动提交租户网络。运行命令、接线、纳管与手工测试详见相邻项目的
``docker/README.md`` 和用户指南。

已有完整环境时不要再按本页创建同名节点或生成另一套令牌。
独立容器的首次配置继续 :doc:`quickstart`；完整网络原理与验证见 :doc:`evpn-lab`。

启动参数速查
------------------------------------------------------------

.. list-table::
   :header-rows: 1
   :widths: 25 25 50

   * - 参数
     - 默认值
     - 用途
   * - ``-config``
     - 原生为空；容器默认 /etc/sdn-agent/agent.conf
     - 严格 JSON 配置，详见 :doc:`configuration`
   * - ``-check-config``
     - false
     - 只校验并显示不含令牌的有效配置，不启动服务
   * - ``-listen``
     - ``:9090``
     - Agent gRPC 监听地址
   * - ``-endpoint``
     - 与 listen 相同
     - Controller 回连地址；接入时必须显式设置
   * - ``-controller``
     - 空
     - 注册/心跳目标；为空时独立运行
   * - ``-state-dir``
     - ``/var/lib/sdn-agent``
     - 身份、进程锁，以及文件模式日志
   * - ``-redis``
     - 空
     - Redis 地址；为空时用本地 journal.json
   * - ``-frr``
     - ``true``
     - 是否注册 FRR 驱动；Linux-only 开发可以设 false
   * - ``-cert`` / ``-key`` / ``-ca``
     - 空
     - Agent 服务证书 / 私钥 / Controller 信任 CA
   * - ``-insecure``
     - ``false``
     - 显式允许实验明文 gRPC，仍验证令牌

实例首次启动会生成 node_id / registration_id，注册确认后保存最终 node_id；
每次进程启动生成新的 instance_id。``agent.lock`` 只阻止本机使用同一状态目录的重复进程，
不能把身份目录复制到其他节点并同时运行。
