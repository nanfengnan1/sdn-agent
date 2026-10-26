镜像打包、Git tag 与 GitHub Release
============================================================

本页面向 sdn-agent 的版本维护者，从已提交源码制作带文档的离线包，
再发布 Git tag 和 GitHub Release。接收方安装和升级见 :doc:`/user/release`。
``v1.0`` 已发布；下文新版本示例使用 ``v1.0.1``，仅用于演示，执行前替换为计划版本。

.. contents:: 发布步骤
   :local:
   :depth: 2

先分清四种版本标记
------------------------------------------------------------

.. list-table::
   :header-rows: 1
   :widths: 22 38 40

   * - 名称
     - 用途
     - 本项目示例
   * - Git 分支
     - 持续提交代码
     - main；可选维护分支 release/1.0
   * - Git tag
     - 标记一次确定的源码提交
     - v1.0；tag 本身没有压缩包附件
   * - Docker 镜像标签
     - 标记构建出的容器镜像
     - alpine/sdn-agent:v1.0
   * - GitHub Release
     - 关联一个 Git tag，提供说明和二进制附件
     - v1.0 页面下的 sdn-agent-v1.0.tar.xz

创建 Release 可以直接使用远程已有的 tag，不需要创建或推送 release/1.0 分支。
GitHub 自动提供的 Source code ZIP / tar.gz 只包含标签对应源码；Docker 镜像包需要另外上传。
``alpine/sdn-*`` 在本流程中是本地镜像名称，不代表已推送到 Docker Hub 的 alpine 组织。

发布包内容与命名
------------------------------------------------------------

一个项目发布一个外层包，Web 的一个包可以包含多个镜像：

.. code-block:: text

   sdn-agent-v1.0.1/
   ├── README.md                 # 离线加载、配置与启动说明
   ├── load-images.sh            # 校验后加载 images/ 内的全部镜像
   ├── images/                   # docker save 输出的原始 tar
   ├── docs/html/index.html      # 完整离线手册，包括静态资源和搜索
   ├── deploy/                   # 公开的配置模板；不复制运行凭据
   ├── sources/                  # 三个配套项目的精确提交源码
   ├── SOURCE-REVISIONS.txt
   ├── manifest.json             # 版本、平台、源码提交、镜像 ID
   ├── VERIFICATION.md           # 本次实际验证结果和限制
   ├── LICENSE
   └── SHA256SUMS                # 包内文件校验

Agent 包含一个镜像 ``alpine/sdn-agent:<版本>``，内部有 Agent、sdnctl、MCP、Redis 和 FRR。

外层文件名为 ``sdn-agent-v1.0.1.tar.xz``，旁边放同名 ``.sha256``。
当前发行平台是 Linux/amd64；扩展多架构时应在附件名加入架构，并分别构建、测试和记录。
本教程的包包含手工部署模板；已发布的 v1.0 包还附带 Compose 和 init-config.py，
包括 Controller 五容器示例及 Web 分离部署示例。新增自动部署模板时也要纳入对应验证。

