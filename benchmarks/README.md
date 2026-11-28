# Lynguage Benchmark Suite

Each benchmark contains equivalent `bench.lyn` and `bench.py` programs. Both implementations time only the core workload with a nanosecond timer and print the same deterministic checksum.

Run the default five measured rounds with one warmup round:

```powershell
python benchmarks/run_bench.py
```

Use a shorter smoke run while changing the suite:

```powershell
python benchmarks/run_bench.py --rounds 1 --warmup 0
```

The runner builds the Lynguage CLI once, executes Python, the bytecode VM, and `--transpile`, rejects checksum mismatches, and prints a Markdown table of median internal elapsed times.
