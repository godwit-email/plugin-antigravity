#!/usr/bin/env python3
"""Build this computer's plugin into dist/antigravity/, ready to install from that folder."""
import os
from pathlib import Path
import shutil
import subprocess

root = Path(__file__).resolve().parent.parent
os.chdir(root)
goos = subprocess.check_output(["go", "env", "GOOS"], text=True).strip()
assert goos == "darwin", "macOS only: elsewhere the server keeps its token in a plain file (ADR 0033)"
dest = root / "dist" / "antigravity"  # Godwit requires the folder to carry the plugin's name
dest.mkdir(parents=True, exist_ok=True)
library = dest / "plugin.dylib"
subprocess.run(["go", "build", "-trimpath", "-buildmode=c-shared", "-ldflags=-s -w", "-o", str(library), "."], check=True)
(dest / "plugin.h").unlink(missing_ok=True)
assert 0 < library.stat().st_size <= 64 << 20, "the library exceeds Godwit's native artifact limit"
for name in ["plugin.json", "LICENSE"]:
    shutil.copyfile(root / name, dest / name)
print(library.relative_to(root))
