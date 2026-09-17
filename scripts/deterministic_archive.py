#!/usr/bin/env python3
from __future__ import annotations

import argparse
import gzip
import os
import stat
import tarfile
import tempfile
import zipfile
from pathlib import Path


EXCLUDED_NAMES = {".DS_Store", "__pycache__", "node_modules"}
EXCLUDED_SUFFIXES = {".pyc"}


def parse_source(source: str) -> tuple[Path, Path]:
    source_path, separator, archive_path = source.partition("=")
    if not separator or not source_path or not archive_path:
        raise ValueError(f"source must be PATH=ARCHIVE_PATH, got {source!r}")
    return Path(source_path), Path(archive_path)


def is_excluded(path: Path) -> bool:
    name = path.name if isinstance(path, Path) else path
    return name in EXCLUDED_NAMES or Path(name).suffix in EXCLUDED_SUFFIXES


def collect_entries(sources: list[str]) -> list[tuple[Path, Path, bool]]:
    entries: list[tuple[Path, Path, bool]] = []
    for source in sources:
        source_path, archive_path = parse_source(source)
        if source_path.is_symlink() or not source_path.is_dir():
            if not source_path.is_file():
                raise FileNotFoundError(source_path)
            entries.append((source_path, archive_path, False))
            continue

        entries.append((source_path, archive_path, True))
        for child in sorted(source_path.rglob("*"), key=lambda item: item.as_posix()):
            if is_excluded(child) or any(is_excluded(part) for part in child.parts):
                continue
            if child.is_symlink() or child.is_file():
                entries.append((child, archive_path / child.relative_to(source_path), False))
            elif child.is_dir():
                entries.append((child, archive_path / child.relative_to(source_path), True))
    return entries


def file_mode(path: Path) -> int:
    if stat.S_IMODE(path.stat().st_mode) & stat.S_IXUSR:
        return 0o755
    return 0o644


def write_tar_gz(output: Path, sources: list[str]) -> None:
    entries = collect_entries(sources)
    with tempfile.NamedTemporaryFile(suffix=".tar", delete=False) as temporary:
        temporary_path = Path(temporary.name)
    try:
        with tarfile.open(temporary_path, "w", format=tarfile.PAX_FORMAT) as archive:
            for source_path, archive_path, is_directory in entries:
                info = tarfile.TarInfo(archive_path.as_posix())
                info.uid = 0
                info.gid = 0
                info.uname = ""
                info.gname = ""
                info.mtime = 0
                if source_path.is_symlink():
                    info.type = tarfile.SYMTYPE
                    info.linkname = os.readlink(source_path)
                    info.mode = 0o777
                    archive.addfile(info)
                elif is_directory:
                    info.type = tarfile.DIRTYPE
                    info.mode = 0o755
                    archive.addfile(info)
                else:
                    info.type = tarfile.REGTYPE
                    info.size = source_path.stat().st_size
                    info.mode = file_mode(source_path)
                    with source_path.open("rb") as content:
                        archive.addfile(info, content)

        with output.open("wb") as compressed, gzip.GzipFile(
            filename="", mode="wb", fileobj=compressed, compresslevel=9, mtime=0
        ) as gzip_file, temporary_path.open("rb") as tar_content:
            while chunk := tar_content.read(1024 * 1024):
                gzip_file.write(chunk)
    finally:
        temporary_path.unlink(missing_ok=True)


def write_zip(output: Path, sources: list[str]) -> None:
    entries = collect_entries(sources)
    with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
        for source_path, archive_path, is_directory in entries:
            if source_path.is_symlink():
                raise ValueError(f"ZIP source contains a symlink: {source_path}")
            info = zipfile.ZipInfo(
                archive_path.as_posix() + "/" if is_directory else archive_path.as_posix(),
                date_time=(1980, 1, 1, 0, 0, 0),
            )
            info.compress_type = zipfile.ZIP_DEFLATED
            info.create_system = 3
            mode = 0o755 if is_directory else file_mode(source_path)
            info.external_attr = mode << 16
            if is_directory:
                info.external_attr |= 0x10
                archive.writestr(info, b"")
            else:
                archive.writestr(info, source_path.read_bytes())


def main() -> int:
    parser = argparse.ArgumentParser(description="Create deterministic tar.gz and ZIP archives")
    subparsers = parser.add_subparsers(dest="format", required=True)
    for name in ("tar-gz", "zip"):
        command = subparsers.add_parser(name)
        command.add_argument("output", type=Path)
        command.add_argument("--source", action="append", required=True)
    arguments = parser.parse_args()
    try:
        if arguments.format == "tar-gz":
            write_tar_gz(arguments.output, arguments.source)
        else:
            write_zip(arguments.output, arguments.source)
    except (OSError, ValueError, tarfile.TarError, zipfile.BadZipFile) as error:
        parser.error(str(error))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
