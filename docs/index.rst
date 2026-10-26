sdn-agent 文档
============================================================

.. container:: doc-lead

   管理本地数据面，通过适配器扩展执行后端；当前实现 Linux 和 FRR 适配器。
   先部署 Agent，再通过 CLI 或 Controller 配置网络，查询状态并处理事务恢复。

.. container:: doc-cards

   .. container:: doc-card

      :ref:`源码编译与安装 <agent-source-install>`

      准备 Linux / FRR 和 Go 工具链，编译并安装程序，配置身份、认证和存储，启动后查询状态。

   .. container:: doc-card

      :ref:`Docker 源码镜像部署 <agent-docker-deploy>`

      从源码构建含 FRR 的 Alpine 镜像，挂载持久化目录，并选择独立运行或连接 Controller。

   .. container:: doc-card

      :doc:`用户指南 <user/index>`

      创建租户、BD 和 VXLAN，提交本地事务，检查状态并处理失败恢复。

   .. container:: doc-card

      :doc:`开发指南 <developer/index>`

      协议、事务日志、依赖调度、Linux / FRR 适配器和测试。

从哪里开始
------------------------------------------------------------

独立节点可先按源码或 Docker 教程启动，再阅读 :doc:`第一个 CLI 事务 <user/quickstart>`。加入整网管理时，先部署 Controller，再配置 Agent 的注册目标和可回连地址。

让 AI 查询或操作独立节点时，阅读 :doc:`MCP 接入 <user/mcp>`；
默认提供只读工具，显式启用写模式后沿用 Agent 的版本与事务流程。

教程中的 ``<部署服务器IP>`` 请替换为你部署服务的实际地址。
``127.0.0.1`` 表示命令所在机器本身，``controller:19090`` 等容器 DNS
只在对应 Docker 网络内使用，不能直接填入另一台电脑的浏览器。

生成或清理本手册
------------------------------------------------------------

在 docs/ 目录执行 ``make help``，或在项目根目录执行 ``make -C docs help``，查看可用命令。
下面命令构建的是 HTML 文档。软件编译、安装、配置和启动请使用上面的部署入口。

首次构建文档先按仓库 ``docs/README.md`` 安装 ``docs/requirements.txt``，
然后在项目目录执行：

.. code-block:: bash

   make -C docs html
   # 需要清理生成的文档时执行：
   make -C docs clean

生成入口为 ``docs/html/index.html``，两类手册分别在
``docs/html/user/index.html`` 和 ``docs/html/developer/index.html``。
``clean`` 仅删除文档产物 ``docs/html/`` 与 ``docs/_build/``。
原有 Markdown 设计记录和验收报告继续保留；其中历史环境地址不作为部署参数。

.. toctree::
   :hidden:
   :maxdepth: 2
   :caption: 用户指南

   user/index

.. toctree::
   :hidden:
   :maxdepth: 2
   :caption: 开发指南

   developer/index
