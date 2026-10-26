# 测试系统

## 2026-10-25 独立资源与 IPv6 扩展

- `go test ./...`、`go test -race ./...`、`go vet ./...` 通过。
- Docker supervisor 的 8 个 Python 单元测试通过。
- 独占 FRR 10.2.2 容器验证新增 6 类 op 的 Apply、实际 readback、snapshot rollback：OSPF6_INSTANCE、OSPF6_INTERFACE、BGP_AF_PEER、BGP_NETWORK、BGP_REDISTRIBUTE、EVPN_TYPE5。
- 实际验证 BGP IPv6、EVPN route/VNI 及 OSPFv3 summary/interface/neighbor/database/route JSON 查询。真实 JSON 已保存到 `internal/adapters/frr/testdata/`。
- 查询测试覆盖 VRF/BD/端口实际关联、未绑定对象、IPv6 路由/邻居、Type-5 IPv6 prefix、错误与空表区分、分页数据改变和只读资源验证屏障。
- 真实 Linux 6.8 接口 fixture 验证桥 hello_timer/gc_timer 等变化不使分页失败或跨页重复，配置 ageing_time、接口新增、路由撤销及下一跳变化仍使旧令牌失效。
- 双栈冷启动故障已定位为 IPv6 业务 SVI 缺少链路本地地址。仅切换已 UP 接口的 addrgenmode 不会补生成地址；Controller 显式补 LL 后，删除两个指定主机全局邻居，双向首包均成功，NS/NA 抓包证据见 `artifacts/ipv6-cold-start/`。
- 临时容器 `sdn-agent-feature-probe` 已删除；没有修改现有 `sdn-docker-*` 或 `sevpn-*` 容器。

本节为 Agent 单节点及适配器结果。Controller/Web 多节点端到端结果由总体集成报告记录。

`make regression` 顺序执行全部自动测试、race、vet、构建、清理本项目旧实验、从空环境创建并测试新实验。命令日志和退出码分别保存为 `artifacts/regression-*.log` 和 `artifacts/regression-results.json`；失败立即停止。

## 单元和 gRPC 测试

```bash
go test -count=1 -v ./...
go test -race -count=1 ./...
go vet ./...
go test -coverprofile=coverage.out ./...
```

覆盖多后端全部执行、依赖顺序、Apply 部分失败、Verify 失败、旧值恢复、回滚失败屏障、revision 冲突、commit 重试、node_id 校验、依赖循环、错误 oneof、进程重启补偿、gRPC 鉴权与事件游标重放、注册冲突/重试、过期 instance，以及真实文件持久化后的 oneof 还原。

测试替身用于确定性故障注入；真实网络行为由独立 Docker 实验验证，不能用 fake 测试结果替代转发验证。

## 实际实验

```bash
make lab
python3 lab/run.py --verify-only
```

实验使用 Linux bridge / VRF / VXLAN、FRR 10.0.0、OSPF Underlay、iBGP EVPN RR、不同 L2VNI 的对称 IRB。测试通过真实 gRPC 客户端配置 Spine / Leaf1 / Leaf2，随后检查 BGP 邻居、Type-2 主机 MAC/IP、Type-3 IMET、L3VNI 分类、PC1/PC2 双向跨网段通信、注入失败后的 Linux/FRR 恢复、租户/BD/VXLAN 删除、Agent SIGKILL 后身份/日志恢复，以及 Controller 断开/重连。纯 Type-2 测试先让两端 PC ping 本地网关，确保静默主机产生本地 ARP 学习。

测试结果与证据写入 `artifacts/`：每节点任务结果、Running config、GetState JSON、Spine EVPN 表、Leaf VNI 表和 `lab-results.json`。失败保留容器供诊断；不要直接修改实验 FRR 后再宣称是 Agent 自动配置成功，应修复适配器后重建实验复测。

清理：`python3 lab/run.py --cleanup`，只移除 `sdn-agent.lab=sdna-e2e` 的容器和同标签管理网络。保留日志/身份/AOF 供检查；再次 setup 会使用新的运行目录和数据库目录。

实验文件共享挂载、特权容器和明文 gRPC 只用于隔离开发网络。没有把宿主机密码写入项目；实验 token 随机生成，保存在被忽略的运行目录中。

## 新增适配器

实现 `backend.Driver` 的 Name/Codes/Validate/Prepare/Apply/Verify/Rollback/Observe，在启动代码中注册。Prepare 必须只查询，Undo 必须能持久序列化；Apply 与 Rollback 需要处理部分执行和重试；Verify 必须读取真实后端状态。给该后端添加实际系统集成测试，不能只验证命令字符串。
