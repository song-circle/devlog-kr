// 부동소수점(FP) 부하 모듈 DLL: bench_api.h 구현
//
// 측정 종류(workload): matmul(기본, 128x128 double 행렬곱), nbody(물체 256개 가속도)
// 코드 경로: CPU가 AVX2를 지원하면 avx2, 아니면 scalar. 환경 변수 CPU_BENCH_FP_PATH=scalar|avx2로 고정할 수 있다
// (경로끼리 결과 비교·속도 비교용). 두 경로의 digest는 같아야 한다.
#define BENCH_MODULE_BUILD
#include "bench_api.h"
#include "bench_harness.h"
#include "fp_kernels.h"

#include <cstdlib>
#include <cstring>
#include <new>
#include <vector>

struct BenchContext {
    BenchConfig cfg{};
    fpk::Workload workload = fpk::Workload::MatMul;
    fpk::Path path = fpk::Path::Scalar;
    fpk::Input input;
    uint64_t expected = 0;  // bench_init에서 한 번 계산한 기준 digest
    std::vector<fpk::DVec> out;  // 워커별 출력 버퍼 (64바이트 정렬)
};

namespace {

const BenchInfo kInfo = {sizeof(BenchInfo), BENCH_API_VERSION, "fp", "Floating point (matrix multiply, N-body)",
                         "0.1.0", "unit", "matmul,nbody"};

bool parse_workload(const char* s, fpk::Workload& w) {
    if (!s || !*s || !std::strcmp(s, "matmul")) w = fpk::Workload::MatMul;
    else if (!std::strcmp(s, "nbody")) w = fpk::Workload::NBody;
    else return false;
    return true;
}

// 0 = 성공, 그 외 BENCH_E_*
int32_t choose_path(fpk::Path& p) {
    const bool avx2 = fpk::cpu_has_avx2();
    char buf[16] = {};
    size_t len = 0;
    if (getenv_s(&len, buf, sizeof(buf), "CPU_BENCH_FP_PATH") == 0 && len > 1) {
        if (!std::strcmp(buf, "scalar")) { p = fpk::Path::Scalar; return BENCH_OK; }
        if (!std::strcmp(buf, "avx2")) {
            if (!avx2) return BENCH_E_UNSUPPORTED;
            p = fpk::Path::Avx2;
            return BENCH_OK;
        }
        return BENCH_E_INVALID_ARG;
    }
    p = avx2 ? fpk::Path::Avx2 : fpk::Path::Scalar;
    return BENCH_OK;
}

}  // namespace

extern "C" {

BENCH_EXPORT const BenchInfo* BENCH_CALL bench_get_info(void) { return &kInfo; }

BENCH_EXPORT int32_t BENCH_CALL bench_init(const BenchConfig* config, BenchContext** out_ctx) {
    if (!out_ctx) return BENCH_E_INVALID_ARG;
    *out_ctx = nullptr;
    BenchConfig cfg;
    if (!bh::copy_config(config, cfg)) return BENCH_E_INVALID_ARG;
    fpk::Workload w;
    if (!parse_workload(cfg.workload, w) || cfg.working_set_bytes) return BENCH_E_UNSUPPORTED;
    fpk::Path p;
    if (int32_t rc = choose_path(p); rc != BENCH_OK) return rc;
    try {
        auto* ctx = new BenchContext();
        ctx->cfg = cfg;
        ctx->cfg.workload = nullptr;  // 호출자 문자열을 붙잡아 두지 않는다
        ctx->workload = w;
        ctx->path = p;
        ctx->input = fpk::make_input(cfg.seed);
        ctx->out.assign(cfg.threads, fpk::DVec(fpk::out_size(w)));
        ctx->expected = fpk::run_unit(w, p, ctx->input, ctx->out[0].data());
        *out_ctx = ctx;
        return BENCH_OK;
    } catch (const std::bad_alloc&) {
        return BENCH_E_OUT_OF_MEMORY;
    } catch (...) {
        return BENCH_E_INTERNAL;
    }
}

BENCH_EXPORT int32_t BENCH_CALL bench_run(BenchContext* ctx, BenchResult* out, const volatile int32_t* cancel,
                                          BenchProgressFn progress, void* progress_user) {
    if (!ctx || !bh::result_size_ok(out)) return BENCH_E_INVALID_ARG;

    const uint64_t expected = ctx->expected;
    auto step = [&](uint32_t idx, bh::WorkerStats& st) {
        uint64_t d = fpk::run_unit(ctx->workload, ctx->path, ctx->input, ctx->out[idx].data());
        if (d != expected) ++st.mismatches;
        ++st.ops;
    };

    bh::RunTotals t;
    int32_t rc = bh::run_workers(ctx->cfg, cancel, progress, progress_user, step, t);
    if (rc != BENCH_OK) return rc;
    bh::fill_result(out, ctx->cfg.threads, t, expected, fpk::path_name(ctx->path), 0, "unit");
    return bh::status_of(t);
}

BENCH_EXPORT void BENCH_CALL bench_shutdown(BenchContext* ctx) { delete ctx; }

}  // extern "C"
