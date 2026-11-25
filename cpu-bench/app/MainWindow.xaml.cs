using System.Diagnostics;
using System.Text.Json;
using System.Windows;
using System.Windows.Documents;
using System.Windows.Media;

namespace CpuBench.App;

public partial class MainWindow : Window
{
    private readonly Runner? _runner = Runner.FindNextToApp();
    private readonly List<FindingRow> _findings = new();
    private readonly ResultStore _store = new();
    private readonly AppSettings _settings = AppSettings.Load();
    private string? _updateUrl;
    // 요약에 쓰는 가장 최근 결과 (앱을 다시 켜면 저장된 결과에서 읽음)
    private JsonElement? _info, _check, _score, _throttle, _internet, _fp, _multi;

    // 점수 항목 키 → 화면 이름
    private static readonly Dictionary<string, string> ItemNames = new()
    {
        ["int_st"] = "정수 (싱글)",
        ["mem_bw_st"] = "메모리 읽기 (싱글)",
        ["mem_lat_st"] = "메모리 지연 (싱글)",
        ["int_mt"] = "정수 (멀티)",
        ["mem_bw_mt"] = "메모리 읽기 (멀티)",
    };

    public MainWindow()
    {
        InitializeComponent();
        VersionRun.Text = $"  v{UpdateCheck.Current}";
        UpdateCheckBox.IsChecked = _settings.CheckUpdates;
        Loaded += async (_, _) =>
        {
            ShowHistory();
            _check = _store.LoadLatest("check");
            _score = _store.LoadLatest("score");
            _throttle = _store.LoadLatest("throttle");
            _internet = _store.LoadLatest("internet");
            _fp = _store.LoadLatest("fp");
            if (_fp is JsonElement fp) ShowFp(fp);
            _multi = _store.LoadLatest("multicore");
            if (_multi is JsonElement mc) ShowMulti(mc);
            if (_check is JsonElement c) ShowFindings(Diagnosis.FromCheck(c), f => !f.Title.Contains("쓰로틀링"));
            if (_throttle is JsonElement t) ShowFindings(new[] { Diagnosis.FromThrottle(t) }, f => f.Title.Contains("쓰로틀링"));
            if (_score is JsonElement sc) ShowScore(sc);
            RefreshSummary();
            await LoadInfoAsync();
            RefreshSummary();
            if (_settings.CheckUpdates) await CheckUpdateAsync();
        };
    }

    private async Task CheckUpdateAsync()
    {
        var r = await UpdateCheck.CheckAsync(UpdateCheck.Current, TimeSpan.FromSeconds(5));
        if (r is null) return;
        _updateUrl = r.Url;
        UpdateText.Text = $"새 버전 v{r.Version}이(가) 나왔습니다. 지금 버전은 v{UpdateCheck.Current}입니다.";
        UpdateBanner.Visibility = Visibility.Visible;
    }

    private void Update_Click(object sender, RoutedEventArgs e)
    {
        if (_updateUrl != null) Process.Start(new ProcessStartInfo(_updateUrl) { UseShellExecute = true });
    }

    private void UpdateCheckBox_Changed(object sender, RoutedEventArgs e)
    {
        bool on = UpdateCheckBox.IsChecked == true;
        if (on == _settings.CheckUpdates) return;
        _settings.CheckUpdates = on;
        _settings.Save();
        if (!on) UpdateBanner.Visibility = Visibility.Collapsed;
    }

