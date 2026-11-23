#include "fp_kernels.h"

#include <cmath>
#include <cstring>
#include <intrin.h>

namespace fpk {

namespace {

uint64_t splitmix64(uint64_t& state) {
    state += 0x9E3779B97F4A7C15ull;
    uint64_t z = state;
    z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9ull;
    z = (z ^ (z >> 27)) * 0x94D049BB133111EBull;
    return z ^ (z >> 31);
}

// [0, 1) 균일 분포: 상위 53비트를 그대로 쓴다 (Python에서도 같은 값이 나온다)
double unit_double(uint64_t& state) { return double(splitmix64(state) >> 11) * (1.0 / 9007199254740992.0); }

uint64_t digest(const double* v, size_t n) {
    uint64_t h = 0xcbf29ce484222325ull;
    for (size_t i = 0; i < n; ++i) {
        uint64_t bits;
        std::memcpy(&bits, &v[i], sizeof(bits));
        h ^= bits;
        h *= 0x100000001b3ull;
    }
    return h;
}

// C = A·B. i-k-j 순서: C[i][j]에 k = 0, 1, 2, ... 순서로 a[i][k]*b[k][j]를 더한다.
void matmul_scalar(const double* a, const double* b, double* c) {
    constexpr size_t N = kMatN;
    for (size_t i = 0; i < N * N; ++i) c[i] = 0.0;
    for (size_t i = 0; i < N; ++i) {
        double* ci = c + i * N;
        for (size_t k = 0; k < N; ++k) {
            const double aik = a[i * N + k];
            const double* bk = b + k * N;
            for (size_t j = 0; j < N; ++j) ci[j] = ci[j] + aik * bk[j];
        }
    }
}

// 물체 i의 가속도 = 모든 j(자기 자신 포함, 그때 dx=dy=dz=0이라 0이 더해짐)에 대해
//   r2 = ((dx*dx + dy*dy) + dz*dz) + eps,  s = m[j] / (r2 * sqrt(r2)) 대신 m[j] * (1 / (r2 * sqrt(r2)))
void nbody_scalar(const Input& in, double* acc) {
    constexpr size_t n = kBodies;
    for (size_t i = 0; i < n; ++i) {
        double ax = 0.0, ay = 0.0, az = 0.0;
        for (size_t j = 0; j < n; ++j) {
            const double dx = in.x[j] - in.x[i];
            const double dy = in.y[j] - in.y[i];
            const double dz = in.z[j] - in.z[i];
            const double r2 = ((dx * dx + dy * dy) + dz * dz) + kSoftening;
            const double inv = 1.0 / (r2 * std::sqrt(r2));
            const double s = in.m[j] * inv;
            ax = ax + dx * s;
            ay = ay + dy * s;
            az = az + dz * s;
        }
        acc[i] = ax;
        acc[n + i] = ay;
        acc[2 * n + i] = az;
    }
}

}  // namespace

Input make_input(uint64_t seed) {
    Input in;
    uint64_t state = seed;
    in.a.resize(kMatN * kMatN);
    in.b.resize(kMatN * kMatN);
    for (double& v : in.a) v = unit_double(state) * 2.0 - 1.0;
    for (double& v : in.b) v = unit_double(state) * 2.0 - 1.0;
    in.x.resize(kBodies);
    in.y.resize(kBodies);
    in.z.resize(kBodies);
    in.m.resize(kBodies);
    for (size_t i = 0; i < kBodies; ++i) {
        in.x[i] = unit_double(state) * 2.0 - 1.0;
        in.y[i] = unit_double(state) * 2.0 - 1.0;
        in.z[i] = unit_double(state) * 2.0 - 1.0;
        in.m[i] = unit_double(state) + 0.5;
    }
    return in;
}

size_t out_size(Workload w) { return w == Workload::MatMul ? kMatN * kMatN : 3 * kBodies; }

uint64_t run_unit(Workload w, Path p, const Input& in, double* out) {
    if (w == Workload::MatMul) {
        if (p == Path::Avx2) matmul_avx2(in.a.data(), in.b.data(), out);
        else matmul_scalar(in.a.data(), in.b.data(), out);
    } else {
        if (p == Path::Avx2) nbody_avx2(in, out);
        else nbody_scalar(in, out);
    }
    return digest(out, out_size(w));
}

bool cpu_has_avx2() {
    int r[4];
    __cpuid(r, 0);
    if (r[0] < 7) return false;
    __cpuid(r, 1);
    const bool osxsave = (r[2] & (1 << 27)) != 0;
    const bool avx = (r[2] & (1 << 28)) != 0;
    if (!osxsave || !avx) return false;
    if ((_xgetbv(0) & 0x6) != 0x6) return false;  // OS가 XMM·YMM 레지스터를 저장해 주는지
    __cpuidex(r, 7, 0);
    return (r[1] & (1 << 5)) != 0;
}

const char* path_name(Path p) { return p == Path::Avx2 ? "avx2" : "scalar"; }

}  // namespace fpk
