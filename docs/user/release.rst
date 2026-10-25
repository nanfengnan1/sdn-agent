版本发布、交付与升级
========================================

本页说明如何把验证过的源码交付为 Linux 二进制、Docker 镜像和 HTML 手册。
首次编译与安装见 :doc:`installation`，配置及启动方式见 :doc:`configuration`。
当前使用手动发布流程，没有 make release / make install 目标或自动发布流水线。
示例版本 ``v0.1.0`` 仅用于说明命名，实际发布时替换为本次版本；不表示已有公开发行版。
镜像仓库地址同样需要替换为自己的仓库。

依赖与发布前验证
----------------------------------------

.. list-table::
   :header-rows: 1
   :widths: 28 72

   * - 环节
     - 依赖
   * - 原生编译
     - Linux、Go 1.25+、GNU Make，以及 go.mod 所需依赖或模块缓存。
   * - 单元 / race 检查
     - Python 3；race 还需要 C 编译器和 CGO。普通测试无需启动 FRR / Redis。
   * - 原生运行
     - iproute2、sysctl、FRR、vtysh、frr-reload.py，以及网络管理权限；Redis 模式需另行运行 Redis。
   * - Docker 构建与运行
     - Linux Docker；镜像内安装 Agent、Redis、FRR 及运行工具。
   * - 文档
     - Python 3.10+、docs/requirements.txt 中的 Sphinx 和主题。
   * - 打包与校验
     - Git、tar、sha256sum，以及对应架构的编译 / 验证环境。

先将源码、测试、配置模板和文档纳入 Git，记录可追溯的提交并使用对应版本标签。
从该提交的干净工作目录构建，交付包不得包含真实 token、证书私钥、节点身份或数据库卷。
依赖下载与文档虚拟环境准备见 :doc:`/developer/testing` 和 :doc:`/developer/documentation`。

.. code-block:: bash
   :caption: Linux 发布机：sdn-agent 项目根目录，已准备 Go 和文档虚拟环境

   make test
   make race
   go vet ./...
   make build
   make -C docs html

发布记录应包含源码提交、操作系统 / 架构、Go / FRR / Redis 版本、镜像摘要、测试命令及结果。
涉及驱动、FRR / Redis 版本或恢复行为变化时，还需在专属隔离环境完成对应集成检查。
普通单元测试通过不能写成实际转发验收通过；默认跳过的真实 FRR 测试需明确列出。
``make regression`` 会重建实验，使用前先阅读 :doc:`/developer/testing`。

交付 Linux 二进制
----------------------------------------

以下示例在与目标相同架构的 Linux 发布机上执行，创建一个新的版本目录。
已有同名目录时命令停止，应核对原发布内容；不覆盖旧版本。
原生二进制包包含 Agent 和 CLI，运行时依赖仍需按 :doc:`installation` 安装。
mock-controller 是开发工具，不作为正式 Controller 交付。

.. code-block:: bash
   :caption: Bash：sdn-agent 项目根目录；前述检查已经通过

   set -eu
   SDNA_VERSION='v0.1.0'
   SDNA_COMMIT="$(git rev-parse HEAD)"
   test -z "$(git status --porcelain)"
   test "$(go env GOOS)" = linux
   SDNA_ARCH="$(go env GOARCH)"
   SDNA_PACKAGE="sdn-agent-${SDNA_VERSION}-linux-${SDNA_ARCH}"
   SDNA_RELEASE_DIR="$PWD/artifacts/releases/$SDNA_VERSION"
   test ! -e "$SDNA_RELEASE_DIR"
   mkdir -p "$SDNA_RELEASE_DIR/$SDNA_PACKAGE/bin"

   CGO_ENABLED=0 go build -trimpath -o "$SDNA_RELEASE_DIR/$SDNA_PACKAGE/bin/sdn-agent" ./cmd/sdn-agent
   CGO_ENABLED=0 go build -trimpath -o "$SDNA_RELEASE_DIR/$SDNA_PACKAGE/bin/sdnctl" ./cmd/sdnctl
   "$SDNA_RELEASE_DIR/$SDNA_PACKAGE/bin/sdn-agent" -h
   "$SDNA_RELEASE_DIR/$SDNA_PACKAGE/bin/sdnctl" -h
   cp README.md "$SDNA_RELEASE_DIR/$SDNA_PACKAGE/"
   printf '%s\n' "version=$SDNA_VERSION" "commit=$SDNA_COMMIT" "platform=linux/$SDNA_ARCH" > "$SDNA_RELEASE_DIR/$SDNA_PACKAGE/RELEASE.txt"
   go version -m "$SDNA_RELEASE_DIR/$SDNA_PACKAGE/bin/sdn-agent" > "$SDNA_RELEASE_DIR/$SDNA_PACKAGE/BUILD-INFO.txt"
   tar -C "$SDNA_RELEASE_DIR" -czf "$SDNA_RELEASE_DIR/$SDNA_PACKAGE.tar.gz" "$SDNA_PACKAGE"
   (cd "$SDNA_RELEASE_DIR" && sha256sum "$SDNA_PACKAGE.tar.gz" > SHA256SUMS)

