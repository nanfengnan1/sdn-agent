开发指南
========

.. container:: doc-lead

   从协议和事务边界理解实现，再增加操作、驱动或恢复能力。
   本指南描述当前 Go 代码；仓库根目录 ``agent.md`` 还包含长期设计，不能把目标组件视为已交付。

先准备开发环境并构建程序，请读 :doc:`testing`；把构建结果安装、配置并运行到节点，
请直接读 :ref:`agent-source-install` 或 :ref:`agent-docker-deploy`。
生成 HTML 手册不属于 Agent 程序的编译步骤。
制作带文档的离线包、推送 tag 和上传 GitHub 附件，按 :doc:`release` 执行；
接收方安装和升级见 :doc:`版本发布、交付与升级 </user/release>`。
增加新的数据面或执行后端时，按 :doc:`适配器扩展流程 <adapters>` 确定资源归属、实现接口、注册能力并验证恢复。
AI 工具接入与扩展见 :doc:`MCP 适配层设计 <mcp>`；它通过公开 gRPC 复用事务，不直接执行数据面命令。

.. container:: doc-path

   **阅读顺序**　架构 → 协议 → 执行与存储 → 驱动 → 测试 → 文档维护。

.. toctree::
   :maxdepth: 2

   architecture
   protocol
   resources
   execution-storage
   adapters
   vpp
   ips
   vpp-keepalive
   mcp
   testing
   release
   documentation

当前关键约定
------------

* 单节点事务包含一个 op 的全部注册驱动；没有随机挑选一个后端的逻辑。
* 依赖由调用方显式提供；Agent 校验并排序，不负责整网资源规划。
* Undo 在调用 Apply 前持久化；失败与进程恢复都以保存的 Undo 补偿。
* 回滚逆序执行，在第一处失败停止；RetryRollback 只重试 Undo。
* Running 是成功操作日志，Operational 是实际回读，二者不可互相替代。
* 驱动以进程内 Go 对象注册；VPP composite 另调用本机 Python / Binary API 适配进程，见 :doc:`vpp`。远程动态注册协议尚未交付。

运行软件请从 :doc:`用户指南 </user/index>` 开始。