    private async Task LoadInfoAsync()
    {
        if (_runner is null)
        {
            CpuText.Text = "cpu-bench-runner.exe를 찾지 못했습니다. 앱과 같은 폴더에 있어야 합니다.";
            FullButton.IsEnabled = CheckButton.IsEnabled = ScoreButton.IsEnabled = ThrottleButton.IsEnabled = FpButton.IsEnabled = MultiButton.IsEnabled = false;
            return;
        }
        var r = await _runner.RunAsync("info --json");
        if (r.Json is null)
        {
            CpuText.Text = "PC 정보를 읽지 못했습니다.";
            return;
        }
        var j = r.Json.RootElement;
        _info = j.Clone();
        CpuText.Text = $"{j.GetProperty("cpu").GetString()}  ·  논리 코어 {j.GetProperty("logical").GetInt32()}개" +
                       (j.GetProperty("hybrid").GetBoolean() ? $" (성능 코어 {j.GetProperty("perf_cores").GetInt32()}개 포함)" : "");
        if (j.TryGetProperty("memory", out var mem))
        {
            var mods = mem.GetProperty("modules").EnumerateArray().Where(m => m.GetProperty("size_bytes").GetInt64() > 0).ToList();
            int ch = mem.GetProperty("channels").GetInt32();
            string chText = ch switch { 1 => "싱글 채널", 2 => "듀얼 채널", 0 => "채널 구성 알 수 없음", _ => $"{ch}채널" };
            string speed = mods.Count > 0 ? $"{mods[0].GetProperty("type").GetString()} {mods[0].GetProperty("configured_speed").GetInt32()} MT/s" : "";
            MemText.Text = $"메모리 {mem.GetProperty("total_bytes").GetInt64() / (1L << 30)} GB · {mods.Count}개 모듈 · {chText} · {speed}";
        }
    }

    private void SetBusy(bool busy, string status)
    {
        FullButton.IsEnabled = CheckButton.IsEnabled = ScoreButton.IsEnabled = ThrottleButton.IsEnabled = NetButton.IsEnabled = FpButton.IsEnabled = MultiButton.IsEnabled = !busy;
        Progress.Visibility = busy ? Visibility.Visible : Visibility.Hidden;
        Progress.IsIndeterminate = busy;
        StatusText.Text = status;
    }

    private void ShowFindings(IEnumerable<Finding> add, Func<Finding, bool>? replaceWhere = null)
    {
        if (replaceWhere != null) _findings.RemoveAll(r => replaceWhere(r.Source));
        _findings.AddRange(add.Select(f => new FindingRow(f)));
        Findings.ItemsSource = null;
        Findings.ItemsSource = _findings.OrderBy(f => f.Source.Ok).ToList();
        DiagHint.Visibility = _findings.Count > 0 ? Visibility.Collapsed : Visibility.Visible;
    }

    private void RefreshSummary()
    {
        var (state, verdict, cards) = Summary.Build(_info, _check, _score, _throttle, _internet);
        var brush = (string st) => (Brush)Application.Current.Resources[st == "ok" ? "Good" : st == "warn" ? "Accent" : "Muted"];
        VerdictIcon.Text = state == "ok" ? "✔" : state == "warn" ? "⚠" : "•";
        VerdictIcon.Foreground = brush(state);
        VerdictText.Text = verdict;
        SummaryCards.ItemsSource = cards.Select(c => new { c.Title, c.Value, c.Detail, Brush = brush(c.State) }).ToList();
        var perf = Summary.Perf(_info, _check, _score, _throttle);
        if (perf is null)
        {
            PerfText.Text = "—";
            PerfText.Foreground = brush("none");
            PerfDetail.Text = "'전체 점검'을 하면 나옵니다";
        }
        else
        {
            PerfText.Text = $"{perf.Percent:0}%";
            PerfText.Foreground = brush(perf.Percent >= 90 ? "ok" : "warn");
            PerfDetail.Text = $"발열 {perf.Thermal:0}% × 메모리 {perf.Memory:0}% × 쉴 때 여유 {perf.Background:0}%\n전원 설정 문제는 숫자에 넣지 않고 따로 표시";
        }
    }

    private async void FullButton_Click(object sender, RoutedEventArgs e)
    {
        if (_runner is null) return;
        var ok = MessageBox.Show(this,
            "약 2분 30초 걸립니다.\n\n1. 환경 점검 (3초)\n2. 점수 측정 (1분 남짓)\n3. 발열 검사 (1분): 모든 코어에 부하를 걸어서 팬 소리가 커지고 PC가 따뜻해질 수 있습니다.\n\n정확한 결과를 위해 그동안 다른 프로그램은 잠시 쉬게 해 주세요.",
            "전체 점검", MessageBoxButton.OKCancel, MessageBoxImage.Information);
        if (ok != MessageBoxResult.OK) return;
        if (!await RunCheckAsync("1/3 ")) return;
        if (!await RunScoreAsync("2/3 ")) return;
        if (!await RunThrottleAsync("3/3 ")) return;
        StatusText.Text = "전체 점검 완료";
    }

