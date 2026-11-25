// 동기화·스케일링(MT) 부하 모듈 DLL: bench_api.h 구현
//
// 측정 종류(workload)
//   shared     : 모든 스레드가 카운터 하나를 원자적으로 +1 (캐시 라인 하나를 서로 빼앗는다)
//   padded     : 스레드마다 자기 카운터를 +1, 카운터끼리 64바이트(캐시 라인) 떨어뜨림 — 경합 없는 기준선
//   falseshare : 스레드마다 자기 카운터를 +1, 카운터 8개를 한 캐시 라인에 붙여 둠 — 거짓 공유
//                (스레드 9번째부터는 다음 캐시 라인이라, 9개일 때 9번째만 혼자 빠르게 돌아 합계가 튄다:
//                 i5-14400F 8개 80M → 9개 270M → 10개 170M/s. 거짓 공유가 캐시 라인 단위라는 증거)
//   queue      : lock-free 큐(Vyukov MPMC)에 모든 스레드가 64개 넣고 64개 빼기를 반복
//
// 정확성 검사: 다른 모듈은 작업 단위마다 결과(digest)를 비교하지만, 여기서는 스레드끼리 섞이는 것 자체를
// 재므로 작업 단위 결과가 매번 다르다. 대신 측정이 끝난 뒤 합계를 비교한다.
//   카운터: 카운터 값 = 스레드가 센 횟수(합). 다르면 원자적 연산이 깨진 것(mismatches).
//   큐    : 넣은 값의 합 = 뺀 값의 합, 넣은 개수 = 뺀 개수, 큐가 비어 있음. 다르면 유실·중복(mismatches).
// checksum은 (측정 종류, seed)로 정해지는 고정값이다.
#define BENCH_MODULE_BUILD
#include "bench_api.h"
#include "bench_harness.h"
#include "mt_kernels.h"

#include <immintrin.h>

#include <cstring>
#include <memory>
#include <new>

#ifdef _MSC_VER
#pragma warning(disable : 4324)  // 캐시 라인 정렬 때문에 생기는 패딩은 의도한 것
#endif

namespace {

struct alignas(64) PaddedCounter {
    std::atomic<uint64_t> v{0};
};

constexpr uint32_t kPerLine = 64 / sizeof(uint64_t);  // 한 캐시 라인에 붙여 두는 카운터 수
struct alignas(64) PackedLine {
    std::atomic<uint64_t> v[kPerLine];
};

struct alignas(64) Local {  // 스레드별 기록(다른 스레드가 건드리지 않음)
    uint64_t ops = 0;
    uint64_t pushed_n = 0, popped_n = 0;
    uint64_t pushed_sum = 0, popped_sum = 0;
};

const BenchInfo kInfo = {sizeof(BenchInfo), BENCH_API_VERSION, "mt",
                         "Synchronization (atomic contention, false sharing, lock-free queue)", "0.1.0", "increment",
                         "shared,padded,falseshare,queue"};

bool parse_workload(const char* s, mtk::Workload& w) {
    if (!s || !*s || !std::strcmp(s, "shared")) w = mtk::Workload::Shared;
    else if (!std::strcmp(s, "padded")) w = mtk::Workload::Padded;
    else if (!std::strcmp(s, "falseshare")) w = mtk::Workload::FalseShare;
    else if (!std::strcmp(s, "queue")) w = mtk::Workload::Queue;
    else return false;
    return true;
}

uint64_t fixed_checksum(mtk::Workload w, uint64_t seed) {
    return mtk::item_value(seed, 0xffffffu, uint64_t(w) + 1);
}

// 다른 스레드가 칸을 쓰는 중이면 잠깐 기다린다. 오래 걸리면(스레드가 밀려났을 때) 차례를 넘긴다.
inline void backoff(uint32_t& spins) {
    if (++spins < 64) _mm_pause();
    else {
        SwitchToThread();
        spins = 0;
    }
}

}  // namespace

struct BenchContext {
    BenchConfig cfg{};
    mtk::Workload workload = mtk::Workload::Shared;
    uint64_t checksum = 0;
    PaddedCounter shared;
    std::unique_ptr<PaddedCounter[]> padded;
    std::unique_ptr<PackedLine[]> packed;
    std::unique_ptr<Local[]> local;
    std::unique_ptr<mtk::MpmcQueue> queue;
    uint32_t lines = 0;

    void reset() {
        shared.v.store(0, std::memory_order_relaxed);
        for (uint32_t i = 0; i < cfg.threads; ++i) {
            padded[i].v.store(0, std::memory_order_relaxed);
            local[i] = Local{};
        }
        for (uint32_t l = 0; l < lines; ++l)
            for (auto& c : packed[l].v) c.store(0, std::memory_order_relaxed);
        if (queue) queue->reset();
        std::atomic_thread_fence(std::memory_order_seq_cst);
    }

    std::atomic<uint64_t>& packed_counter(uint32_t idx) { return packed[idx / kPerLine].v[idx % kPerLine]; }