.. important::

   外层 tar.xz 包含文档，必须先解压，再对 ``images/*.tar`` 执行 docker load。
   Docker 能读取 xz 压缩的镜像归档，但不能把任意“文档 + 镜像”的外层包当作镜像。
   内层 tar 使用 docker save 生成；不要使用 docker export，也不要手工编写 manifest.json。
   本包根目录的 manifest.json 是交付记录，与 Docker 镜像归档内部的清单不同。

1. 准备源码并提交依赖修正
------------------------------------------------------------

发布机需要 Linux/amd64、Bash、Git、Docker/BuildKit、Python 3.10+（含 venv/pip）、
GNU tar/coreutils、xz 和 GitHub CLI。执行源码测试还需 :doc:`testing` 中的工具链。
首次构建需联网下载基础镜像、Go/npm/Alpine 依赖；使用离线包的机器不需要编译工具链。

三个源码目录保持同级，Docker 构建上下文使用它们的父目录。
首次获取源码时，在空的工作目录执行；已有仓库则先确认计划交付的兼容提交，不重复 clone：

.. code-block:: bash

   mkdir -p ~/program/github
   cd ~/program/github
   git clone https://github.com/nanfengnan1/sdn-agent.git
   git clone https://github.com/nanfengnan1/sdn-controller.git
   git clone https://github.com/nanfengnan1/sdn-web.git

Controller 的 Go 模块引用相邻 Agent；Web Gateway 引用相邻 Controller 和 Agent。
三个仓库的 tag 不必同名，但必须记录实际配套提交，不能仅记录当前项目版本。
出现 ``go.mod needs updates`` 或 ``missing go.sum entry`` 时，先在发布分支修正锁文件：

.. code-block:: bash

   cd ~/program/github
   (cd sdn-agent && go mod tidy)
   (cd sdn-controller && go mod tidy)
   (cd sdn-web/gateway && go mod tidy)

检查差异，在对应仓库运行 :doc:`testing` 中的源码测试；将有意修改的源码、锁文件、
文档和公开配置模板提交后再冻结版本。以下以本项目为例，``git add -p`` 选择已有文件的改动；
新文件需另外使用 ``git add -- 文件路径``，确认暂存内容后再提交：

.. code-block:: bash

   cd ~/program/github/sdn-agent
   git status --short
   git diff
   git add -p
   git diff --cached
   git commit -m "release: prepare v1.0.1"

没有待提交修改时跳过 commit。新版本的依赖修正应进入源码提交，不把未记录的本地补丁
混入发布镜像。不要把本机 .env、token、私钥、节点身份、数据库或实验运行目录加入 Git/包。

2. 冻结提交并创建干净构建副本
------------------------------------------------------------

以下第 2～6 步在同一个 Bash 终端顺序执行。输出目录存在时停止，避免覆盖已制作的版本。
换项目时修改 SDN_COMPONENT；版本号不会自动创建 tag 或发布附件。

.. code-block:: bash

   set -euo pipefail
   cd ~/program/github
   SDN_SOURCE_ROOT="$PWD"
   SDN_COMPONENT='sdn-agent'
   SDN_VERSION='v1.0.1'
   SDN_PLATFORM='linux/amd64'
   SDN_REPO="nanfengnan1/$SDN_COMPONENT"
   SDN_OUT="$SDN_SOURCE_ROOT/$SDN_COMPONENT/artifacts/releases/$SDN_VERSION"
   SDN_SOURCE="$SDN_OUT/work/source"
   SDN_BUNDLE="$SDN_OUT/bundles/$SDN_COMPONENT-$SDN_VERSION"
   case "$SDN_VERSION" in ''|*[!A-Za-z0-9._-]*|.|..) exit 1;; esac
   for project in sdn-agent sdn-controller sdn-web; do
     test -z "$(git -C "$SDN_SOURCE_ROOT/$project" status --porcelain)"
   done
   test ! -e "$SDN_OUT"
   mkdir -p "$SDN_SOURCE" "$SDN_BUNDLE/images" "$SDN_BUNDLE/sources" "$SDN_BUNDLE/deploy"
   SDN_COMMIT="$(git -C "$SDN_SOURCE_ROOT/$SDN_COMPONENT" rev-parse HEAD)"
   for project in sdn-agent sdn-controller sdn-web; do
     revision="$(git -C "$SDN_SOURCE_ROOT/$project" rev-parse HEAD)"
     printf '%s %s\n' "$project" "$revision" >> "$SDN_BUNDLE/SOURCE-REVISIONS.txt"
     git -C "$SDN_SOURCE_ROOT/$project" archive --format=tar.gz \
       --prefix="$project/" --output="$SDN_BUNDLE/sources/$project-$revision.tar.gz" "$revision"
     tar -xzf "$SDN_BUNDLE/sources/$project-$revision.tar.gz" -C "$SDN_SOURCE"
   done

只对已提交内容使用 git archive，避免把 node_modules、运行状态等工作目录内容打进包。
新增的本发布页和离线 README 模板也应先提交，否则上述快照不会包含它们。
若干净目录检查失败，返回第 1 步处理；不要删除别人的本地修改来通过检查。

3. 构建发布镜像
------------------------------------------------------------

.. code-block:: bash

   SDN_IMAGES=("alpine/sdn-agent:$SDN_VERSION")
   docker build --platform "$SDN_PLATFORM" \
     -f "$SDN_SOURCE/sdn-agent/docker/Dockerfile" \
     --label "org.opencontainers.image.version=$SDN_VERSION" \
     --label "org.opencontainers.image.revision=$SDN_COMMIT" \
     -t "${SDN_IMAGES[0]}" "$SDN_SOURCE"

基础镜像标签和 Alpine 软件源会更新，重建不保证得到相同镜像 ID。
需要固定基础镜像时，使用 Dockerfile 的 GO_IMAGE、ALPINE_IMAGE 等 ARG 传入已验证的 digest；
Agent 另有 REDIS_IMAGE，Controller 有 ETCD_IMAGE，Web 有 NODE_IMAGE。
同一已发布版本不重新打标为不同内容；修复后使用新版本。

Web Dockerfile 在构建 UI 时执行 pnpm test、typecheck 和 build；Go 程序构建本身不等于
全部测试通过。按 :doc:`testing` 执行适用检查，再在独立部署环境验证这些精确镜像。
不要把正在使用的 sdn-docker 实验作为发布测试时可清理的临时环境。

4. 加入文档、来源与配置模板
------------------------------------------------------------

离线 README 模板可 :download:`单独下载 <offline-package.md>`。
本模板从 manifest.json 读取镜像版本，不要求接收方安装 Go/Node。
新增功能需要同步更新该模板与用户手册。

.. code-block:: bash

   python3 -m venv "$SDN_OUT/work/docs-venv"
   "$SDN_OUT/work/docs-venv/bin/python" -m pip install \
     -r "$SDN_SOURCE/$SDN_COMPONENT/docs/requirements.txt"
   "$SDN_OUT/work/docs-venv/bin/python" -m sphinx -W --keep-going -n -b html \
     -d "$SDN_OUT/work/docs-cache" \
     "$SDN_SOURCE/$SDN_COMPONENT/docs" "$SDN_BUNDLE/docs/html"
   cp "$SDN_SOURCE/$SDN_COMPONENT/docs/developer/offline-package.md" "$SDN_BUNDLE/README.md"
   cp "$SDN_SOURCE/$SDN_COMPONENT/LICENSE" "$SDN_BUNDLE/LICENSE"
   cp "$SDN_SOURCE/sdn-agent/docker/agent.conf" "$SDN_BUNDLE/deploy/agent.conf"
   for image in "${SDN_IMAGES[@]}"; do
     image_file="${image#alpine/}"
     docker save --output "$SDN_BUNDLE/images/${image_file/:/-}.tar" "$image"
   done
   docker image inspect "${SDN_IMAGES[@]}" > "$SDN_OUT/work/images.json"
   python3 - "$SDN_BUNDLE" "$SDN_VERSION" "$SDN_COMPONENT" "$SDN_PLATFORM" \
     "$SDN_OUT/work/images.json" "${SDN_IMAGES[@]}" <<'PY'
   import datetime, json, sys
   from pathlib import Path
   root, version, component, platform, inspected, *refs = sys.argv[1:]
   root = Path(root)
   images = json.loads(Path(inspected).read_text())
   assert len(images) == len(refs)
   assert all(item['Os'] + '/' + item['Architecture'] == platform for item in images)
   manifest = {
       'format': 'sdn-offline-release', 'version': version, 'component': component,
       'platform': platform, 'built_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
       'sources': dict(line.split() for line in (root / 'SOURCE-REVISIONS.txt').read_text().splitlines()),
       'images': [{'reference': ref, 'id': item['Id'], 'size': item['Size']}
                  for ref, item in zip(refs, images)],
   }
   (root / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
   PY
   cat > "$SDN_BUNDLE/load-images.sh" <<'SH'
   #!/usr/bin/env bash
   set -euo pipefail
   cd -- "$(dirname -- "${BASH_SOURCE[0]}")"
   sha256sum --check --quiet SHA256SUMS
   for image in images/*.tar; do
     docker load --input "$image"
   done
   SH
   chmod 755 "$SDN_BUNDLE/load-images.sh"

本次实际验证记录写入 ``$SDN_BUNDLE/VERIFICATION.md``：记录命令、平台、结果、跳过项、
配套版本和仍未验证的范围。至少验证镜像启动、认证、实际服务请求、持久化挂载；
Web 还需确认页面/API 和合并/分离部署，Agent 网络功能需实际读回。
仅容器 healthy、源码测试通过或重启后能读到 Desired，不等于业务转发和整机重建恢复已通过。
填写完成后再封包；不要把历史 v1.0 的测试结果复制成新版测试结论。

5. 压缩、校验和解压试装
------------------------------------------------------------

.. code-block:: bash

   test -s "$SDN_BUNDLE/VERIFICATION.md"
   (
     cd "$SDN_BUNDLE"
     find . -type f ! -name SHA256SUMS -print0 | LC_ALL=C sort -z | \
       xargs -0 sha256sum > SHA256SUMS
     sha256sum --check --quiet SHA256SUMS
   )
   SDN_ARCHIVE="$SDN_COMPONENT-$SDN_VERSION.tar.xz"
   test ! -e "$SDN_OUT/$SDN_ARCHIVE"
   tar -C "$SDN_OUT/bundles" -cf - "$SDN_COMPONENT-$SDN_VERSION" | \
     xz -T4 -6 > "$SDN_OUT/$SDN_ARCHIVE"
   (
     cd "$SDN_OUT"
     sha256sum "$SDN_ARCHIVE" > "$SDN_ARCHIVE.sha256"
     xz --test "$SDN_ARCHIVE"
     sha256sum --check "$SDN_ARCHIVE.sha256"
   )
   SDN_CHECK="$(mktemp -d)"
   tar -xJf "$SDN_OUT/$SDN_ARCHIVE" -C "$SDN_CHECK"
   bash "$SDN_CHECK/$SDN_COMPONENT-$SDN_VERSION/load-images.sh"

上述加载会导入包中的镜像标签，应在发布验证机执行。
按包内 README 使用新的配置和独立数据目录试装，核对镜像 ID 与 manifest.json、离线手册、
认证和实际 API 请求；失败时修复并重新验证，成功后才进入发布步骤。
发给使用者的是 tar.xz 和对应 .sha256，不需要上传整个 artifacts/ 或 work/ 目录。

6. 创建并推送 Git tag
------------------------------------------------------------

下面针对尚未发布的新版本，在构建、文档和部署验证通过后执行。
示例从 main 发布；检查 main 仍是本次构建提交，避免源码已经前进却把 tag 打到另一版本。
三仓库分别管理提交和 tag；只发布本项目时不自动修改另外两个仓库的 tag。

.. code-block:: bash

   SDN_GIT="$SDN_SOURCE_ROOT/$SDN_COMPONENT"
   test "$(git -C "$SDN_GIT" rev-parse main)" = "$SDN_COMMIT"
   git -C "$SDN_GIT" tag -a "$SDN_VERSION" "$SDN_COMMIT" -m "$SDN_COMPONENT $SDN_VERSION"
   git -C "$SDN_GIT" show --no-patch "$SDN_VERSION"
   git -C "$SDN_GIT" push origin main
   git -C "$SDN_GIT" push origin "refs/tags/$SDN_VERSION"
   git -C "$SDN_GIT" ls-remote origin "refs/tags/$SDN_VERSION" "refs/tags/$SDN_VERSION^{}"

push tag 只传送版本引用及所需 Git 对象，不上传镜像包。带注释 tag 的第一行是 tag 对象，
``^{}`` 行是其指向的提交；应与本次 SDN_COMMIT 一致。
如果远程已经有正确的 v1.0 tag，就跳过创建/推送，直接为原来匹配该 tag 的包创建 Release。
不要通过删除、强制移动已发布 tag 来解决重名；新增修复版本时改用 v1.0.1 等新 tag。
推送被拒绝时先确认远程变化，不在发布教程中使用 force push。

7. 登录 GitHub 并上传 Release
------------------------------------------------------------

Git SSH 推送权限和 gh 的 GitHub API 授权分别验证。以下命令在存放发布包的 Linux 主机上执行：

.. code-block:: bash

   gh auth login --hostname github.com --web
   gh api user --jq .login

无图形浏览器时，在自己的电脑打开 ``https://github.com/login/device`` 输入终端显示的一次性码，
完成网页授权，并等待 Linux 终端提示 Authentication complete。用户名本身不是授权凭据；
不能把 token 写入发布说明、源码或压缩包。

.. code-block:: bash

   cat > "$SDN_OUT/$SDN_COMPONENT-release-notes.md" <<EOF
   # $SDN_COMPONENT $SDN_VERSION

   平台：$SDN_PLATFORM
   源码：$SDN_COMMIT；配套提交见包内 SOURCE-REVISIONS.txt。
   镜像：${SDN_IMAGES[*]}。
   附件包含镜像、使用说明、离线 HTML 手册、公开配置模板、源码和校验文件。
   下载 tar.xz 和 .sha256，先校验并解压，再执行包内 load-images.sh。
   实际验证范围与限制见 VERIFICATION.md。
   EOF

检查并补充说明中的功能变化、修复和兼容性要求，然后创建 Release：

.. code-block:: bash

   # 先检查远程 tag 已存在；查询失败就停止，不让 gh 自动创建指向其他提交的 tag。
   gh api "repos/$SDN_REPO/git/ref/tags/$SDN_VERSION" --jq .object.sha
   gh release create "$SDN_VERSION" \
     "$SDN_OUT/$SDN_ARCHIVE" "$SDN_OUT/$SDN_ARCHIVE.sha256" \
     --repo "$SDN_REPO" --title "$SDN_COMPONENT $SDN_VERSION" \
     --notes-file "$SDN_OUT/$SDN_COMPONENT-release-notes.md"

本命令创建公开 Release 并上传两个附件；现有 tag 保持不变。
新版 gh 支持 ``--verify-tag`` 时可加上该选项；上面的远程查询也适用于本次使用的 gh 2.4.0。

已有 Release 仅补传附件
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

这是补传旧版附件的独立操作路径，不要接在新版本 create 命令后执行。
如果页面已存在，使用 upload。新终端先设置真实版本及包目录：

.. code-block:: bash

   SDN_COMPONENT='sdn-agent'
   SDN_VERSION='v1.0'
   SDN_REPO="nanfengnan1/$SDN_COMPONENT"
   SDN_PACKAGE_DIR='/实际存放发布包的目录'
   gh release upload "$SDN_VERSION" \
     "$SDN_PACKAGE_DIR/$SDN_COMPONENT-$SDN_VERSION.tar.xz" \
     "$SDN_PACKAGE_DIR/$SDN_COMPONENT-$SDN_VERSION.tar.xz.sha256" \
     --repo "$SDN_REPO"

一次性上传中途失败时，先查看页面已经存在的附件，只补传缺少的那个文件。
默认不加 --clobber；同名文件存在时先校验内容，不覆盖已经提供给使用者的版本。
只有 tag 没有 Release 时，upload 会报 release not found，应使用上一节 create。

8. 验证远端附件
------------------------------------------------------------

查看版本页面和每个附件状态、大小：

.. code-block:: bash

   gh api "repos/$SDN_REPO/releases/tags/$SDN_VERSION" \
     --jq '{url: .html_url, draft: .draft, assets: [.assets[] | {name, size, state, digest}]}'

必须存在 tar.xz 与 .sha256，draft 为 false，state 为 uploaded。
旋转符号 ``⢿`` 表示请求仍在处理，不是报错；命令成功结束会输出 Release URL。
不要仅看到进度动画就重新启动一轮上传。

在第 5 步制作包的同一终端，也可以重新下载并核对本地记录，避免只验证文件名和大小：

.. code-block:: bash

   SDN_DOWNLOAD="$(mktemp -d)"
   gh release download "$SDN_VERSION" --repo "$SDN_REPO" \
     --pattern "$SDN_COMPONENT-$SDN_VERSION.tar.xz*" --dir "$SDN_DOWNLOAD"
   (
     cd "$SDN_DOWNLOAD"
     sha256sum --check "$SDN_COMPONENT-$SDN_VERSION.tar.xz.sha256"
   )
   cmp "$SDN_OUT/$SDN_COMPONENT-$SDN_VERSION.tar.xz.sha256" \
     "$SDN_DOWNLOAD/$SDN_COMPONENT-$SDN_VERSION.tar.xz.sha256"

若只是补传旧版包，最后一条 cmp 的本地目录应改为 SDN_PACKAGE_DIR。
把版本 URL、附件大小/SHA256、源码提交和实际验证记录一起保留。

v1.0 已发布记录与重建差异
------------------------------------------------------------

本项目已发布的版本见 `sdn-agent v1.0 <https://github.com/nanfengnan1/sdn-agent/releases/tag/v1.0>`_。
2026-10-26 三个包均完成上传，远端附件的大小/SHA256 与本地一致，原 Git tag 没有移动。
配套源码提交为：

.. code-block:: text

   sdn-agent       290d18d2867d0a249ebc64a2f3ef4614fd403ecd
   sdn-controller  9a9c831ce2d6eff1329a4cdf186f85f23d3db82a
   sdn-web         678983c5f645ecf19faf392c69bc7487c50d6a7d

v1.0 的 Controller/Web 锁文件没有同步 Agent 的间接依赖，构建副本中使用 go mod tidy
将 golang.org/x/sys 同步到 v0.41.0，并修正 Web 对 Agent 的直接依赖声明。
原标签未改动；原始源码、build-patches/ 和实际构建源码 sdn-v1.0-build-source.tar.gz
均在发布包 sources/ 中，重建该历史版本时先阅读其中 BUILD.md。
后续版本应把需要的修正正式提交，再按上面的干净快照流程构建。

v1.0 包含三份严格构建的离线手册，Web 的 13 项测试和类型检查、33 项 Python 测试通过；
Go 测试中 Agent 有一个包在并行构建时超时，单独复跑通过。
独立环境验证了合并/分离 Web、三节点注册、鉴权、真实 Linux 网桥创建和重启后的持久记录。
这次发布验证没有覆盖完整多主机 EVPN 转发或删除容器后的全部内核网络重建，详细范围见包内 VERIFICATION.md。
本页后续文档更新不会自动替换已发布的 v1.0 附件或其离线文档。

常见问题
------------------------------------------------------------

* **Bad credentials / HTTP 401**：在实际上传主机完成 gh auth login，并确认 gh api user 成功；
  Git SSH 可以 push 不代表 gh 已登录。
* **release already exists**：检查原页面，缺少附件时使用 upload。
* **release not found**：只有 tag 时先创建关联它的 Release；也检查仓库和版本是否写对。
* **tag already exists**：复核现有 tag 的提交；正确就复用，需要新内容就使用新版本。
* **invalid archive / 缺少 manifest.json**：先解压外层发布包，再加载 images/ 内的 tar；
  制作端应使用 docker save，无需人为制造 Docker 清单。
* **git push 不会上传 tar.xz**：压缩包通过 gh / GitHub Release 附件分发，不执行 git add artifacts/。

参考：`GitHub Release 与 tag <https://docs.github.com/en/repositories/releasing-projects-on-github/about-releases>`_、
`gh release create <https://cli.github.com/manual/gh_release_create>`_、
`gh release upload <https://cli.github.com/manual/gh_release_upload>`_、
`docker save <https://docs.docker.com/reference/cli/docker/image/save/>`_、
`docker load <https://docs.docker.com/reference/cli/docker/image/load/>`_。