    private async void CheckButton_Click(object sender, RoutedEventArgs e) => await RunCheckAsync("");

    private async void NetButton_Click(object sender, RoutedEventArgs e)
    {
        if (!_settings.MlabConsent)
        {
            var ok = MessageBox.Show(this,
                "인터넷 속도는 M-Lab(Measurement Lab)의 공개 측정 서버와 데이터를 주고받아 잽니다. 다운로드·업로드 각 10초, 데이터를 수백 MB 쓸 수 있습니다.\n\n" +
                "M-Lab은 인터넷 연구용 공개 플랫폼이라, 이 측정의 결과(속도)와 공인 IP 주소가 M-Lab의 공개 데이터로 남습니다. PC 측정 결과나 PC 정보는 보내지 않습니다.\n\n" +
                "계속할까요? (한 번 동의하면 다시 묻지 않습니다)",
                "인터넷 속도 측정", MessageBoxButton.OKCancel, MessageBoxImage.Information);
            if (ok != MessageBoxResult.OK) return;
            _settings.MlabConsent = true;
            _settings.Save();
        }
        SetBusy(true, "인터넷 속도 측정 중…");
        try
        {
            var r = await NetSpeed.RunAsync(UpdateCheck.Current.ToString(), new Progress<string>(s => StatusText.Text = s), CancellationToken.None);
            var json = JsonSerializer.SerializeToElement(new
            {
                download_mbps = r.DownloadMbps, upload_mbps = r.UploadMbps, min_rtt_ms = r.MinRttMs, server = r.Server, city = r.City,
            });
            _store.Save("internet", json);
            _internet = json;
            SetBusy(false, $"인터넷 속도 측정 완료 (M-Lab {r.City})");
            RefreshSummary();
        }
        catch (Exception ex) when (ex is System.Net.Http.HttpRequestException or System.Net.WebSockets.WebSocketException or OperationCanceledException or JsonException or KeyNotFoundException or InvalidOperationException)
        {
            SetBusy(false, $"인터넷 속도 측정 실패: {ex.Message}");
        }
    }
    private async void ScoreButton_Click(object sender, RoutedEventArgs e) => await RunScoreAsync("");
    private async void ThrottleButton_Click(object sender, RoutedEventArgs e) => await RunThrottleAsync("");

    private async Task<bool> RunCheckAsync(string step)
    {
        if (_runner is null) return false;
        SetBusy(true, $"{step}환경 점검 중… (3초 동안 쉬고 있을 때의 CPU 사용률을 잽니다)");
        // 점검은 '쉬고 있을 때' 사용률을 재므로, 이 앱도 그동안 아무것도 그리지 않는다.
        // 진행 막대 애니메이션을 켜 두면 앱 자신이 백그라운드 사용률 목록에 올라왔다(버그 005).
        Progress.Visibility = Visibility.Hidden;
        Progress.IsIndeterminate = false;
        var r = await _runner.RunAsync("check --json");
        SetBusy(false, r.Json is null ? $"환경 점검 실패: {r.StdErr.Trim()}" : $"{step}환경 점검 완료");
        if (r.Json is null) return false;
        _store.Save("check", r.Json.RootElement);
        _check = r.Json.RootElement.Clone();
        ShowFindings(Diagnosis.FromCheck(r.Json.RootElement), f => !f.Title.Contains("쓰로틀링"));
        RefreshSummary();
        return true;
    }

