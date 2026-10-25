第一个 CLI 事务
===============

目标是在专用实验节点创建 ``TENANT-B``、``br30``，把 BD 归属到租户，
并区分候选配置、任务结果和实际设备状态。

前提是已完成 :doc:`installation` 的 ``sdna-doc-demo`` 容器启动，并构建本地 ``bin/sdnctl``。
下列客户端命令在 Linux 宿主机的 ``sdn-agent`` 仓库根目录执行，
需要 ``jq``，且当前终端的 ``SDN_TOKEN`` 与容器一致。
所有 ``sdnctl`` 选项必须放在子命令前。

1. 查询身份与基线
-----------------

.. code-block:: bash
   :caption: CLI 客户端

   bin/sdnctl -addr 127.0.0.1:39090 -insecure capabilities
   bin/sdnctl -addr 127.0.0.1:39090 -insecure state

没有传 ``-node`` 时，CLI 先通过 GetCapabilities 发现 UUID。
初始节点应为 ``RUNNING``，Running ``revision`` 为 0，
且不存在同名租户、BD 或其他待执行事务。

2. 准备类型化操作
-----------------

将以下内容保存为客户端当前目录的 ``ops.json``：

.. code-block:: json

   {
     "operations": [
       {
         "id": "tenant",
         "code": "TENANT",
         "action": "CREATE",
         "tenant": {"name": "TENANT-B", "table_id": 1002}
       },
       {
         "id": "bd",
         "code": "BD",
         "action": "CREATE",
         "depends_on": ["tenant"],
         "bridge": {"name": "br30", "stp": false, "mtu": 1500}
       },
       {
         "id": "bind",
         "code": "BD_TENANT",
         "action": "BIND",
         "depends_on": ["bd"],
         "binding": {"bridge": "br30", "tenant": "TENANT-B"}
       },
       {
         "id": "up",
         "code": "INTERFACE",
         "action": "SET",
         "depends_on": ["bind"],
         "interface": {"name": "br30", "up": true}
       }
     ]
   }

``TENANT`` 由 Linux 与 FRR 两个驱动执行，其他三个操作由 Linux 执行。
``depends_on`` 明确执行顺序，JSON 数组本身的排列顺序不是依赖声明。

3. 建立并编辑 Candidate
-----------------------

.. code-block:: bash
   :caption: CLI 客户端

   TX_ID=$(bin/sdnctl -addr 127.0.0.1:39090 -insecure \
     -request-id demo-tenant-001 begin | jq -r .id)
   bin/sdnctl -addr 127.0.0.1:39090 -insecure \
     -transaction "$TX_ID" -file ops.json stage

Begin 自动读取当前 Running revision 作为事务基线。
Stage 应返回包含四个操作的事务，且 ``frozen`` 尚未为 true。
这时配置只进入 Candidate；以下查询不应找到 ``br30``：

.. code-block:: bash
   :caption: Linux 宿主机：只读确认 Stage 尚未修改网络

   docker exec sdna-doc-demo ip link show dev br30

这里的 ``Device does not exist`` 是预期结果。
如需放弃，此时可以运行 ``sdnctl ... -transaction "$TX_ID" abort``；
已 Commit 的事务不能使用 Abort 取消。

4. 提交并等待结果
-----------------

.. code-block:: bash
   :caption: CLI 客户端

   bin/sdnctl -addr 127.0.0.1:39090 -insecure \
     -transaction "$TX_ID" -request-id demo-tenant-001 commit

底层 Commit RPC 持久受理任务后返回，CLI 会继续订阅，直到终态或超时。
成功标志是输出 ``state: SUCCEEDED``、非空任务 ``id``，且 ``revision`` 变为 1。
默认 ProtoJSON 输出字段可能省略默认值，uint64 版本号通常显示为字符串。

保存任务 ID，后续可以查询：

.. code-block:: bash
   :caption: CLI 客户端：替换任务占位符

   TASK_ID='<上一步返回的任务ID>'
   bin/sdnctl -addr 127.0.0.1:39090 -insecure -task "$TASK_ID" task
   bin/sdnctl -addr 127.0.0.1:39090 -insecure state

5. 检查实际状态
---------------

.. code-block:: bash
   :caption: Linux 宿主机：在专用节点内查询

   docker exec sdna-doc-demo ip -d link show dev TENANT-B
   docker exec sdna-doc-demo ip -d link show dev br30
   docker exec sdna-doc-demo vtysh -c 'show running-config'

应看到 VRF 使用 table 1002、``br30`` 的 master 为 ``TENANT-B``，
FRR 配置包含 ``vrf TENANT-B``。没有连接主机的空 bridge 可能没有 carrier，
这不代表配置事务失败。此示例未配置业务地址，也不验证业务转发。

便利命令和重试
--------------

在另一个空白节点上，也可把前三个 RPC 合并：

.. code-block:: bash

   bin/sdnctl -addr 127.0.0.1:39090 -insecure \
     -request-id demo-new-workflow -file ops.json submit

已完成上面的示例后不要直接重跑这条创建命令。
``submit`` 是便捷工作流；可靠重试要保存 transaction_id、task_id 和 request_id，
查询原任务或重复原 Commit。CLI 超时不会取消已持久受理的任务。

删除同样需要事务：先用 ``BD_TENANT UNBIND`` 解除 ``br30``，
再用 ``BD DELETE`` 删除 ``br30``，最后 ``TENANT DELETE`` 删除 ``TENANT-B``，
并用 ``depends_on`` 表示上述顺序。若自行添加了成员、地址或 L3VNI，
先解除相应引用，参见 :doc:`operations`。

下一步可学习 :doc:`transactions`，或进行 :doc:`evpn-lab`。