    // 측정이 끝난 뒤 합계 검사. 틀린 곳 수를 돌려준다.
    uint64_t verify() {
        uint64_t bad = 0, total_ops = 0;
        for (uint32_t i = 0; i < cfg.threads; ++i) total_ops += local[i].ops;
        switch (workload) {
            case mtk::Workload::Shared:
                bad += shared.v.load() != total_ops;
                break;
            case mtk::Workload::Padded:
                for (uint32_t i = 0; i < cfg.threads; ++i) bad += padded[i].v.load() != local[i].ops;
                break;
            case mtk::Workload::FalseShare:
                for (uint32_t i = 0; i < cfg.threads; ++i) bad += packed_counter(i).load() != local[i].ops;
                break;
            case mtk::Workload::Queue: {
                uint64_t pn = 0, qn = 0, ps = 0, qs = 0;
                for (uint32_t i = 0; i < cfg.threads; ++i) {
                    pn += local[i].pushed_n;
                    qn += local[i].popped_n;
                    ps += local[i].pushed_sum;
                    qs += local[i].popped_sum;
                }
                uint64_t left = 0;
                bad += pn != qn;
                bad += ps != qs;
                bad += queue->pop(left);  // 비어 있어야 한다
                break;
            }
        }
        return bad;
    }
};

extern "C" {

BENCH_EXPORT const BenchInfo* BENCH_CALL bench_get_info(void) { return &kInfo; }

BENCH_EXPORT int32_t BENCH_CALL bench_init(const BenchConfig* config, BenchContext** out_ctx) {
    if (!out_ctx) return BENCH_E_INVALID_ARG;
    *out_ctx = nullptr;
    BenchConfig cfg;
    if (!bh::copy_config(config, cfg)) return BENCH_E_INVALID_ARG;
    mtk::Workload w;
    if (!parse_workload(cfg.workload, w) || cfg.working_set_bytes) return BENCH_E_UNSUPPORTED;
    try {
        auto ctx = std::make_unique<BenchContext>();
        ctx->cfg = cfg;
        ctx->cfg.workload = nullptr;  // 호출자 문자열을 붙잡아 두지 않는다
        ctx->workload = w;
        ctx->checksum = fixed_checksum(w, cfg.seed);
        ctx->padded.reset(new PaddedCounter[cfg.threads]);
        ctx->lines = (cfg.threads + kPerLine - 1) / kPerLine;
        ctx->packed.reset(new PackedLine[ctx->lines]);
        ctx->local.reset(new Local[cfg.threads]);
        if (w == mtk::Workload::Queue)
            ctx->queue = std::make_unique<mtk::MpmcQueue>(mtk::queue_capacity_for(cfg.threads));
        ctx->reset();
        *out_ctx = ctx.release();
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
    ctx->reset();
    const uint64_t seed = ctx->cfg.seed;

    auto count = [&](std::atomic<uint64_t>& c, uint32_t idx, bh::WorkerStats& st) {
        for (uint32_t k = 0; k < mtk::kCounterBatch; ++k) c.fetch_add(1, std::memory_order_relaxed);
        ctx->local[idx].ops += mtk::kCounterBatch;
        st.ops += mtk::kCounterBatch;
    };

    auto queue_step = [&](uint32_t idx, bh::WorkerStats& st) {
        Local& me = ctx->local[idx];
        mtk::MpmcQueue& q = *ctx->queue;
        uint32_t spins = 0;
        for (uint32_t k = 0; k < mtk::kQueueBatch; ++k) {
            const uint64_t v = mtk::item_value(seed, idx, me.pushed_n);
            while (!q.push(v)) backoff(spins);
            ++me.pushed_n;
            me.pushed_sum += v;
        }
        // 이 스레드가 넣은 만큼은 큐에 남아 있으므로 빼기는 잠깐 기다리면 반드시 성공한다
        for (uint32_t k = 0; k < mtk::kQueueBatch; ++k) {
            uint64_t v;
            while (!q.pop(v)) backoff(spins);
            ++me.popped_n;
            me.popped_sum += v;
        }
        me.ops += mtk::kQueueBatch;
        st.ops += mtk::kQueueBatch;
    };

    auto step = [&](uint32_t idx, bh::WorkerStats& st) {
        switch (ctx->workload) {
            case mtk::Workload::Shared: count(ctx->shared.v, idx, st); break;
            case mtk::Workload::Padded: count(ctx->padded[idx].v, idx, st); break;
            case mtk::Workload::FalseShare: count(ctx->packed_counter(idx), idx, st); break;
            case mtk::Workload::Queue: queue_step(idx, st); break;
        }
    };

    bh::RunTotals t;
    int32_t rc = bh::run_workers(ctx->cfg, cancel, progress, progress_user, step, t);
    if (rc != BENCH_OK) return rc;
    t.mismatches += ctx->verify();
    const bool is_queue = ctx->workload == mtk::Workload::Queue;
    bh::fill_result(out, ctx->cfg.threads, t, ctx->checksum, is_queue ? "mpmc-ring" : "lock-add", 0,
                    is_queue ? "item" : "increment");
    return bh::status_of(t);
}

BENCH_EXPORT void BENCH_CALL bench_shutdown(BenchContext* ctx) { delete ctx; }

}  // extern "C"