    private async Task<bool> RunScoreAsync(string step)
    {
        if (_runner is null) return false;
        SetBusy(true, $"{step}점수 측정 중…");
        Progress.IsIndeterminate = false;
        Progress.Value = 0;
        var r = await _runner.RunAsync("score --json --progress", p => Dispatcher.Invoke(() =>
        {
            int k = p.GetProperty("progress").GetInt32(), n = p.GetProperty("total").GetInt32();
            string key = p.GetProperty("item").GetString() ?? "";
            Progress.Value = (double)k / n;
            StatusText.Text = $"{step}점수 측정 중… {k + 1}/{n} {ItemNames.GetValueOrDefault(key, key)}";
        }));
        if (r.Json is null)
        {
            SetBusy(false, $"점수 측정 실패: {r.StdErr.Trim()}");
            return false;
        }
        var j = r.Json.RootElement;
        _store.Save("score", j);
        _score = j.Clone();
        ShowHistory();
        string note = ShowScore(j);
        bool valid = j.GetProperty("valid").GetBoolean();
        SetBusy(false, valid ? $"{step}점수 측정 완료 (점수 체계 v{j.GetProperty("score_version").GetInt32()}){note}"
                             : "계산 결과가 기대값과 달라서 점수를 내지 않았습니다. 오버클럭이나 메모리 설정이 불안정할 수 있습니다.");
        RefreshSummary();
        return true;
    }

    /// <summary>점수 카드에 표시하고, 방해·다시 재기 안내 문구를 돌려준다</summary>
    private string ShowScore(JsonElement j)
    {
        bool valid = j.GetProperty("valid").GetBoolean();
        double single = j.GetProperty("single").GetDouble(), multi = j.GetProperty("multi").GetDouble();
        SingleText.Text = valid ? single.ToString("0") : "무효";
        MultiText.Text = valid ? multi.ToString("0") : "무효";
        bool wasDisturbed = j.TryGetProperty("disturbed", out var dist) && dist.GetBoolean();
        // 방해 표시는 상태 줄에만 잠깐 나오면 앱을 다시 켰을 때 사라진다 → 점수 카드에 계속 남긴다
        ScoreWords.Text = !valid ? "" : (wasDisturbed ? "⚠ 이 점수는 측정 중 다른 프로그램이 CPU를 많이 써서 낮게 나왔을 수 있습니다. 다른 프로그램을 닫고 다시 재 보세요.\n" : "")
                          + Summary.ScoreInWords(single, multi);
        ScoreWords.Foreground = (Brush)Application.Current.Resources[wasDisturbed ? "Accent" : "Text"];
        ClockText.Text = Summary.ClockInWords(j);
        ScoreItems.ItemsSource = j.GetProperty("items").EnumerateArray().Select(it =>
        {
            string key = it.GetProperty("key").GetString() ?? "";
            string unit = it.GetProperty("unit").GetString() ?? "";
            return new
            {
                Label = ItemNames.GetValueOrDefault(key, key),
                Value = $"{it.GetProperty("value").GetDouble():0.##} {unit}  (기준 {it.GetProperty("reference").GetDouble():0.##}, ×{it.GetProperty("ratio").GetDouble():0.00})",
            };
        }).ToList();
        int retries = j.GetProperty("items").EnumerateArray().Sum(it => it.TryGetProperty("retries", out var rr) ? rr.GetInt32() : 0);
        bool disturbed = j.TryGetProperty("disturbed", out var d) && d.GetBoolean();
        return disturbed ? " · 측정 중 다른 프로그램이 CPU를 많이 써서 결과가 낮게 나왔을 수 있습니다. 다른 프로그램을 닫고 다시 재 보세요."
             : retries > 0 ? $" · 다른 프로그램 때문에 {retries}회를 다시 쟀습니다." : "";
    }

    // 부동소수점 측정: 행렬곱·N-body를 싱글·멀티로. 점수 v1에는 넣지 않고 따로 보여 준다.
    private static readonly (string Workload, bool Multi, string Label)[] FpRuns =
    {
        ("matmul", false, "행렬곱 (싱글)"), ("nbody", false, "물체 인력 계산 (싱글)"),
        ("matmul", true, "행렬곱 (멀티)"), ("nbody", true, "물체 인력 계산 (멀티)"),
    };

