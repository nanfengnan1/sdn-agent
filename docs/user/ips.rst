启用 Snort3 IPS
===============

本页按“构建镜像 → 启动 Agent → 挂载基础配置 → Web 下发规则 → 查询与恢复”
组织。IPS 只支持 VPP 的三层 IP 路径，默认关闭。

.. list-table:: 两类配置的归属
   :header-rows: 1
   :widths: 25 35 40

   * - 内容
     - 来源
     - 更新方式
   * - 基础参数、HOME_NET、检测与日志
     - 宿主机只读 Lua 文件
     - sdn-snort-reload 或可选自动重载
   * - 规则和接口绑定
     - Web / Controller 候选
     - 预览、事务提交、实际回读

准备镜像
--------

IPS 使用包含 VPP、FRR、Snort3 和 confd 的 Agent 镜像。
复用容器内 Redis，无需为 Agent 部署 etcd。

首次构建（在 sdn-agent 仓库根目录，VPP_SOURCE 替换为自己的本地分支目录）：

.. code-block:: console

   BUILD_BASES=1 \
   VPP_SOURCE=/home/alexan/program/subversion/vpp VPP_REF=sdn/v26.06 \
   VPP_IMAGE=alpine/vpp:sdn-base \
   FRR_IMAGE=alpine/frr:sdn-base \
   SNORT_IMAGE=alpine/snort:sdn-base \
   AGENT_IMAGE=alpine/sdn-agent:ips \
   sh docker/build-vpp.sh

默认 Snort 3.12.2.0、libdaq 3.0.27；
VPP/FRR 源码选择沿用 :doc:`vpp`。
本次需要配套 VPP 分支中的 Snort 单实例 DAQ 与查询修复。
配套 VPP 修改只在本地提交、尚未推送时，构建命令使用
``VPP_SOURCE=/home/alexan/program/subversion/vpp VPP_REF=sdn/v26.06``，
使用本地已修改的源码；推送后可使用默认远程分支构建。
仅更新 Agent 镜像而复用缺少修复的旧 VPP 基础镜像，不能完成此次 IPS 集成。

可以通过 ``SNORT_REF`` 选择其他上游 tag，但需要验证与 libdaq/VPP DAQ 的兼容性。
只重建 Snort 和 Agent 时设置 ``BUILD_BASES=0 BUILD_SNORT=1``；
复用全部基础镜像时使用 ``BUILD_BASES=0 BUILD_SNORT=0``。

配置与启动
----------

在已有 agent.conf 中启用 IPS，保留自己的 Controller、身份和 TLS 设置：

.. code-block:: json

   {
     "agent": {
       "dataplane": "vpp",
       "frr": true,
       "listen": ":9090",
       "stateDir": "/var/lib/sdn-agent",
       "insecure": true
     },
     "redis": {
       "enabled": true,
       "dataDir": "/var/lib/redis",
       "appendOnly": true,
       "appendFsync": "always"
     },
     "ips": {
       "enabled": true,
       "socket": "/run/sdn-agent/ips.sock",
       "templateDir": "/etc/sdn-agent/snort",
       "configFile": "/etc/sdn-agent/snort/base.lua",
       "autoReload": false,
       "startupTimeoutSeconds": 90
     }
   }

``insecure`` 只用于隔离实验；生产管理连接按配置章节启用 TLS。
自定义 VPP startup.conf 时必须启用 ``snort_plugin.so``，保留 API socket。
镜像提供基础配置 ``/etc/sdn-agent/snort/base.lua``，可通过 ``ips.configFile``
指定自己的只读挂载文件。``templateDir`` 中的 ``snort.toml`` 和
``snort.lua.tmpl`` 是 Agent 内部生成模板，使用镜像默认版本；不要通过修改模板
配置检测参数。

.. code-block:: console

   mkdir -p state/agent state/redis state/frr
   export SDN_TOKEN='替换为至少16字节的独立令牌'
   docker run -d --name sdn-ips-node --privileged \
     --stop-timeout 150 -e SDN_TOKEN \
     -v "$PWD/agent.conf:/etc/sdn-agent/agent.conf:ro" \
     -v "$PWD/state/agent:/var/lib/sdn-agent" \
     -v "$PWD/state/redis:/var/lib/redis" \
     -v "$PWD/state/frr:/etc/frr" \
     alpine/sdn-agent:ips

这条示例不发布管理端口；纳管时按现有部署方式连接管理网络，并配置
``agent.controller`` 与其他容器可达的 ``agent.endpoint``。

从宿主机配置 Snort
-------------------------

基础参数和动态规则分开管理：宿主机 Lua 设置网络变量、检测模块和日志；
Web/Agent 管理规则。Agent 强制 inline 模式，保留基础配置中的其他 ``ips`` 选项；
``ips.rules`` 与 ``ips.include`` 由 Agent 独占，基础文件设置它们会被拒绝，
以免基础文件中的规则与 Web SID 冲突。

在 Agent 仓库根目录准备配置：

.. code-block:: console

   mkdir -p config/snort
   cp docker/snort/base.lua config/snort/snort.lua

