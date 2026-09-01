package eval

import (
	"math"
	"slices"
	"time"
)

// PercentileMethod は Percentile が使う定義を人が読める形で表したもの。
//
// 🔴 レポートに必ず載せる。ベンチマークの追記が残した教訓は
// 「モデルもランタイムも一致しているのに再現できないのは、記録が計算式を
// 欠いているからである」だった (docs/benchmarks/2026-09-01-baseline.md)。
// パーセンタイルは定義が複数あり（nearest-rank・線形補間・R の9種類）、
// どれを使ったかを書かない数字は後から検証できない。
const PercentileMethod = "nearest-rank: sorted[ceil(p/100*n)-1], 1-indexed, no interpolation"

// RecallAt は上位 k 件に入った正解の割合を返す。
//
// 定義は |relevant ∩ ranked[:k]| / |relevant| である。
//
// 🔴 「1件でも当たれば 1.0」(success@k / hit rate) ではない。正解が複数ある
// クエリで、1件だけ当てた場合と全部当てた場合が同じ数字になってしまう。
// ADR 0009 が測ると決めたのは recall なので、分母は正解の総数にする
// (docs/adr/0009-retrieval-evaluation-is-in-scope.md)。
//
// ⚠️ この定義の帰結として、正解が k 件を超えるクエリでは recall@k の上限が
// 1.0 に届かない。limit=10・k=10 の構成では、正解を 11 件書いた注釈が
// 「決して満点にならないクエリ」になる。注釈を書くときに気をつけること。
//
// ranked は順位順の eval_key、relevant は正解の eval_key。
// relevant が空のときは 0 を返す（ローダが空を拒否するので通常は起きない）。
func RecallAt(ranked, relevant []string, k int) float64 {
	if len(relevant) == 0 || k < 1 {
		return 0
	}

	top := ranked
	if len(top) > k {
		top = top[:k]
	}

	found := 0

	for _, key := range relevant {
		if slices.Contains(top, key) {
			found++
		}
	}

	return float64(found) / float64(len(relevant))
}

// ReciprocalRank は最初の正解が現れた順位の逆数を返す。
//
// 圏外（返された順位のどこにも正解が無い）なら 0 を返す。0 を返すのは
// 「順位が無限に遠い」の極限としてであって、失敗を表す番兵ではない。
// MRR はこの値をクエリ全体で平均したものである。
func ReciprocalRank(ranked, relevant []string) float64 {
	rank := FirstRelevantRank(ranked, relevant)
	if rank == nil {
		return 0
	}

	return 1 / float64(*rank)
}

// FirstRelevantRank は最初の正解が現れた1始まりの順位を返す。圏外なら nil。
//
// nil は「省略可能な値が無い」＝圏外だけを意味する (GO-004)。
// 0 を圏外の印にすると、順位 0 という有効値と見分けがつかなくなる。
func FirstRelevantRank(ranked, relevant []string) *int {
	for i, key := range ranked {
		if slices.Contains(relevant, key) {
			rank := i + 1

			return &rank
		}
	}

	return nil
}

// RankOf は key が現れた1始まりの順位を返す。圏外なら nil。
//
// レポートの per-query 生データで、正解ごとの順位を記録するために使う。
// 圏外を null として残せるのは、集計値を第三者が再計算するのに要るからである。
func RankOf(ranked []string, key string) *int {
	if i := slices.Index(ranked, key); i >= 0 {
		rank := i + 1

		return &rank
	}

	return nil
}

// Percentile は p パーセンタイルを nearest-rank で返す。
//
// 定義は PercentileMethod のとおり。補間しないので、返る値は必ず実測サンプルの
// いずれかと一致する。補間した値は「実際には一度も観測されていない所要時間」に
// なるので、実測を正本とするこのリポジトリでは採らない。
//
// samples は変更しない（複製してから並べ替える）。空なら 0 を返す。
func Percentile(samples []time.Duration, p float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}

	sorted := slices.Clone(samples)
	slices.Sort(sorted)

	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}

	if rank > len(sorted) {
		rank = len(sorted)
	}

	return sorted[rank-1]
}

// Mean は平均を返す。空なら 0。
func Mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}

	var sum float64
	for _, v := range values {
		sum += v
	}

	return sum / float64(len(values))
}
