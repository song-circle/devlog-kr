// AVX2 경로. 이 파일만 /arch:AVX2로 컴파일한다(CMakeLists.txt).
// 원소 하나의 연산 순서는 fp_kernels.cpp의 scalar 경로와 같고, 원소 4개를 한 레지스터에서 나란히 계산한다.
// 곱셈과 덧셈은 따로 한다(_mm256_fmadd_pd를 쓰지 않음): FMA는 반올림이 한 번이라 scalar와 결과가 달라진다.
#include "fp_kernels.h"

#include <immintrin.h>

namespace fpk {

void matmul_avx2(const double* a, const double* b, double* c) {
    constexpr size_t N = kMatN;
    static_assert(N % 4 == 0, "kMatN must be a multiple of 4");
    const __m256d zero = _mm256_setzero_pd();
    for (size_t i = 0; i < N * N; i += 4) _mm256_storeu_pd(c + i, zero);
    for (size_t i = 0; i < N; ++i) {
        double* ci = c + i * N;
        for (size_t k = 0; k < N; ++k) {
            const __m256d aik = _mm256_set1_pd(a[i * N + k]);
            const double* bk = b + k * N;
            for (size_t j = 0; j < N; j += 4) {
                const __m256d prod = _mm256_mul_pd(aik, _mm256_loadu_pd(bk + j));
                _mm256_storeu_pd(ci + j, _mm256_add_pd(_mm256_loadu_pd(ci + j), prod));
            }
        }
    }
}

void nbody_avx2(const Input& in, double* acc) {
    constexpr size_t n = kBodies;
    static_assert(n % 4 == 0, "kBodies must be a multiple of 4");
    const __m256d eps = _mm256_set1_pd(kSoftening);
    const __m256d one = _mm256_set1_pd(1.0);
    for (size_t i = 0; i < n; i += 4) {  // 물체 i..i+3을 나란히
        const __m256d xi = _mm256_loadu_pd(&in.x[i]);
        const __m256d yi = _mm256_loadu_pd(&in.y[i]);
        const __m256d zi = _mm256_loadu_pd(&in.z[i]);
        __m256d ax = _mm256_setzero_pd(), ay = _mm256_setzero_pd(), az = _mm256_setzero_pd();
        for (size_t j = 0; j < n; ++j) {
            const __m256d dx = _mm256_sub_pd(_mm256_set1_pd(in.x[j]), xi);
            const __m256d dy = _mm256_sub_pd(_mm256_set1_pd(in.y[j]), yi);
            const __m256d dz = _mm256_sub_pd(_mm256_set1_pd(in.z[j]), zi);
            __m256d r2 = _mm256_add_pd(_mm256_mul_pd(dx, dx), _mm256_mul_pd(dy, dy));
            r2 = _mm256_add_pd(_mm256_add_pd(r2, _mm256_mul_pd(dz, dz)), eps);
            const __m256d inv = _mm256_div_pd(one, _mm256_mul_pd(r2, _mm256_sqrt_pd(r2)));
            const __m256d s = _mm256_mul_pd(_mm256_set1_pd(in.m[j]), inv);
            ax = _mm256_add_pd(ax, _mm256_mul_pd(dx, s));
            ay = _mm256_add_pd(ay, _mm256_mul_pd(dy, s));
            az = _mm256_add_pd(az, _mm256_mul_pd(dz, s));
        }
        _mm256_storeu_pd(acc + i, ax);
        _mm256_storeu_pd(acc + n + i, ay);
        _mm256_storeu_pd(acc + 2 * n + i, az);
    }
}

}  // namespace fpk
