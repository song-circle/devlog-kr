// cpu-bench-runner: 측정 전용 프로세스 (CLI)
//
//   cpu-bench-runner info [--json]
//   cpu-bench-runner check [--seconds N] [--json]   측정 전 환경 점검
//   cpu-bench-runner clock [--json]                 코어별 유효 클럭
//   cpu-bench-runner throttle [--seconds N] [--threads N | --mt] [--json]   지속 부하 중 클럭·처리량 변화
//   cpu-bench-runner score [--runs N] [--duration-ms N] [--json]            점수(싱글·멀티, 점수 체계 v2)
//   cpu-bench-runner run <module id | dll 경로> [--workload W] [--size N] [--threads N | --mt]
//                        [--duration-ms N] [--runs N] [--seed N] [--no-pin] [--json]
//   cpu-bench-runner sweep <module> [--workload W] [--from N] [--to N] [측정 옵션...]
//
// 측정 절차: 프로세스 우선순위 HIGH → 워밍업 1회 → N회 측정 → 중앙값 채택, 변동계수(CV) 계산
// sweep은 작업 데이터 크기를 from부터 2배씩 to까지 늘려 가며 크기마다 같은 절차를 반복한다.
#include "bench_api.h"
#include "clock.h"
#include "clockwatch.h"
#include "envcheck.h"
#include "netlink.h"
#include "pcie.h"
#include "ramspec.h"
#include "score.h"
#include "smbios.h"
#include "throttle.h"
#include "topology.h"

#define WIN32_LEAN_AND_MEAN
#define NOMINMAX
#include <windows.h>

#include <algorithm>
#include <atomic>
#include <cmath>
#include <cstddef>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <vector>

