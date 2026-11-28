from __future__ import annotations

import argparse
import re
import statistics
import subprocess
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path


BENCHMARKS = ("micro_loop", "micro_slice", "macro_astar")
RESULT_PATTERN = re.compile(r"^elapsed_ns (\d+) checksum (-?\d+)$", re.MULTILINE)


@dataclass(frozen=True)
class Engine:
    name: str
    command_prefix: tuple[str, ...]
    source_name: str


@dataclass(frozen=True)
class Measurement:
    elapsed_ns: int
    checksum: int


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Compare Python, Lynguage VM, and transpiled Lynguage benchmarks."
    )
    parser.add_argument("--rounds", type=int, default=5, help="measured rounds per engine (default: 5)")
    parser.add_argument("--warmup", type=int, default=1, help="warmup rounds per engine (default: 1)")
    parser.add_argument("--timeout", type=float, default=120.0, help="seconds allowed per process")
    parser.add_argument(
        "--lynguage",
        type=Path,
        help="existing Lynguage CLI; by default the runner builds a temporary binary",
    )
    args = parser.parse_args()
    if args.rounds < 1:
        parser.error("--rounds must be at least 1")
    if args.warmup < 0:
        parser.error("--warmup cannot be negative")
    if args.timeout <= 0:
        parser.error("--timeout must be positive")
    return args


def build_cli(repository_root: Path, output: Path) -> None:
    completed = subprocess.run(
        ["go", "build", "-o", str(output), "./cmd/lynguage"],
        cwd=repository_root,
        capture_output=True,
        text=True,
        encoding="utf-8",
        check=False,
    )
    if completed.returncode != 0:
        raise RuntimeError(f"failed to build Lynguage CLI:\n{completed.stderr}{completed.stdout}")


def execute(command: list[str], repository_root: Path, timeout: float) -> Measurement:
    try:
        completed = subprocess.run(
            command,
            cwd=repository_root,
            capture_output=True,
            text=True,
            encoding="utf-8",
            timeout=timeout,
            check=False,
        )
    except subprocess.TimeoutExpired as error:
        raise RuntimeError(f"benchmark timed out after {timeout:g}s: {' '.join(command)}") from error
    if completed.returncode != 0:
        raise RuntimeError(
            f"benchmark exited with {completed.returncode}: {' '.join(command)}\n"
            f"stdout:\n{completed.stdout}\nstderr:\n{completed.stderr}"
        )
    match = RESULT_PATTERN.search(completed.stdout)
    if match is None:
        raise RuntimeError(
            f"benchmark returned an invalid result: {' '.join(command)}\nstdout:\n{completed.stdout}"
        )
    return Measurement(elapsed_ns=int(match.group(1)), checksum=int(match.group(2)))


def run_engine(
    engine: Engine,
    benchmark_dir: Path,
    repository_root: Path,
    rounds: int,
    warmup: int,
    timeout: float,
) -> list[Measurement]:
    command = [*engine.command_prefix, str(benchmark_dir / engine.source_name)]
    for _ in range(warmup):
        execute(command, repository_root, timeout)
    return [execute(command, repository_root, timeout) for _ in range(rounds)]


def format_table(
    all_results: dict[str, dict[str, list[Measurement]]], rounds: int, warmup: int
) -> str:
    lines = [
        "# Python vs Lynguage VM vs Lynguage (--transpile)",
        "",
        f"Measured rounds: {rounds}; warmup rounds: {warmup}; values are median internal elapsed nanoseconds.",
        "",
        "| Benchmark | Python (ns) | Lynguage VM (ns) | Lynguage --transpile (ns) | VM/Python | Transpile/Python | Checksum |",
        "| --- | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]
    for benchmark in BENCHMARKS:
        results = all_results[benchmark]
        checksums = {
            measurement.checksum
            for measurements in results.values()
            for measurement in measurements
        }
        if len(checksums) != 1:
            details = {name: [item.checksum for item in values] for name, values in results.items()}
            raise RuntimeError(f"checksum mismatch for {benchmark}: {details}")
        medians = {
            name: int(statistics.median(item.elapsed_ns for item in measurements))
            for name, measurements in results.items()
        }
        python_ns = medians["Python"]
        lines.append(
            "| "
            + " | ".join(
                (
                    benchmark,
                    f"{python_ns:,}",
                    f"{medians['Lynguage VM']:,}",
                    f"{medians['Lynguage --transpile']:,}",
                    f"{medians['Lynguage VM'] / python_ns:.2f}x",
                    f"{medians['Lynguage --transpile'] / python_ns:.2f}x",
                    f"{next(iter(checksums)):,}",
                )
            )
            + " |"
        )
    return "\n".join(lines)


def collect(
    cli: Path, benchmark_root: Path, repository_root: Path, args: argparse.Namespace
) -> dict[str, dict[str, list[Measurement]]]:
    engines = (
        Engine("Python", (sys.executable,), "bench.py"),
        Engine("Lynguage VM", (str(cli), "run"), "bench.lyn"),
        Engine("Lynguage --transpile", (str(cli), "run", "--transpile"), "bench.lyn"),
    )
    all_results: dict[str, dict[str, list[Measurement]]] = {}
    for benchmark in BENCHMARKS:
        benchmark_dir = benchmark_root / benchmark
        all_results[benchmark] = {}
        for engine in engines:
            all_results[benchmark][engine.name] = run_engine(
                engine,
                benchmark_dir,
                repository_root,
                args.rounds,
                args.warmup,
                args.timeout,
            )
    return all_results


def main() -> int:
    args = parse_args()
    benchmark_root = Path(__file__).resolve().parent
    repository_root = benchmark_root.parent

    if args.lynguage is not None:
        cli = args.lynguage.resolve()
        if not cli.is_file():
            raise RuntimeError(f"Lynguage CLI does not exist: {cli}")
        results = collect(cli, benchmark_root, repository_root, args)
    else:
        executable_name = "lynguage.exe" if sys.platform == "win32" else "lynguage"
        with tempfile.TemporaryDirectory(prefix="lynguage-bench-") as temp_dir:
            cli = Path(temp_dir) / executable_name
            build_cli(repository_root, cli)
            results = collect(cli, benchmark_root, repository_root, args)

    print(format_table(results, args.rounds, args.warmup))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, RuntimeError) as error:
        print(f"error: {error}", file=sys.stderr)
        raise SystemExit(1)
