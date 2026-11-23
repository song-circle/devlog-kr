// AVX2 경로. 이 파일만 /arch:AVX2로 컴파일한다(CMakeLists.txt).
// 원소 하나의 연산 순서는 fp_kernels.cpp의 scalar 경로와 같고, 원소 4개를 한 레지스터에서 나란히 계산한다.
// 버퍼는 64바이트 정렬(fp_kernels.h의 DVec)이라 32바이트 로드·저장이 캐시 라인 경계에 걸리지 않는다.
// 곱셈과 덧셈은 따로 한다(_mm256_fmadd_pd를 쓰지 않음): FMA는 반올림이 한 번이라 scalar와 결과가 달라진다.
#include "fp_kernels.h"

#include <immintrin.h>

namespace fpk {

// C의 4행 x 8열(= YMM 8개) 조각을 레지스터에 둔 채 k = 0..N-1을 끝까지 더하고 마지막에 한 번만 저장한다.
// 원소 C[i][j] 하나만 보면 k 순서대로 a[i][k]*b[k][j]를 더하는 것은 scalar와 같다(결과 비트 동일).
// 처음 버전(i-k-j 순서로 C 한 행을 매번 읽고 쓰기)은 k마다 C를 메모리에 다시 읽고 써서 scalar의 1.6배에 그쳤다(버그 015).
void matmul_avx2(const double* a, const double* b, double* c) {
    constexpr size_t N = kMatN;
    static_assert(N % 8 == 0, "kMatN must be a multiple of 8");
    for (size_t i = 0; i < N; i += 4) {
        const double* a0 = a + i * N;
        const double* a1 = a0 + N;
        const double* a2 = a1 + N;
        const double* a3 = a2 + N;
        for (size_t j = 0; j < N; j += 8) {
            __m256d c00 = _mm256_setzero_pd(), c01 = _mm256_setzero_pd();
            __m256d c10 = _mm256_setzero_pd(), c11 = _mm256_setzero_pd();
            __m256d c20 = _mm256_setzero_pd(), c21 = _mm256_setzero_pd();
            __m256d c30 = _mm256_setzero_pd(), c31 = _mm256_setzero_pd();
            for (size_t k = 0; k < N; ++k) {
                const __m256d b0 = _mm256_loadu_pd(b + k * N + j);
                const __m256d b1 = _mm256_loadu_pd(b + k * N + j + 4);
                __m256d x = _mm256_set1_pd(a0[k]);
                c00 = _mm256_add_pd(c00, _mm256_mul_pd(x, b0));
                c01 = _mm256_add_pd(c01, _mm256_mul_pd(x, b1));
                x = _mm256_set1_pd(a1[k]);
                c10 = _mm256_add_pd(c10, _mm256_mul_pd(x, b0));
                c11 = _mm256_add_pd(c11, _mm256_mul_pd(x, b1));
                x = _mm256_set1_pd(a2[k]);
                c20 = _mm256_add_pd(c20, _mm256_mul_pd(x, b0));
                c21 = _mm256_add_pd(c21, _mm256_mul_pd(x, b1));
                x = _mm256_set1_pd(a3[k]);
                c30 = _mm256_add_pd(c30, _mm256_mul_pd(x, b0));
                c31 = _mm256_add_pd(c31, _mm256_mul_pd(x, b1));
            }
            double* ci = c + i * N + j;
            _mm256_storeu_pd(ci, c00);
            _mm256_storeu_pd(ci + 4, c01);
            _mm256_storeu_pd(ci + N, c10);
            _mm256_storeu_pd(ci + N + 4, c11);
            _mm256_storeu_pd(ci + 2 * N, c20);
            _mm256_storeu_pd(ci + 2 * N + 4, c21);
            _mm256_storeu_pd(ci + 3 * N, c30);
            _mm256_storeu_pd(ci + 3 * N + 4, c31);
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
