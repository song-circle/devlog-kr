// 동기화 모듈 커널 테스트: lock-free 큐가 값을 잃거나 두 번 내주지 않는지
#include "mt_kernels.h"

#include <atomic>
#include <cstdio>
#include <thread>
#include <vector>

static int g_failed = 0;

static void check(bool ok, const char* what) {
    std::printf("[%s] %s\n", ok ? "PASS" : "FAIL", what);
    if (!ok) ++g_failed;
}

int main() {
    // 한 스레드: 넣은 순서대로 나온다, 가득 차면 push 실패, 비면 pop 실패
    {
        mtk::MpmcQueue q(4);
        check(q.capacity() == 4, "capacity rounds to power of two");
        bool pushed = true;
        for (uint64_t i = 1; i <= 4; ++i) pushed = pushed && q.push(i * 10);
        check(pushed, "push up to capacity");
        check(!q.push(99), "push fails when full");
        uint64_t v = 0;
        bool order = true;
        for (uint64_t i = 1; i <= 4; ++i) order = order && q.pop(v) && v == i * 10;
        check(order, "pop returns values in FIFO order");
        check(!q.pop(v), "pop fails when empty");
        // 한 바퀴 돈 뒤에도 같은 동작
        bool wrap = true;
        for (int round = 0; round < 10; ++round)
            for (uint64_t i = 0; i < 3; ++i) wrap = wrap && q.push(i) && q.pop(v) && v == i;
        check(wrap, "wrap-around keeps working");
        q.reset();
        check(!q.pop(v) && q.push(7) && q.pop(v) && v == 7, "reset empties the queue");
    }

    check(mtk::queue_capacity_for(1) == 1024, "min capacity 1024");
    check(mtk::queue_capacity_for(64) >= 64u * mtk::kQueueBatch * 2, "capacity grows with threads");
    check(mtk::item_value(1, 0, 0) == mtk::item_value(1, 0, 0) && mtk::item_value(1, 0, 0) != mtk::item_value(1, 0, 1) &&
              mtk::item_value(1, 0, 0) != mtk::item_value(2, 0, 0) && mtk::item_value(1, 0, 0) != mtk::item_value(1, 1, 0),
          "item values depend on seed, worker and index");

    // 여러 스레드: 모듈과 같은 방식(64개 넣고 64개 빼기)으로 돌린 뒤 합계 비교
    {
        const uint32_t threads = 8;
        const uint32_t rounds = 2000;
        mtk::MpmcQueue q(mtk::queue_capacity_for(threads));
        std::vector<uint64_t> pushed(threads), popped(threads), pushed_n(threads), popped_n(threads);
        std::atomic<int> go{0};
        std::vector<std::thread> ts;
        for (uint32_t t = 0; t < threads; ++t) {
            ts.emplace_back([&, t] {
                while (!go.load()) std::this_thread::yield();
                uint64_t n = 0;
                for (uint32_t r = 0; r < rounds; ++r) {
                    for (uint32_t k = 0; k < mtk::kQueueBatch; ++k) {
                        const uint64_t v = mtk::item_value(5, t, n++);
                        while (!q.push(v)) std::this_thread::yield();
                        pushed[t] += v;
                        ++pushed_n[t];
                    }
                    for (uint32_t k = 0; k < mtk::kQueueBatch; ++k) {
                        uint64_t v;
                        while (!q.pop(v)) std::this_thread::yield();
                        popped[t] += v;
                        ++popped_n[t];
                    }
                }
            });
        }
        go.store(1);
        for (auto& th : ts) th.join();
        uint64_t ps = 0, qs = 0, pn = 0, qn = 0;
        for (uint32_t t = 0; t < threads; ++t) {
            ps += pushed[t];
            qs += popped[t];
            pn += pushed_n[t];
            qn += popped_n[t];
        }
        uint64_t left;
        check(pn == qn && pn == uint64_t(threads) * rounds * mtk::kQueueBatch, "8 threads: counts match");
        check(ps == qs, "8 threads: sum of popped values equals sum of pushed values");
        check(!q.pop(left), "8 threads: queue is empty at the end");
    }

    std::printf("%s\n", g_failed ? "FAILED" : "ALL PASSED");
    return g_failed ? 1 : 0;
}
