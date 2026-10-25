维护与构建文档
==============

本项目使用独立 Sphinx 文档系统，结构与阅读样式参考 route-ovs，
但构建不依赖相邻仓库。提供 help、html、clean 三个 Make 目标，不生成 PDF 或版本归档。

目录约定
--------

.. code-block:: text

   docs/
   |-- conf.py
   |-- Makefile              # help / html / clean
   |-- requirements.txt
   |-- index.rst
   |-- user/                 # 使用、部署、运维
   |   +-- index.rst
   |-- developer/            # 设计、协议、扩展、测试
   |   +-- index.rst
   |-- _static/              # 样式、脚本、images/
   |-- _templates/
   |-- _scripts/clean.py
   +-- html/                 # 生成物，不手工编辑

原有 Markdown 设计记录和报告继续保留。RST 中的当前行为以代码为准，
引用历史设计时注明目标与已实现范围。

构建 HTML
---------

在 docs/ 目录执行 ``make help``，或在项目根目录执行 ``make -C docs help``，
可查看可用目标、依赖准备和输出位置。帮助命令无需安装 Sphinx；默认目标仍为 html。

需要 Python 3.10+、GNU Make；固定依赖为 Sphinx 8.1.3、sphinx-rtd-theme 3.1.0。

.. code-block:: bash
   :caption: Linux / macOS：sdn-agent 仓库根目录

   python3 -m venv .venv-docs
   . .venv-docs/bin/activate
   python -m pip install -r docs/requirements.txt
   make -C docs html

成功产物为 ``docs/html/index.html``，两个入口分别为
``docs/html/user/index.html`` 与 ``docs/html/developer/index.html``。
发布或复制时保留整个 html 目录，包括本地搜索、样式和脚本。

.. code-block:: powershell
   :caption: Windows：不依赖 Make 的等价构建

   py -m venv .venv-docs
   .venv-docs\Scripts\python.exe -m pip install -r docs/requirements.txt
   .venv-docs\Scripts\python.exe -m sphinx -W --keep-going -n -a -b html docs docs/html

构建启用 ``-W`` 和 ``-n``，警告及缺失引用会导致失败。
最终还需打开两个入口，确认目录、表格、文本图和代码块在浏览器中可读。

清理生成物
----------

.. code-block:: bash
   :caption: 项目根目录

   make -C docs clean

Windows 等价命令是 ``py docs/_scripts/clean.py``。
清理仅删除本项目的 ``docs/html`` 与 ``docs/_build``；
不会删除 RST、图片、Markdown、源码、虚拟环境、应用数据或容器。

新增或修改页面
--------------

1. 用户任务放 user，内部协议、实现和扩展放 developer。
2. 把新页面加入对应 index.rst 的 toctree，保持一次完整构建可达。
3. 页面内链接使用 ``:doc:`` 或 ``:ref:``；源码路径作文字，必要时提供下载。
4. 代码块标明运行位置、前提和成功证据，token 只使用占位符。
5. 截图放 ``docs/_static/images`` 并去除凭据；不要截图运行卷中的敏感内容。
6. 运行 html 构建，修复全部警告并检查页面。

标题下划线应覆盖中文显示宽度。代码块使用 bash、powershell、json、go、proto 或 text，
不要把带提示符的交互输出混入可复制命令。
跨项目文档的部署位置可能不同，优先说明相邻仓库路径，避免生成指向仓库外的失效 HTML 链接。

维护用户可见行为
----------------

变更 op 时同步 :doc:`操作目录 </user/operations>`；变更事务或恢复时同步
:doc:`用户恢复说明 </user/transactions>` 与 :doc:`execution-storage`；
增加 RPC 或错误时同步 :doc:`protocol`；增加后端依赖或测试时同步
:doc:`用户安装说明 </user/installation>` 与 :doc:`testing`。

测试报告应区分本次真实执行、历史已验收结果和未覆盖场景，不能将文档构建通过写成网络回归通过。