修改 ``config/snort/snort.lua``，例如：

.. code-block:: lua

   include '/usr/etc/snort/snort.lua'
   ips.variables.nets.HOME_NET = '10.0.0.0/8'
   alert_json = { file = true }

在现有 ``agent.conf`` 的 ``ips`` 对象中设置：

.. code-block:: json

   {
     "enabled": true,
     "configFile": "/etc/snort-local/snort.lua",
     "autoReload": false
   }

在前面的 ``docker run`` 命令中增加：

.. code-block:: console

   --mount type=bind,src="$PWD/config/snort",dst=/etc/snort-local,readonly

挂载整个目录，编辑器通过重命名替换文件时容器仍能看到新文件。
不要挂载覆盖 Agent 自动生成的 ``/var/lib/sdn-agent/ips/snort.lua``。

修改宿主机文件后执行：

.. code-block:: console

   docker exec sdn-ips-node sdn-snort-reload

成功时返回新配置摘要和加载代次；失败返回非零退出码及原因，恢复上一份
已生效的基础配置，已加载的 Web 规则保留。命令不重启 Snort。
若自定义了 ``ips.socket``，附加 ``--socket /你的/ips.sock``。

若希望保存文件后自动生效，将 ``ips.autoReload`` 设为 ``true`` 后重启容器
使此开关生效。运行时约每 3 秒检查一次，连续两次内容相同才尝试加载；
同一份错误内容不反复重载，修改内容后重新尝试。建议用临时文件加原子重命名发布修改。
默认关闭自动重载时，宿主机修改保持待加载状态；Web 更新规则仍使用已接受的基础配置。

基础文件必须是非空 UTF-8 文本，最大 1 MiB。启动时缺失或无效会拒绝启动，
不会静默使用镜像默认值。运行中的错误修改不会覆盖上一份已生效配置；
再次启动前应修正宿主机文件。

运行时对基础文件本身做内容快照，确保校验、重载和失败恢复使用同一份内容。
示例中的镜像自带 include 可以使用；额外依赖的 Lua/规则文件不会被自动监控或快照，
请使用绝对路径并保持其版本稳定。相对路径不以宿主机基础文件目录为工作目录。
并非所有 Snort 参数都允许热修改；重载不支持的参数会报错并恢复，
需要维护窗口重启的变更不能仅靠文件自动重载完成。

从 Web 配置
-----------

1. 连接包含新协议版本的 Controller/Web，确认节点已纳管。
2. 打开左侧 **IPS 配置**。首次使用独立资源时先验证并导入基线。
3. 新建资源候选，添加规则 SID 和完整 Snort 规则。
4. 添加检查接口：填写实际 VPP 接口名和方向。
   使用三层接口或 BVI；普通 BD 二层端口不支持此检查路径。
5. 预览全部变更，再提交。任务成功后查询 **实际生效规则** 和
   **IPS 服务与重载状态**。

示例：阻断 IPv4 ICMP Echo Request，SID 填 ``1000001``：

.. code-block:: text

   drop icmp any any -> any any (msg:"Block IPv4 ping"; itype:8; sid:1000001; rev:1;)

``alert`` 只告警，``drop`` 在 inline 路径上阻断。
IPv6 ICMP 类型不同；应提供相应规则并用 IPv6 流量单独验收。
删除规则同样需要加入候选并提交，会触发热重载。

绑定/解除接口会短暂切换接口管理状态；规则增删不切换接口。
当前规则集对本节点所有 IPS 绑定接口生效，尚无每租户独立 Snort 实例。
Snort 断连时已绑定方向丢弃流量，因此不要直接绑定管理链路。

查询与故障定位
--------------

.. code-block:: console

   docker exec sdn-ips-node sdnctl -insecure -kind IPS_STATUS query
   docker exec sdn-ips-node sdnctl -insecure -kind IPS_RULES query
   docker exec sdn-ips-node vppctl show snort instances
   docker exec sdn-ips-node vppctl show snort interfaces
   docker logs --tail 100 sdn-ips-node

查询显示配置代次、Snort PID、已加载的 Agent 托管规则数、实际绑定和错误信息。
``baseConfigPath`` 是宿主机挂载文件的容器路径，
``baseConfigSHA256`` 是已接受内容的摘要，``baseConfigPending`` 表示磁盘文件有待加载改动；
``baseConfigError`` 单独报告基础配置错误，``autoReload`` 显示自动重载开关。
Web 的 **IPS 服务与重载状态** 查询也显示文件路径、待加载状态和错误。
告警日志目录默认 ``/var/lib/sdn-agent/ips/logs``，
对应宿主机示例中的 ``state/agent/ips/logs``；实际路径以查询结果为准。

不要手工修改生成的 ``ips/snort.lua`` 或 Redis 的 IPS 键：
Web/Agent 才是规则配置入口，外部修改会导致回读校验失败。
错误规则不会记为成功；发生恢复失败时按任务页重试回滚。
持久化 Agent 与 Redis 两个目录后，规则可以在容器重建后恢复；
手工创建的网口/地址/路由仍需自己的启动配置。
