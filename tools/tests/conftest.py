import sys
from pathlib import Path

# Tools are plain scripts in tools/; make them importable as modules in tests.
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
