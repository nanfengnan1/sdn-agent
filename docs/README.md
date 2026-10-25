# sdn-agent 文档系统

用户手册与开发手册使用 Sphinx，目录与阅读样式参考相邻 `route-ovs/docs`。每个项目的构建配置、模板、静态资源和内容均独立维护，构建不依赖 route-ovs 或其他项目的文档目录。

- [用户指南源文件](user/index.rst)
- [开发指南源文件](developer/index.rst)
- 现有 `.md` 设计记录、协议说明和验收报告保留在此目录。

这里的 `html` 命令只生成手册，不编译或安装 Agent。部署软件请先读 [源码编译、安装与部署](user/installation.rst)，生成后可从 `html/user/installation.html` 直接进入；包含源码安装和 Docker 部署两条完整路径。

软件版本打包、镜像分发、HTML 手册交付和升级回退见 [版本发布、交付与升级](user/release.rst)。

## 查看帮助

在 `docs/` 目录执行 `make help`，或在项目根目录执行 `make -C docs help`。
帮助列出构建、清理、依赖准备和输出位置，无需先安装 Sphinx。直接执行 `make` 仍默认构建 HTML。

## 构建 HTML

Linux/macOS，Python 3.10+、GNU Make。在项目根目录运行：

```bash
python3 -m venv .venv-docs
. .venv-docs/bin/activate
python -m pip install -r docs/requirements.txt
make -C docs html
```

Windows 可使用 Python，省去 Make：

```powershell
py -m venv .venv-docs
.venv-docs\Scripts\python.exe -m pip install -r docs/requirements.txt
.venv-docs\Scripts\python.exe -m sphinx -W --keep-going -n -a -b html docs docs/html
```

打开 `docs/html/index.html`。两类手册分别在 `docs/html/user/index.html` 和 `docs/html/developer/index.html`。发布时复制整个 `html/`，不能只复制单个页面。搜索、样式和脚本均使用本地静态资源。

## 清理

```bash
make -C docs clean
```

Windows 等价命令：

```powershell
py docs/_scripts/clean.py
```

只删除本项目文档构建产物 `docs/html/` 和 `docs/_build/`；不会清除源码、图片、Markdown 文档、应用数据、容器或虚拟环境。脚本拒绝清理指向外部路径的符号链接。

## 编辑规则

新增页面放入 `user/` 或 `developer/`，并加入所属 `index.rst` 的 toctree。页面内链接使用 Sphinx 的 `:doc:` 或 `:ref:`，截图放在 `_static/images/`。代码示例写明运行位置，令牌使用占位符。

`html` 构建默认开启严格引用检查，任何 Sphinx 警告都会使命令失败。此文档系统提供 `help`、`html`、`clean` 三个 Make 目标，不包含 PDF 或版本归档。
