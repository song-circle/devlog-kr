// 부동소수점(FP) 모듈의 계산 커널. DLL 인터페이스와 분리해서 단위 테스트에서 직접 링크한다.
// 같은 계산의 Python 독립 구현: tools/fp_reference.py
//
// 경로(scalar / avx2)가 달라도 결과가 비트 단위로 같아야 한다. 그래서
//   - 원소 하나를 계산하는 연산 순서를 경로마다 똑같이 둔다(AVX2는 원소 4개를 나란히 계산할 뿐).
//   - 곱셈과 덧셈을 하나로 합치는 FMA를 쓰지 않는다(반올림이 한 번 줄어 결과가 달라진다).
#pragma once

#include <cstddef>
#include <cstdint>
#include <vector>

namespace fpk {

constexpr size_t kMatN   = 128;  // 행렬곱: 128x128 double (행렬 하나 128 KiB, A·B·C 합쳐 L2에 들어가는 크기)
constexpr size_t kBodies = 256;  // N-body: 물체 256개, 작업 단위 하나 = 모든 물체의 가속도 1회
constexpr double kSoftening = 0.01;  // 두 물체가 겹쳐도 0으로 나누지 않게 더하는 값

enum class Workload { MatMul, NBody };
enum class Path { Scalar, Avx2 };

struct Input {
    std::vector<double> a, b;           // kMatN * kMatN, 행 우선
    std::vector<double> x, y, z, m;     // kBodies
};

Input make_input(uint64_t seed);

// 출력 버퍼 크기 (double 개수): 행렬곱 kMatN*kMatN, N-body 3*kBodies
size_t out_size(Workload w);

// 작업 단위 1회. out은 out_size(w)개. digest(결과 비트의 FNV-1a 64)를 돌려준다.
uint64_t run_unit(Workload w, Path p, const Input& in, double* out);

// 이 CPU와 OS가 AVX2(YMM 레지스터 저장 포함)를 지원하는지
bool cpu_has_avx2();

const char* path_name(Path p);

// AVX2 커널 (fp_avx2.cpp, /arch:AVX2로 따로 컴파일). cpu_has_avx2()가 참일 때만 부른다.
void matmul_avx2(const double* a, const double* b, double* c);
void nbody_avx2(const Input& in, double* acc);

}  // namespace fpk
