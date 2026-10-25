"""Standalone HTML documentation for sdn-agent."""
from pathlib import Path

DOCS_DIR = Path(__file__).resolve().parent
project = "sdn-agent"
author = "SDN 项目组"
copyright = "2026, SDN 项目组"
language = "zh_CN"
root_doc = "index"
source_suffix = {".rst": "restructuredtext"}
extensions = ["sphinx_rtd_theme"]
exclude_patterns = [
    "html", "_build", "_scripts", ".venv", "**/__pycache__",
    "Thumbs.db", ".DS_Store",
]
templates_path = ["_templates"]
html_theme = "sphinx_rtd_theme"
html_title = "sdn-agent 文档"
html_theme_options = {"navigation_depth": 3, "collapse_navigation": True}
html_static_path = ["_static"]
html_css_files = ["reading.css"]
html_js_files = ["reading.js"]
html_show_sourcelink = False
html_show_sphinx = False
html_copy_source = False
html_last_updated_fmt = None
