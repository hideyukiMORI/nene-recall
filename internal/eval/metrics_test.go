package eval_test

import (
	"testing"
	"time"

	"github.com/hideyukiMORI/nene-recall/internal/eval"
)

// TestRecallAtIsAFractionOfRelevantNotAHitRate は、正解が複数あるクエリで
// 「1件当てた」と「全部当てた」が別の数字になることを見る。
//
// 🔴 ここを success@k（1件でも当たれば 1.0）で実装すると、複数正解のクエリで
// 取りこぼしが見えなくなる。定義の取り違えは値域も型も同じなので、
// テストで縛らないと誰も気づかない。
func TestRecallAtIsAFractionOfRelevantNotAHitRate(t *testing.T) {
	ranked := []string{"a", "b", "c", "d", "e"}

	cases := []struct {
		name     string
		relevant []string
		k        int
		want     float64
	}{
		{name: "正解2件のうち1件だけ上位", relevant: []string{"a", "z"}, k: 5, want: 0.5},
		{name: "正解2件とも上位", relevant: []string{"a", "b"}, k: 5, want: 1},
		{name: "k で切ると片方が圏外", relevant: []string{"a", "e"}, k: 2, want: 0.5},
		{name: "1位だけを見る", relevant: []string{"a"}, k: 1, want: 1},
		{name: "1位に無い", relevant: []string{"b"}, k: 1, want: 0},
		{name: "k が件数を超えても落ちない", relevant: []string{"a"}, k: 100, want: 1},
		{name: "正解が空なら 0", relevant: nil, k: 5, want: 0},
		{name: "k が 0 なら 0", relevant: []string{"a"}, k: 0, want: 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := eval.RecallAt(ranked, c.relevant, c.k); got != c.want {
				t.Errorf("RecallAt(%v, %d) = %v, want %v", c.relevant, c.k, got, c.want)
			}
		})
	}
}

// TestRecallAtCannotReachOneWhenRelevantExceedsK は、正解が k 件を超えると
// 満点に届かないことを実際に見せる。
//
// これは実装の欠陥ではなく recall の定義そのものだが、注釈を書く側が
// 踏みうる罠なので、性質としてテストに固定しておく（limit=10 の判断に
// 再検討が要る条件そのものである・ADR 0013）。
func TestRecallAtCannotReachOneWhenRelevantExceedsK(t *testing.T) {
	ranked := []string{"a", "b"}
	relevant := []string{"a", "b", "c"}

	got := eval.RecallAt(ranked, relevant, 2)
	want := 2.0 / 3.0

	if got != want {
		t.Errorf("RecallAt = %v, want %v", got, want)
	}
}

// TestReciprocalRankUsesTheFirstHit は最初の正解の順位だけを見ることを確かめる。
func TestReciprocalRankUsesTheFirstHit(t *testing.T) {
	cases := []struct {
		name     string
		ranked   []string
		relevant []string
		want     float64
	}{
		{name: "1位が正解", ranked: []string{"a", "b"}, relevant: []string{"a"}, want: 1},
		{name: "2位が正解", ranked: []string{"x", "a"}, relevant: []string{"a"}, want: 0.5},
		{name: "4位が正解", ranked: []string{"x", "y", "z", "a"}, relevant: []string{"a"}, want: 0.25},
		{name: "圏外は 0", ranked: []string{"x", "y"}, relevant: []string{"a"}, want: 0},
		{name: "後ろの正解は無視する", ranked: []string{"x", "a", "b"}, relevant: []string{"b", "a"}, want: 0.5},
		{name: "空の順位は 0", ranked: nil, relevant: []string{"a"}, want: 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := eval.ReciprocalRank(c.ranked, c.relevant); got != c.want {
				t.Errorf("ReciprocalRank = %v, want %v", got, c.want)
			}
		})
	}
}

