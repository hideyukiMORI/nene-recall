package postgres

import "fmt"

// 本ファイルは「順位を付ける前に、何行を候補にするか」を型にする。
//
// 🔴 Fusion（スコアをどうまとめるか）とは独立した軸である。2つを1つの列挙に
// 潰さないこと。潰すと exhaustive × weighted-sum・candidates × rrf のような
// 組み合わせが表現できなくなり、ADR 0015 が残した留保——「候補集合を絞る構成
// では RRF の評価が変わりうる」——を測れなくなる。
//
// 🔴 どちらも計測のための実装である。既定を candidates へ移すのは、
// after の実測（recall@10 の低下幅・p95）を見て別の ADR を書いてからである
// (docs/adr/0022-indexed-candidate-search.md Status)。

// SearchMode は候補集合の作り方。
//
// 🔑 ゼロ値は SearchModeExhaustive である。既定を変えるのは実測を見て ADR を
// 書いてからなので、ゼロ値が現状維持になるように並べてある（Fusion と同じ流儀）。
type SearchMode int

const (
	// SearchModeExhaustive は全行に両方のスコアを付けてから並べる（現行）。
	//
	// 🔴 索引が張られていても、この SQL は索引を使わない。ORDER BY が
	// alpha*vector + (1-alpha)*lexical という合成式であり、どの索引の順序でも
	// ないためである。⇒ before/after を並べるとき、この経路の数字は
	// 「索引を張る前」と実質同じものになる (ADR 0022 Decision 3)。
	SearchModeExhaustive SearchMode = iota

	// SearchModeCandidates は両側 top-K の和集合だけにスコアを付けて並べる。
	//
	// ベクトル側は HNSW（vector_ip_ops）、語彙側は GIN（lexemes）が効く形に
	// なっている。合成の式は exhaustive と同じで、違うのは「何行を対象にするか」
	// だけである (ADR 0022 Decision 1)。
	//
	// ⚠️ 語彙スコアの正規化に使う最大値は**候補集合内**の最大値になる。全行の
	// 最大値とは一般に異なるので、alpha の意味が変わる。既定 0.8 をこの経路へ
	// そのまま持ち込まないこと (ADR 0022 Consequences・ADR 0015 Decision 3)。
	SearchModeCandidates
)

// searchModeExhaustiveName / searchModeCandidatesName は外部表現。
//
// 🔴 レポート・環境変数・コマンドラインの3箇所でこの文字列を使う。表記が
// 分かれると、「どの条件で測ったか」の記録と「どう指定したか」が食い違いうる。
// config.SearchMode の値とも同じ綴りである。
const (
	searchModeExhaustiveName = "exhaustive"
	searchModeCandidatesName = "candidates"
)

// DefaultCandidateK は候補モードの両側 top-K の既定値。
//
// 🔴 100 に根拠は無い。ADR 0022 が「まず動かして測る」ために置いた値であって、
// 掃引して選んだ値ではない。alpha の 0.7 がそうだったのと同じ性質の数字なので、
// 「調整済み」であるかのように書かないこと (CLAUDE.md 地雷7)。
const DefaultCandidateK = 100

// DefaultEfSearch は HNSW の探索幅 hnsw.ef_search の既定値。
//
// 40 は pgvector 自身の既定である。K ≤ ef_search でなければ HNSW は K 件を
// 返せないので、K を上げるときは必ずこちらも上げること (ADR 0022 Decision 4)。
const DefaultEfSearch = 40

// String は候補の作り方の外部表現を返す。
func (m SearchMode) String() string {
	switch m {
	case SearchModeExhaustive:
		return searchModeExhaustiveName
	case SearchModeCandidates:
		return searchModeCandidatesName
	}

	return fmt.Sprintf("unknown(%d)", int(m))
}

// ParseSearchMode は外部表現から候補の作り方を読む。
//
// 🔴 未知の文字列を既定へ黙って倒さない。綴り誤りが「exhaustive で測った」
// 結果として記録され、後から条件を取り違える。ParseFusion と同じ理由である。
func ParseSearchMode(name string) (SearchMode, error) {
	switch name {
	case searchModeExhaustiveName:
		return SearchModeExhaustive, nil
	case searchModeCandidatesName:
		return SearchModeCandidates, nil
	}

	return 0, fmt.Errorf("%w: %q (want %q or %q)",
		errUnknownSearchMode, name, searchModeExhaustiveName, searchModeCandidatesName)
}

// SearchModeNames は指定できる候補の作り方の一覧を返す。
//
// コマンドラインの説明文に使う。関数で返すのは可変のパッケージ変数を
// 作らないため (GO-007)。
func SearchModeNames() []string {
	return []string{searchModeExhaustiveName, searchModeCandidatesName}
}

// valid は候補の作り方が既知かを返す。
func (m SearchMode) valid() bool {
	switch m {
	case SearchModeExhaustive, SearchModeCandidates:
		return true
	}

	return false
}

// searchStatement は候補の作り方と融合方式に応じた SQL を返す。
//
// 🔴 2つの switch を入れ子にせず、段で分けてある。exhaustive linter は
// 列挙が増えたときに「片方だけ書き足した」を捕まえるが、入れ子にすると
// 4通りが1つの関数に集まり、GO-011 の複雑度にも掛かる。
//
// 🔑 末尾の return は、範囲外の int を SearchMode に変換された場合の番人である
// (GO-003)。New が構築時に弾いているので通常はここに来ないが、番人が無いと
// 「SQL が空文字」という読めない失敗になる。
func searchStatement(mode SearchMode, f Fusion) (string, error) {
	switch mode {
	case SearchModeExhaustive:
		return exhaustiveStatement(f)
	case SearchModeCandidates:
		return candidateStatement(f)
	}

	return "", fmt.Errorf("%w: %d", errUnknownSearchMode, int(mode))
}

// exhaustiveStatement は全行走査の SQL を返す。
func exhaustiveStatement(f Fusion) (string, error) {
	switch f {
	case FusionWeightedSum:
		return searchWeightedSumSQL, nil
	case FusionRRF:
		return searchRRFSQL, nil
	}

	return "", fmt.Errorf("%w: %d", errUnknownFusion, int(f))
}

// candidateStatement は候補生成の SQL を返す。
//
// 🔴 融合方式ごとに書き分けるが、候補集合の定義（candidateSelectionCTE）は
// 1箇所しか無い。分離条件 (org_id) と絞り込みを方式ごとに書くと、片方だけ
// 直して片方を直し忘れる経路ができる (ADR 0003)。
func candidateStatement(f Fusion) (string, error) {
	switch f {
	case FusionWeightedSum:
		return searchCandidateWeightedSumSQL, nil
	case FusionRRF:
		return searchCandidateRRFSQL, nil
	}

	return "", fmt.Errorf("%w: %d", errUnknownFusion, int(f))
}
