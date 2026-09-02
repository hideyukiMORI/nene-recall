package postgres

import "fmt"

// 本ファイルは「ベクトルと語彙のスコアをどう1つの順位にまとめるか」を型にする。
//
// 🔴 2つの方式は、どちらが良いか分かっていないから2つある。要件定義 Q-1/Q-3 は
// 実測で決める未決事項であり、選ぶのは測る人であって実装する人ではない。
// 片方を手を抜いて実装すると、その手抜きが「方式の優劣」として記録に残る。
// 両方を同じ丁寧さで書くこと。

// Fusion はベクトルと語彙のスコアを1つの順位にまとめる方式。
//
// 🔑 ゼロ値は FusionWeightedSum である。既定を変えるのは実測を見て ADR を
// 書いてからなので、ゼロ値が現状維持になるように並べてある。
type Fusion int

const (
	// FusionWeightedSum は加重和。alpha*vector + (1-alpha)*lexical。
	//
	// 語彙スコアはクエリごとの候補集合内の最大値で割ってから合成に入れる。
	// 🔴 この正規化が無いと加重和は機能しない。2026-09-02 の実測で
	// lexical_score は 0.000〜0.0036（中央値 0.00016）、vector_score は
	// 0.23〜0.74（中央値 0.44）とスケールが3桁違い、alpha=0.7 と alpha=1.0 で
	// 順位が変わったクエリは 58 件中 1 件だけだった。
	//
	// この方式は alpha の契約（要件定義 F-4・OpenAPI）をそのまま保つ。
	FusionWeightedSum Fusion = iota

	// FusionRRF は順位融合（Reciprocal Rank Fusion）。
	//
	// スコアの値ではなく順位だけを使うので、スケールの違いが原理的に問題に
	// ならない。⚠️ alpha はこの方式では重みとして意味を持たず、無視される。
	//
	// 🔴 これは計測のための実装である。alpha が効かない方式を既定にするのは
	// 要件定義 F-4 と OpenAPI の契約を変えることなので、実測を見て ADR を
	// 書くまで契約は触らない。
	FusionRRF
)

// RRFK は RRF の平滑化定数 k。
//
// 🔴 掃引するときはここ1箇所を変える。既定 60 は RRF の原論文
// (Cormack et al. 2009) が使った値で、この評価セットに合わせて選んだ値ではない。
//
// k が大きいほど上位の順位差が均され、小さいほど1位が強くなる。
// ⚠️ 最適値はコーパスと候補集合の大きさに依存するので、60 を「調整済み」と
// 読まないこと。かつての alpha=0.7 と同じ性質（根拠なし）の数字である
// ——alpha のほうは ADR 0015 が実測で 0.8 に置き換えた。
const RRFK = 60

// fusionWeightedSumName / fusionRRFName は方式の外部表現。
//
// 🔴 レポートとコマンドラインの両方でこの文字列を使う。表記が2つあると、
// 「どの条件で測ったか」の記録と「どう指定したか」が食い違いうる。
const (
	fusionWeightedSumName = "weighted-sum"
	fusionRRFName         = "rrf"
)

// storeName はレポートに記録するバックエンドの名前。
//
// 🔴 config.Store の値 "postgres" と同じ綴りにしてある。設定で選んだ名前と
// レポートに出る名前が違うと、条件を追う人が対応を取れない。
const storeName = "postgres"

// lexicalScorerName は語彙スコアの採点関数の名前。
//
// 🔑 sqlite 側は "fts5-bm25" である (ADR 0017)。同じ評価セットで測った2つの
// レポートを並べたとき、recall の差がどちらの違いによるものかを分けて読む
// ための印になる。
const lexicalScorerName = "ts_rank"

// String は方式の外部表現を返す。
func (f Fusion) String() string {
	switch f {
	case FusionWeightedSum:
		return fusionWeightedSumName
	case FusionRRF:
		return fusionRRFName
	}

	return fmt.Sprintf("unknown(%d)", int(f))
}

// ParseFusion は外部表現から方式を読む。
//
// 🔴 未知の文字列を既定へ黙って倒さない。綴り誤りが「既定で測った」結果として
// 記録され、後から条件を取り違える。計測の条件は必ず明示的に決まること。
func ParseFusion(name string) (Fusion, error) {
	switch name {
	case fusionWeightedSumName:
		return FusionWeightedSum, nil
	case fusionRRFName:
		return FusionRRF, nil
	}

	return 0, fmt.Errorf("%w: %q (want %q or %q)",
		errUnknownFusion, name, fusionWeightedSumName, fusionRRFName)
}

// FusionNames は指定できる方式の一覧を返す。
//
// コマンドラインの説明文に使う。関数で返すのは可変のパッケージ変数を
// 作らないため (GO-007)。
func FusionNames() []string {
	return []string{fusionWeightedSumName, fusionRRFName}
}

