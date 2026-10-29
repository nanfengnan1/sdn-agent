Snort IPS 与 Redis/confd
========================

职责与数据流
------------

IPS 是可选后端。启用后由一个 Agent 容器运行 VPP、FRR、Redis、
Agent 和 Snort 运行时。confd 按次执行；Agent 无需额外运行 etcd。
Controller 继续使用自己的存储，Web 不直接访问 Redis。

.. code-block:: text

   Web IPS 页面 / Controller 候选
                 |
                 v
   IPS_RULE / IPS_INTERFACE typed operations
                 |
   Agent: Prepare -> Undo journal -> Apply -> Verify
                 |
                 v
   Unix socket: /run/sdn-agent/ips.sock
                 |
   Snort runtime (single execution thread)
       |         |                         |
       |         +-> VPP Binary API        +-> status / rules
       |             instance + attachment
       v
   Redis complete snapshot -> confd -onetime
       |                         |
       |                         v
       |                 candidate snort.lua
       |                         |
       |                 snort -T validation
       |                         |
       |                 replace live file
       |                         |
       |                 reload_config()
       |                         |
       +<--- accepted state <----+ reload complete + generation
                                 |
   VPP IP feature -> shared-memory DAQ -> Snort verdict -> VPP

代码分工
--------

* ``internal/adapters/snort``：Go Driver、事务 Undo、实际回读和能力协商。
* ``docker/snort/runtime.py``：私有 Unix RPC、规则校验、Redis 快照、
  confd 执行、Snort 子进程和重载确认、VPP 绑定。
* ``docker/snort/snort.toml`` / ``snort.lua.tmpl``：默认配置资源及模板。
  模板是内部渲染协议，使用镜像默认版本；检测参数通过基础 Lua 配置。
* ``docker/supervisor.py``：Redis/VPP/FRR 就绪后启动 IPS；
  IPS 就绪后再启动 Agent。退出时先排空 Agent 事务，再停止 IPS 和 VPP。
* ``docker/snort/Dockerfile``：独立构建 Snort/libdaq/confd，
  ``Dockerfile.vpp`` 组成最终 Agent 镜像。
  VPP 基础镜像也安装 Snort 与 DAQ。

资源模型
--------

``IPS_RULE`` 使用 SID 作为稳定标识，支持 CREATE/SET/DELETE。
一条操作包含一个完整的单行 Snort 文本规则；SID 必须与规则内容一致。
禁止 include、Lua、共享对象规则及控制字符；规则内容只作为 Lua 字符串转义，
不拼接可执行 Lua 或 shell。最终语法校验由镜像中的 ``snort -T`` 完成。

``IPS_INTERFACE`` 使用 VPP 原生接口名，方向为 input/output/inout。
当前为单节点单实例 ``sdn-ips``，全部规则作用于所有绑定方向。
``snort-ips/v1`` 只在启用且运行时成功启动后声明；
``IPS_STATUS`` 和 ``IPS_RULES`` 分别提供重载/进程/绑定状态与实际加载规则。
旧 Agent 不声明能力时，Controller 不会向它下发这些操作。

一致性与恢复
------------

Redis 键使用独立前缀 ``/sdn/ips/v1/``。``desired`` 保存已确认的整个快照；
``render`` 保存当前候选生成的完整 Lua 内容。一次 SET 提交整个值，
避免 confd 分别读取多个规则键时混入不同版本。
使用 Agent 已有的专用 Redis、AOF 和持久化目录，不设置键过期时间。

confd 的 ``check_cmd`` 校验临时文件，``reload_cmd`` 调用受控重载程序。
程序必须收到 Snort 的 ``reload complete`` 并确认配置代次递增，
才写入文件摘要与代次凭据。发送 SIGHUP 成功不等于加载成功。

confd 本身不会在重载失败时自动恢复文件，也可能跳过内容未变化的文件。
因此运行时负责恢复旧渲染值、配置文件和接口绑定；必要时强制重载并回读。
Agent 在 Apply 前持久化完整旧快照，跨进程恢复继续由原有 Undo 机制处理。
回滚失败会保留失败任务，不伪造成功状态。

冷启动先从 Redis 恢复规则，再启动 Snort。接口可能随后才由网络适配器创建；
运行时将缺失绑定报告为 DEGRADED，并在接口出现后重建绑定。
规则持久化不等于自动重建手工创建的 TAP/路由，这些仍需原有启动配置。

转发与边界
----------

VPP 插件使用 ip4-unicast/ip6-unicast 和 ip4-output/ip6-output feature arcs。
纯 BD 二层转发不会经过这些节点；运行时拒绝直接绑定普通 BD 成员，
可在合适的三层接口/BVI 上检查经过路由的流量。
外层 VXLAN 口与租户 BVI 的检查位置不同，不能把外层规则效果当作内层验收。

原生插件要求接口 Down 才能更改绑定；运行时短暂切换管理状态，
在成功或失败后恢复原 Up 状态。该动作可能触发邻接变化。
规则热重载不切换接口状态、不重启 Snort。

断连策略固定为 drop：Snort 消失时已绑定方向阻断流量。
默认 IPS 关闭且没有绑定接口。日志与告警位于 Agent stateDir 下的
``ips/logs``，查询返回实际路径。

外部基础配置
------------

``ips.configFile`` 默认 ``/etc/sdn-agent/snort/base.lua``，
``ips.autoReload`` 默认 false。管理员目录只读挂载，生成文件仍由 Agent 写入 stateDir。
Go 检查路径；Python 拒绝空文件、超限内容、NUL、非 UTF-8 和与生成文件相同的路径。

