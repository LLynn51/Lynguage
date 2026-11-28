from time import perf_counter_ns


iterations = 500_000
values: list[int] = []
index = 0
checksum = 0

started = perf_counter_ns()
while index < iterations:
    value = (index * 17 + 3) % 1_000_003
    values.append(value)
    checksum = checksum + value
    index = index + 1
checksum = checksum + len(values)
elapsed = perf_counter_ns() - started

print("elapsed_ns", elapsed, "checksum", checksum)
