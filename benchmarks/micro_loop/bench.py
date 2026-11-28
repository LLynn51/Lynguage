from time import perf_counter_ns


iterations = 1_000_000
index = 0
checksum = 0

started = perf_counter_ns()
while index < iterations:
    checksum = checksum + ((index * 31 + 7) % 1_000_003)
    index = index + 1
elapsed = perf_counter_ns() - started

print("elapsed_ns", elapsed, "checksum", checksum)
