"""APFS allocation controls. Temporary files only; no production data."""
import fcntl
import json
import os
from pathlib import Path
import platform
import struct
import tempfile


def snapshot(fd):
    stat = os.fstat(fd)
    return {"logical": stat.st_size, "allocated": stat.st_blocks * 512}


results = []
with tempfile.TemporaryDirectory(prefix="trunk-revision-controls-") as directory:
    for name in ("headers-every-2KiB", "one-page-in-sixteen", "prior-wide-holes"):
        if name == "prior-wide-holes":
            size = 64 * 1024 * 1024
            writes = [(at, bytes([73]) * 16384) for at in (0, 16 * 1024 * 1024, 32 * 1024 * 1024, size - 16384)]
        else:
            size = 8 * 1024 * 1024
            writes = [(i * 2048, bytes([73]) * 56) for i in range(4096)
                      if name == "headers-every-2KiB" or (i // 2) % 16 == 0]
        fd = os.open(os.path.join(directory, name), os.O_RDWR | os.O_CREAT, 0o600)
        try:
            os.ftruncate(fd, size)
            for at, data in writes:
                assert os.pwrite(fd, data, at) == len(data)
            os.fsync(fd)
            row = {"case": name, "beforePunch": snapshot(fd), "writtenBytes": sum(len(data) for _, data in writes)}
            # Never punch a 4KiB unit containing any written byte.
            occupied = set()
            for at, data in writes:
                occupied.update(range(at // 4096, (at + len(data) - 1) // 4096 + 1))
            calls = 0
            if platform.system() == "Darwin":
                page = 0
                while page < size // 4096:
                    if page in occupied:
                        page += 1
                        continue
                    start = page
                    while page < size // 4096 and page not in occupied:
                        page += 1
                    # macOS F_PUNCHHOLE: uint flags, uint reserved, off_t offset, off_t length.
                    fcntl.fcntl(fd, 99, struct.pack("=IIqq", 0, 0, start * 4096, (page - start) * 4096))
                    calls += 1
                os.fsync(fd)
                row["afterPunch"] = snapshot(fd)
                row["punchCalls"] = calls
            for at, data in writes:
                assert os.pread(fd, len(data), at) == data
            row["verified"] = True
            results.append(row)
        finally:
            os.close(fd)
Path(__file__).with_name("native-controls.json").write_text(json.dumps({"platform": platform.platform(), "results": results}, indent=2) + "\n")
print(json.dumps(results, indent=2))
