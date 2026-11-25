// 동기화(MT) 모듈 커널: lock-free 큐와 값 생성
//
// 큐는 Dmitry Vyukov의 bounded MPMC 큐(칸마다 순번을 두어 넣기·빼기를 CAS 한 번으로 처리).
// 칸이 아직 다른 스레드에게 쓰이는 중이면 push/pop이 false를 돌려준다(잠깐 기다렸다 다시 시도).
#pragma once

#include <atomic>
#include <cstddef>
#include <cstdint>
#include <memory>

namespace mtk {

enum class Workload { Shared, Padded, FalseShare, Queue };

// 측정 한 덩어리(step)의 크기. 시간 확인 비용이 묻힐 만큼 크게.
constexpr uint32_t kCounterBatch = 1024;  // 원자적 +1 횟수
constexpr uint32_t kQueueBatch = 64;      // 넣고 빼는 항목 수

// 큐에 넣는 값: seed·워커·순번으로 정해진다(넣은 값 합 = 뺀 값 합으로 유실·중복을 확인).
uint64_t item_value(uint64_t seed, uint32_t worker, uint64_t n);

// 워커마다 최대 kQueueBatch개만 큐에 남겨 두므로 이 크기면 가득 차지 않는다.
size_t queue_capacity_for(uint32_t threads);

#ifdef _MSC_VER
#pragma warning(push)
#pragma warning(disable : 4324)  // 캐시 라인 정렬 때문에 생기는 패딩은 의도한 것
#endif
class MpmcQueue {
public:
    explicit MpmcQueue(size_t capacity);  // 2의 거듭제곱으로 올림
    bool push(uint64_t v);
    bool pop(uint64_t& v);
    size_t capacity() const { return mask_ + 1; }
    void reset();  // 다른 스레드가 쓰지 않을 때만

private:
    struct Cell {
        std::atomic<size_t> seq;
        uint64_t data;
    };
    std::unique_ptr<Cell[]> cells_;
    size_t mask_ = 0;
    // 넣는 쪽·빼는 쪽 위치를 서로 다른 캐시 라인에 둔다
    alignas(64) std::atomic<size_t> enq_{0};
    alignas(64) std::atomic<size_t> deq_{0};
};
#ifdef _MSC_VER
#pragma warning(pop)
#endif

}  // namespace mtk
