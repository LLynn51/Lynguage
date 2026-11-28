from time import perf_counter_ns


def absolute(value: int) -> int:
    if value < 0:
        return -value
    return value


def heuristic(node: int, goal: int, width: int) -> int:
    x = node % width
    y = node // width
    goal_x = goal % width
    goal_y = goal // width
    return absolute(goal_x - x) + absolute(goal_y - y)


def is_blocked(node: int, width: int, height: int) -> bool:
    x = node % width
    y = node // width
    if x == 0 or y == height - 1:
        return False
    vertical_wall = x % 9 == 4 and y % 10 != 5
    horizontal_wall = y % 13 == 7 and x % 12 != 6
    return vertical_wall or horizontal_wall


def relax(
    current: int,
    neighbor: int,
    width: int,
    height: int,
    scores: list[int],
    opened: list[bool],
    closed: list[bool],
) -> None:
    if closed[neighbor] or is_blocked(neighbor, width, height):
        return
    candidate = scores[current] + 1
    if not opened[neighbor] or candidate < scores[neighbor]:
        scores[neighbor] = candidate
        opened[neighbor] = True


width = 64
height = 64
node_count = width * height
goal = node_count - 1
infinity = 1_000_000_000

started = perf_counter_ns()
scores = [0] * node_count
opened = [False] * node_count
closed = [False] * node_count
index = 0
while index < node_count:
    scores[index] = infinity
    index = index + 1
scores[0] = 0
opened[0] = True

found = False
searching = True
expanded = 0
while searching and not found:
    best = -1
    best_score = infinity
    index = 0
    while index < node_count:
        if opened[index] and not closed[index]:
            estimate = scores[index] + heuristic(index, goal, width)
            if estimate < best_score:
                best = index
                best_score = estimate
        index = index + 1

    if best < 0:
        searching = False
    elif best == goal:
        found = True
    else:
        opened[best] = False
        closed[best] = True
        expanded = expanded + 1
        x = best % width
        y = best // width
        if x > 0:
            relax(best, best - 1, width, height, scores, opened, closed)
        if x + 1 < width:
            relax(best, best + 1, width, height, scores, opened, closed)
        if y > 0:
            relax(best, best - width, width, height, scores, opened, closed)
        if y + 1 < height:
            relax(best, best + width, width, height, scores, opened, closed)

checksum = -1
if found:
    checksum = scores[goal] * 1_000_000 + expanded
elapsed = perf_counter_ns() - started

print("elapsed_ns", elapsed, "checksum", checksum)