    private async void FpButton_Click(object sender, RoutedEventArgs e)
    {
        if (_runner is null) return;
        SetBusy(true, "부동소수점 측정 중…");
        Progress.IsIndeterminate = false;
        var items = new List<object>();
        for (int i = 0; i < FpRuns.Length; ++i)
        {
            var (w, multi, label) = FpRuns[i];
            Progress.Value = (double)i / FpRuns.Length;
            StatusText.Text = $"부동소수점 측정 중… {i + 1}/{FpRuns.Length} {label}";
            var r = await _runner.RunAsync($"run fp --workload {w}{(multi ? " --mt" : "")} --runs 3 --duration-ms 1000 --json");
            if (r.Json is null)
            {
                SetBusy(false, $"부동소수점 측정 실패: {r.StdErr.Trim()}");
                return;
            }
            var j = r.Json.RootElement;
            items.Add(new
            {
                workload = w, multi, threads = j.GetProperty("threads").GetInt32(),
                value = j.GetProperty("median_value").GetDouble(), cv = j.GetProperty("cv_percent").GetDouble(),
                variant = j.GetProperty("variant").GetString(), checksum_ok = j.GetProperty("checksum_ok").GetBoolean(),
            });
        }
        var json = JsonSerializer.SerializeToElement(new { module_version = "0.1.0", items });
        _store.Save("fp", json);
        _fp = json;
        ShowFp(json);
        SetBusy(false, "부동소수점 측정 완료");
    }

    private void ShowFp(JsonElement j)
    {
        var items = j.GetProperty("items").EnumerateArray().ToList();
        FpHint.Visibility = Visibility.Collapsed;
        FpItems.ItemsSource = items.Select(it =>
        {
            string w = it.GetProperty("workload").GetString() ?? "";
            bool multi = it.GetProperty("multi").GetBoolean();
            string label = FpRuns.FirstOrDefault(r => r.Workload == w && r.Multi == multi).Label ?? w;
            string value = it.GetProperty("checksum_ok").GetBoolean() ? $"{it.GetProperty("value").GetDouble():N0} 회/초" : "계산 오류";
            return new { Label = label, Value = value };
        }).ToList();
        string variant = items.Count > 0 ? items[0].GetProperty("variant").GetString() ?? "" : "";
        bool allOk = items.All(it => it.GetProperty("checksum_ok").GetBoolean());
        FpWords.Text = (allOk ? "" : "⚠ 계산 결과가 기대값과 달랐습니다. 오버클럭이나 메모리 설정이 불안정할 수 있습니다.\n") +
                       (variant == "avx2" ? "이 CPU는 AVX2(한 번에 실수 4개 계산)로 쟀습니다." : "이 CPU는 AVX2가 없어 일반 방식으로 쟀습니다.") +
                       " 1회 = 128×128 행렬곱 한 번 / 물체 256개의 인력 한 번 계산. 다른 PC와 비교하는 기능은 아직 없습니다.";
    }

    // 멀티코어: 확장 곡선(정수 측정을 스레드 1→N) + 동기화 측정(모든 스레드). 점수 v1에는 넣지 않는다.
    private static readonly (string Workload, string Label, string Unit)[] SyncRuns =
    {
        ("padded", "각자 자기 변수에 +1 (서로 떨어뜨려 둠)", "회/초"),
        ("falseshare", "각자 자기 변수에 +1 (바로 옆에 붙여 둠)", "회/초"),
        ("shared", "모두 변수 하나에 +1", "회/초"),
        ("queue", "작업 큐에 넣고 빼기", "개/초"),
    };

