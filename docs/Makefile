# Build both user and developer guides as one standalone HTML site.
.DEFAULT_GOAL := html
SPHINXBUILD ?= sphinx-build
PYTHON ?= python3
SPHINXOPTS ?= -W --keep-going -n

.PHONY: help html clean

help:
	@printf '%s\n' \
	  'sdn-agent 文档命令（在 docs/ 目录执行）：' \
	  '  make help   显示帮助，无需安装 Sphinx' \
	  '  make html   构建用户手册和开发手册（默认目标）' \
	  '  make clean  清理 html/ 和 _build/ 文档产物' \
	  '' \
	  '首次准备依赖（Python 3.10+）：' \
	  '  python3 -m venv ../.venv-docs' \
	  '  . ../.venv-docs/bin/activate' \
	  '  python -m pip install -r requirements.txt' \
	  '  make html' \
	  '' \
	  '生成入口：html/index.html' \
	  '在项目根目录执行：make -C docs help' \
	  '可覆盖变量：SPHINXBUILD、SPHINXOPTS、PYTHON' \
	  '示例：make html SPHINXBUILD=../.venv-docs/bin/sphinx-build'

html:
	$(SPHINXBUILD) $(SPHINXOPTS) -a -b html "." "html"

clean:
	$(PYTHON) _scripts/clean.py