把 tar.gz、SHA256SUMS 和同版本 HTML 手册交付给部署方，另附测试与兼容性记录。
项目尚未提供内置版本查询参数，源码提交和 ``go version -m`` 记录用于追溯构建。
其他架构需要独立构建和验证，不能只修改压缩包文件名。

.. code-block:: bash
   :caption: Linux 接收机：交付文件所在目录，版本和架构替换为实际值

   sha256sum -c SHA256SUMS
   tar -xzf sdn-agent-v0.1.0-linux-amd64.tar.gz
   ./sdn-agent-v0.1.0-linux-amd64/bin/sdn-agent -h
   ./sdn-agent-v0.1.0-linux-amd64/bin/sdnctl -h

校验通过后，按 :ref:`agent-source-install` 的安装步骤把包内两个程序安装到
``/usr/local/bin``，配置身份目录、令牌和 FRR，再进行 capabilities / state 检查。
从二进制包安装时可跳过源码编译步骤。

发布与传递 Docker 镜像
----------------------------------------

需要已验证的源码及镜像仓库的推送权限。以下命令会推送到指定仓库，
实际发布时才执行；文档构建和单元测试均不会执行这些命令。
Docker 构建上下文仍是 sdn-agent 的父目录。

.. code-block:: bash
   :caption: Bash：源码父目录，例如 ~/program/github

   set -eu
   SDNA_VERSION='v0.1.0'
   SDNA_REGISTRY='registry.example.com'
   SDNA_IMAGE="$SDNA_REGISTRY/network/sdn-agent:$SDNA_VERSION"
   docker build -f sdn-agent/docker/Dockerfile -t "$SDNA_IMAGE" .
   docker run --rm "$SDNA_IMAGE" sdn-agent -h
   docker run --rm "$SDNA_IMAGE" sdnctl -h
   docker login "$SDNA_REGISTRY"
   docker push "$SDNA_IMAGE"
   docker image inspect "$SDNA_IMAGE" --format '{{json .RepoDigests}}'

上述 -h 只检查程序入口；发布前还需验证配置、服务健康和对应业务场景。
记录推送后得到的仓库 digest，接收方通过 ``docker pull`` 获取镜像，并在部署配置中
使用该版本或已记录的 ``仓库路径@sha256:摘要``。同一版本标签不重复指向不同内容。
构建时可通过 GO_IMAGE、ALPINE_IMAGE、REDIS_IMAGE 指定已验证的基础镜像 digest；
Alpine 包下载仍会影响 FRR 等依赖，需记录实际包版本，不能宣称仅固定标签就可逐字节复现。

离线环境使用镜像归档。在同一个构建终端执行，沿用上述 SDNA_IMAGE 和 SDNA_VERSION：

.. code-block:: bash

   SDNA_IMAGE_ARCH="$(docker image inspect "$SDNA_IMAGE" --format '{{.Architecture}}')"
   SDNA_IMAGE_TAR="sdn-agent-${SDNA_VERSION}-${SDNA_IMAGE_ARCH}.image.tar"
   docker save -o "$SDNA_IMAGE_TAR" "$SDNA_IMAGE"
   sha256sum "$SDNA_IMAGE_TAR" > "$SDNA_IMAGE_TAR.sha256"