    private async void MultiButton_Click(object sender, RoutedEventArgs e)
    {
        if (_runner is null) return;
        SetBusy(true, "멀티코어 측정 중… 1/2 확장 곡선 (스레드를 하나씩 늘려 가며, 약 40초)");
        var s = await _runner.RunAsync("scale int --duration-ms 500 --runs 3 --json");
        if (s.Json is null)
        {
            SetBusy(false, $"멀티코어 측정 실패: {s.StdErr.Trim()}");
            return;
        }
        var scale = s.Json.RootElement.Clone();
        Progress.IsIndeterminate = false;
        var sync = new List<object>();
        for (int i = 0; i < SyncRuns.Length; ++i)
        {
            var (w, label, _) = SyncRuns[i];
            Progress.Value = (double)i / SyncRuns.Length;
            StatusText.Text = $"멀티코어 측정 중… 2/2 동기화 {i + 1}/{SyncRuns.Length} {label}";
            var r = await _runner.RunAsync($"run mt --workload {w} --mt --runs 3 --duration-ms 1000 --json");
            if (r.Json is null)
            {
                SetBusy(false, $"멀티코어 측정 실패: {r.StdErr.Trim()}");
                return;
            }
            var j = r.Json.RootElement;
            sync.Add(new
            {
                workload = w, threads = j.GetProperty("threads").GetInt32(),
                ops_per_sec = j.GetProperty("median_ops_per_sec").GetDouble(),
                cv = j.GetProperty("cv_percent").GetDouble(), checksum_ok = j.GetProperty("checksum_ok").GetBoolean(),
            });
        }
        var json = JsonSerializer.SerializeToElement(new { version = 1, scale, sync });
        _store.Save("multicore", json);
        _multi = json;
        ShowMulti(json);
        SetBusy(false, "멀티코어 측정 완료");
    }

    private static readonly Brush BlueBrush = new SolidColorBrush(Color.FromRgb(0x79, 0xC0, 0xFF));
    private static readonly Brush GoodBrush = new SolidColorBrush(Color.FromRgb(0x3F, 0xB9, 0x50));
    private static readonly Brush AccentBrush = new SolidColorBrush(Color.FromRgb(0xE8, 0x87, 0x5F));

    private void ShowMulti(JsonElement j)
    {
        MultiHint.Visibility = Visibility.Collapsed;
        var points = j.GetProperty("scale").GetProperty("points").EnumerateArray().Select(p => (
            T: p.GetProperty("threads").GetInt32(),
            Core: p.GetProperty("added_core").GetString() ?? "",
            Ops: p.GetProperty("ops_per_sec").GetDouble(),
            Speedup: p.GetProperty("speedup").GetDouble(),
            Ok: p.GetProperty("checksum_ok").GetBoolean())).ToList();
        if (points.Count == 0) return;
        double max = Math.Max(1, points.Max(p => p.Speedup));
        ScaleBars.ItemsSource = points.Select(p => new
        {
            Threads = p.T,
            Height = Math.Max(2, p.Speedup / max * 128),
            Brush = p.Core.Contains("SMT") ? AccentBrush : p.Core.StartsWith("E") ? BlueBrush : GoodBrush,
            Tip = $"스레드 {p.T}개: {p.Speedup:F2}배 ({p.Ops:N0}/초)",
        }).ToList();
        ScaleLegend.Visibility = Visibility.Visible;

        // 코어 종류별로 나눠 말로 설명: 성능(또는 일반) 코어 → 효율 코어 → 하이퍼스레딩
        double o(int t) => points.First(p => p.T == t).Ops;
        int nP = points.Count(p => !p.Core.Contains("SMT") && !p.Core.StartsWith("E") && !p.Core.EndsWith("+"));
        int nE = points.Count(p => p.Core.StartsWith("E") && !p.Core.Contains("SMT"));
        int nS = points.Count(p => p.Core.Contains("SMT") && !p.Core.EndsWith("+"));
        double one = o(1);
        var parts = new List<string>();
        bool hybrid = nE > 0;
        if (nP > 0) parts.Add($"{(hybrid ? "성능 코어" : "코어")} {nP}개까지 {o(nP) / one:F1}배");
        if (nE > 0) parts.Add($"효율 코어 하나 ≈ 성능 코어의 {(o(nP + nE) - o(nP)) / nE / one * 100:F0}%");
        if (nS > 0) parts.Add($"하이퍼스레딩 하나 ≈ 코어의 {(o(nP + nE + nS) - o(nP + nE)) / nS / one * 100:F0}%");
        var last = points[^1];
        ScaleWords.Text = $"스레드 {last.T}개 → {last.Speedup:F1}배 (스레드 1개 대비, 정수 계산). " + string.Join(" · ", parts) + ".";

        var sync = j.GetProperty("sync").EnumerateArray().ToList();
        double v(string w) => sync.Where(x => x.GetProperty("workload").GetString() == w)
                                  .Select(x => x.GetProperty("ops_per_sec").GetDouble()).FirstOrDefault();
        SyncItems.ItemsSource = sync.Select(x =>
        {
            string w = x.GetProperty("workload").GetString() ?? "";
            var run = SyncRuns.FirstOrDefault(r => r.Workload == w);
            string value = x.GetProperty("checksum_ok").GetBoolean()
                ? $"{x.GetProperty("ops_per_sec").GetDouble() / 1e6:N1}백만 {run.Unit}" : "계산 오류";
            return new { Label = run.Label ?? w, Value = value };
        }).ToList();
        bool allOk = points.All(p => p.Ok) && sync.All(x => x.GetProperty("checksum_ok").GetBoolean());
        int threads = sync.Count > 0 ? sync[0].GetProperty("threads").GetInt32() : last.T;
        double fs = v("falseshare") > 0 ? v("padded") / v("falseshare") : 0;
        MultiWords.Text = (allOk ? "" : "⚠ 측정이 끝난 뒤 합계가 맞지 않았습니다. 오버클럭이나 메모리 설정이 불안정할 수 있습니다.\n") +
            $"동기화는 스레드 {threads}개로 잽니다. " +
            (fs > 0 ? $"자기 변수만 쓰는데도 서로 붙어 있으면 {fs:F0}배 느립니다(거짓 공유: CPU는 메모리를 64바이트 단위로 주고받음). " : "") +
            "1스레드 값이 실행마다 10% 가까이 흔들릴 수 있어 배수는 대략으로 보세요. 다른 PC와 비교하는 기능은 아직 없습니다.";
    }

