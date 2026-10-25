# 实测验收记录

日期：2026-10-24。最终版本执行 `make regression`，全部阶段通过。

本文是历史验收记录，地址和路径保留当时环境作为证据，不是新部署的默认参数。新部署使用实际部署服务器 IP，并按 [当前安装手册](user/installation.rst) 操作。

## 环境

- 宿主机：`10.8.126.103`，Linux / Docker。
- 项目：`/home/alexan/program/github/sdn-agent`；Windows 共享路径 `X:\program\github\sdn-agent`。
- 实测编译器：Go 1.27.3 linux/amd64；go.mod 声明最低 Go 1.25。
- FRR：`quay.io/frrouting/frr:10.0.0`，CLI 显示 `10.0_git`。
- FRR 镜像 digest：`sha256:572c60d3d678887bbd97281e7995a976f7eceeb7f40bf43a13cbd7905d52af41`。
- Redis：7.4.11，AOF 开启，appendfsync always。
- 新建并保留：`sdna-e2e-spine`、`sdna-e2e-leaf1`、`sdna-e2e-leaf2`、`sdna-e2e-pc1`、`sdna-e2e-pc2`、`sdna-e2e-controller`、`sdna-e2e-redis`、`sdna-e2e-helper`。

## 最终结果

| 阶段 | 结果 | 证据 |
|---|---|---|
| 自动测试 | 14 个顶层 Test 全部通过，包含额外子用例 | [unit 日志](../artifacts/regression-unit.log) |
| race | 全部通过，无检测到的数据竞争 | [race 日志](../artifacts/regression-race.log) |
| go vet | 通过 | [vet 日志](../artifacts/regression-vet.log) |
| 三个可执行文件构建 | 通过 | [build 日志](../artifacts/regression-build.log) |
| 全新 Docker EVPN 实验 | 14 项检查通过 | [实验日志](../artifacts/regression-evpn.log) |

机器可读结果：[各阶段退出码](../artifacts/regression-results.json)、[实验检查列表](../artifacts/lab-results.json)。这些 artifacts 是本次本地执行产物，重新运行会覆盖；不作为源码提交。

真实实验的 14 项检查：

1. Spine 与两个 Leaf 的 EVPN BGP 会话 Established。
2. 两个接入网络通过 PC ping 网关产生 ARP 学习。
3. PC1 → PC2：L2VNI 10010 / L3VNI 50000 / L2VNI 10020 跨网段通信。
4. PC2 → PC1 反向通信。
5. Spine 接收到两端主机 Type-2 MAC/IP 与两个 VTEP 的 Type-3 IMET。
6. Zebra 将 50000 识别为 L3VNI。
7. 创建 bridge 后注入下一步失败，bridge 被删除，Running revision 不变。
8. 修改已有 uplink MTU 后注入失败，恢复旧 MTU 9000。
9. 修改 FRR RD 后注入失败，恢复原 RD 并通过配置回读。
10. 独立测试租户、BD、VXLAN 的创建、绑定、解绑和依赖顺序删除。
11. 已完成配置后 SIGKILL 并重启 Agent，UUID 与 Running revision 保持，instance_id 更新。
12. 重启后仍能查询原成功任务。
13. 停止 mock-controller 后，Agent 变为 DOWN，业务转发保持。
14. 重启 mock-controller 后身份保持，Agent 恢复 RUNNING。

进行中的事务崩溃恢复由确定性日志故障测试覆盖；真实 SIGKILL 实验覆盖的是已提交状态/任务的进程重启恢复，两者不混为同一种测试。

Spine 实际出现的主机 NLRI 包括：

```text
RD 10.255.0.11:10
 [2]:[0]:[48]:[02:00:00:10:00:11]:[32]:[192.168.10.11]
 [3]:[0]:[32]:[10.255.0.11]
RD 10.255.0.12:20
 [2]:[0]:[48]:[02:00:00:20:00:12]:[32]:[192.168.20.12]
 [3]:[0]:[32]:[10.255.0.12]
```

完整证据：[Spine EVPN 表](../artifacts/spine-evpn.txt)、[Leaf1 VNI 分类](../artifacts/leaf1-vni.txt)、[Leaf1 状态](../artifacts/leaf1-state.json)。

## 在当前服务器复现

```bash
cd ~/program/github/sdn-agent
export PATH="$HOME/.cache/sdn-agent-tools/go/bin:$HOME/.cache/sdn-agent-tools/bin:$PATH"
make regression
```

此命令重建本项目自己的实验容器。若只检查当前保留环境，使用 `python3 lab/run.py --verify-only`；它也会运行失败注入、测试资源创建删除、Agent 重启与 Controller 断连测试。只查看数据用 README 中的 state / vtysh 命令。

范围：当前交付是 Linux + FRR 的实验可用版本。FRR 回滚要求独占实例；Controller 为模拟器；尚无 VPP/交换机、FPM 接收器、主动 ARP/ND 保活和全网拓扑聚合。没有把实验成功等同于生产高可用或所有 FRR/内核版本兼容。
