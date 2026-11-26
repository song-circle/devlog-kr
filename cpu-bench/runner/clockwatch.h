// 측정 "도중" 한 코어의 평균 유효 클럭: Windows 성능 카운터(작업 관리자가 쓰는 것과 같은 값).
//   유효 클럭(GHz) = Processor Frequency(MHz, 기준) × % Processor Performance / 100 / 1000
// % Processor Performance는 비율 카운터라 begin()과 end() 두 번 읽은 사이의 평균이 나온다.
// 덧셈 체인(clock.h)은 측정이 끝난 "뒤"에만 잴 수 있었다(조사 006). 이건 측정과 같은 구간을 본다.
// 드라이버·관리자 권한 불필요. 카운터를 못 열면 0을 돌려준다.
// 같은 물리 코어의 하이퍼스레딩 짝(SMT 형제)이 그 구간에 얼마나 바빴는지도 함께 본다(버그 018: 짝이 바쁘면
// AVX2 행렬곱 싱글이 절반 가까이 떨어지는데, 전체 다른 프로그램 사용률로는 잡히지 않았다).
#pragma once

#include <cstdint>

class ClockWatch {
public:
    ClockWatch() = default;
    ClockWatch(const ClockWatch&) = delete;
    ClockWatch& operator=(const ClockWatch&) = delete;
    ~ClockWatch();

    // 프로세서 그룹·번호의 카운터를 연다(측정 전에 한 번, 시간이 조금 걸린다)
    bool open(uint16_t group, uint8_t number);
    // 하이퍼스레딩 짝의 사용률 카운터를 더한다(open 뒤에). 짝이 없으면 부르지 않는다.
    bool add_sibling(uint16_t group, uint8_t number);
    void begin();      // 회차 시작 직전
    double end_ghz();  // 회차 끝 직후: begin() 이후 평균 유효 클럭, 실패하면 0
    double sibling_busy_percent() const { return sibling_busy_; }  // end_ghz() 구간의 짝 사용률(%), 모르면 -1

private:
    void* query_ = nullptr;
    void* perf_ = nullptr;
    void* freq_ = nullptr;
    void* sibling_ = nullptr;
    double sibling_busy_ = -1;
};
