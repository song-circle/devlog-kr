#include "clockwatch.h"

#include <windows.h>
#include <pdh.h>

#include <cwchar>

ClockWatch::~ClockWatch() {
    if (query_) PdhCloseQuery(static_cast<PDH_HQUERY>(query_));
}

bool ClockWatch::open(uint16_t group, uint8_t number) {
    PDH_HQUERY q = nullptr;
    if (PdhOpenQueryW(nullptr, 0, &q) != ERROR_SUCCESS) return false;
    wchar_t path[128];
    PDH_HCOUNTER perf = nullptr, freq = nullptr;
    std::swprintf(path, 128, L"\\Processor Information(%u,%u)\\%% Processor Performance", group, number);
    const bool ok1 = PdhAddEnglishCounterW(q, path, 0, &perf) == ERROR_SUCCESS;
    std::swprintf(path, 128, L"\\Processor Information(%u,%u)\\Processor Frequency", group, number);
    const bool ok2 = PdhAddEnglishCounterW(q, path, 0, &freq) == ERROR_SUCCESS;
    if (!ok1 || !ok2) {
        PdhCloseQuery(q);
        return false;
    }
    query_ = q;
    perf_ = perf;
    freq_ = freq;
    PdhCollectQueryData(q);  // 비율 카운터의 첫 기준점
    return true;
}

bool ClockWatch::add_sibling(uint16_t group, uint8_t number) {
    if (!query_) return false;
    wchar_t path[128];
    std::swprintf(path, 128, L"\\Processor Information(%u,%u)\\%% Processor Time", group, number);
    PDH_HCOUNTER c = nullptr;
    if (PdhAddEnglishCounterW(static_cast<PDH_HQUERY>(query_), path, 0, &c) != ERROR_SUCCESS) return false;
    sibling_ = c;
    PdhCollectQueryData(static_cast<PDH_HQUERY>(query_));
    return true;
}

void ClockWatch::begin() {
    if (query_) PdhCollectQueryData(static_cast<PDH_HQUERY>(query_));
}

double ClockWatch::end_ghz() {
    sibling_busy_ = -1;
    if (!query_ || PdhCollectQueryData(static_cast<PDH_HQUERY>(query_)) != ERROR_SUCCESS) return 0.0;
    PDH_FMT_COUNTERVALUE s{};
    if (sibling_ && PdhGetFormattedCounterValue(static_cast<PDH_HCOUNTER>(sibling_), PDH_FMT_DOUBLE, nullptr, &s) == ERROR_SUCCESS)
        sibling_busy_ = s.doubleValue;
    PDH_FMT_COUNTERVALUE p{}, f{};
    if (PdhGetFormattedCounterValue(static_cast<PDH_HCOUNTER>(perf_), PDH_FMT_DOUBLE | PDH_FMT_NOCAP100, nullptr, &p) != ERROR_SUCCESS ||
        PdhGetFormattedCounterValue(static_cast<PDH_HCOUNTER>(freq_), PDH_FMT_DOUBLE, nullptr, &f) != ERROR_SUCCESS)
        return 0.0;
    return f.doubleValue * p.doubleValue / 100.0 / 1000.0;
}
