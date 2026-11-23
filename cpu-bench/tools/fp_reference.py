"""부동소수점 모듈 작업 단위(digest)의 독립 구현 (Python 표준 라이브러리만 사용).

C++ 구현(scalar·avx2 두 경로 모두)과 비트 단위로 같은 결과가 나오는지 교차 확인하는 용도다.
Python float은 IEEE 754 double이고 math.sqrt는 정확히 반올림되므로, 연산 순서가 같으면 결과도 같다.
  python -X utf8 tools/fp_reference.py 1          -> seed 1의 matmul, nbody digest 출력
"""
import math
import struct
import sys

M = (1 << 64) - 1
MAT_N = 128
BODIES = 256
SOFTENING = 0.01


def make_input(seed):
    state = seed & M

    def unit():
        nonlocal state
        state = (state + 0x9E3779B97F4A7C15) & M
        z = state
        z = ((z ^ (z >> 30)) * 0xBF58476D1CE4E5B9) & M
        z = ((z ^ (z >> 27)) * 0x94D049BB133111EB) & M
        z ^= z >> 31
        return (z >> 11) * (1.0 / 9007199254740992.0)

    a = [unit() * 2.0 - 1.0 for _ in range(MAT_N * MAT_N)]
    b = [unit() * 2.0 - 1.0 for _ in range(MAT_N * MAT_N)]
    x, y, z, m = [], [], [], []
    for _ in range(BODIES):
        x.append(unit() * 2.0 - 1.0)
        y.append(unit() * 2.0 - 1.0)
        z.append(unit() * 2.0 - 1.0)
        m.append(unit() + 0.5)
    return a, b, x, y, z, m


def digest(values):
    h = 0xCBF29CE484222325
    for v in values:
        h ^= struct.unpack("<Q", struct.pack("<d", v))[0]
        h = (h * 0x100000001B3) & M
    return h


def matmul(a, b):
    n = MAT_N
    c = [0.0] * (n * n)
    for i in range(n):
        row = i * n
        for k in range(n):
            aik = a[row + k]
            bk = k * n
            for j in range(n):
                c[row + j] = c[row + j] + aik * b[bk + j]
    return c


def nbody(x, y, z, m):
    n = BODIES
    acc = [0.0] * (3 * n)
    for i in range(n):
        ax = ay = az = 0.0
        for j in range(n):
            dx = x[j] - x[i]
            dy = y[j] - y[i]
            dz = z[j] - z[i]
            r2 = ((dx * dx + dy * dy) + dz * dz) + SOFTENING
            inv = 1.0 / (r2 * math.sqrt(r2))
            s = m[j] * inv
            ax = ax + dx * s
            ay = ay + dy * s
            az = az + dz * s
        acc[i], acc[n + i], acc[2 * n + i] = ax, ay, az
    return acc


if __name__ == "__main__":
    seed = int(sys.argv[1]) if len(sys.argv) > 1 else 1
    a, b, x, y, z, m = make_input(seed)
    print("matmul 0x%016x" % digest(matmul(a, b)))
    print("nbody  0x%016x" % digest(nbody(x, y, z, m)))
