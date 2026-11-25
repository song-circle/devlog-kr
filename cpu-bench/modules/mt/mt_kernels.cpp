#include "mt_kernels.h"

namespace mtk {

namespace {
uint64_t splitmix64(uint64_t x) {
    x += 0x9e3779b97f4a7c15ull;
    x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9ull;
    x = (x ^ (x >> 27)) * 0x94d049bb133111ebull;
    return x ^ (x >> 31);
}
}  // namespace

uint64_t item_value(uint64_t seed, uint32_t worker, uint64_t n) {
    return splitmix64(seed ^ splitmix64((uint64_t(worker) << 40) ^ n));
}

size_t queue_capacity_for(uint32_t threads) {
    size_t need = size_t(threads) * kQueueBatch * 2;
    size_t cap = 1024;
    while (cap < need) cap <<= 1;
    return cap;
}

MpmcQueue::MpmcQueue(size_t capacity) {
    size_t cap = 2;
    while (cap < capacity) cap <<= 1;
    cells_.reset(new Cell[cap]);
    mask_ = cap - 1;
    reset();
}

void MpmcQueue::reset() {
    for (size_t i = 0; i <= mask_; ++i) {
        cells_[i].seq.store(i, std::memory_order_relaxed);
        cells_[i].data = 0;
    }
    enq_.store(0, std::memory_order_relaxed);
    deq_.store(0, std::memory_order_relaxed);
    std::atomic_thread_fence(std::memory_order_release);
}

bool MpmcQueue::push(uint64_t v) {
    size_t pos = enq_.load(std::memory_order_relaxed);
    for (;;) {
        Cell& c = cells_[pos & mask_];
        const size_t seq = c.seq.load(std::memory_order_acquire);
        const intptr_t dif = intptr_t(seq) - intptr_t(pos);
        if (dif == 0) {
            if (enq_.compare_exchange_weak(pos, pos + 1, std::memory_order_relaxed)) {
                c.data = v;
                c.seq.store(pos + 1, std::memory_order_release);
                return true;
            }
        } else if (dif < 0) {
            return false;  // 가득 참(또는 그 칸을 빼는 중)
        } else {
            pos = enq_.load(std::memory_order_relaxed);
        }
    }
}

bool MpmcQueue::pop(uint64_t& v) {
    size_t pos = deq_.load(std::memory_order_relaxed);
    for (;;) {
        Cell& c = cells_[pos & mask_];
        const size_t seq = c.seq.load(std::memory_order_acquire);
        const intptr_t dif = intptr_t(seq) - intptr_t(pos + 1);
        if (dif == 0) {
            if (deq_.compare_exchange_weak(pos, pos + 1, std::memory_order_relaxed)) {
                v = c.data;
                c.seq.store(pos + mask_ + 1, std::memory_order_release);
                return true;
            }
        } else if (dif < 0) {
            return false;  // 비었음(또는 그 칸에 넣는 중)
        } else {
            pos = deq_.load(std::memory_order_relaxed);
        }
    }
}

}  // namespace mtk