    private async Task<bool> RunThrottleAsync(string step)
    {
        if (_runner is null) return false;
        SetBusy(true, $"{step}발열 검사 중… (1분 동안 모든 코어에 부하를 겁니다. 팬 소리가 커질 수 있습니다)");
        var r = await _runner.RunAsync("throttle --seconds 60 --json");
        SetBusy(false, r.Json is null ? $"발열 검사 실패: {r.StdErr.Trim()}" : $"{step}발열 검사 완료");
        if (r.Json is null) return false;
        _store.Save("throttle", r.Json.RootElement);
        _throttle = r.Json.RootElement.Clone();
        ShowFindings(new[] { Diagnosis.FromThrottle(r.Json.RootElement) }, f => f.Title.Contains("쓰로틀링"));
        RefreshSummary();
        return true;
    }

    private void ShowHistory()
    {
        var list = _store.LoadScores(10);
        History.ItemsSource = list.Select(h => new
        {
            When = $"{h.When:yyyy-MM-dd HH:mm}  (v{h.Version})",
            Single = h.Valid ? $"싱글 {h.Single:0}" : "무효",
            Multi = h.Valid ? $"멀티 {h.Multi:0}" : "",
        }).ToList();
        HistoryHint.Visibility = list.Count > 0 ? Visibility.Collapsed : Visibility.Visible;
    }

    private void OpenResults_Click(object sender, RoutedEventArgs e)
    {
        System.IO.Directory.CreateDirectory(_store.Folder);
        Process.Start(new ProcessStartInfo(_store.Folder) { UseShellExecute = true });
    }

    private void Settings_Click(object sender, RoutedEventArgs e)
    {
        if (sender is Hyperlink { Tag: string uri } && !string.IsNullOrEmpty(uri))
            Process.Start(new ProcessStartInfo(uri) { UseShellExecute = true });
    }

    private sealed class FindingRow
    {
        public FindingRow(Finding f) => Source = f;
        public Finding Source { get; }
        public string Icon => Source.Ok ? "✔" : "⚠";
        public Brush IconBrush => (Brush)Application.Current.Resources[Source.Ok ? "Good" : "Accent"];
        public string Title => Source.Title;
        public string Detail => Source.Detail;
        public string? SettingsUri => Source.SettingsUri;
        public Visibility LinkVisibility => Source.SettingsUri is null ? Visibility.Collapsed : Visibility.Visible;
    }
}
