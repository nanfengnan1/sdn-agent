"""Remove only this documentation site's generated directories."""
from pathlib import Path
import shutil

docs = Path(__file__).resolve().parent.parent
if docs.name != "docs" or not (docs / "conf.py").is_file():
    raise SystemExit("Refusing cleanup outside the documentation directory")

# Validate every target before removing anything. Never follow linked outputs.
targets = [docs / name for name in ("html", "_build")]
for target in targets:
    if target.is_symlink() or target.resolve().parent != docs:
        raise SystemExit(f"Refusing cleanup of linked or external path: {target}")
    if target.exists() and not target.is_dir():
        raise SystemExit(f"Expected an output directory: {target}")

for target in targets:
    if target.exists():
        shutil.rmtree(target)
        print(f"Removed {target}")