namespace {

constexpr double kCvWarnPercent = 2.0;  // 재현성 목표: 반복 측정 편차 2% 이내

std::string narrow(const std::wstring& w) {
    if (w.empty()) return {};
    int n = WideCharToMultiByte(CP_UTF8, 0, w.c_str(), int(w.size()), nullptr, 0, nullptr, nullptr);
    std::string s(size_t(n), '\0');
    WideCharToMultiByte(CP_UTF8, 0, w.c_str(), int(w.size()), s.data(), n, nullptr, nullptr);
    return s;
}

std::string json_str(const std::string& s) {
    std::string o = "\"";
    for (unsigned char c : s) {
        switch (c) {
            case '"': o += "\\\""; break;
            case '\\': o += "\\\\"; break;
            case '\n': o += "\\n"; break;
            case '\r': o += "\\r"; break;
            case '\t': o += "\\t"; break;
            default:
                if (c < 0x20) {
                    char b[8];
                    std::snprintf(b, sizeof(b), "\\u%04x", c);
                    o += b;
                } else {
                    o += char(c);
                }
        }
    }
    return o + "\"";
}

const char* status_name(int32_t rc) {
    switch (rc) {
        case BENCH_OK: return "ok";
        case BENCH_E_INVALID_ARG: return "invalid_arg";
        case BENCH_E_OUT_OF_MEMORY: return "out_of_memory";
        case BENCH_E_UNSUPPORTED: return "unsupported";
        case BENCH_E_CANCELLED: return "cancelled";
        case BENCH_E_CHECKSUM: return "checksum_mismatch";
        default: return "internal_error";
    }
}

int usage() {
    std::fprintf(stderr,
                 "cpu-bench-runner " CPU_BENCH_VERSION "\n"
                 "usage:\n"
                 "  cpu-bench-runner version\n"
                 "  cpu-bench-runner info [--json]\n"
                 "  cpu-bench-runner check [--seconds N] [--json]\n"
                 "  cpu-bench-runner clock [--json]\n"
                 "  cpu-bench-runner throttle [--seconds N] [--threads N | --mt] [--json]\n"
                 "  cpu-bench-runner score [--runs N] [--duration-ms N] [--json] [--progress]\n"
                 "  cpu-bench-runner run <module id | dll path> [--workload W] [--size N] [--threads N | --mt]\n"
                 "                       [--duration-ms N] [--runs N] [--seed N] [--no-pin] [--json]\n"
                 "  cpu-bench-runner sweep <module id | dll path> [--workload W] [--from N] [--to N]\n"
                 "                       [--threads N | --mt] [--duration-ms N] [--runs N] [--seed N] [--no-pin] [--json]\n"
                 "  cpu-bench-runner scale <module id | dll path> [--workload W] [--size N] [--to N]\n"
                 "                       [--duration-ms N] [--runs N] [--seed N] [--no-pin] [--json]\n"
                 "sizes accept K/M/G suffixes (binary): 16K, 256M, 1G\n");
    return 2;
}

// ---------------------------------------------------------------- check

int cmd_check(int argc, wchar_t** argv) {
    double seconds = 3.0;
    bool json = false;
    for (int i = 2; i < argc; ++i) {
        std::wstring a = argv[i];
        if (a == L"--json") json = true;
        else if (a == L"--seconds" && i + 1 < argc) {
            wchar_t* end = nullptr;
            seconds = std::wcstod(argv[++i], &end);
            if (!end || *end || seconds < 0.5 || seconds > 60) return usage();
        } else return usage();
    }
    if (!json) std::printf("Checking the environment (sampling idle CPU use for %.1f s)...\n", seconds);
    const EnvReport r = check_environment(seconds, 5);
    auto warnings = evaluate(r);
    MemoryInfo mem;
    std::string mem_err;
    const bool mem_ok = query_memory(mem, mem_err);
    if (mem_ok)
        for (auto& w : evaluate_memory(mem)) warnings.push_back(std::move(w));
    const auto pcie = query_pcie();
    for (auto& w : evaluate_pcie(pcie)) warnings.push_back(std::move(w));
    const auto net = query_netlinks();
    for (auto& w : evaluate_netlinks(net)) warnings.push_back(std::move(w));
    if (json) {
        std::printf("{\"power\":{\"known\":%s,\"on_ac\":%s,\"has_battery\":%s,\"battery_percent\":%d},"
                    "\"power_plan\":{\"guid\":%s,\"name\":%s,\"kind\":%s,\"overlay\":%s,\"max_processor_state\":%d,"
                    "\"min_processor_state\":%d},\"background\":{\"sample_seconds\":%.1f,\"busy_percent\":%.2f,\"process_percent\":%.2f,\"top\":[",
                    r.power_known ? "true" : "false", r.on_ac ? "true" : "false", r.has_battery ? "true" : "false",
                    r.battery_percent, json_str(r.scheme_guid).c_str(), json_str(r.scheme_name).c_str(),
                    json_str(r.scheme_kind).c_str(), json_str(r.overlay).c_str(), r.max_processor_state,
                    r.min_processor_state, r.sample_seconds, r.busy_percent, r.process_percent);
        for (size_t i = 0; i < r.top.size(); ++i)
            std::printf("%s{\"name\":%s,\"pid\":%u,\"cpu_percent\":%.2f}", i ? "," : "", json_str(r.top[i].name).c_str(),
                        r.top[i].pid, r.top[i].cpu_percent);
        std::printf("]},\"pcie\":[");
        for (size_t i = 0; i < pcie.size(); ++i)
            std::printf("%s{\"kind\":%s,\"name\":%s,\"cur_gen\":%u,\"max_gen\":%u,\"cur_width\":%u,\"max_width\":%u}", i ? "," : "",
                        json_str(pcie[i].kind).c_str(), json_str(pcie[i].name).c_str(), pcie[i].cur_gen, pcie[i].max_gen,
                        pcie[i].cur_width, pcie[i].max_width);
        std::printf("],\"network\":[");
        for (size_t i = 0; i < net.size(); ++i)
            std::printf("%s{\"kind\":%s,\"name\":%s,\"rx_bps\":%llu,\"tx_bps\":%llu}", i ? "," : "", json_str(net[i].kind).c_str(),
                        json_str(net[i].name).c_str(), static_cast<unsigned long long>(net[i].rx_bps),
                        static_cast<unsigned long long>(net[i].tx_bps));
        std::printf("],\"warnings\":[");
        for (size_t i = 0; i < warnings.size(); ++i)
            std::printf("%s{\"id\":%s,\"message\":%s}", i ? "," : "", json_str(warnings[i].id).c_str(),
                        json_str(warnings[i].message).c_str());
        std::printf("]}\n");
        return 0;
    }
    if (r.power_known)
        std::printf("Power    : %s%s\n", r.on_ac ? "AC" : "battery",
                    r.has_battery ? (r.battery_percent >= 0 ? (", battery " + std::to_string(r.battery_percent) + "%").c_str() : ", battery") : "");
    std::printf("Plan     : %s (%s)\n", r.scheme_kind.c_str(), r.scheme_guid.c_str());
    std::printf("Mode     : %s\n", r.overlay.c_str());
    if (r.max_processor_state >= 0)
        std::printf("CPU state: min %d%%, max %d%% (%s)\n", r.min_processor_state, r.max_processor_state, r.on_ac ? "AC" : "battery");
    std::printf("Idle CPU : %.1f%% busy over %.1f s (processes %.1f%%, rest is interrupts/DPC)\n", r.busy_percent, r.sample_seconds, r.process_percent);
    for (const auto& p : r.top) std::printf("  %5.1f%%  %s (pid %u)\n", p.cpu_percent, p.name.c_str(), p.pid);
    if (mem_ok) {
        const unsigned ch = mem.channels();
        uint32_t speed = 0;
        for (const auto& m : mem.modules)
            if (m.size_bytes) speed = m.configured_speed;
        std::printf("Memory   : %zu module(s), %s, %u MT/s\n", mem.populated(),
                    ch == 1 ? "single channel" : ch == 2 ? "dual channel" : ch ? "multi channel" : "channel layout unknown", speed);
    } else {
        std::printf("Memory   : unavailable (%s)\n", mem_err.c_str());
    }
    for (const auto& d : pcie)
        std::printf("PCIe     : %-4s gen %u/%u, x%u/x%u  %s\n", d.kind.c_str(), d.cur_gen, d.max_gen, d.cur_width, d.max_width, d.name.c_str());
    for (const auto& l : net)
        std::printf("Network  : %-8s %.0f Mbps  %s\n", l.kind.c_str(), double(std::max(l.rx_bps, l.tx_bps)) / 1e6, l.name.c_str());
    if (warnings.empty()) std::printf("Result   : ready to measure\n");
    for (const auto& w : warnings) std::printf("Warning  : %s\n", w.message.c_str());
    return 0;
}

// ---------------------------------------------------------------- clock

// 코어마다 한 번씩 스레드를 옮겨 가며, 다른 코어가 쉬는 상태의 싱글 코어 유효 클럭을 잰다.
int cmd_clock(bool json) {
    Topology topo;
    std::string err;
    if (!query_topology(topo, err)) {
        std::fprintf(stderr, "error: %s\n", err.c_str());
        return 1;
    }
    SetPriorityClass(GetCurrentProcess(), HIGH_PRIORITY_CLASS);
    struct Row { uint32_t core; uint16_t group; uint8_t number; bool perf; CoreClock clk; };
    std::vector<Row> rows;
    for (const auto& c : topo.cpus) {
        if (!c.first_in_core) continue;
        if (!pin_current_thread(c)) continue;
        Sleep(1);  // 새 코어로 옮겨 가도록
        rows.push_back({c.core, c.group, c.number, c.efficiency == topo.max_efficiency, measure_core_clock()});
    }
    std::sort(rows.begin(), rows.end(), [](const Row& a, const Row& b) { return a.core < b.core; });
    if (json) {
        std::printf("{\"cpu\":%s,\"cores\":[", json_str(topo.brand).c_str());
        for (size_t i = 0; i < rows.size(); ++i)
            std::printf("%s{\"core\":%u,\"group\":%u,\"cpu\":%u,\"type\":\"%s\",\"max_ghz\":%.3f,\"median_ghz\":%.3f}",
                        i ? "," : "", rows[i].core, rows[i].group, rows[i].number,
                        topo.hybrid ? (rows[i].perf ? "P" : "E") : "", rows[i].clk.max_ghz, rows[i].clk.median_ghz);
        std::printf("]}\n");
        return 0;
    }
    std::printf("CPU      : %s\n", topo.brand.c_str());
    std::printf("Single-core effective clock, 30 ms warm-up + 100 ms sampling per core (max / median):\n");
    for (const auto& r : rows)
        std::printf("  core %2u%s (cpu %2u): %.2f / %.2f GHz\n", r.core, topo.hybrid ? (r.perf ? " P" : " E") : "",
                    r.number, r.clk.max_ghz, r.clk.median_ghz);
    return 0;
}

// ---------------------------------------------------------------- throttle

void throttle_progress(unsigned sec, double units, void*) {
    if (sec % 5 == 0) {
        std::printf("  %4u s  %10.1f unit/s\n", sec, units);
        std::fflush(stdout);
    }
}

int cmd_throttle(int argc, wchar_t** argv) {
    unsigned seconds = 60, threads = 0;
    bool json = false;
    for (int i = 2; i < argc; ++i) {
        std::wstring a = argv[i];
        auto next = [&](unsigned& v, unsigned lo, unsigned hi) {
            if (i + 1 >= argc) return false;
            wchar_t* end = nullptr;
            unsigned long x = std::wcstoul(argv[++i], &end, 10);
            if (!end || *end || x < lo || x > hi) return false;
            v = unsigned(x);
            return true;
        };
        if (a == L"--json") json = true;
        else if (a == L"--mt") threads = 0;
        else if (a == L"--seconds" && next(seconds, 5, 1800)) {}
        else if (a == L"--threads" && next(threads, 1, 4096)) {}
        else return usage();
    }
    Topology topo;
    std::string err;
    if (!query_topology(topo, err)) {
        std::fprintf(stderr, "error: %s\n", err.c_str());
        return 1;
    }
    if (threads == 0) threads = unsigned(topo.cpus.size());
    // 우선순위는 보통(NORMAL)으로 둔다: 모든 코어를 오래 점유하므로 HIGH면 화면이 멈춘 것처럼 보일 수 있다
    if (!json) {
        std::printf("CPU      : %s\n", topo.brand.c_str());
        std::printf("Load     : integer workload on %u threads for %u s (pinned), clock sampled every 0.25 s per thread\n",
                    threads, seconds);
    }
    const ThrottleResult r = run_throttle(topo, threads, seconds, json ? nullptr : &throttle_progress, nullptr);
    if (json) {
        std::printf("{\"cpu\":%s,\"threads\":%u,\"seconds\":%u,\"hybrid\":%s,\"timeline\":[", json_str(topo.brand).c_str(),
                    r.threads, r.seconds, r.hybrid ? "true" : "false");
        for (size_t s = 0; s < r.timeline.size(); ++s)
            std::printf("%s{\"units_per_sec\":%.1f,\"p_ghz\":%.3f,\"e_ghz\":%.3f}", s ? "," : "", r.timeline[s].units_per_sec,
                        r.timeline[s].p_ghz, r.timeline[s].e_ghz);
        std::printf("],\"start\":{\"units_per_sec\":%.1f,\"p_ghz\":%.3f,\"e_ghz\":%.3f},"
                    "\"end\":{\"units_per_sec\":%.1f,\"p_ghz\":%.3f,\"e_ghz\":%.3f},"
                    "\"throughput_change\":%.2f,\"p_change\":%.2f,\"e_change\":%.2f,\"p_drop_at\":%d,\"e_drop_at\":%d,"
                    "\"throttled\":%s}\n",
                    r.start.units_per_sec, r.start.p_ghz, r.start.e_ghz, r.end.units_per_sec, r.end.p_ghz, r.end.e_ghz,
                    r.throughput_change, r.p_change, r.e_change, r.p_drop_at, r.e_drop_at, r.throttled ? "true" : "false");
        return 0;
    }
    const char* p_label = r.hybrid ? "P clock" : "clock";
    std::printf("\n%10s %14s %10s%s\n", "time", "throughput", p_label, r.hybrid ? "    E clock" : "");
    for (unsigned s = 0; s < r.seconds; s += 5) {
        double u = 0, p = 0, e = 0;
        unsigned n = 0, np = 0, ne = 0;
        for (unsigned k = s; k < std::min(s + 5, r.seconds); ++k, ++n) {
            u += r.timeline[k].units_per_sec;
            if (r.timeline[k].p_ghz > 0) p += r.timeline[k].p_ghz, ++np;
            if (r.timeline[k].e_ghz > 0) e += r.timeline[k].e_ghz, ++ne;
        }
        std::printf("  %3u-%3u s %9.1f unit/s %6.2f GHz", s, std::min(s + 5, r.seconds), u / n, np ? p / np : 0.0);
        if (r.hybrid) std::printf("  %6.2f GHz", ne ? e / ne : 0.0);
        std::printf("\n");
    }
    std::printf("Start    : %.1f unit/s, %s %.2f GHz", r.start.units_per_sec, p_label, r.start.p_ghz);
    if (r.hybrid) std::printf(", E clock %.2f GHz", r.start.e_ghz);
    std::printf("\nEnd      : %.1f unit/s, %s %.2f GHz", r.end.units_per_sec, p_label, r.end.p_ghz);
    if (r.hybrid) std::printf(", E clock %.2f GHz", r.end.e_ghz);
    std::printf("\nChange   : throughput %+.1f%%, %s %+.1f%%", r.throughput_change, p_label, r.p_change);
    if (r.hybrid) std::printf(", E clock %+.1f%%", r.e_change);
    std::printf("\n");
    if (r.p_drop_at >= 0) std::printf("Note     : %s first dropped below the start level at %d s\n", p_label, r.p_drop_at);
    if (r.e_drop_at >= 0)
        std::printf("Note     : E clock first dropped below the start level at %d s (%+.1f%%). This can come from the\n"
                    "           CPU's power limit or its temperature; the effect on throughput is shown above.\n",
                    r.e_drop_at, r.e_change);
    if (r.throttled)
        std::printf("Warning  : performance dropped more than %.0f%% under sustained load. Check CPU cooling and BIOS power limits.\n",
                    kThrottleWarnPercent);
    else
        std::printf("Result   : no throttling (throughput and %s within %.0f%%)\n", p_label, kThrottleWarnPercent);
    return 0;
}

// ---------------------------------------------------------------- info

int cmd_info(bool json) {
    Topology topo;
    std::string err;
    if (!query_topology(topo, err)) {
        std::fprintf(stderr, "error: %s\n", err.c_str());
        return 1;
    }
    MemoryInfo mem;
    std::string mem_err;
    const bool mem_ok = query_memory(mem, mem_err);
    if (json) {
        std::printf("{\"version\":\"%s\",\"cpu\":%s,\"logical\":%zu,\"cores\":%u,\"groups\":%u,\"hybrid\":%s,\"perf_cores\":%u,\"caches\":[",
                    CPU_BENCH_VERSION, json_str(topo.brand).c_str(), topo.cpus.size(), topo.cores, topo.groups,
                    topo.hybrid ? "true" : "false", topo.perf_cores);
        for (size_t i = 0; i < topo.caches.size(); ++i) {
            const auto& c = topo.caches[i];
            std::printf("%s{\"level\":%u,\"type\":\"%s\",\"size\":%u,\"count\":%u}", i ? "," : "", c.level, c.type,
                        c.size_bytes, c.count);
        }
        std::printf("]");
        if (mem_ok) {
            std::printf(",\"memory\":{\"smbios\":\"%u.%u\",\"total_bytes\":%llu,\"populated\":%zu,\"channels\":%u,\"modules\":[",
                        mem.smbios_major, mem.smbios_minor, static_cast<unsigned long long>(mem.total_bytes()),
                        mem.populated(), mem.channels());
            bool first = true;
            for (const auto& m : mem.modules) {
                std::printf("%s{\"slot\":%s,\"bank\":%s,\"channel\":%s,\"size_bytes\":%llu,\"type\":\"%s\",\"form\":\"%s\","
                            "\"rank\":%u,\"max_speed\":%u,\"configured_speed\":%u,\"manufacturer\":%s,\"part_number\":%s}",
                            first ? "" : ",", json_str(m.device_locator).c_str(), json_str(m.bank_locator).c_str(),
                            json_str(m.channel).c_str(), static_cast<unsigned long long>(m.size_bytes),
                            memory_type_name(m.memory_type), form_factor_name(m.form_factor), m.rank, m.max_speed,
                            m.configured_speed, json_str(m.manufacturer).c_str(), json_str(m.part_number).c_str());
                first = false;
            }
            std::printf("]}");
        }
        std::printf("}\n");
        return 0;
    }
    std::printf("Version  : %s\n", CPU_BENCH_VERSION);
    std::printf("CPU      : %s\n", topo.brand.c_str());
    std::printf("Logical  : %zu  (cores %u, groups %u)\n", topo.cpus.size(), topo.cores, topo.groups);
    if (topo.hybrid)
        std::printf("Hybrid   : yes, %u performance cores / %u total\n", topo.perf_cores, topo.cores);
    else
        std::printf("Hybrid   : no\n");
    for (const auto& c : topo.caches)
        std::printf("L%u %-11s: %6u KB x %u\n", c.level, c.type, c.size_bytes / 1024, c.count);
    if (!mem_ok) {
        std::printf("Memory   : unavailable (%s)\n", mem_err.c_str());
        return 0;
    }
    const unsigned ch = mem.channels();
    std::printf("Memory   : %.0f GiB, %zu of %zu slots used, %s\n", double(mem.total_bytes()) / double(1ull << 30),
                mem.populated(), mem.modules.size(),
                ch == 1 ? "single channel" : ch == 2 ? "dual channel" : ch ? (std::to_string(ch) + " channels").c_str()
                                                                           : "channel layout unknown");
    for (const auto& m : mem.modules) {
        if (!m.size_bytes) {
            std::printf("  %-22s: empty\n", m.device_locator.c_str());
            continue;
        }
        std::printf("  %-22s: %.0f GiB %s %s, max %u MT/s, configured %u MT/s%s%s%s%s\n", m.device_locator.c_str(),
                    double(m.size_bytes) / double(1ull << 30), memory_type_name(m.memory_type),
                    form_factor_name(m.form_factor), m.max_speed, m.configured_speed,
                    m.channel.empty() ? "" : ", channel ", m.channel.c_str(),
                    m.part_number.empty() ? "" : ", ", m.part_number.c_str());
        const RatedSpeed rs = rated_speed_from_part(m.part_number);
        if (rs.mts && !rs.jedec_only)
            std::printf("  %-22s  rated %u MT/s per part number (%s)\n", "", rs.mts, rs.brand);
        else if (rs.jedec_only)
            std::printf("  %-22s  standard (JEDEC) module per part number (%s)\n", "", rs.brand);
    }
    return 0;
}

// ---------------------------------------------------------------- run / sweep

struct Module {
    HMODULE dll = nullptr;
    PFN_bench_get_info get_info = nullptr;
    PFN_bench_init init = nullptr;
    PFN_bench_run run = nullptr;
    PFN_bench_shutdown shutdown = nullptr;
    const BenchInfo* info = nullptr;
    std::vector<std::string> workloads;  // v2 모듈이 알려 준 측정 종류. 첫 번째가 기본값
};

std::wstring resolve_module(const std::wstring& arg) {
    // 확장자가 없으면 모듈 id로 보고 실행 파일 옆의 bench_<id>.dll을 찾는다
    if (arg.find(L'.') == std::wstring::npos && arg.find(L'\\') == std::wstring::npos &&
        arg.find(L'/') == std::wstring::npos) {
        wchar_t exe[MAX_PATH];
        DWORD n = GetModuleFileNameW(nullptr, exe, MAX_PATH);
        std::wstring dir(exe, n);
        dir = dir.substr(0, dir.find_last_of(L"\\/") + 1);
        return dir + L"bench_" + arg + L".dll";
    }
    wchar_t full[MAX_PATH];
    DWORD n = GetFullPathNameW(arg.c_str(), MAX_PATH, full, nullptr);
    return (n > 0 && n < MAX_PATH) ? std::wstring(full, n) : arg;
}

// FARPROC -> 실제 함수 타입. void*를 거쳐 clang-cl의 함수 타입 불일치 경고를 피한다.
template <class T>
T get_proc(HMODULE dll, const char* name) {
    return reinterpret_cast<T>(reinterpret_cast<void*>(GetProcAddress(dll, name)));
}

bool load_module(const std::wstring& path, Module& m, std::string& err) {
    m.dll = LoadLibraryExW(path.c_str(), nullptr,
                           LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR | LOAD_LIBRARY_SEARCH_DEFAULT_DIRS);
    if (!m.dll) {
        err = "cannot load " + narrow(path) + " (error " + std::to_string(GetLastError()) + ")";
        return false;
    }
    m.get_info = get_proc<PFN_bench_get_info>(m.dll, "bench_get_info");
    m.init = get_proc<PFN_bench_init>(m.dll, "bench_init");
    m.run = get_proc<PFN_bench_run>(m.dll, "bench_run");
    m.shutdown = get_proc<PFN_bench_shutdown>(m.dll, "bench_shutdown");
    if (!m.get_info || !m.init || !m.run || !m.shutdown) {
        err = "module does not export the bench_* functions";
        return false;
    }
    // 구조체는 뒤에만 필드를 추가하므로 이전 버전 모듈도 읽을 수 있다
    m.info = m.get_info();
    if (!m.info || m.info->api_version < 1 || m.info->api_version > BENCH_API_VERSION) {
        err = "module API version mismatch";
        return false;
    }
    if (m.info->struct_size >= offsetof(BenchInfo, workloads) + sizeof(m.info->workloads) && m.info->workloads) {
        std::string cur;
        for (const char* p = m.info->workloads;; ++p) {
            if (*p && *p != ',') {
                cur += *p;
                continue;
            }
            if (!cur.empty()) m.workloads.push_back(cur);
            cur.clear();
            if (!*p) break;
        }
    }
    return true;
}

struct PinPlan {
    std::vector<LogicalCpu> order;
    bool enabled = true;
    std::atomic<int> failures{0};
};

void BENCH_CALL on_thread_init(uint32_t worker, void* user) {
    auto* plan = static_cast<PinPlan*>(user);
    if (!plan->enabled || plan->order.empty()) return;
    const LogicalCpu& cpu = plan->order[worker % plan->order.size()];
    if (!pin_current_thread(cpu)) plan->failures.fetch_add(1);
}

double median(std::vector<double> v) {
    std::sort(v.begin(), v.end());
    size_t n = v.size();
    return n % 2 ? v[n / 2] : 0.5 * (v[n / 2 - 1] + v[n / 2]);
}

// "16K", "256M", "1G", "4096" (K/M/G는 1024 단위)
bool parse_size(const std::wstring& s, uint64_t& out) {
    wchar_t* end = nullptr;
    uint64_t v = std::wcstoull(s.c_str(), &end, 0);
    if (!end || end == s.c_str()) return false;
    int shift = 0;
    if (*end == L'K' || *end == L'k') shift = 10;
    else if (*end == L'M' || *end == L'm') shift = 20;
    else if (*end == L'G' || *end == L'g') shift = 30;
    if (shift) ++end;
    if (*end || v == 0 || v > (~0ull >> shift)) return false;
    out = v << shift;
    return true;
}

std::string format_size(uint64_t b) {
    char buf[32];
    const unsigned long long u = b;
    if (b >= (1ull << 30) && b % (1ull << 30) == 0) std::snprintf(buf, sizeof(buf), "%llu GiB", u >> 30);
    else if (b >= (1ull << 20) && b % (1ull << 20) == 0) std::snprintf(buf, sizeof(buf), "%llu MiB", u >> 20);
    else if (b >= (1ull << 10) && b % (1ull << 10) == 0) std::snprintf(buf, sizeof(buf), "%llu KiB", u >> 10);
    else if (b >= (1ull << 20)) std::snprintf(buf, sizeof(buf), "%.1f MiB", double(b) / double(1ull << 20));
    else if (b >= (1ull << 10)) std::snprintf(buf, sizeof(buf), "%.1f KiB", double(b) / 1024.0);
    else std::snprintf(buf, sizeof(buf), "%llu B", u);
    return buf;
}

// 결과 단위에 맞춘 표시: byte -> GB/s, load -> 로드 1회 지연(ns), 그 외 -> <unit>/s
struct Display {
    std::string raw_unit;  // 모듈이 준 op 단위
    std::string unit;      // 표시 단위
};

Display display_of(const BenchResult& r, const BenchInfo* info) {
    Display d;
    d.raw_unit = r.unit_name[0] ? r.unit_name : info->unit_name;
    if (d.raw_unit == "byte") d.unit = "GB/s";
    else if (d.raw_unit == "load") d.unit = "ns";
    else d.unit = d.raw_unit + "/s";
    return d;
}

double display_value(const BenchResult& r, const Display& d) {
    if (d.raw_unit == "byte") return r.ops_per_sec / 1e9;
    if (d.raw_unit == "load") return r.ops_per_sec > 0 ? double(r.threads_used) * 1e9 / r.ops_per_sec : 0.0;
    return r.ops_per_sec;
}

struct Options {
    std::wstring module;
    std::string workload;  // 비어 있으면 모듈 기본값
    uint64_t size = 0;     // 0이면 모듈 기본값
    uint64_t from = 16ull << 10, to = 512ull << 20;
    uint32_t threads = 1, duration_ms = 2000, runs = 5;
    uint64_t seed = 1;
    bool mt_all = false, pin = true, json = false;
    uint32_t max_threads = 0;  // scale: 0이면 논리 CPU 수
};

bool parse_options(int argc, wchar_t** argv, bool sweep, Options& o, bool scale = false) {
    if (argc < 3) return false;
    o.module = argv[2];
    if (sweep || scale) {
        o.duration_ms = 500;
        o.runs = 3;
    }
    for (int i = 3; i < argc; ++i) {
        std::wstring a = argv[i];
        const bool has_value = i + 1 < argc;
        auto next_u64 = [&](uint64_t& v) {
            if (!has_value) return false;
            wchar_t* end = nullptr;
            v = std::wcstoull(argv[++i], &end, 0);
            return end && *end == 0;
        };
        auto next_size = [&](uint64_t& v) { return has_value && parse_size(argv[++i], v); };
        uint64_t v = 0;
        if (scale && a == L"--to") {
            if (!next_u64(v) || v == 0 || v > 4096) return false;
            o.max_threads = uint32_t(v);
        } else if (!scale && a == L"--threads" && next_u64(v) && v > 0 && v <= 4096) o.threads = uint32_t(v);
        else if (!scale && a == L"--mt") o.mt_all = true;
        else if (a == L"--duration-ms" && next_u64(v) && v > 0 && v <= 3600000) o.duration_ms = uint32_t(v);
        else if (a == L"--runs" && next_u64(v) && v > 0 && v <= 1000) o.runs = uint32_t(v);
        else if (a == L"--seed" && next_u64(v)) o.seed = v;
        else if (a == L"--no-pin") o.pin = false;
        else if (a == L"--json") o.json = true;
        else if (a == L"--workload" && has_value) o.workload = narrow(argv[++i]);
        else if (!sweep && a == L"--size" && next_size(v)) o.size = v;
        else if (sweep && a == L"--from" && next_size(v)) o.from = v;
        else if (sweep && a == L"--to" && next_size(v)) o.to = v;
        else return false;
    }
    return !sweep || o.from <= o.to;
}

// 측정 한 세트: bench_init → 워밍업 1회 → N회 → bench_shutdown
// 측정 1회 동안 다른 프로그램이 쓴 CPU: 시스템 전체 사용 시간에서 이 프로세스 사용 시간을 뺀 값
struct CpuSample {
    uint64_t idle = 0, total = 0, self = 0;
};

uint64_t ft_u64(const FILETIME& f) { return (uint64_t(f.dwHighDateTime) << 32) | f.dwLowDateTime; }

CpuSample sample_cpu() {
    CpuSample s;
    FILETIME i, k, u, c, e, pk, pu;
    if (GetSystemTimes(&i, &k, &u)) {
        s.idle = ft_u64(i);
        s.total = ft_u64(k) + ft_u64(u);  // 커널 시간에 idle이 포함됨
    }
    if (GetProcessTimes(GetCurrentProcess(), &c, &e, &pk, &pu)) s.self = ft_u64(pk) + ft_u64(pu);
    return s;
}

// 전체 CPU 용량 대비 % (논리 코어 전부 = 100%)
double other_cpu_percent(const CpuSample& a, const CpuSample& b) {
    const uint64_t total = b.total - a.total;
    if (!total) return 0.0;
    const uint64_t busy = total - std::min(total, b.idle - a.idle);
    const uint64_t self = b.self - a.self;
    return 100.0 * double(busy > self ? busy - self : 0) / double(total);
}

struct Measurement {
    std::vector<BenchResult> results;
    std::vector<double> values;  // 표시 단위 값
    std::vector<double> others;  // 회차마다 다른 프로그램이 쓴 CPU %
    std::vector<double> clocks;  // 싱글 측정: 회차 직후 그 코어의 유효 클럭(GHz), 멀티는 0
    std::vector<double> clocks_during;  // 싱글 측정: 회차 도중 그 코어의 평균 유효 클럭(성능 카운터, GHz), 못 재면 0
    std::vector<double> siblings;       // 싱글 측정: 회차 도중 같은 코어의 하이퍼스레딩 짝 사용률(%), 모르면 -1
    std::vector<std::string> tops;      // 회차마다 CPU를 많이 쓴 다른 프로그램 상위 3개 ("name 1.2%, ...")
    std::vector<std::pair<double, std::string>> rejected;  // 방해로 버린 회차: (다른 프로그램 %, 상위 3개)
    Display display;
    double median = 0, cv = 0;
    uint64_t mismatches = 0;
    bool ok = false;  // 회차 간 checksum 같음 + 불일치 0
    uint32_t retries = 0;      // 방해 때문에 다시 잰 횟수
    double others_max = 0;     // 채택한 회차 중 다른 프로그램 사용률 최댓값
    double sibling_max = -1;   // 채택한 회차 중 하이퍼스레딩 짝 사용률 최댓값(싱글만, 모르면 -1)
};

// 다른 프로그램이 max_others_percent를 넘게 CPU를 쓴 회차는 버리고 다시 잰다(최대 max_retries번).
// 기본값(음수)이면 다시 재지 않는다.
// 싱글 측정은 같은 물리 코어의 하이퍼스레딩 짝이 max_sibling_percent를 넘게 바빴던 회차도 다시 잰다(버그 018).
struct RetryPolicy {
    double max_others_percent = -1;
    uint32_t max_retries = 0;
    double max_sibling_percent = -1;
};

template <class OnRun>
int32_t measure(const Module& mod, const BenchConfig& cfg, uint32_t runs, Measurement& m, OnRun&& on_run,
                RetryPolicy policy = {}) {
    BenchContext* ctx = nullptr;
    int32_t rc = mod.init(&cfg, &ctx);
    if (rc != BENCH_OK) return rc;
    int32_t worst = BENCH_OK;
    // 싱글 측정: 0번 워커는 고정 계획의 첫 논리 CPU(성능 코어)에 고정된다 → 그 CPU의 클럭을 회차 도중 내내 본다.
    // 버그 018 전에는 이 스레드가 지금 돌고 있는 CPU(GetCurrentProcessorNumberEx)를 봤는데, 이 스레드는 고정되지 않아서
    // 실제로 측정하는 코어가 아니라 그때그때 다른 코어의 클럭을 읽고 있었다.
    // 같은 물리 코어의 하이퍼스레딩 짝이 바쁘면 AVX2 행렬곱 싱글이 절반 가까이 떨어지므로 짝의 사용률도 본다.
    ClockWatch watch;
    if (cfg.threads == 1 && cfg.thread_init == &on_thread_init && cfg.user) {
        const auto* plan = static_cast<const PinPlan*>(cfg.user);
        if (plan->enabled && !plan->order.empty()) {
            const LogicalCpu& me = plan->order[0];
            watch.open(me.group, me.number);
            for (const auto& c : plan->order)
                if (c.core == me.core && (c.group != me.group || c.number != me.number)) {
                    watch.add_sibling(c.group, c.number);
                    break;
                }
        }
    }
    auto top_text = [](const ProcessSnapshot& a, const ProcessSnapshot& b) {
        std::string t;
        char buf[160];
        for (const auto& p : top_between(a, b, 3)) {
            std::snprintf(buf, sizeof(buf), "%s%s %.1f%%", t.empty() ? "" : ", ", p.name.c_str(), p.cpu_percent);
            t += buf;
        }
        return t;
    };
    for (uint32_t r = 0; r <= runs; ++r) {  // r == 0은 워밍업
        BenchResult res{};
        res.struct_size = sizeof(res);
        const ProcessSnapshot snap_a = take_process_snapshot();
        const CpuSample before = sample_cpu();
        watch.begin();
        rc = mod.run(ctx, &res, nullptr, nullptr, nullptr);
        const double clock_during = cfg.threads == 1 ? watch.end_ghz() : 0.0;
        const double sibling = cfg.threads == 1 ? watch.sibling_busy_percent() : -1.0;
        const double others = other_cpu_percent(before, sample_cpu());
        const ProcessSnapshot snap_b = take_process_snapshot();
        // 싱글 측정은 이 스레드(성능 코어에 고정됨)가 0번 워커라, 끝나자마자 같은 코어의 클럭을 잴 수 있다.
        // 코어가 아직 바쁜 상태라 워밍업 없이 짧게 여러 번 재서 가장 높은 값을 쓴다.
        double clock_ghz = 0;
        if (cfg.threads == 1)
            for (int k = 0; k < 5; ++k) clock_ghz = std::max(clock_ghz, measure_clock_ghz(500000));
        if (rc != BENCH_OK && rc != BENCH_E_CHECKSUM) {
            mod.shutdown(ctx);
            return rc;
        }
        if (rc == BENCH_E_CHECKSUM) worst = rc;
        if (r == 0) continue;
        const bool busy_others = policy.max_others_percent >= 0 && others > policy.max_others_percent;
        const bool busy_sibling = policy.max_sibling_percent >= 0 && sibling > policy.max_sibling_percent;
        if ((busy_others || busy_sibling) && m.retries < policy.max_retries) {
            std::string why = top_text(snap_a, snap_b);
            if (busy_sibling) {
                char buf[64];
                std::snprintf(buf, sizeof(buf), "%sSMT sibling %.0f%%", why.empty() ? "" : ", ", sibling);
                why += buf;
            }
            m.rejected.push_back({others, why});
            ++m.retries;  // 이 회차는 버리고 같은 번호로 다시
            --r;
            continue;
        }
        m.others_max = std::max(m.others_max, others);
        if (m.results.empty()) m.display = display_of(res, mod.info);
        m.results.push_back(res);
        m.values.push_back(display_value(res, m.display));
        m.others.push_back(others);
        m.clocks.push_back(clock_ghz);
        m.clocks_during.push_back(clock_during);
        m.siblings.push_back(sibling);
        m.sibling_max = std::max(m.sibling_max, sibling);
        m.tops.push_back(top_text(snap_a, snap_b));
        on_run(r, res, m.values.back());
    }
    mod.shutdown(ctx);

    bool stable = true;
    for (const auto& r : m.results) {
        m.mismatches += r.mismatches;
        if (r.checksum != m.results.front().checksum) stable = false;
    }
    double mean = 0;
    for (double v : m.values) mean += v;
    mean /= double(m.values.size());
    double var = 0;
    for (double v : m.values) var += (v - mean) * (v - mean);
    double stdev = m.values.size() > 1 ? std::sqrt(var / double(m.values.size() - 1)) : 0.0;
    m.cv = mean > 0 ? 100.0 * stdev / mean : 0.0;
    m.median = median(m.values);
    m.ok = m.mismatches == 0 && stable && worst == BENCH_OK;
    return BENCH_OK;
}

// run / sweep 공통 준비: 토폴로지, 모듈 로드, 우선순위, 코어 고정 계획, 기본 설정
struct Session {
    Topology topo;
    Module mod;
    PinPlan plan;
    bool high_priority = false;
    std::string workload;  // 표시용 (지정하지 않으면 모듈의 첫 번째 종류)
    BenchConfig cfg{};
};

bool open_session(const Options& o, Session& s) {
    std::string err;
    if (!query_topology(s.topo, err) || !load_module(resolve_module(o.module), s.mod, err)) {
        std::fprintf(stderr, "error: %s\n", err.c_str());
        return false;
    }
    if (s.mod.workloads.empty() && (!o.workload.empty() || o.size)) {
        std::fprintf(stderr, "error: module '%s' does not support --workload/--size\n", s.mod.info->id);
        return false;
    }
    if (!o.workload.empty() &&
        std::find(s.mod.workloads.begin(), s.mod.workloads.end(), o.workload) == s.mod.workloads.end()) {
        std::string list;
        for (const auto& w : s.mod.workloads) list += (list.empty() ? "" : ", ") + w;
        std::fprintf(stderr, "error: unknown workload '%s' (available: %s)\n", o.workload.c_str(), list.c_str());
        return false;
    }
    s.workload = !o.workload.empty() ? o.workload : (s.mod.workloads.empty() ? "" : s.mod.workloads.front());

    // REALTIME은 쓰지 않는다 (시스템이 멈출 수 있음). HIGH만 사용.
    s.high_priority = SetPriorityClass(GetCurrentProcess(), HIGH_PRIORITY_CLASS) != 0;
    s.plan.enabled = o.pin;
    s.plan.order = placement_order(s.topo);

    s.cfg.struct_size = sizeof(s.cfg);
    s.cfg.threads = o.mt_all ? uint32_t(s.topo.cpus.size()) : o.threads;
    s.cfg.duration_ms = o.duration_ms;
    s.cfg.flags = BENCH_FLAG_NONE;
    s.cfg.seed = o.seed;
    s.cfg.thread_init = &on_thread_init;
    s.cfg.user = &s.plan;
    s.cfg.workload = o.workload.empty() ? nullptr : o.workload.c_str();
    s.cfg.working_set_bytes = o.size;
    return true;
}

void report_error(int32_t rc, const Session& s) {
    if (rc == BENCH_E_INVALID_ARG && s.cfg.working_set_bytes)
        std::fprintf(stderr, "error: working set %s is too small for %u threads (min 4 KiB per thread)\n",
                     format_size(s.cfg.working_set_bytes).c_str(), s.cfg.threads);
    else
        std::fprintf(stderr, "error: benchmark failed (%s)\n", status_name(rc));
}

const char* pin_text(const Options& o, const Session& s) {
    if (!o.pin) return "";
    return s.cfg.threads == 1 && s.topo.hybrid ? ", pinned to a performance core" : ", pinned";
}

void print_session_warnings(const Session& s) {
    if (!s.high_priority) std::printf("Warning  : could not raise process priority\n");
    if (s.plan.failures) std::printf("Warning  : %d thread(s) could not be pinned\n", s.plan.failures.load());
}

int cmd_run(int argc, wchar_t** argv) {
    Options o;
    if (!parse_options(argc, argv, false, o)) return usage();
    Session s;
    if (!open_session(o, s)) return 1;
    const BenchInfo* info = s.mod.info;

    if (!o.json) {
        std::printf("Module   : %s %s\n", info->name, info->version);
        std::printf("CPU      : %s\n", s.topo.brand.c_str());
        std::printf("Threads  : %u%s, %u ms x %u runs (+1 warm-up), seed %llu\n", s.cfg.threads, pin_text(o, s),
                    o.duration_ms, o.runs, static_cast<unsigned long long>(o.seed));
    }

    Measurement m;
    int32_t rc = measure(s.mod, s.cfg, o.runs, m, [&](uint32_t r, const BenchResult& res, double v) {
        if (o.json) return;
        if (r == 1 && !s.workload.empty())
            std::printf("Workload : %s, %s\n", s.workload.c_str(), format_size(res.working_set_bytes).c_str());
        std::printf("  run %u  : %12.2f %-6s (%llu %ss, %.3f s)%s", r, v, m.display.unit.c_str(),
                    static_cast<unsigned long long>(res.iterations), m.display.raw_unit.c_str(), res.elapsed_s,
                    res.mismatches ? "  CHECKSUM MISMATCH" : "");
        if (!m.siblings.empty() && m.siblings.back() >= 0) std::printf("  [SMT sibling busy %.0f%%]", m.siblings.back());
        std::printf("\n");
    });
    if (rc != BENCH_OK) {
        report_error(rc, s);
        return 1;
    }

    const BenchResult& first = m.results.front();
    if (o.json) {
        std::printf("{\"module\":%s,\"module_version\":%s,\"workload\":%s,\"working_set_bytes\":%llu,"
                    "\"variant\":%s,\"cpu\":%s,\"threads\":%u,\"pinned\":%s,\"pin_failures\":%d,"
                    "\"high_priority\":%s,\"duration_ms\":%u,\"seed\":%llu,\"unit\":%s,\"value_unit\":%s,\"runs\":[",
                    json_str(info->id).c_str(), json_str(info->version).c_str(), json_str(s.workload).c_str(),
                    static_cast<unsigned long long>(first.working_set_bytes), json_str(first.variant).c_str(),
                    json_str(s.topo.brand).c_str(), s.cfg.threads, o.pin ? "true" : "false",
                    s.plan.failures.load(), s.high_priority ? "true" : "false", o.duration_ms,
                    static_cast<unsigned long long>(o.seed), json_str(m.display.raw_unit).c_str(),
                    json_str(m.display.unit).c_str());
        std::vector<double> ops;
        for (size_t i = 0; i < m.results.size(); ++i) {
            const BenchResult& r = m.results[i];
            ops.push_back(r.ops_per_sec);
            std::printf("%s{\"ops_per_sec\":%.3f,\"value\":%.4f,\"iterations\":%llu,\"elapsed_s\":%.6f,"
                        "\"mismatches\":%llu,\"others_cpu_percent\":%.2f,\"clock_ghz\":%.3f,\"clock_during_ghz\":%.3f,"
                        "\"sibling_busy_percent\":%.2f,\"others_top\":%s}",
                        i ? "," : "", r.ops_per_sec, m.values[i], static_cast<unsigned long long>(r.iterations),
                        r.elapsed_s, static_cast<unsigned long long>(r.mismatches), m.others[i], m.clocks[i],
                        m.clocks_during[i], m.siblings[i], json_str(m.tops[i]).c_str());
        }
        std::printf("],\"median_ops_per_sec\":%.3f,\"median_value\":%.4f,\"cv_percent\":%.3f,"
                    "\"checksum\":\"0x%016llx\",\"checksum_ok\":%s}\n",
                    median(ops), m.median, m.cv, static_cast<unsigned long long>(first.checksum),
                    m.ok ? "true" : "false");
    } else {
        std::printf("Median   : %.2f %s  (CV %.2f%%)\n", m.median, m.display.unit.c_str(), m.cv);
        std::printf("Variant  : %s\n", first.variant);
        std::printf("Checksum : 0x%016llx %s\n", static_cast<unsigned long long>(first.checksum),
                    m.ok ? "(stable)" : "(MISMATCH - results are not trustworthy)");
        if (m.cv > kCvWarnPercent)
            std::printf("Warning  : run-to-run variation is above %.0f%%. Close background apps and retry.\n",
                        kCvWarnPercent);
        print_session_warnings(s);
    }
    return m.ok ? 0 : 3;
}

int cmd_sweep(int argc, wchar_t** argv) {
    Options o;
    if (!parse_options(argc, argv, true, o)) return usage();
    Session s;
    if (!open_session(o, s)) return 1;
    const BenchInfo* info = s.mod.info;
    if (s.mod.workloads.empty()) {
        std::fprintf(stderr, "error: module '%s' has a fixed working set; use 'run'\n", info->id);
        return 1;
    }

    if (o.json) {
        std::printf("{\"module\":%s,\"module_version\":%s,\"workload\":%s,\"cpu\":%s,\"threads\":%u,\"pinned\":%s,"
                    "\"duration_ms\":%u,\"runs\":%u,\"seed\":%llu,\"points\":[",
                    json_str(info->id).c_str(), json_str(info->version).c_str(), json_str(s.workload).c_str(),
                    json_str(s.topo.brand).c_str(), s.cfg.threads, o.pin ? "true" : "false", o.duration_ms, o.runs,
                    static_cast<unsigned long long>(o.seed));
    } else {
        std::printf("Module   : %s %s\n", info->name, info->version);
        std::printf("CPU      : %s\n", s.topo.brand.c_str());
        std::printf("Workload : %s\n", s.workload.c_str());
        std::printf("Threads  : %u%s, %u ms x %u runs per size (+1 warm-up), seed %llu\n", s.cfg.threads,
                    pin_text(o, s), o.duration_ms, o.runs, static_cast<unsigned long long>(o.seed));
        std::string caches;
        for (const auto& c : s.topo.caches) {
            if (std::string(c.type) == "instruction") continue;
            char b[64];
            std::snprintf(b, sizeof(b), "%sL%u %s x %u", caches.empty() ? "" : ", ", c.level,
                          format_size(c.size_bytes).c_str(), c.count);
            caches += b;
        }
        std::printf("Caches   : %s\n", caches.c_str());
        std::printf("\n%12s %14s %9s  %s\n", "Size", "Median", "CV", "Checksum");
    }

    bool all_ok = true;
    for (uint64_t size = o.from;; size *= 2) {
        s.cfg.working_set_bytes = size;
        Measurement m;
        int32_t rc = measure(s.mod, s.cfg, o.runs, m, [](uint32_t, const BenchResult&, double) {});
        if (rc != BENCH_OK) {
            if (o.json) std::printf("]}\n");
            report_error(rc, s);
            return 1;
        }
        all_ok = all_ok && m.ok;
        const BenchResult& first = m.results.front();
        if (o.json) {
            std::printf("%s{\"working_set_bytes\":%llu,\"median_value\":%.4f,\"value_unit\":%s,\"cv_percent\":%.3f,"
                        "\"checksum\":\"0x%016llx\",\"checksum_ok\":%s}",
                        size == o.from ? "" : ",", static_cast<unsigned long long>(first.working_set_bytes),
                        m.median, json_str(m.display.unit).c_str(), m.cv,
                        static_cast<unsigned long long>(first.checksum), m.ok ? "true" : "false");
        } else {
            char val[32];
            std::snprintf(val, sizeof(val), "%.2f %s", m.median, m.display.unit.c_str());
            std::printf("%12s %14s %8.2f%%  %s%s\n", format_size(first.working_set_bytes).c_str(), val, m.cv,
                        m.ok ? "ok" : "MISMATCH", m.cv > kCvWarnPercent ? "  (unstable)" : "");
            std::fflush(stdout);
        }
        if (size > o.to / 2) break;
    }
    if (o.json) {
        std::printf("],\"checksum_ok\":%s}\n", all_ok ? "true" : "false");
    } else {
        print_session_warnings(s);
        if (!all_ok) std::printf("Checksum mismatch detected - results are not trustworthy\n");
    }
    return all_ok ? 0 : 3;
}

// ---------------------------------------------------------------- scale

// 확장 곡선: 같은 측정을 스레드 1개부터 N개(기본: 논리 CPU 수)까지 늘려 가며 반복한다.
// 스레드는 run --mt와 같은 순서로 코어에 고정된다(성능 코어 → 효율 코어 → SMT 형제).
// 표의 "Added"는 그 줄에서 새로 붙은 스레드가 앉은 코어 종류다.
int cmd_scale(int argc, wchar_t** argv) {
    Options o;
    if (!parse_options(argc, argv, false, o, true)) return usage();
    Session s;
    if (!open_session(o, s)) return 1;
    const BenchInfo* info = s.mod.info;
    const uint32_t max_t = o.max_threads ? o.max_threads : uint32_t(s.topo.cpus.size());

    auto added_core = [&](uint32_t t) -> std::string {
        if (!o.pin || s.plan.order.empty()) return "-";
        const LogicalCpu& c = s.plan.order[(t - 1) % s.plan.order.size()];
        std::string k = !s.topo.hybrid ? "core" : (c.efficiency == s.topo.max_efficiency ? "P" : "E");
        if (!c.first_in_core) k += "-SMT";
        if (t > s.plan.order.size()) k += "+";  // 논리 CPU보다 스레드가 많음(같은 CPU를 나눠 씀)
        return k;
    };

    if (o.json) {
        std::printf("{\"module\":%s,\"module_version\":%s,\"workload\":%s,\"cpu\":%s,\"hybrid\":%s,\"pinned\":%s,"
                    "\"duration_ms\":%u,\"runs\":%u,\"seed\":%llu,\"points\":[",
                    json_str(info->id).c_str(), json_str(info->version).c_str(), json_str(s.workload).c_str(),
                    json_str(s.topo.brand).c_str(), s.topo.hybrid ? "true" : "false", o.pin ? "true" : "false",
                    o.duration_ms, o.runs, static_cast<unsigned long long>(o.seed));
    } else {
        std::printf("Module   : %s %s\n", info->name, info->version);
        std::printf("CPU      : %s (%zu logical, %u cores%s)\n", s.topo.brand.c_str(), s.topo.cpus.size(),
                    s.topo.cores, s.topo.hybrid ? ", hybrid" : "");
        if (!s.workload.empty()) std::printf("Workload : %s\n", s.workload.c_str());
        std::printf("Threads  : 1 to %u%s, %u ms x %u runs each (+1 warm-up), seed %llu\n", max_t,
                    o.pin ? ", pinned P -> E -> SMT" : "", o.duration_ms, o.runs,
                    static_cast<unsigned long long>(o.seed));
    }

    bool all_ok = true;
    double base = 0;
    for (uint32_t t = 1; t <= max_t; ++t) {
        s.cfg.threads = t;
        Measurement m;
        int32_t rc = measure(s.mod, s.cfg, o.runs, m, [](uint32_t, const BenchResult&, double) {});
        if (rc != BENCH_OK) {
            if (o.json) std::printf("]}\n");
            report_error(rc, s);
            return 1;
        }
        all_ok = all_ok && m.ok;
        std::vector<double> ops;
        for (const auto& r : m.results) ops.push_back(r.ops_per_sec);
        const double total = median(ops);
        if (t == 1) {
            base = total;
            if (!o.json) {
                std::printf("Unit     : %s/s (all threads together)\n\n", m.display.raw_unit.c_str());
                std::printf("%7s %-7s %16s %9s %11s %8s  %s\n", "Threads", "Added", "Throughput", "Speedup",
                            "Efficiency", "CV", "Check");
            }
        }
        const double speedup = base > 0 ? total / base : 0.0;
        const double eff = speedup / double(t);
        const std::string core = added_core(t);
        if (o.json) {
            std::printf("%s{\"threads\":%u,\"added_core\":%s,\"ops_per_sec\":%.3f,\"unit\":%s,\"speedup\":%.4f,"
                        "\"efficiency\":%.4f,\"cv_percent\":%.3f,\"checksum_ok\":%s}",
                        t == 1 ? "" : ",", t, json_str(core).c_str(), total, json_str(m.display.raw_unit).c_str(),
                        speedup, eff, m.cv, m.ok ? "true" : "false");
        } else {
            std::printf("%7u %-7s %16.0f %8.2fx %10.0f%% %7.2f%%  %s%s\n", t, core.c_str(), total, speedup,
                        100.0 * eff, m.cv, m.ok ? "ok" : "MISMATCH", m.cv > kCvWarnPercent ? "  (unstable)" : "");
            std::fflush(stdout);
        }
    }
    if (o.json) {
        std::printf("],\"checksum_ok\":%s}\n", all_ok ? "true" : "false");
    } else {
        print_session_warnings(s);
        if (!all_ok) std::printf("Checksum mismatch detected - results are not trustworthy\n");
    }
    return all_ok ? 0 : 3;
}

// ---------------------------------------------------------------- score

// 점수 체계 v2: 싱글·멀티 점수. 항목·기준값은 score.h (v2에서 부동소수점 행렬곱·N-body 추가)
// 측정 절차: 항목마다 워밍업 1회 + 5회, 중앙값. 다른 프로그램이 CPU를 7% 넘게 쓴 회차는 버리고 다시 잰다
// (항목당 최대 5번). 싱글 메모리 읽기가 가끔 10% 떨어지는 회차 때문에 3회 중앙값으로는 싱글 점수가 흔들렸다(조사 006).
// 통제 실험: 바쁜 프로그램 하나만 띄워도 싱글 정수가 평균 2.8% 떨어졌고, 그때 다른 프로그램 사용률은 평균 10.6%였다.
constexpr double kScoreMaxOthersPercent = 7.0;  // 개발 PC 평소 3%대, 바쁜 프로그램 하나(16개 중 1개)면 10% 안팎 → 그 경우를 걸러 낸다
constexpr uint32_t kScoreMaxRetries = 5;
// 싱글 측정 코어의 하이퍼스레딩 짝: 개발 PC 평소 0~5%. 짝이 100% 바쁘면 AVX2 행렬곱 싱글이 약 10,000 → 5,700으로
// 떨어지는데(버그 018), 전체 다른 프로그램 사용률로는 1/16밖에 안 보여서 7% 기준에 걸리지 않았다.
constexpr double kScoreMaxSiblingPercent = 10.0;

int cmd_score(int argc, wchar_t** argv) {
    uint32_t runs = 5, duration_ms = 2000;
    bool json = false, progress = false;
    for (int i = 2; i < argc; ++i) {
        std::wstring a = argv[i];
        auto next = [&](uint32_t& v, uint32_t lo, uint32_t hi) {
            if (i + 1 >= argc) return false;
            wchar_t* end = nullptr;
            unsigned long x = std::wcstoul(argv[++i], &end, 10);
            if (!end || *end || x < lo || x > hi) return false;
            v = uint32_t(x);
            return true;
        };
        if (a == L"--json") json = true;
        else if (a == L"--progress") progress = true;
        else if (a == L"--runs" && next(runs, 1, 100)) {}
        else if (a == L"--duration-ms" && next(duration_ms, 100, 600000)) {}
        else return usage();
    }

    struct Row {
        const ScoreItem* item; double value; double ratio; std::string unit; std::string variant; uint64_t checksum; bool ok;
        uint32_t retries; double others_max;
        Measurement m;  // 회차별 기록(값, 다른 프로그램, 도중 클럭, 버린 회차) → JSON
    };
    std::vector<Row> rows;
    std::string cpu;
    bool all_ok = true;
    if (!json) std::printf("Score    : version %d, %u ms x %u runs per item (+1 warm-up)\n", kScoreVersion, duration_ms, runs);
    for (const ScoreItem& it : score_items()) {
        Options o;
        o.module.assign(it.module, it.module + std::strlen(it.module));
        if (it.workload) o.workload = it.workload;
        o.size = it.working_set;
        o.mt_all = it.multi_thread;
        o.duration_ms = duration_ms;
        o.runs = runs;
        Session s;
        if (!open_session(o, s)) return 1;
        cpu = s.topo.brand;
        if (!json) {
            std::printf("  %s %-6s ... ", it.label, it.multi_thread ? "(MT)" : "(ST)");
            std::fflush(stdout);
        }
        if (progress) {  // 화면 앱용: stderr에 한 줄 JSON
            std::fprintf(stderr, "{\"progress\":%zu,\"total\":%zu,\"item\":%s}\n", rows.size(), score_items().size(),
                         json_str(it.key).c_str());
            std::fflush(stderr);
        }
        Measurement m;
        int32_t rc = measure(s.mod, s.cfg, o.runs, m, [](uint32_t, const BenchResult&, double) {},
                             RetryPolicy{kScoreMaxOthersPercent, kScoreMaxRetries, kScoreMaxSiblingPercent});
        if (rc != BENCH_OK) {
            report_error(rc, s);
            return 1;
        }
        const double ratio = score_ratio(it, m.median);
        rows.push_back({&it, m.median, ratio, m.display.unit, m.results.front().variant, m.results.front().checksum, m.ok, m.retries, m.others_max, m});
        all_ok = all_ok && m.ok;
        if (!json) {
            std::printf("%10.2f %-6s (reference %g, x%.3f)%s", m.median, m.display.unit.c_str(), it.reference, ratio,
                        m.ok ? "" : "  CHECKSUM MISMATCH");
            if (m.retries) std::printf("  [%u run(s) redone: other programs were busy]", m.retries);
            std::printf("\n");
        }
    }
    std::vector<double> st, mt;
    for (const auto& r : rows) (r.item->multi_thread ? mt : st).push_back(r.ratio);
    const double st_score = all_ok ? score_from_ratios(st) : 0.0;
    const double mt_score = all_ok ? score_from_ratios(mt) : 0.0;

    if (json) {
        bool disturbed = false;  // 다시 재기를 다 써도 방해가 남은 항목이 있음
        for (const auto& r : rows)
            disturbed = disturbed || r.others_max > kScoreMaxOthersPercent || r.m.sibling_max > kScoreMaxSiblingPercent;
        std::printf("{\"score_version\":%d,\"cpu\":%s,\"single\":%.0f,\"multi\":%.0f,\"valid\":%s,\"disturbed\":%s,"
                    "\"max_others_percent\":%g,\"items\":[",
                    kScoreVersion, json_str(cpu).c_str(), st_score, mt_score, all_ok ? "true" : "false",
                    disturbed ? "true" : "false", kScoreMaxOthersPercent);
        for (size_t i = 0; i < rows.size(); ++i) {
            std::printf("%s{\"key\":%s,\"value\":%.4f,\"unit\":%s,\"reference\":%g,\"ratio\":%.4f,\"checksum\":\"0x%016llx\","
                        "\"variant\":%s,\"checksum_ok\":%s,\"retries\":%u,\"others_max_percent\":%.2f,\"runs\":[",
                        i ? "," : "", json_str(rows[i].item->key).c_str(), rows[i].value, json_str(rows[i].unit).c_str(),
                        rows[i].item->reference, rows[i].ratio, static_cast<unsigned long long>(rows[i].checksum),
                        json_str(rows[i].variant).c_str(), rows[i].ok ? "true" : "false", rows[i].retries, rows[i].others_max);
            const Measurement& m = rows[i].m;
            for (size_t k = 0; k < m.values.size(); ++k)
                std::printf("%s{\"value\":%.4f,\"others_cpu_percent\":%.2f,\"clock_ghz\":%.3f,\"clock_during_ghz\":%.3f,"
                            "\"sibling_busy_percent\":%.2f,\"others_top\":%s}",
                            k ? "," : "", m.values[k], m.others[k], m.clocks[k], m.clocks_during[k], m.siblings[k],
                            json_str(m.tops[k]).c_str());
            std::printf("],\"rejected\":[");
            for (size_t k = 0; k < m.rejected.size(); ++k)
                std::printf("%s{\"others_cpu_percent\":%.2f,\"others_top\":%s}", k ? "," : "", m.rejected[k].first,
                            json_str(m.rejected[k].second).c_str());
            std::printf("]}");
        }
        std::printf("]}\n");
        return all_ok ? 0 : 3;
    }
    if (!all_ok) {
        std::printf("Result   : checksum mismatch - no score (results are not trustworthy)\n");
        return 3;
    }
    std::printf("Single   : %.0f\n", st_score);
    std::printf("Multi    : %.0f\n", mt_score);
    std::printf("(score v%d: geometric mean of item / reference x 1000. References are fixed numbers, not a specific PC.)\n",
                kScoreVersion);
    return 0;
}

}  // namespace

int wmain(int argc, wchar_t** argv) {
    if (argc < 2) return usage();
    std::wstring cmd = argv[1];
    if (cmd == L"version" || cmd == L"--version") {
        std::printf("%s\n", CPU_BENCH_VERSION);
        return 0;
    }
    if (cmd == L"info") return cmd_info(argc > 2 && std::wstring(argv[2]) == L"--json");
    if (cmd == L"run") return cmd_run(argc, argv);
    if (cmd == L"sweep") return cmd_sweep(argc, argv);
    if (cmd == L"scale") return cmd_scale(argc, argv);
    if (cmd == L"check") return cmd_check(argc, argv);
    if (cmd == L"throttle") return cmd_throttle(argc, argv);
    if (cmd == L"score") return cmd_score(argc, argv);
    if (cmd == L"clock") return cmd_clock(argc > 2 && std::wstring(argv[2]) == L"--json");
    return usage();
}
