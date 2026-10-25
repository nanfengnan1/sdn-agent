扩展 Linux / FRR 与新增 Driver
==================================================

sdn-agent 负责管理本地数据面，通过 Driver 接口扩展不同的数据面和执行后端。
当前实现 Linux 与 FRR 适配器：Linux 管理内核网络对象，FRR 管理路由协议配置；
当前组合中的 TENANT 操作需要两个驱动共同参与。

扩展方式是在源码中增加进程内 Go Driver，编译并部署新的 Agent。
进程启动时创建 Driver，Registry 保存 op 到多个 Driver 的映射。
当前没有运行时加载插件、远程 Adapter 注册或通用后端选择配置；
VPP、OVS 或硬件交换机的驱动需要另行实现，不能只增加一个配置名称就启用。

Driver 接口
-----------

接口定义在 ``internal/backend/backend.go``：

.. code-block:: go

   type Driver interface {
       Name() string
       Codes() []pb.OpCode
       Validate(*pb.Operation) error
       Prepare(context.Context, *pb.Operation) (*model.Undo, error)
       Apply(context.Context, *pb.Operation) error
       Verify(context.Context, *pb.Operation) error
       Rollback(context.Context, *pb.Operation, *model.Undo) error
       Observe(context.Context) (map[string]string, error)
   }

Name 必须唯一且升级后稳定，因为持久任务通过名称查找 Driver。
Codes 只声明实际支持的操作；没有注册实现的 op 在计划阶段拒绝。
同一个 code 注册多个 Driver 时，它们全部执行，不按优先级任选一个。

每个生命周期方法的责任
----------------------

.. list-table::
   :header-rows: 1
   :widths: 24 76

   * - 方法
     - 责任
   * - Validate
     - 校验动作、oneof、字段范围和后端限制；不得修改设备。
   * - Prepare
     - 只查询旧状态，返回可持久序列化的 Undo；不能在这个阶段先做配置。
   * - Apply
     - 配置目标后端，允许执行中部分修改后返回错误；错误必须保留诊断信息。
   * - Verify
     - 从实际后端回读相关对象和字段；不能只比较生成的命令字符串。
   * - Rollback
     - 根据保存的 Undo 恢复并核验；处理部分 Apply、部分旧 Undo 和重复重试。
   * - Observe
     - 读取实际状态；错误必须返回，不能把不可读状态伪装成空且正常。

Undo 不能包含进程内指针、临时 socket 或仅在原进程有效的句柄。
删除对象前必须保存足够的旧配置；无法完整恢复的资源形态应在修改前明确拒绝。
升级 Driver 时要考虑日志中旧 Undo 的可读取性。

Linux 驱动
----------

路径为 ``internal/adapters/linux``。使用参数数组调用 iproute2 / sysctl，
读取 JSON 状态，保存旧字段与恢复命令。通用 Runner 不启动 shell，
不要为新增操作拼接可注入的 shell 字符串。

当前支持简单 bridge / VRF / VXLAN 及接口、地址、路由和绑定。
删除检查包括 master、成员和地址，避免不能完整恢复的隐式级联删除。
空 bridge 的自动全零 MAC 不能通过显式设置恢复，应恢复自动选择语义；
显式配置的非零 MAC 按原值恢复。

VXLAN 设备创建、加入 BD、加入租户和 FRR L3VNI 映射是不同操作。
不能把 FRR 的 ``vni`` 命令当成 Linux 隧道创建接口。

FRR 驱动
--------

路径为 ``internal/adapters/frr``。通过 ``vtysh -c`` 下发与回读，
Prepare 保存 running-config，Rollback 使用 frr-reload.py 恢复独占实例的配置快照。
读取配置时要按 FRR 输出语义比较，例如 route-target both 与独立 import / export。

仅检查命令退出码不足以证明成功。FRR 可能输出命令错误但返回零；
驱动还检查错误文本与配置回读。
当前已验证的批处理模式不额外发送 ``mgmt commit apply``，
更换 FRR 版本或会话模式必须重新验证。

TENANT 的删除顺序是明确例外：先让 Linux VRF inactive，再删除 FRR VRF。
一般 DELETE / UNBIND 的多驱动顺序反向于注册顺序，但不能机械套用于这个操作。

接入新数据面的步骤
----------------------------------------

先确定新增 Driver 是替代一个现有后端，还是与它共同完成操作。
例如，新数据面实现同样的 BD / VXLAN 操作时，把它追加到 Linux 后面会让两个后端都执行；
Registry 不会根据接口名、优先级或请求自动挑选一个后端。
需要替代 Linux 时，应明确修改启动时的驱动选择；需要协同时，应定义资源归属、
执行顺序、删除依赖，以及两个后端任一失败时的恢复行为。

1. **确定操作契约。** 列出支持的 OpCode、action、参数、依赖、默认值和不可恢复形态。
   复用已有操作时保持原语义；扩展参数时区分未提供与显式 false / 0。
   调用方显式提供 depends_on，驱动不额外启动绕过 Worker 的配置任务。
2. **建立驱动目录。** 在 ``internal/adapters/<backend>/`` 实现 Driver，
   为外部命令或 API 客户端设置可替换的调用接口。可参考 Linux 的 Runner 注入方式，
   为 SDK / socket 实现注入可控客户端；超时和取消应传递到后端调用。
