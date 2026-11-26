// 점수 체계 v2 (v1: 2026-11-17, 정수·메모리만 / v2: 2026-11-26, 부동소수점 행렬곱·N-body 추가)
//
// 기준은 특정 PC가 아니라 고정 상수다. 각 항목의 측정값을 기준값 대비 비율로 바꾸고,
// 싱글·멀티 점수 각각 비율의 기하평균 × 1000을 점수로 한다.
//   - 기준값은 반올림한 숫자로 정했을 뿐 특별한 의미는 없다. 한 번 정하면 바꾸지 않고,
//     바꿔야 하면 kScoreVersion을 올린다(버전이 다른 점수끼리는 비교하지 않는다).
//   - 기하평균이라 한 항목만 유난히 높아도 점수가 과하게 오르지 않는다.
//   - 같은 CPU 사용자 대비 백분위는 결과 서버가 생기면 따로 보여 준다(기획서 결정 2026-11-17).
#pragma once

#include <cstddef>
#include <cstdint>
#include <vector>

constexpr int kScoreVersion = 2;

struct ScoreItem {
    const char* key;        // JSON 키 (예: "int_st")
    const char* label;      // 화면 표시 (영어, 러너 출력용)
    const char* module;     // 모듈 id
    const char* workload;   // nullptr = 모듈 기본
    uint64_t working_set;   // 0 = 모듈 기본
    bool multi_thread;      // false = 싱글(성능 코어 1개), true = 모든 논리 코어
    double reference;       // 이 값이면 1000점 (display 단위: unit/s, GB/s, ns)
    bool lower_is_better;   // 지연처럼 작을수록 좋은 항목
};

// v2 항목과 기준값. v1 다섯 항목의 기준값은 그대로 두고 부동소수점 네 항목을 더했다.
// 멀티코어 동기화(MT 모듈)는 코어가 많을수록 값이 낮아지는 측정이라 점수에 넣지 않는다(진단용).
inline const std::vector<ScoreItem>& score_items() {
    static const std::vector<ScoreItem> items = {
        {"int_st", "Integer        ", "int", nullptr, 0, false, 1000.0, false},
        {"mem_bw_st", "Memory read    ", "mem", "read", 256ull << 20, false, 20.0, false},
        {"mem_lat_st", "Memory latency ", "mem", "latency", 256ull << 20, false, 100.0, true},
        {"int_mt", "Integer        ", "int", nullptr, 0, true, 10000.0, false},
        {"mem_bw_mt", "Memory read    ", "mem", "read", 256ull << 20, true, 50.0, false},
        {"fp_matmul_st", "FP matmul      ", "fp", "matmul", 0, false, 5000.0, false},
        {"fp_nbody_st", "FP n-body      ", "fp", "nbody", 0, false, 5000.0, false},
        {"fp_matmul_mt", "FP matmul      ", "fp", "matmul", 0, true, 50000.0, false},
        {"fp_nbody_mt", "FP n-body      ", "fp", "nbody", 0, true, 50000.0, false},
    };
    return items;
}

// 측정값 → 기준 대비 비율 (클수록 좋음)
double score_ratio(const ScoreItem& item, double measured);

// 비율들의 기하평균 × 1000. 비율이 하나라도 0 이하이면 0.
double score_from_ratios(const std::vector<double>& ratios);
