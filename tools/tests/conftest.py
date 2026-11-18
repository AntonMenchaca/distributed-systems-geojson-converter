import sys
from pathlib import Path

# Tools are plain scripts in tools/; make them importable as modules in tests.
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))


def pytest_configure(config):
    # test_p4_single_feature_over_200mb writes a ~250 MB file; deselect with -m "not slow".
    config.addinivalue_line("markers", "slow: writes large files (run them with plain pytest)")