启动读取一次基础文件；render 将其作为转义后的独立 Lua chunk 执行，
随后保留基础 ips 选项并设置 inline 模式和托管规则。
拒绝基础文件中的 ips.rules/ips.include，避免受控 SID 被覆盖。
日志配置保留在基础文件中，不由托管规则生成器重写。

手动命令 ``sdn-snort-reload`` 经私有 Unix socket 调用 reload-base。
自动监控与 Web 规则操作使用同一串行事件循环，约三秒轮询、两次内容稳定后尝试；
重复坏文件不反复重载。校验和热重载只使用内存中的不可变内容快照，
不会在验证后再次读取宿主机文件。模板内容一致性校验与基础文件快照一致，
因此自定义基础参数不再触发固定模板误报。

加载失败用旧基础快照重新渲染并确认恢复；若恢复也失败，明确报告 recovery failed。
基础文件错误与规则事务错误分别记录。Web 规则 Undo 只回滚规则和绑定，
不撤销管理员独立提交的基础配置。基础配置源仍以宿主机文件为准，
不是 Controller 的候选资源；冷启动时无效源文件拒绝启动。

监控只覆盖主文件；额外 include 不是同一个版本快照，也不会独立触发重载。
生产配置应把可变参数集中在主文件，额外文件使用稳定版本。
开启自动加载后应采用目录挂载和原子重命名；仅挂载单个文件可能继续引用旧 inode。

验收入口：

.. code-block:: console

   SDN_IPS_EXTERNAL=1 \
   SDN_IPS_IMAGE=alpine/sdn-agent:ips-external-test \
   python3 lab/snort_ips.py

测试改变 HOME_NET 并用真实 IPv4 报文验证判定变化，覆盖手动/自动重载、
错误配置恢复、保留 Web 规则、规则更新保留基础配置、重启及双栈回归。

测试
----

.. code-block:: console

   python3 -m unittest discover -s docker -p '*test.py'
   python3 -m unittest discover -s docker/snort -p '*test.py'
   go test ./internal/adapters/snort ./internal/config ./internal/backend ./mcp

整栈验收使用 ``lab/snort_ips.py``：独立 Controller/Web/Agent 容器，
通过 Web Gateway API 提交规则，验证双栈转发、drop、删除恢复、
PID 不变、配置代次变化、错误配置回滚和重启持久化。
不占用物理网卡，不替换现有 fabric。

浏览器验收使用配套 Web 仓库的 ``tests/e2e/snort-ips.cjs``，
实际完成登录、IPS 页面创建规则、查询生效状态和删除规则；
脚本只接受 ``sdn-ips-`` 前缀的隔离环境访问文件。

.. code-block:: console

   SDN_IPS_OUTPUT="$PWD/artifacts/ips/manual" \
   SDN_IPS_BROWSER=1 SDN_IPS_KEEP=1 python3 lab/snort_ips.py

脚本输出 READY 后，在另一个终端运行：

.. code-block:: console

   SDN_TEST_ACCESS="$PWD/artifacts/ips/manual/stack-access.json" \
   PLAYWRIGHT_MODULE=/绝对路径/playwright-core \
   CHROME_BIN=/绝对路径/chrome \
   node ../sdn-web/tests/e2e/snort-ips.cjs

报告保存为 ``report.json``、``web-ips.json``，截图为 ``web-ips.png``。
``SDN_IPS_KEEP=1`` 保留测试容器和网络供排查；测试后按访问文件的 stamp
删除该次测试容器、网络和令牌文件。常规 API 验收不设置这两个开关即可自动清理。

2026-10-28 验收记录：31 项 Python、20 项 Web 单测和三个服务的 Go 测试通过；
20 项 IPS API/转发/重启检查、真实浏览器创建/查询/删除规则和 29 项双栈 EVPN
回归通过。汇总见 ``artifacts/ips/redis-20261028-164849/verification.json``。
浏览器初次的下拉框等待错误已修正并复测；原始失败记录保留。
本次为软件 TAP 路径，未进行物理网卡或性能验收。

2026-10-29 外部配置验收通过：31 项隔离容器检查、12 项 runtime 单测、
25 项容器单测、相关 Go 包测试、21 项 Web 单测及类型检查/构建。
默认配置组合与禁止基础文件规则 include 使用实际 Snort 校验。
报告位于 ``artifacts/ips/external-20261029-094908/verification.json``。

租户 BVI 冷邻居回归
-------------------

设置 ``SDN_IPS_BVI=1`` 运行同一脚本，可将接入 TAP 放入独立 BD，
通过 BVI 和非默认租户 FIB 1001 进行双栈路由，再绑定 IPS。
测试主动清空隔离容器内的邻居，验证输入检查时重新学习 ARP/ND，
以及 inout 检查后的输出 rewrite；随后执行规则增删、阻断和重启恢复。

.. code-block:: console

   SDN_IPS_BVI=1 SDN_IPS_IMAGE=alpine/sdn-agent:v1.0 \
   SDN_CONTROLLER_IMAGE=alpine/sdn-controller:v1.0 \
   SDN_WEB_IMAGE=alpine/sdn-web:v1.0 \
   python3 lab/snort_ips.py

VPP IP feature arc 的 DAQ 元数据必须保存在描述符中，不能覆盖 buffer 的
IP adjacency/FIB/rewrite union。SFDP 的自定义 continuation 保持原协议。
本回归不覆盖主动响应报文注入或吞吐性能。
