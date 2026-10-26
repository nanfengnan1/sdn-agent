测试系统与贡献
==============

测试分为确定性单元 / gRPC 故障测试和实际 Linux / FRR 网络实验。
Fake Driver 可以证明调用与恢复规则，不能证明真实配置生效或报文转发。

开发工具链与首次编译
----------------------------------------

普通编译需要 Linux、Go 1.25+、GNU Make 和可下载 go.mod 依赖的网络或模块缓存。
``make test`` 还需要 Python 3，包含不启动真实服务的容器监督逻辑测试。
源码包含生成的 .pb.go，不修改协议时无需 protoc；仅做单元测试不必启动 FRR/Redis。
race 检查还需要 C 编译器及启用 CGO，与发布二进制的静态构建选项不同。

.. code-block:: bash
   :caption: Linux 开发机

   cd ~/program/github/sdn-agent
   go version
   make --version
   go mod download
   make build
   ./bin/sdn-agent -h
   ./bin/sdnctl -h

生成的服务端/客户端在 bin/。部署安装、配置目录、启动和停止步骤见
:ref:`agent-source-install`；希望由 Docker 一并准备 Go 编译器和 FRR 时使用
:ref:`agent-docker-deploy`。无需把开发工具链安装到最终 Agent 运行镜像中。

重新生成 Protobuf
----------------------------------------

仅在修改 ``api/agent/v1/agent.proto`` 时执行。先安装可用的 protoc 编译器，
再准备 Go 生成插件；下面的插件版本来自当前提交的生成文件头：

.. code-block:: bash
   :caption: Linux 开发机：sdn-agent 仓库根目录

   protoc --version
   go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
   export PATH="$(go env GOPATH)/bin:$PATH"
   protoc-gen-go --version
   protoc-gen-go-grpc --version
   make generate
   make build
   make test

现有生成文件记录的 protoc 版本为 v7.35.1。要求保持生成结果一致时应对齐生成工具，
不要假定操作系统默认 protoc 包恰好是这个版本。升级生成工具需要审查生成差异、
协议兼容性和 go.mod 运行库要求，并把 proto 与生成代码一起提交。

本地检查
--------

.. code-block:: bash
   :caption: Linux 开发机：sdn-agent 仓库根目录

   go test -count=1 -v ./...
   go test -race -count=1 ./...
   go vet ./...
   make build

Makefile 也提供 ``make test``、``make race``。需要覆盖率时另运行：

.. code-block:: bash

   go test -coverprofile=coverage.out ./...

逐命令与动作测试
----------------------------------------

``internal/adapters/linux/operations_test.go`` 与
``internal/adapters/frr/operations_test.go`` 与 Linux 驱动测试为全部 26 类网络操作建立独立子测试。
下表共有 47 个不同的 code/action 组合；TENANT 的两个动作分别验证 Linux 与 FRR，
因此驱动生命周期测试共 49 个组合（Linux 18、FRR 31）。

.. list-table::
   :header-rows: 1
   :widths: 30 45 25

   * - 操作
     - 支持动作
     - 后端
   * - NODE_NETWORK
     - SET
     - Linux
   * - INTERFACE
     - SET
     - Linux
   * - ADDRESS
     - CREATE / DELETE
     - Linux
   * - TENANT
     - CREATE / DELETE
     - Linux + FRR
   * - BD
     - CREATE / SET / DELETE
     - Linux
   * - BD_PORT
     - BIND / UNBIND / SET
     - Linux
   * - BD_TENANT
     - BIND / UNBIND
     - Linux
   * - VXLAN
     - CREATE / DELETE
     - Linux
   * - STATIC_ROUTE
     - CREATE / DELETE
     - Linux
   * - OSPF_INSTANCE
     - CREATE / SET / DELETE
     - FRR
   * - OSPF_NETWORK
     - CREATE / SET / DELETE
     - FRR
   * - OSPF_INTERFACE
     - SET / DELETE
     - FRR
   * - BGP_INSTANCE
     - CREATE / SET / DELETE
     - FRR
   * - BGP_PEER_GROUP
     - CREATE / SET / DELETE
     - FRR
   * - BGP_NEIGHBOR
     - CREATE / SET / DELETE
     - FRR
   * - BGP_EVPN_PEER
     - SET / DELETE
     - FRR
   * - BGP_EVPN_GLOBAL
     - SET / DELETE
     - FRR
   * - EVPN_L2VNI
     - CREATE / SET / DELETE
     - FRR
   * - EVPN_TENANT
     - SET / DELETE
     - FRR
   * - TENANT_L3VNI
     - BIND / UNBIND / SET
     - FRR