// RankingSettings はストアが順位付けに使った条件。
//
// 🔑 レポートに残すためだけの値である。どの方式・どの係数で測ったかが
// 記録されていないレポートは、後から条件を特定できないので正本になれない
// (docs/adr/0013-evaluation-harness-design.md)。
//
// 🔴 定数を直接公開せず、ストアに聞く形にしてある。定数を変えたときに
// レポートが自動で追随し、「コードは変えたが記録は古いまま」が起きない。
type RankingSettings struct {
	// Fusion は融合方式の名前。
	Fusion string
	// Store はバックエンドの名前。
	//
	// 🔴 定数を返すだけの項目だが、レポートに載せるために要る。比較用の
	// SQLite ストア (ADR 0017) が入って以降、「どちらで測ったか」が記録から
	// 読めなければレポートを並べられない。
	Store string
	// LexicalScorer は語彙スコアの採点関数の名前。
	//
	// 🔴 postgres は ts_rank、sqlite は FTS5 の bm25 を使う。2つのストアの
	// recall の差には「ストアの差」と「採点関数の差」が混ざるので、後者を
	// 名指しできる印が要る (ADR 0017)。
	LexicalScorer string
	// TokenizerID は取り込みと検索に使った分割器の識別子 (Tokenizer.ID())。
	//
	// 🔴 これが無いと、bigram と形態素 (ADR 0018) のレポートを条件表で
	// 見分けられない。ファイル名やラベルは人が付けるもので、実際に何で
	// 測ったかの記録にはならない。Store・LexicalScorer と同じ理由である。
	TokenizerID string
	// TsRankNormalization は ts_rank に渡した正規化フラグ。
	TsRankNormalization int
	// RRFK は RRF の平滑化定数。方式が RRF でなくても記録する
	// （条件表が方式によって欠けると、並べて読めなくなる）。
	RRFK int
	// SearchMode は候補集合の作り方（"exhaustive" / "candidates"）。
	//
	// 🔴 alpha と融合方式だけでは条件が決まらない。候補の作り方が変わると
	// 語彙スコアの正規化に使う最大値の母集団が変わり、同じ alpha が別の
	// 効き方をする (ADR 0022 Consequences)。これが無いレポートは、索引を
	// 入れた後の数字を入れる前の数字と並べて読めない。
	SearchMode string
	// CandidateK は候補モードの両側 top-K。
	//
	// 🔴 ポインタなのは「無い」と「0」を区別するためである。exhaustive では
	// K というつまみがそもそも存在しないので nil にする。0 を入れると
	// 「K=0 で測った」と読め、それは candidates では起こりえない条件である
	// （New が K < 1 を拒否する）。TsRankNormalization を sqlite で nil に
	// したのと同じ判断である（様式 v4）。
	CandidateK *int
	// EfSearch は HNSW の探索幅 hnsw.ef_search。CandidateK と同じ理由で
	// ポインタであり、exhaustive では nil になる。
	//
	// 🔑 exhaustive では索引そのものが使われないので、探索幅は結果にも
	// latency にも影響しない。記録すると「効いていた条件」に見える。
	EfSearch *int
}

// statement は方式と候補の作り方に応じた SQL と、その方式が使う $8 の値を返す。
//
// 🔴 $8 は方式ごとに意味が違う（方式A は alpha、方式B は RRF の k）。
// 番号を共有しているのは、参照されない引数があると PostgreSQL が型を決められず
// Parse に失敗するためで、意味が同じだからではない。
//
// 🔴 switch は exhaustive linter が網羅を強制する。方式を足したのに SQL を
// 足し忘れる、という形の抜けを lint で捕まえる。文の選択そのものは
// searchStatement（searchmode.go）が持つ——候補の作り方と方式の2軸を1つの
// switch に畳むと GO-011 の複雑度に掛かり、組み合わせの抜けも読みにくくなる。
//
// 🔑 Store ではなく Fusion のメソッドにしてあるのは、$8 の意味が方式そのものの
// 性質だからである。ストアの状態を1つも見ない。
func (f Fusion) statement(mode SearchMode, alpha float32) (string, any, error) {
	statement, err := searchStatement(mode, f)
	if err != nil {
		return "", nil, err
	}

	switch f {
	case FusionWeightedSum:
		return statement, alpha, nil
	case FusionRRF:
		// alpha は使わない。重みを表す場所がこの方式には無い。
		return statement, RRFK, nil
	}

	return "", nil, fmt.Errorf("%w: %d", errUnknownFusion, int(f))
}

// valid は方式が既知かを返す。
func (f Fusion) valid() bool {
	switch f {
	case FusionWeightedSum, FusionRRF:
		return true
	}

	return false
}
