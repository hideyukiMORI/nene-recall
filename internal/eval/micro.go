package eval

import "slices"

// 本ファイルは正解チャンク単位（micro）の内訳を集計する。
//
// ⚠️ Summary.Recall（クエリ単位のマクロ平均）とは別の数え方である。マクロ平均は
// 正解が1件のクエリと8件のクエリを同じ重みで扱うので、「どのチャンクが拾えて
// いないか」を見るには粗すぎる。基準線が「131 / 236」と併記しているのがこちらである。
//
// 🔑 集計の入力は QueryReport.RelevantRanks（per-query の生データ）と
// corpus.jsonl の本文だけである。どちらもレポートと評価セットに残るので、
// ここで出す数字は第三者が再計算できる (ADR 0013 Decision 7)。

// microCutoff は micro 内訳で「上位何件以内」を数えるかを返す。
//
// KValues の最大値に合わせる。主指標が recall@10 である以上、内訳も同じ k で
// 数えなければ並べて読めない。ここに 10 を直接書かないのは、k の一覧を
// 変えたときに内訳だけが古い k のまま残るのを防ぐためである。
func microCutoff() int {
	return slices.Max(KValues())
}

// withinCutoff は順位が上位 cutoff 件以内かを返す。
//
// rank が nil なら圏外である。nil を 0 と同一視しないこと (GO-004)。
func withinCutoff(rank *int, cutoff int) bool {
	return rank != nil && *rank <= cutoff
}

// ratio は分数を実数にする。分母が 0 なら 0。
//
// 🔑 呼び出し側は分子と分母も一緒にレポートへ載せる。割った値だけを残すと、
// それが何件中の何件なのかが失われ、1件の増減がどれだけ効くのかを読む人が
// 判断できない (testdata/eval/README.md「数字の読み方」)。
func ratio(hits, total int) float64 {
	if total == 0 {
		return 0
	}

	return float64(hits) / float64(total)
}

// summarizeMicro は正解チャンク単位の内訳を出す。
func summarizeMicro(reports []QueryReport) MicroRecall {
	cutoff := microCutoff()
	hits, total := 0, 0

	for _, report := range reports {
		for _, rank := range report.RelevantRanks {
			total++

			if withinCutoff(rank.Rank, cutoff) {
				hits++
			}
		}
	}

	return MicroRecall{Hits: hits, Total: total, Cutoff: cutoff, Value: ratio(hits, total)}
}

// summarizeGoldLength は gold チャンクの長さ別に micro 内訳を割る。
//
// 🔴 これは Q-1（tsvector か BM25 か）の交絡要因への対処である。
// BM25 は文書長で正規化し、Postgres の ts_rank は既定でしない。長文チャンクが
// gold として繰り返し使われているこの評価セットでは、両手法の差が
// 「検索品質の差」なのか「長文優遇の差」なのかを総合値からは切り分けられない
// (testdata/eval/README.md「既知の性質」の申し送り)。
func summarizeGoldLength(reports []QueryReport, runes map[string]int) []GoldLengthBucket {
	short := GoldLengthBucket{
		Label: "<=520", MinRunes: 0, MaxRunes: GoldLengthThreshold,
		Hits: 0, Total: 0, Value: 0,
	}
	long := GoldLengthBucket{
		// 上限なしの区分は MaxRunes を 0 で表す。
		Label: ">520", MinRunes: GoldLengthThreshold + 1, MaxRunes: 0,
		Hits: 0, Total: 0, Value: 0,
	}

	cutoff := microCutoff()

	for _, report := range reports {
		for _, rank := range report.RelevantRanks {
			// コーパスに無いキーは 0 字として短い側に入る。ローダが
			// dangling な正解キーを拒否するので、実際には起きない。
			bucket := &short
			if runes[rank.Key] > GoldLengthThreshold {
				bucket = &long
			}

			bucket.Total++

			if withinCutoff(rank.Rank, cutoff) {
				bucket.Hits++
			}
		}
	}

	short.Value = ratio(short.Hits, short.Total)
	long.Value = ratio(long.Hits, long.Total)

	return []GoldLengthBucket{short, long}
}

// summarizeLongChunks は名指しの長文 gold チャンクを追う。
//
// 🔑 長さの区分だけでは足りない。この評価セットの偏りは「長い chunk が多い」
// ではなく「特定の3つが繰り返し正解になっている」という形をしているので、
// その3つが拾えたかどうかを直接数える。
func summarizeLongChunks(reports []QueryReport, runes map[string]int) LongChunkRecall {
	tracked := LongGoldKeys()
	index := make(map[string]int, len(tracked))
	keys := make([]LongChunkKey, 0, len(tracked))

	for i, key := range tracked {
		index[key] = i
		keys = append(keys, LongChunkKey{Key: key, Runes: runes[key], Hits: 0, Total: 0})
	}

	cutoff := microCutoff()
	hits, total := 0, 0

	for _, report := range reports {
		for _, rank := range report.RelevantRanks {
			i, tracked := index[rank.Key]
			if !tracked {
				continue
			}

			keys[i].Total++
			total++

			if withinCutoff(rank.Rank, cutoff) {
				keys[i].Hits++
				hits++
			}
		}
	}

	return LongChunkRecall{Keys: keys, Hits: hits, Total: total, Value: ratio(hits, total)}
}
