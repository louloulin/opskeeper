import importlib.util
from pathlib import Path


def load_archive_helper():
    path = Path(__file__).parents[1] / "scripts" / "deterministic_archive.py"
    spec = importlib.util.spec_from_file_location("deterministic_archive", path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def test_root_manifest_precedes_nested_adapter_manifests(tmp_path):
    helper = load_archive_helper()
    root_manifest = tmp_path / "plugin.yaml"
    root_manifest.write_text("apiVersion: example\n")
    adapters = tmp_path / "adapters"
    adapter_dir = adapters / "qwenpaw"
    adapter_dir.mkdir(parents=True)
    adapter_manifest = adapter_dir / "plugin.json"
    adapter_manifest.write_text('{"id":"example"}\n')

    entries = helper.collect_entries(
        [
            f"{root_manifest}=plugin.yaml",
            f"{adapters}=adapters",
        ]
    )
    file_names = [archive_path.as_posix() for _, archive_path, is_dir in entries if not is_dir]

    assert file_names[0] == "plugin.yaml"
    assert "adapters/qwenpaw/plugin.json" in file_names