每个支持动作都检查参数合法性、Prepare 不修改状态、Apply 的 argv、
独立状态样本的 Verify、Undo 序列化后的恢复及重复 Rollback。
每个操作还检查缺失 / 错误 oneof、所有不支持的枚举动作及未知动作；
边界测试覆盖接口名、MTU、VNI、端口、VRF 表号、地址、ASN、RD/RT 格式等。
执行失败测试检查读取失败、写入失败及后续写入停止。
Linux 另外检查部分 MTU 修改后的旧值恢复、逆向创建后中断的回滚续跑、
删除前引用约束、可选 sysctl 缺失以及静态路由的表号 / 默认路由回读。
FRR 另外检查 CLI 错误文本、write memory 失败和恢复后的配置不一致。

这些测试运行实际驱动代码，Runner 返回独立的 iproute2 JSON 或 FRR 配置样本，
不会执行系统网络命令或启动 FRR。样本不由被测驱动的 commands/render 生成。
它们验证驱动行为契约，不证明目标内核 / FRR 版本接受配置，也不替代报文转发验收。
新增 code 时能力覆盖测试要求它有后端；新增后端 code 时驱动测试要求它有用例。
增加支持的 action 时，应同步更新用例矩阵和本表。

``cmd/sdnctl/main_test.go`` 在独立进程中执行真实 main、flag、transport、输出与退出路径，
连接临时 localhost gRPC 服务。覆盖 capabilities、state、begin、stage、commit、submit、
abort、task、watch、retry-rollback 全部 10 个子命令，逐项检查 RPC 次序、请求参数、
节点发现、请求 ID、JSON 输出和每一步的 RPC 失败。
还覆盖必填参数、非法 JSON、TLS 参数、失败任务终态、超时、流错误和提前 EOF。
正常游标续接到已消费的终态事件时，CLI 通过 GetTask 确认状态；watch 不重复输出旧事件。
提前 EOF 且任务仍未完成、无法查询任务或任务失败时返回非零退出码。

``internal/backend/backend_test.go`` 用真实驱动校验 TENANT / BD / VXLAN 的组合计划，
验证乱序输入的依赖排序及反向解绑删除次序，包含 TENANT 删除时 Linux 先于 FRR 的规则。
原有 agent_test.go / recovery_test.go 继续验证事务回滚、请求幂等和持久恢复。

.. code-block:: bash
   :caption: 按命令 / 动作单独选择，不改变现有实验容器

   # 全部逐命令测试，含组合计划
   go test -count=1 -v ./internal/adapters/... ./internal/backend ./cmd/sdnctl

   # 单个 Linux 操作的全部动作，或只选择 DELETE
   go test -v ./internal/adapters/linux -run '^TestLinuxOperationLifecycle$/^STATIC_ROUTE$'
   go test -v ./internal/adapters/linux -run '^TestLinuxOperationLifecycle$/^STATIC_ROUTE$/^DELETE$'

   # 单个 FRR 操作，以及其非法参数 / 执行错误
   go test -v ./internal/adapters/frr -run '^TestFRROperationLifecycle$/^BGP_NEIGHBOR$'
   go test -v ./internal/adapters/frr -run '^TestFRR(ParameterBoundaries|ExecutionFailures)$/^BGP_NEIGHBOR$'

   # 单个 CLI 子命令，或完整 submit 失败链
   go test -v ./cmd/sdnctl -run '^TestCLICommands$/^submit$'
   go test -v ./cmd/sdnctl -run '^TestCLIRPCFailures$/^submit$'
   go test -v ./internal/backend -run '^TestPlanNetworkLifecycleDependencies$'

上述测试随 ``go test ./...`` 和 ``make test`` 自动执行。
CLI 使用子进程运行，被测 main 的执行不计入普通 go test 的包内行覆盖率；
不能据此把 CLI 的低行覆盖率解释为未运行子命令，也不能把用例数量当作所有参数组合覆盖。
独立运行的真实 FRR 恢复测试仍需专用环境，默认跳过。

配置与容器启动器测试
----------------------------------------

``internal/config/config_test.go`` 验证原有 CLI 默认值、文件/环境变量/CLI 优先级、
未知字段、大小写重复字段、类型错误、凭证脱敏，以及内置 Redis 端口与数据目录约束。
无效配置必须在创建 Agent 状态目录和启动依赖进程前失败。

``docker/supervisor_test.py`` 使用标准库 unittest 和模拟子进程，验证配置预检、
Redis argv、依赖退出、动态健康检查、退出顺序、utility 参数和专用数据目录检查。
这些测试不会启动 Redis、FRR 或 Docker。单独运行：

.. code-block:: bash

   go test -race -count=1 ./internal/config
   make test-runtime

