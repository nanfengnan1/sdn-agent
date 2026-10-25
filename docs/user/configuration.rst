agent.conf 与容器内服务
============================================================

每个 Agent 容器运行自己的 Agent、Redis 和 FRR。Redis 不再需要单独的服务容器；
它只监听容器内的 loopback 地址。``agent.conf`` 同时描述 Agent 参数与内置 Redis 参数。

配置文件格式与优先级
------------------------------------------------------------

文件名使用 ``.conf``，内容是严格 JSON。当前顶层仅接受 ``agent``、``redis``；
未知字段、重复键、null、错误类型、超出范围的端口和不匹配的内置 Redis 地址都会拒绝启动。
不支持注释、环境变量模板或任意 shell 命令。

优先级是显式命令行选项覆盖文件，文件覆盖程序默认值。
令牌使用 ``SDN_TOKEN`` 环境变量覆盖 ``agent.token``；显式空环境变量也会覆盖文件并导致校验失败。
没有 ``-config`` 的原生二进制保留原 CLI 行为。
容器启动器默认加载 ``/etc/sdn-agent/agent.conf``，并允许传入其他 ``-config`` 路径。

.. code-block:: json

   {
     "agent": {
       "listen": ":9090",
       "endpoint": "leaf1:9090",
       "controller": "controller:19090",
       "stateDir": "/var/lib/sdn-agent",
       "redis": "127.0.0.1:6379",
       "frr": true,
       "token": "<替换为Controller配置的Agent令牌>",
       "insecure": true
     },
     "redis": {
       "enabled": true,
       "bind": "127.0.0.1",
       "port": 6379,
       "dataDir": "/var/lib/redis",
       "appendOnly": true,
       "appendFsync": "always",
       "startupTimeoutSeconds": 30,
       "shutdownTimeoutSeconds": 30
     }
   }

这里的 controller / leaf1 是同一 Compose 管理网的服务名。分机部署时改成双方实际可达的管理地址，
不要把浏览器 Web IP、VTEP 地址和 gRPC endpoint 混用。
独立 Agent 将 controller 留空；endpoint 留空时取 listen 的值。

``insecure: true`` 是随镜像提供的隔离实验配置，原生程序默认仍要求 TLS。
正式 TLS 设置使用 ``agent.cert``、``agent.key``、``agent.ca``，并把 insecure 设为 false。
配置里的路径都是容器内路径，相应证书需要另外只读挂载。FRR 路由配置仍由事务操作和
``/etc/frr`` 管理，不把 BGP 配置文本塞进 agent.conf。

从容器外挂载覆盖
------------------------------------------------------------

在有源码和 Docker 的 Linux 部署机执行。下面启动的是一个新的独立测试节点：

.. code-block:: bash

   cd ~/program/github/sdn-agent
   install -m 0600 docker/agent.conf ./agent.conf
   # 编辑 agent.conf；独立测试保留 controller 和 endpoint 为空。
   export SDN_TOKEN='<替换为至少16字节的实际令牌>'
   docker run -d --name sdna-doc-demo --privileged --stop-timeout 240 \
     -p 127.0.0.1:39090:9090 -e SDN_TOKEN \
     --mount type=bind,src="$(pwd)/agent.conf",dst=/etc/sdn-agent/agent.conf,readonly \
     -v sdna-doc-demo-state:/var/lib/sdn-agent \
     -v sdna-doc-demo-redis:/var/lib/redis \
     -v sdna-doc-demo-frr:/etc/frr \
     sdn-agent:alpine

也可在文件中填写 token 并省略环境变量；文件应按密钥保护，不进入仓库或镜像。
镜像模板没有真实令牌。挂载整个有效文件会替换镜像中的模板；缺省字段使用程序默认值，
不会把缺失字段从被覆盖的镜像模板中补回来，因此推荐先复制提供的模板再修改。

修改文件后需重新启动进程或容器才生效，没有配置热加载。
重启容器会重建网络命名空间，持久卷不能自动恢复已丢失的 Linux 网络对象；
对有业务的节点先按 :doc:`transactions` 确认恢复方案。

验证而不启动服务
------------------------------------------------------------

.. code-block:: bash

   docker run --rm -e SDN_TOKEN \
     --mount type=bind,src="$(pwd)/agent.conf",dst=/etc/sdn-agent/agent.conf,readonly \
     sdn-agent:alpine -check-config

命令校验配置并显示不含 token 的有效参数，不启动 Redis、FRR 或 Agent，不创建运行状态目录。
原生二进制可执行 ``sdn-agent -config ./agent.conf -check-config``。
``-h`` 同样不会启动内置服务。

若覆盖 ``-listen :19091``，Docker 的端口映射也要指向 19091；健康检查使用合并后的端口，
不会固定探测 9090。内置 Redis 换端口时须同时修改 agent.redis 与 redis.port，
或省略 agent.redis 让它由内置 Redis 设置推导。

存储和生命周期
------------------------------------------------------------

.. list-table:: 持久化位置
   :header-rows: 1

   * - 路径
     - 保存内容
   * - /var/lib/sdn-agent
     - node UUID、注册身份、进程锁；文件模式时还包含任务日志
   * - /var/lib/redis
     - 内置 Redis 的 AOF/RDB，保存本节点 Candidate/Running/Operational/Task
   * - /etc/frr
     - FRR 配置

首次启动仍生成 UUID 并与 Controller 协商，最终身份写入 identity.json。
agent.conf 不包含手填 node-id；网络操作请求中的 nodeId 使用注册后的 UUID。
不要把同一身份目录挂给多个并行 Agent。

内置 Redis 使用与原实验相同的 7.4 系列服务二进制。迁移旧独立 Redis 卷时，
把原 /data 卷挂到配置的 dataDir；启动器保留文件，只调整 Redis 用户访问权限。
迁移前仍需停止旧实例并备份，不能让两份 Redis 同时打开一个数据目录。
dataDir 必须是 Redis 专用绝对目录，不能与 Agent 状态目录重叠或指向系统目录。
启动器拒绝符号链接、无关文件和子目录；不会对用户指定的任意目录递归修改所有权。

启动顺序为配置校验、Redis 就绪、FRR 就绪、Agent 监听；任何必需进程退出会使整个容器失败退出。
停止时先给 Agent 最多 120 秒完成退出/补偿，再停止 FRR 和 Redis；默认 Docker 示例使用
240 秒 stop timeout。若增加 Redis shutdownTimeoutSeconds，应相应增加容器停止预算。
健康检查验证 Agent 监听、内置 Redis PING 和 FRR 进程状态。

默认 appendOnly=true、appendFsync=always。选择 everysec/no 或关闭 AOF 会降低断电后的日志保证，
不能把它当作与默认设置等价的持久化。Redis 密码/TLS 接入当前未实现，内置 bind 只接受 loopback IP。

原生二进制本身不拉起 Redis/FRR 子进程，容器启动器承担这部分管理。
保留的开发兼容模式可以把 redis.enabled 设为 false，并设置 agent.redis 连接已有 Redis，
或将 agent.redis 清空使用文件模式；切换存储不会迁移已有日志。
