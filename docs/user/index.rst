用户指南
========

.. container:: doc-lead

   sdn-agent 管理本地数据面，默认使用 Linux 和 FRR 适配器，可选 VPP EVPN / 对称 IRB 实验模式。
   你可以通过 Controller 管理整网，也可以使用 ``sdnctl`` 在独立实验节点上提交事务。

**第一次部署从这里开始：** :ref:`agent-source-install` → 配置与启动 → 验证服务；
使用容器则直接阅读 :ref:`agent-docker-deploy`。
部署前先核对 :ref:`支持平台与 Linux / FRR 版本 <agent-supported-versions>`：当前内核验收基线为
Linux 6.8.0-138-generic，Linux 数据面的 Alpine 3.22.6 容器使用 FRR 10.2.2；
VPP 实验基线为 VPP 26.06 / FRR 10.7.0，FRR 10.0.0 属于历史回归版本。
这里的编译安装是构建 Agent 程序，与生成 HTML 手册的 ``make -C docs html`` 不同。

.. container:: doc-cards

   .. container:: doc-card

      :doc:`源码编译、安装与部署 <installation>`

      准备依赖、编译安装程序、配置令牌、前台或 systemd 启动；也可选择 Docker 部署。

   .. container:: doc-card

      :doc:`第一个 CLI 事务 <quickstart>`

      创建租户与 BD，分别执行 Begin、Stage、Commit，并检查实际状态。

   .. container:: doc-card

      :doc:`配置操作目录 <operations>`

      查询操作参数、动作和 Linux / FRR 归属；新增 IPv6、OSPFv3 与资源查询见资源手册。

   .. container:: doc-card

      :doc:`任务与恢复 <transactions>`

      理解受理、执行、失败补偿和回滚重试；确认重启能够恢复什么。

.. toctree::
   :maxdepth: 2

   installation
   release
   configuration
   quickstart
   mcp
   operations
   resources
   transactions
   evpn-lab
   vpp
   troubleshooting

先分清操作位置
--------------

* **Linux 宿主机**：构建代码和镜像、启动容器；仓库通常位于 ``~/program/github/sdn-agent``。
* **Agent 节点**：网络命令作用于 Agent 所在网络命名空间；容器内 Agent 管理该容器的接口。
* **CLI 客户端**：运行 ``sdnctl``，通过 gRPC 连接目标 Agent。
* **AI / MCP 客户端**：启动 ``sdn-agent-mcp`` stdio 子进程，通过工具调用访问固定 Agent；接入见 :doc:`mcp`。
* **Controller / Web**：跨节点规划和管理入口，分别属于独立的 ``sdn-controller``、``sdn-web`` 项目。

已由 Controller 纳管的节点应从 Controller 或 Web 修改配置，让全局期望配置与节点事务保持一致。
本手册的独立 CLI 教程使用专用实验容器。

当前范围
--------

已实现 Linux + FRR 驱动、带依赖的单节点事务、持久任务日志、失败回滚、
``RetryRollback``、实际状态采集和注册心跳。提交成功表示本地配置回读通过，
不等于 BGP 已收敛或业务已经可达。

VPP + FRR 的二层 EVPN、租户 FIB、L3VNI 和对称 IRB 见 :doc:`vpp`，
通过配置选择数据面并重启生效。硬件交换机驱动、FPM / ZAPI 接收器、
主动 ARP / ND 保活和全量冷启动 Running 重放仍未实现。
Type-2 / Type-3 观测提供 Overlay 信息，不能独立还原物理布线。

修改代码或增加驱动，请阅读 :doc:`开发指南 </developer/index>`。
