# sdn-agent MCP

`sdn-agent-mcp` 让支持 MCP 的 AI 客户端查询 Agent 能力、实际网络状态和任务，或在显式启用写模式后提交配置事务。代码位于本目录，与 Agent 共用根目录的 Go 模块；它是通过 gRPC 调用既有 Agent 的独立进程，不直接执行 Linux/FRR 命令。

当前使用官方 [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk)，提供 **stdio** 传输。AI 客户端负责启动进程并收发 MCP 消息；没有 HTTP 监听端口。默认只注册 6 个只读工具，增加 `-allow-writes` 后提供全部 12 个工具。

## 构建与运行

在仓库根目录执行，Go 版本与根目录 `go.mod` 一致：

```bash
go mod download
make build-mcp
./bin/sdn-agent-mcp -h
sudo install -m 0755 bin/sdn-agent-mcp /usr/local/bin/sdn-agent-mcp
```

`make build` 也会构建此程序。先按 Agent 安装手册准备可访问的 Agent，使用同一 `SDN_TOKEN`（至少 16 字节）。令牌由启动 MCP 子进程的客户端环境传入，不放入工具参数、URL 或模型提示词。

下面是常见的客户端配置形状；具体配置文件位置和环境继承方式以客户端为准：

```json
{
  "mcpServers": {
    "sdn-agent": {
      "command": "/usr/local/bin/sdn-agent-mcp",
      "args": ["-addr", "leaf1.example:9090", "-ca", "/etc/sdn-client/ca.pem"]
    }
  }
}
```

客户端需将既有 `SDN_TOKEN` 传给子进程。隔离实验可将 `-ca` 参数替换为 `-insecure`；准备写入独立实验节点时才增加 `-allow-writes`。工具不能更换目标地址、凭据或节点 UUID；`-node` 可固定预期节点身份。

新构建的 Agent 镜像包含 MCP 程序，也可让客户端以 `docker` 为 command、以下数组为 args：

```json
["exec", "-i", "<目标Agent容器名>", "sdn-agent-mcp", "-addr", "127.0.0.1:9090", "-insecure"]
```

替换容器名，不使用 `-t`。进程继承容器环境中的 `SDN_TOKEN`；如果凭据仅写在 Agent 配置文件中，需由部署方另行向 MCP 进程提供同值环境变量。旧镜像需要重新构建才会包含新程序。

## 工具与事务

| 模式 | 工具 |
|---|---|
| 默认只读 | `sdn_capabilities`、`sdn_state`、`sdn_query`、`sdn_verify`、`sdn_task`、`sdn_watch` |
| `-allow-writes` 后增加 | `sdn_begin`、`sdn_stage`、`sdn_commit`、`sdn_abort`、`sdn_retry_rollback`、`sdn_submit` |

参数和结果使用 `snake_case`。所有 64 位 revision/sequence 使用十进制字符串；Operation 是结构化对象，保留 optional 字段的未设置、`false` 和 `0` 区别。先用 `tools/list` 查看 schema，再调用工具。

写操作沿用 Begin → Stage → Commit 和 Agent 的版本、幂等、Undo 与读回机制。Commit 返回任务受理结果，使用 task/watch 判断终态；`ACCEPTED` 不是配置成功，`SUCCEEDED` 也不等于业务流量已验证。超时或回复丢失后保留原 request ID、事务和任务 ID，不能用新请求键盲目重复提交。

已由 Controller 纳管的节点应通过 Controller/Web 修改配置。MCP 直接写 Agent 不同步 Controller 的 Desired/Applied，可能造成 revision 漂移；默认只读模式适合此类节点的状态检查。

## 手册与验证

- [用户接入、参数、工具示例和恢复](../docs/user/mcp.rst)
- [设计、源码地图和扩展方法](../docs/developer/mcp.rst)
- [Agent 事务与恢复](../docs/user/transactions.rst)

```bash
go test ./mcp/...
make test-mcp
go vet ./mcp/...
make -C docs html
```

这些命令验证 MCP 协议适配与代码；真实 Agent、Linux/FRR 读回、协议收敛和业务转发需要专用环境验证。MCP 客户端配置示例不代表已经在每一种 AI 产品中实测。

`make test-mcp` 使用 race 检查，需要 C 编译器与启用 CGO。stdio 单条输入消息上限为 4 MiB；超限时会话退出，应缩小操作集合并重新连接，不要在结果未知时更换提交请求键。