.. code-block:: bash
   :caption: Linux 接收机：镜像归档所在目录，替换实际版本和架构

   sha256sum -c sdn-agent-v0.1.0-amd64.image.tar.sha256
   docker load -i sdn-agent-v0.1.0-amd64.image.tar

镜像载入后按 :ref:`agent-docker-deploy` 部署，把示例中的 ``sdn-agent:alpine``
替换为交付镜像引用，按 :doc:`configuration` 挂载实际配置和专属持久卷。
Agent 镜像只包含本节点服务，完整系统还需分别交付 Controller 和 Web。

发布与预览 HTML 手册
----------------------------------------

在源码根目录准备文档依赖并生成 ``docs/html``，步骤见 :doc:`/developer/documentation`。
交付整个 html 目录，包含搜索、样式和脚本，不单独复制 index.html。

.. code-block:: bash
   :caption: Bash：sdn-agent 项目根目录，已激活文档虚拟环境

   set -eu
   SDNA_VERSION='v0.1.0'
   make -C docs html
   mkdir -p artifacts/releases
   SDNA_DOC_ARCHIVE="sdn-agent-docs-${SDNA_VERSION}.tar.gz"
   test ! -e "artifacts/releases/$SDNA_DOC_ARCHIVE"
   tar -C docs -czf "artifacts/releases/$SDNA_DOC_ARCHIVE" html
   (cd artifacts/releases && sha256sum "$SDNA_DOC_ARCHIVE" > "$SDNA_DOC_ARCHIVE.sha256")

接收方校验对应 sha256 文件后解压，再把完整 html 目录放到静态站点目录。
站点地址和认证由部署方配置。发布者可先在本机预览：

.. code-block:: bash
   :caption: 文档预览机：sdn-agent 项目根目录

   python3 -m http.server 8000 --bind 127.0.0.1 --directory docs/html

在同一台机器的浏览器打开 ``http://127.0.0.1:8000/``，检查用户 / 开发手册、表格、
代码块和搜索。此命令只提供文档预览，不启动 Agent；Ctrl+C 停止预览服务。

升级与回退
----------------------------------------

原生进程升级保留网络命名空间；容器替换可能重建网络命名空间，恢复边界见 :doc:`transactions`。
当前没有自动迁移工具、完整网络重建器或已验收的通用版本降级方案。

1. 保存旧版本程序或镜像 digest、新旧版本的源码及测试记录。
   确认新旧版本对协议、配置、Redis / 文件日志和历史 Undo 的兼容性。
2. 暂停该节点的新配置提交，查询并处理在途任务和 ROLLBACK_FAILED；
   记录 node_id、Running revision、关键 Task ID 以及实际网络状态。
3. 对实际部署创建一致备份：身份 / 状态目录、配置 / TLS、FRR 配置，
   Redis 模式另含 Redis 数据。停写或停止相关进程后按部署方式备份，
   不把在线文件的随意复制描述为一致性备份。
4. 原生部署停止 Agent 服务，按安装说明替换两个二进制，再用原身份、配置及存储启动。
   Docker 部署用交付镜像更新原部署定义并保留原卷、节点标识、端口和配置挂载；
   容器重建前应准备业务网络重建方案，不能仅凭旧 Running 判断 Linux 对象仍存在。
5. 检查配置预检、进程健康、capabilities / state、Controller 注册、旧任务查询，
   再验证 FRR 邻居和业务连通，最后恢复新配置提交。
6. 需要回退时先停止新版本，只有在旧程序能读取当前数据且回退路径已验证时才使用旧程序 / 镜像。
   若新版修改了存储或实际网络，必须协调对应恢复步骤；单独恢复旧数据库可能造成状态与设备不一致。

升级和回退的实际结果应单独记录。文档构建通过不代表已完成镜像推送、目标部署或升级演练。
