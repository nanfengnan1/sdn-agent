Spine / Leaf EVPN 实验
========================================

本实验验证 OSPF Underlay、iBGP EVPN RR，以及不同 L2VNI 之间的对称 IRB。
基础设施负责容器、veth、FRR 进程和 PC 地址，节点业务配置由 gRPC 事务完成。

拓扑与参数
----------

.. code-block:: text

   pc1                 leaf1             spine/RR             leaf2                pc2
    | eth0        host10 | uplink ---- swp1 | swp2 ---- uplink | host20        eth0 |
    +--------------------+                 |                  +--------------------+
   192.168.10.11      10.255.0.11       10.255.0.1          10.255.0.12       192.168.20.12
                        |                                     |
                    L2VNI 10010                           L2VNI 10020
                    TENANT-A -------- L3VNI 50000 -------- TENANT-A

.. list-table::
   :header-rows: 1
   :widths: 30 35 35

   * - 项目
     - Leaf1
     - Leaf2
   * - Underlay
     - 10.0.1.2/30，经 Spine 10.0.1.1
     - 10.0.2.2/30，经 Spine 10.0.2.1
   * - 业务 bridge / VXLAN
     - br10 / vx10010
     - br20 / vx10020
   * - 业务网关
     - 192.168.10.254/24
     - 192.168.20.254/24
   * - 租户
     - TENANT-A，table 1001
     - TENANT-A，table 1001
   * - L3 网络
     - br50000 / vx50000，无 SVI 地址
     - br50000 / vx50000，无 SVI 地址
   * - AS / 租户 RT
     - 65000 / 65000:1
     - 65000 / 65000:1

这是跨网段路由实验，不是两个 Leaf 共享同一个 L2VNI 的二层拉通实验。
L3-SVI 仅配置本 Leaf 的 Router MAC。

选择实验入口
------------

**Agent 独立回归环境** 使用 ``sdna-e2e-*`` 容器，入口在本仓库 ``lab/run.py``。
需要 Linux Docker、FRR 10.0.0 实验镜像和 Redis 7.4 Alpine 镜像。

.. code-block:: bash
   :caption: Linux 宿主机：sdn-agent 仓库根目录

   make lab
   python3 lab/run.py --verify-only

``make lab`` 会建立并配置专属实验；不是只读检查。
``--verify-only`` 会执行网络验证，不应在承载其他业务的同名节点上运行。
结果保存在 ``artifacts/lab-results.json`` 及相关节点任务和状态文件。

**完整 Web / Controller 环境** 使用 ``sdn-docker-*`` 容器。
其 Compose、生成的示例与手动测试步骤位于相邻仓库 ``sdn-controller/docker/``，
入口是该项目的用户指南和 ``docker/README.md``。
不要混用两个环境的容器名、token 或身份。
完整环境中的示例配置含当前节点 UUID；重建节点后应重新生成，不能沿用旧 UUID。

只读检查与业务验证
------------------

下面以已部署的 Agent 独立实验为例；完整环境将容器名前缀替换为 ``sdn-docker``，
其 sdnctl 路径为 ``sdnctl``。

.. code-block:: bash
   :caption: Linux 宿主机：查询节点实际配置

   docker exec sdna-e2e-leaf1 /workspace/bin/sdnctl -insecure state
   docker exec sdna-e2e-leaf1 vtysh -c 'show ip ospf neighbor'
   docker exec sdna-e2e-leaf1 vtysh -c 'show bgp l2vpn evpn summary'
   docker exec sdna-e2e-leaf1 vtysh -c 'show evpn vni'
   docker exec sdna-e2e-spine vtysh -c 'show bgp l2vpn evpn'

应看到 OSPF 邻接正常、BGP EVPN 邻居建立、50000 被识别为 L3VNI，
以及业务 L2VNI 对应的 Type-3 IMET。

.. code-block:: bash
   :caption: Linux 宿主机：触发本地主机学习，然后验证跨网段流量

   docker exec sdna-e2e-pc1 ping -c 1 192.168.10.254
   docker exec sdna-e2e-pc2 ping -c 1 192.168.20.254
   docker exec sdna-e2e-pc1 ping -c 3 192.168.20.12
   docker exec sdna-e2e-pc2 ping -c 3 192.168.10.11
   docker exec sdna-e2e-spine vtysh -c 'show bgp l2vpn evpn'

成功证据是两端主机 MAC/IP 的 Type-2 路由和双向 ping 通。
仅看到 Type-3 表示已知 VTEP / 广播域成员，不能证明主机已被学习。
当前没有主动邻居保活，默认实验没有发布 Type-5 网段路由覆盖静默主机；
长时间没有流量时，主机路由可能随本地学习状态变化。

实验结束后
----------

``make clean-lab`` 仅用于本仓库创建且 ownership label 匹配的 ``sdna-e2e`` 实验。
它不会代替完整环境的管理脚本，也不应手工扩大为清理全部 Docker 容器。
先保存 ``artifacts/`` 中的任务、配置、路由和连通性证据，再决定是否清理。

历史报告说明已验证的版本与场景，不保证任意 FRR 版本、宿主机重启或规模场景。
进一步了解验证边界，请阅读 :doc:`开发测试系统 </developer/testing>`。