完整镜像验证还需检查只读外部配置、自定义端口、仅文件令牌、Redis 故障退出及持久卷重启。
这类结果与单元测试分开记录，由相邻 Controller 项目的 Docker 集成环境执行；
不能把模拟进程测试当作真实 FRR 或 EVPN 数据流测试。

重点回归场景
------------

.. list-table::
   :header-rows: 1
   :widths: 34 66

   * - 区域
     - 测试重点
   * - 协议与校验
     - node_id 隔离、错误 oneof、非法依赖、循环、鉴权与事件游标。
   * - 计划和执行
     - 同 op 全后端执行、依赖顺序、Prepare / Apply / Verify 失败。
   * - 事务与存储
     - revision 冲突、Commit 幂等、持久记录中的 oneof 还原、原子写入失败。
   * - 崩溃与断连
     - 持久 Started 后重启补偿、客户端断开后任务继续、注册冲突与过期实例。
   * - 回滚
     - 部分修改、旧值恢复、失败屏障、逆序第一处失败停止。
   * - RetryRollback
     - sequence / revision 校验、幂等、重启继续 Undo、历史保留与不安全旧恢复历史拒绝。

新增恢复测试位于 ``internal/agent/recovery_test.go``，
包括 ``TestRollbackRetrySurvivesRestartAndPreservesHistory`` 和
``TestRollbackRetryRejectsUnsafeLegacyHistory``。
修改恢复语义时必须检查这些测试及原有 agent_test.go，不能只验证接口返回成功。

真实 FRR 恢复测试
----------------------------------------

``internal/agent/recovery_frr_test.go`` 中的
``TestRealFRRRollbackRetry`` 默认跳过，需要 ``SDNA_REAL_FRR_RECOVERY=1``。
它会修改 FRR，必须在可丢弃且独占的专用 FRR fixture 内执行。
普通 ``go test`` 显示该项跳过，不能记为真实 FRR 验证通过。

相邻 Controller 仓库 ``lab/reliability.py`` 已提供真实 FRR fixture 和可靠性回归入口。
运行前阅读它的容器归属及状态目录约定；不要在手工实验或业务节点上直接开启该环境变量。

网络实验与完整回归
------------------

.. code-block:: bash
   :caption: Linux Docker 宿主机：只操作本项目独立实验

   make lab
   python3 lab/run.py --verify-only
   make regression

``make regression`` 包含 Go 测试、Python 容器监督测试、race、vet、构建、清理旧的本项目实验，
再从空环境建立网络验证；它会改变 ``sdna-e2e-*`` 实验。
失败立即停止，保存容器和证据供检查。

实验覆盖 Underlay、BGP EVPN、Type-2 / Type-3、L3VNI 分类、双向跨网段流量、
Linux / FRR 注入失败恢复、资源删除、Agent SIGKILL 恢复及 Controller 断连 / 重连。
纯 Type-2 场景先让两端 PC ping 本地网关产生邻居学习。

关键产物：

* ``artifacts/lab-results.json``：网络实验检查结果。
* ``artifacts/regression-results.json`` 与 ``regression-*.log``：命令、退出码与完整日志。
* ``artifacts/`` 中各节点 Task、GetState、running-config、Spine EVPN 与 Leaf VNI 证据。

完整 Alpine Docker / Web 集成测试属于相邻 Controller / Web 项目。
Agent 独立实验固定 FRR 10.0.0；2026-10-25 的完整 Alpine 验收使用 FRR 10.2.2。
仓库已有历史报告 ``docs/validation.md``，近期可靠性与 Docker 报告还位于 Controller 仓库。
内置 Redis 与外部配置版本的历史结果保留在相邻 Controller 项目
``docs/bundled-validation.md``。本轮独立资源的整网验证记录在该项目
``docs/resource-validation.md``，记录实际协议、资源、CLI、浏览器与部署结果；HTML 构建不能替代网络验收。

尚未被这些结果覆盖的能力
------------------------

目前不声明完成大规模性能、长时间运行、跨节点原子事务、Controller 高可用、
IPv6 全链路、VPP / 硬件交换机、主动邻居保活、主机重建后自动恢复的验收。
更换 FRR / Linux 版本、存储模式、网络权限或 Undo 格式后，应补充相关真实回归。

提交改动前
----------

说明修改对应的行为、协议兼容性和恢复边界；运行与改动相关的测试及项目要求的检查。
网络适配器的行为变化需要真实后端证据，失败实验应修复代码后重新建立，
不能人工改好设备后记为 Agent 自动配置成功。

不提交 token、identity、私钥、Redis AOF、完整运行卷、bin 或临时 artifacts。
报告中可以记录脱敏节点、命令、软件版本、检查结果和限制。
文档改动按 :doc:`documentation` 构建，保持内部引用有效。