3. **实现完整生命周期。** 按前面的接口约定实现校验、旧状态读取、配置、回读、回滚和观测。
   Prepare 返回的 Undo 需要可持久化并可在新进程中恢复；Apply 已经部分成功后仍可能返回错误。
   Rollback 必须处理部分修改、部分恢复与重复调用，并通过实际回读确认恢复成功。
4. **按需扩展协议。** 只有现有消息无法表达能力时才修改 ``api/agent/v1/agent.proto``，
   为操作码和字段使用新编号，随后按 :doc:`testing` 重新生成 Go 文件。
   同步修改调用方和能力检查，不能只更新服务端枚举就假定 Controller 会生成新操作。
5. **注册并配置启用方式。** 修改 ``cmd/sdn-agent/main.go`` 中构造 drivers 和调用
   ``backend.New(drivers...)`` 的位置，初始化新 Driver 及其依赖。
   当前 Linux 总是注册，FRR 由配置中的 frr 开关控制。
   若新增选择配置，还需同步 ``internal/config`` 的严格解析、校验、默认值和对应测试。
6. **接入状态查询。** Observe 提供 GetState 的原始观测。
   需要结构化资源查询时，另实现下面的可选 Querier 接口，并检查查询路由与返回字段。
   配置写入与查询必须指向同一实际资源来源，不能配置新数据面却返回 Linux 的状态。
7. **补齐部署与测试。** 把运行库、socket 挂载、权限、就绪条件和停止顺序写入部署说明；
   需要容器支持时同步 Dockerfile / 启动器及其测试。完成下面的单元与集成验收，
   更新 :doc:`操作目录 </user/operations>`、:doc:`protocol` 和 :doc:`testing`。

Driver.Name 会进入持久任务，升级后应保持稳定。已有未完成任务引用旧驱动时，
直接移除或改名可能使 Undo 无法执行；切换数据面前应先处理在途任务并设计日志兼容方案。
FRR 与新数据面的路由交付关系也需要单独实现和验证，注册两个 Driver 本身不会建立该通道。

扩展涉及的源码入口
----------------------------------------

.. list-table::
   :header-rows: 1
   :widths: 45 55

   * - 文件 / 目录
     - 扩展时的用途
   * - ``api/agent/v1/agent.proto``
     - 操作、参数及查询协议；变更后同步生成的 .pb.go。
   * - ``internal/backend/backend.go``
     - Driver 接口、注册、写入计划展开与能力查询。
   * - ``internal/backend/query.go``
     - 可选 Querier 接口、查询路由和资源核验。
   * - ``internal/model/model.go``
     - Undo、Step、任务记录及依赖排序；新增驱动需遵守其持久化约定。
   * - ``internal/adapters/<backend>/``
     - 新驱动、生命周期测试及独立回读样本。
   * - ``cmd/sdn-agent/main.go``、``internal/config/``
     - 启动时实例化驱动、选择后端、初始化连接及配置校验。
   * - ``docker/``、``docs/user/``
     - 依赖安装、配置模板、权限、启动与运维说明。
   * - ``internal/backend/backend_test.go``、``internal/agent/``
     - 注册能力、组合计划、任务执行、失败与重启恢复测试。

可选的结构化查询接口
----------------------------------------

``internal/backend/query.go`` 定义了独立于 Driver 的 Querier：

.. code-block:: go

   type Querier interface {
       QueryCapabilities() []*pb.QueryCapability
       Query(context.Context, *pb.QueryRequest) ([]map[string]any, error)
   }

QueryCapabilities 声明查询类型及支持的地址族 / OSPF 版本。
Query 校验并应用过滤条件，返回可转换为 Protobuf Struct 的记录；
读取失败应返回错误，不能伪装成成功的空结果。Agent 层负责排序、分页及响应封装。

当前查询路由按 Driver 名称排序，选择第一个声明对应 QueryKind 的实现；
它与写入操作的“全部注册后端执行”规则不同。
Linux TENANTS 与 FRR EVPN_VNI 的关联有专门处理，并不是通用的多后端合并器。
因此新增数据面与已有后端声明相同查询类型时，必须明确调整路由或聚合规则，
并测试 Query 的 source、过滤结果和分页行为。

扩展后的验证流程
----------------------------------------

1. 为每个支持的 code/action 添加正常与异常用例，检查非法参数、错误 oneof 和不支持的动作。
2. 用独立的后端状态样本验证 Prepare / Apply / Verify / Rollback，
   包含读取失败、部分写入、回读不符、重复回滚和 Undo 序列化后的恢复。
3. 更新能力覆盖测试，让它包含新驱动；核对 GetCapabilities、组合依赖排序和多个驱动的执行顺序。
4. 在 Agent 层验证失败屏障、幂等、进程重启和 RetryRollback；
   有 Querier 时另测查询能力、过滤、错误和多后端路由。
5. 在独占的隔离后端环境验证真实配置生效、真实失败恢复和报文转发。
   分开记录模拟测试结果与真实后端结果，不把命令生成正确当成设备验收通过。

.. code-block:: bash
   :caption: Linux 开发机：sdn-agent 项目根目录

   go test -count=1 ./internal/adapters/... ./internal/backend ./internal/agent
   make test
   make race
   go vet ./...
   make build

真实后端实验应有独立入口、资源归属和清理边界，默认单元测试不修改已有业务环境。
Driver 生命周期和测试选择示例见 :doc:`testing`，任务日志规则见 :doc:`execution-storage`。

若未来驱动变成独立进程，需要另外定义连接生命周期、能力注册、请求关联、超时、
版本和失联恢复协议。不能直接把当前函数指针映射当作已经支持远程 Adapter。
