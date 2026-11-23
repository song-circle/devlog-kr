// 부동소수점 모듈 정확성 테스트: scalar·avx2 두 경로의 digest를 Python 독립 구현(tools/fp_reference.py)과 비교
#include "fp_kernels.h"

#include <cstdio>
#include <vector>

static int g_failed = 0;

static void check(bool ok, const char* what) {
    std::printf("[%s] %s\n", ok ? "PASS" : "FAIL", what);
    if (!ok) ++g_failed;
}

int main() {
    // python -X utf8 tools/fp_reference.py <seed>
    struct Golden { uint64_t seed, matmul, nbody; };
    const Golden golden[] = {{1, 0x59be5ed82c971cd9ull, 0x537fdb87735593e3ull},
                             {42, 0xa1488da3189b9ebcull, 0xfe228cabef01f54full}};

    std::vector<fpk::Path> paths = {fpk::Path::Scalar};
    if (fpk::cpu_has_avx2()) paths.push_back(fpk::Path::Avx2);
    else std::printf("[SKIP] avx2: this CPU/OS does not support AVX2\n");

    for (const auto& g : golden) {
        const fpk::Input in = fpk::make_input(g.seed);
        for (fpk::Path p : paths) {
            for (fpk::Workload w : {fpk::Workload::MatMul, fpk::Workload::NBody}) {
                std::vector<double> out(fpk::out_size(w));
                const uint64_t want = w == fpk::Workload::MatMul ? g.matmul : g.nbody;
                const uint64_t got = fpk::run_unit(w, p, in, out.data());
                char what[128];
                std::snprintf(what, sizeof(what), "%s %s seed %llu == 0x%016llx (got 0x%016llx)",
                              w == fpk::Workload::MatMul ? "matmul" : "nbody", fpk::path_name(p),
                              static_cast<unsigned long long>(g.seed), static_cast<unsigned long long>(want),
                              static_cast<unsigned long long>(got));
                check(got == want, what);
                check(fpk::run_unit(w, p, in, out.data()) == got, "run_unit is repeatable");
            }
        }
    }

    std::printf("%s\n", g_failed ? "FAILED" : "ALL PASSED");
    return g_failed ? 1 : 0;
}
