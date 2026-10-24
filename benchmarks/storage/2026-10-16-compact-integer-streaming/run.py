"""Run the single-pass compact integer streaming experiment sequentially; no concurrent timed jobs."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tarfile
from datetime import datetime, timezone

ROOT = Path(__file__).resolve().parent
REPO = ROOT.parents[2]
COUNTS = (1000000,)
PROFILES = ("bounded-unsigned", "bounded-signed", "full-range-unsigned", "full-range-signed")
SHAPES = ("full", "sparse")
ENV = os.environ | {"GOCACHE": "/private/tmp/trie-trunk-go-cache"}
for name in tuple(ENV):
    if name.startswith("TRUNK_MEASURE_"):
        del ENV[name]


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    binary = ROOT / "cache.test"
    subprocess.run(["go", "-C", str(REPO / "go"), "test", "-c", "-o", str(binary), "./server/cache"], env=ENV, check=True)
    sources = sorted((REPO / "go").rglob("*.go")) + [REPO / "go/go.mod", REPO / "go/go.sum"]
    hashes = {str(path.relative_to(REPO)): digest(path) for path in sources}
    archive = ROOT / "source.tar.gz"
    with tarfile.open(archive, "w:gz") as tar:
        for path in sources:
            tar.add(path, arcname=path.relative_to(REPO))
    metadata = {
        "started_utc": datetime.now(timezone.utc).isoformat(),
        "go": subprocess.check_output(["go", "version"], env=ENV, text=True).strip(),
        "source_sha256": hashes,
        "archive_sha256": digest(archive),
        "executable_sha256": digest(binary),
        "profiles": PROFILES, "counts": COUNTS, "shapes": SHAPES,
        "full_range_generator": "math/rand/v2 PCG; seeds 0x70696c, 0x63626f72; Uint64 per non-dictionary integer field",
        "commands": [],
    }
    for n in COUNTS:
        for shape in SHAPES:
            for profile in PROFILES:
                destination = ROOT / "results" / profile / f"{shape}-{n}.json"
                destination.parent.mkdir(parents=True, exist_ok=True)
                env = ENV | {"TRUNK_MEASURE_EVENTS": str(n), "TRUNK_MEASURE_SHAPE": shape,
                             "TRUNK_COMPACT_PROFILE": profile, "TRUNK_MEASURE_OUT": str(destination)}
                command = [str(binary), "-test.run", "^TestMeasureStreamingIntegers$", "-test.timeout", "10m"]
                print(f"Measuring {profile} {shape} {n:,}", flush=True)
                with destination.with_suffix(".log").open("w") as log:
                    subprocess.run(command, cwd=REPO, env=env, stdout=log, stderr=subprocess.STDOUT, check=True)
                metadata["commands"].append({"events": n, "shape": shape, "profile": profile, "output": str(destination.relative_to(ROOT))})
                (ROOT / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
    assert hashes == {str(path.relative_to(REPO)): digest(path) for path in sources}, "source changed during measurements"
    metadata["finished_utc"] = datetime.now(timezone.utc).isoformat()
    (ROOT / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
    print("All measurements completed.", flush=True)


if __name__ == "__main__":
    main()