// TestRankOfDistinguishesOutOfRangeFromFirstPlace は、圏外が nil であって
// 0 ではないことを見る。
//
// 🔴 圏外を 0 で表すと、順位という 1 始まりの値と混ざる。null で残すことが
// レポートの生データから集計値を再計算できる条件になる (ADR 0013)。
func TestRankOfDistinguishesOutOfRangeFromFirstPlace(t *testing.T) {
	ranked := []string{"a", "b", "c"}

	first := eval.RankOf(ranked, "a")
	if first == nil || *first != 1 {
		t.Fatalf("RankOf(a) = %v, want 1", first)
	}

	third := eval.RankOf(ranked, "c")
	if third == nil || *third != 3 {
		t.Fatalf("RankOf(c) = %v, want 3", third)
	}

	if out := eval.RankOf(ranked, "z"); out != nil {
		t.Errorf("RankOf(z) = %v, want nil", out)
	}
}

// TestFirstRelevantRankReturnsNilWhenNothingHits は圏外が nil であることを見る。
func TestFirstRelevantRankReturnsNilWhenNothingHits(t *testing.T) {
	if got := eval.FirstRelevantRank([]string{"x"}, []string{"a"}); got != nil {
		t.Errorf("FirstRelevantRank = %v, want nil", got)
	}

	got := eval.FirstRelevantRank([]string{"x", "a"}, []string{"a"})
	if got == nil || *got != 2 {
		t.Errorf("FirstRelevantRank = %v, want 2", got)
	}
}

// TestPercentileUsesNearestRank は、返る値が必ず実測サンプルのどれかと
// 一致することを見る。
//
// 補間する実装だと「一度も観測されていない所要時間」が返る。実測を正本とする
// 以上、その値をレポートに載せるわけにはいかない。
func TestPercentileUsesNearestRank(t *testing.T) {
	samples := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		30 * time.Millisecond,
		40 * time.Millisecond,
	}

	cases := []struct {
		name string
		p    float64
		want time.Duration
	}{
		{name: "p50 は ceil(2)=2 番目", p: 50, want: 20 * time.Millisecond},
		{name: "p95 は ceil(3.8)=4 番目", p: 95, want: 40 * time.Millisecond},
		{name: "p100 は最大", p: 100, want: 40 * time.Millisecond},
		{name: "p0 は最小に丸める", p: 0, want: 10 * time.Millisecond},
		{name: "p25 は 1 番目", p: 25, want: 10 * time.Millisecond},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := eval.Percentile(samples, c.p); got != c.want {
				t.Errorf("Percentile(%v) = %v, want %v", c.p, got, c.want)
			}
		})
	}
}

// TestPercentileDoesNotMutateItsInput は、呼び出し側の並びを壊さないことを見る。
//
// 生データはレポートに測定順で載せる。ここで並べ替えてしまうと、
// ラウンドごとの latency がどのラウンドのものか分からなくなる。
func TestPercentileDoesNotMutateItsInput(t *testing.T) {
	samples := []time.Duration{30, 10, 20}

	eval.Percentile(samples, 95)

	want := []time.Duration{30, 10, 20}
	for i := range want {
		if samples[i] != want[i] {
			t.Fatalf("入力が並べ替えられた: %v, want %v", samples, want)
		}
	}
}

// TestPercentileOfEmptyIsZero は空入力で落ちないことを見る。
func TestPercentileOfEmptyIsZero(t *testing.T) {
	if got := eval.Percentile(nil, 95); got != 0 {
		t.Errorf("Percentile(nil) = %v, want 0", got)
	}
}

// TestMean は平均と空入力を見る。
func TestMean(t *testing.T) {
	if got := eval.Mean([]float64{1, 2, 3, 4}); got != 2.5 {
		t.Errorf("Mean = %v, want 2.5", got)
	}

	if got := eval.Mean(nil); got != 0 {
		t.Errorf("Mean(nil) = %v, want 0", got)
	}
}
