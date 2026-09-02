package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"github.com/hideyukiMORI/nene-recall/internal/org"
)

// ReportSchema はレポートの様式の版。
//
// 様式を変えたら上げる。過去のレポートを読み直すとき、どの様式で書かれたかが
// 分からないと集計値の意味が確定しない。
//
// v2 (2026-09-02) で変えたところ:
//   - ranked_keys が文字列の配列から、スコア付きのオブジェクトの配列になった
//   - summary に gold_length_recall と long_chunk_recall を足した
//   - conditions に gold_length_threshold_runes と long_chunk_keys を足した
//   - conditions に ranking（融合方式・ts_rank のフラグ・RRF の k）を足した
//
// v3 (2026-09-02) で変えたところ（比較用の SQLite ストア・ADR 0017）:
//   - conditions.ranking に store と lexical_scorer を足した
//   - environment に sqlite_version を足した
//
// 🔴 v3 の追加はどれも「どちらのストアで測ったか」を記録するためである。
// 2つのバックエンドのレポートが条件表で見分けられない状態を作らない
// (ADR 0017 Decision 6)。recall の差には「ストアの差」と「語彙採点関数の差」が
// 混ざるので、後者を名指しできなければレポートは読めない。
//
// 🔴 ranked_keys の名前を残して型だけ変えたのは、旧様式を読む道具が
// 「フィールドが無い」で静かに素通りするのではなく、型の不一致で落ちるように
// するためである。v1 のレポート（docs/benchmarks/data/2026-09-0[12]-*.json）は
// 文字列の配列を持つ。
//
// v4 (2026-09-02) で変えたところ（レポートを読み違える経路を3つ塞ぐ）:
//   - conditions.alpha を float64 にした。float32 経由の 0.6000000238418579 が
//     刻まれると、機械での突き合わせで == 0.6 が偽になる
//   - conditions.ranking の ts_rank_normalization と rrf_k をポインタにして
//     omitempty を付けた。そのストアに無い項目は**キーごと出ない**
//   - conditions.alpha_note をストアごとに変えた。配線点 (cmd/eval) が選ぶ
//
// 🔴 v4 の3点はどれも「測っていないことを書かない」ためである。v3 までは
// sqlite のレポートにも ts_rank_normalization: 0 と postgres 向けの alpha_note が
// 入っており、SQLite に ts_rank は無いのに「フラグ 0 で測った」と読めた。
// 条件表が実際の条件と違うレポートは、正本になれない (ADR 0013)。
// v5 (2026-09-02) で変えたところ（形態素分割器・ADR 0018）:
//   - conditions.ranking に tokenizer_id を足した
//
// 🔴 項目を1つ足しただけでも版を上げる。上げないと「tokenizer_id の無い
// レポート」が2つの意味を持つ——様式が古いのか、その実行で分割器を記録し
// なかったのか、読む側から区別できない。v2 と v3 も純粋な追加で上げている。
//
// v6 (2026-09-02) で変えたところ（10万件規模の実測・ADR 0019）:
//   - conditions に distractors（紛れ込みの path・sha256・件数）を足した。
//     投入していなければ**キーごと出ない**
//   - conditions に embed_cache（埋め込みをディスクから再利用したか）を足した
//
// 🔴 v6 も追加だけである。v5 のレポートを読む道具は、増えた2項目を無視すれば
// そのまま読める。それでも版を上げるのは、**紛れ込みの有無が recall の意味を
// 変える**からである。259 件だけで測った 0.83 と、10万件の紛れ込みの中で測った
// 0.83 は同じ数字ではない。版が同じだと「conditions に distractors が無い」ことが
// 「紛れ込み無しで測った」なのか「その項目を持たない古い様式」なのかを区別
// できず、並べて読めなくなる。
const ReportSchema = "nene-recall/eval-report/v6"

// Report は1回の計測の全記録。JSON でそのまま docs/benchmarks/data/ に残す。
//
// 🔑 この様式は「後から検証できない数字は正本になれない」への回答である
// (docs/benchmarks/2026-09-01-baseline.md の追記)。集計値だけを載せたレポートは、
// それが正しいことを第三者が確かめられない。Queries に per-query の生データを
// 必ず持たせ、Summary をそこから再計算できる状態に保つ
// (docs/adr/0013-evaluation-harness-design.md)。
type Report struct {
	// Schema は様式の版。
	Schema string `json:"schema"`
	// MeasuredAt は計測日時（RFC3339）。
	MeasuredAt string `json:"measured_at"`
	// Environment は再現に要る環境の記録。
	Environment Environment `json:"environment"`
	// Inputs は入力の同一性（sha256）。
	Inputs Inputs `json:"inputs"`
	// Conditions は計測条件。
	Conditions Conditions `json:"conditions"`
	// Queries はクエリごとの生データ。
	Queries []QueryReport `json:"queries"`
	// Summary は集計値。Queries から再計算できる。
	Summary Summary `json:"summary"`
}

// Environment は計測時の環境。
//
// 🔴 Ollama の版とモデル digest を入れるのは、Embedder.ID() が
// "bge-m3:1024"（モデル名＋次元）でしかなく、digest もランタイム版も
// 区別しないためである。同じタグで別の重みが引かれても ADR 0005 の不一致検知は
// 発火しない。検知網の穴はここでは塞げないので、せめて記録に残して
// 後から突き合わせられるようにする。
type Environment struct {
	// GitRevision は runtime/debug の vcs.revision。空なら取得できなかったことを表す。
	GitRevision string `json:"git_revision"`
	// GitModified は vcs.modified。true なら未コミットの変更を含む計測である。
	GitModified bool `json:"git_modified"`
	// GoVersion はビルドに使った Go の版。
	GoVersion string `json:"go_version"`
	// EmbedderID は Embedder.ID()。例 "bge-m3:1024"。
	EmbedderID string `json:"embedder_id"`
	// OllamaVersion は Ollama の /api/version。
	OllamaVersion string `json:"ollama_version"`
	// ModelDigest は /api/tags のモデル digest。照合できなければ空。
	ModelDigest string `json:"model_digest"`
	// PostgresVersion は SELECT version() の結果。
	PostgresVersion string `json:"postgres_version"`
	// PgvectorVersion は pg_extension の extversion。
	PgvectorVersion string `json:"pgvector_version"`
	// SQLiteVersion は SELECT sqlite_version() の結果。
	//
	// 🔴 postgres で測ったレポートでは空、sqlite で測ったレポートでは
	// PostgresVersion と PgvectorVersion が空になる。どちらのストアで測っても
	// 「使ったエンジンの版」が残ることが要点である。版が残らない数字は
	// 後から検証できず、正本になれない
	// (docs/benchmarks/2026-09-01-baseline.md の追記)。
	SQLiteVersion string `json:"sqlite_version"`
	// GPUNote は GPU の占有状況などの自己申告。
	//
	// 基準ベンチは他アプリが GPU を 5.7GB / 17% 使っている状態で測られており、
	// 「下振れ側の数字として読むこと」と注記されている。同じ但し書きを
	// 付けられるようにするための欄である。
	GPUNote string `json:"gpu_note"`
}

// FileInput は入力ファイル1つの同一性。
type FileInput struct {
	// Path は読み込んだ場所。
	Path string `json:"path"`
	// SHA256 は内容のハッシュ。同じ数字が同じ入力から出たことを後から確かめられる。
	SHA256 string `json:"sha256"`
	// Count は件数（コーパスはチャンク数、クエリはクエリ数、語彙はタグ数）。
	Count int `json:"count"`
}

// Inputs は評価セットの同一性。
type Inputs struct {
	// Corpus は corpus.jsonl。
	Corpus FileInput `json:"corpus"`
	// Queries は queries.jsonl。
	Queries FileInput `json:"queries"`
	// Tags はタグ語彙。
	Tags FileInput `json:"tags"`
}

// Conditions は計測条件。
type Conditions struct {
	// OrgID は投入・検索に使ったテナント。
	//
	// 🔴 int64 にしないこと。JSON への出方は同じ（数値）だが、org.ID を
	// 剥がした瞬間に「別の識別子を入れても気づかない」型に戻る。
	// レポートの構造体だからといって例外にしない (CNF-002 / ADR 0003)。
	OrgID org.ID `json:"org_id"`
	// Alpha は合成の重み。
	//
	// 🔴 float64 で持つ。検索へ渡る値は float32 だが (index.Query.Alpha)、
	// 記録するのは float32 へ落とす前の10進である。float64(float32(0.6)) は
	// 0.6000000238418579 になり、レポートを機械で突き合わせる側で == 0.6 が
	// 偽になる。レポートは第三者が集計値を再計算するための正本なので
	// (ADR 0013)、入力に書かれた値がそのまま読める形で刻む。
	Alpha float64 `json:"alpha"`
	// AlphaNote は alpha の読み方の但し書き。
	//
	// 🔴 数字だけを載せると、読んだ人はそれが普遍的に調整済みの値だと受け取る。
	// alpha の最適値は正規化方式・分割器・埋め込みモデル・候補集合の作り方に
	// 依存する条件付きの値である（ADR 0015 Decision 3）。レポートはそれ自体で
	// 読まれるので、但し書きを外部の文書に頼らない。
	//
	// 🔴 文言は配線点 (cmd/eval) が store に応じて選ぶ。定数をこの層に置くと、
	// ストアを知らないはずの internal/eval に Postgres の事情が漏れ (ARC-001)、
	// SQLite のレポートに「ADR 0015 が選んだ 0.8」という測っていない話が載る。
	AlphaNote string `json:"alpha_note"`
	// Limit は1クエリあたりの取得件数。
	Limit int `json:"limit"`
	// Rounds は計測ラウンド数。
	Rounds int `json:"rounds"`
	// WarmupRounds は計測に含めないウォームアップの周回数。
	WarmupRounds int `json:"warmup_rounds"`
	// KValues は recall@k の k。
	KValues []int `json:"k_values"`
	// GoldLengthThresholdRunes は gold チャンクを長短に分ける閾値（文字数）。
	//
	// 🔑 評価セット自身が分割に使っている値と同じにしてある
	// (testdata/eval/README.md)。長文チャンクへの依存度を Q-1 のレポートに
	// 必ず併記せよ、という申し送りへの回答がこの内訳である。
	GoldLengthThresholdRunes int `json:"gold_length_threshold_runes"`
	// LongChunkKeys は名指しで追跡する長文 gold チャンク。
	LongChunkKeys []string `json:"long_chunk_keys"`
	// Ranking はストアが順位付けに使った条件。
	//
	// 🔴 alpha だけでは条件が決まらない。融合方式によって alpha の効き方は
	// 変わり（順位融合では無視される）、語彙スコアの作り方も ts_rank の
	// フラグで変わる。どれか1つでも欠けたレポートは、後から条件を特定できない
	// ので正本になれない (docs/adr/0013-evaluation-harness-design.md)。
	Ranking RankingSettings `json:"ranking"`
	// PercentileMethod はパーセンタイルの定義。
	PercentileMethod string `json:"percentile_method"`
	// Distractors は投入した紛れ込みの同一性。投入していなければキーごと出ない。
	//
	// 🔴 これが無いと、259 件だけで測った数字と 10万件の紛れ込みの中で測った
	// 数字を並べて読めない。recall の定義は変わらないが、意味は変わる——
	// 「正解が上位 10 件に残ったか」の難しさが桁で違う
	// (docs/adr/0019-large-scale-benchmark-corpus.md Decision 2)。
	//
	// 🔴 ポインタなのは「無い」と「0 件」を区別するためである。0 件の
	// 紛れ込みファイルは LoadDistractorFile が拒否するので、nil だけが
	// 「紛れ込み無しで測った」を意味する (GO-004)。
	Distractors *FileInput `json:"distractors,omitempty"`
	// EmbedCache は埋め込みをディスクから再利用したか。
	//
	// 🔑 true のとき、投入に要した時間は GPU の速さを表さない。クエリ側の
	// 埋め込みはキャッシュしないので、系統1 の latency は影響を受けない
	// (ADR 0019 Decision 3)。この区別が記録に無いと、取り込み時間の桁違いが
	// 「速くなった」と読まれる。
	EmbedCache bool `json:"embed_cache"`
}

// RankingSettings はストアが順位付けに使った条件の記録。
//
// 🔑 internal/eval はこの中身を解釈しない。具体ストアを知らない層なので
// (ARC-001)、値は配線点 (cmd/eval) が集めてそのまま運ぶ。
//
// 🔴 そのストアに存在しないつまみは**キーごと出さない**。v3 までは
// ts_rank_normalization が sqlite のレポートにも 0 で入っており、SQLite に
// ts_rank は無いのに「フラグ 0 で測った」と読めた。測っていないことを
// 書かないのが条件表の役目である。
type RankingSettings struct {
	// Fusion は融合方式の名前（例 "weighted-sum" / "rrf"）。
	Fusion string `json:"fusion"`
	// Store はバックエンドの名前（"postgres" / "sqlite"）。
	//
	// 🔴 これが無いと、2つのストアのレポートを条件表で見分けられない
	// (ADR 0017 Decision 6)。ファイル名やラベルは人が付けるもので、
	// 実際に何で測ったかの記録にはならない。
	Store string `json:"store"`
	// LexicalScorer は語彙スコアの採点関数の名前（"ts_rank" / "fts5-bm25"）。
	//
	// 🔴 2つのストアの recall の差には「ストアの差」と「採点関数の差」が
	// 混ざる。分けて読むための印であり、Store とは別に要る。
	LexicalScorer string `json:"lexical_scorer"`
	// TokenizerID は取り込みと検索に使った分割器の識別子。
	//
	// 🔴 例 "bigram:nfkc-lower:v1" / "kagome:ipadic:ascii-words:v1"。
	// 分割器は語彙スコアの入力そのものを変えるので、これが無いレポートは
	// 条件を特定できない (ADR 0018 Decision 3)。alpha の最適値も分割器に
	// 条件付きである (ADR 0015 Decision 3)。
	TokenizerID string `json:"tokenizer_id"`
	// TsRankNormalization は ts_rank に渡した正規化フラグ。postgres 専用。
	//
	// 🔴 ポインタなのは「無い」と「0」を区別するためである。postgres の
	// 正規化フラグは **0 が正しい値**なので、int の omitempty では実際に
	// 測った条件のほうが消えてしまう。nil は「この採点関数にそのつまみが
	// 無い」を意味し、JSON からはキーごと消える (GO-004: nil の意味は一つ)。
	TsRankNormalization *int `json:"ts_rank_normalization,omitempty"`
	// RRFK は RRF の平滑化定数。TsRankNormalization と同じく postgres 専用で、
	// 同じ理由でポインタである。
	RRFK *int `json:"rrf_k,omitempty"`
}

// QueryReport はクエリ1件の生データ。
type QueryReport struct {
	// QueryID はクエリの識別子。
	QueryID string `json:"query_id"`
	// Text は検索語。
	Text string `json:"text"`
	// Tags はクエリの分類。
	Tags []string `json:"tags"`
	// Relevant は正解の eval_key。
	Relevant []string `json:"relevant"`
	// RankedKeys は返ってきた上位 limit 件を順位順に並べたもの。
	//
	// 🔴 eval_key だけでなく3つのスコアを併記する。外したときにベクトル側と
	// 語彙側のどちらが原因かを per-query で追えることが、alpha (Q-3) と
	// 語彙手法 (Q-1) の判断に要る。合成後の値だけでは切り分けられない。
	// index.Result が2つのスコアを分けて持つ理由がここで効く。
	RankedKeys []RankedEntry `json:"ranked_keys"`
	// RelevantRanks は正解ごとの順位。圏外は null。
	RelevantRanks []RelevantRank `json:"relevant_ranks"`
	// Recall はこのクエリの recall@k。
	Recall []RecallAtK `json:"recall"`
	// ReciprocalRank は最初の正解の順位の逆数。圏外なら 0。
	ReciprocalRank float64 `json:"reciprocal_rank"`
	// Latencies はラウンドごとの所要時間。
	Latencies []RoundLatency `json:"latencies"`
}

// RankedEntry は返ってきた1件と、その順位を決めたスコア。
//
// スコアは float64 で持つ。index.Result は float32 だが、JSON に出す時点で
// どちらにせよ十進表記になるので、桁を落とす理由が無い。
type RankedEntry struct {
	// Key は返ってきたチャンクの eval_key。
	Key string `json:"eval_key"`
	// Score は合成スコア。順位はこの値で決まっている。
	Score float64 `json:"score"`
	// VectorScore はベクトル側のスコア。
	VectorScore float64 `json:"vector_score"`
	// LexicalScore は語彙側のスコア。
	LexicalScore float64 `json:"lexical_score"`
}

// RelevantRank は正解1件の順位。
type RelevantRank struct {
	// Key は正解の eval_key。
	Key string `json:"eval_key"`
	// Rank は1始まりの順位。圏外なら null。
	Rank *int `json:"rank"`
}

// RecallAtK は k と、その k での recall。
type RecallAtK struct {
	// K は上位何件を見たか。
	K int `json:"k"`
	// Value は recall の値。
	Value float64 `json:"value"`
}

// RoundLatency は1ラウンドの所要時間。
//
// 2系統をそれぞれ実測する。🔴 系統2 を「系統1 から埋め込み時間を引く」形で
// 求めない。異なる分布のパーセンタイル同士の引き算に統計的な意味は無い
// (docs/adr/0013-evaluation-harness-design.md)。
type RoundLatency struct {
	// Round は1始まりのラウンド番号。
	Round int `json:"round"`
	// WithEmbeddingMS は Search（クエリの埋め込みを含む）の所要ミリ秒。
	WithEmbeddingMS float64 `json:"with_embedding_ms"`
	// WithoutEmbeddingMS は SearchVector（埋め込み済みベクトルを渡す）の所要ミリ秒。
	WithoutEmbeddingMS float64 `json:"without_embedding_ms"`
}

// Summary は集計値。すべて Report.Queries から再計算できる。
type Summary struct {
	// QueryCount は集計に使ったクエリ数。
	QueryCount int `json:"query_count"`
	// Recall は全クエリの recall@k の平均。
	Recall []RecallAtK `json:"recall"`
	// MRR は ReciprocalRank の平均。
	MRR float64 `json:"mrr"`
	// Latency は2系統の所要時間。
	Latency LatencySummary `json:"latency"`
	// TagRecall はタグ別の recall@10。
	//
	// 🔑 総合値は数十クエリでは動きにくい。カテゴリ別の壊れ方のほうが
	// 診断情報として濃いので、必ず併記する。
	TagRecall []TagRecall `json:"tag_recall"`
	// MicroRecall は正解チャンク単位（micro）の内訳。
	//
	// ⚠️ Recall（クエリ単位のマクロ平均）とは別物である。正解が1件のクエリと
	// 8件のクエリを同じ重みで扱うマクロ平均に対し、こちらは正解チャンクを
	// 1件ずつ数える。基準線が「131 / 236」と書いているのはこの値である。
	MicroRecall MicroRecall `json:"micro_recall"`
	// GoldLengthRecall は gold チャンクの長さ別の micro 内訳。
	//
	// 🔴 Q-1（tsvector か BM25 か）を歪めうる交絡要因への対処である。
	// 評価セットには 1,136字の一覧表チャンクが5クエリの正解として繰り返し
	// 現れており、BM25 は文書長で正規化し ts_rank は既定でしない。
	// 併記が無いと、出た差が長文優遇によるものか検索品質によるものかを
	// 切り分けられない (testdata/eval/README.md「既知の性質」)。
	GoldLengthRecall []GoldLengthBucket `json:"gold_length_recall"`
	// LongChunkRecall は名指しの長文 gold チャンクの追跡。
	LongChunkRecall LongChunkRecall `json:"long_chunk_recall"`
}

// MicroRecall は正解チャンク単位の内訳。
//
// 🔑 分数のまま持つ。割った値だけを載せると、それが何件中の何件なのかが
// 失われ、1件の増減がどれだけ効くのかを読む人が判断できない
// (testdata/eval/README.md「数字の読み方」)。
type MicroRecall struct {
	// Hits は上位 Cutoff 件以内に入った正解チャンクの延べ数。
	Hits int `json:"hits"`
	// Total は正解チャンクの延べ数。
	Total int `json:"total"`
	// Cutoff は「上位何件以内」で数えたか。
	Cutoff int `json:"cutoff"`
	// Value は Hits / Total。
	Value float64 `json:"value"`
}

// GoldLengthBucket は gold チャンクの長さで区切った micro 内訳の1区分。
type GoldLengthBucket struct {
	// Label は区分の名前（"<=520" など）。
	Label string `json:"label"`
	// MinRunes は区分の下限（含む）。
	MinRunes int `json:"min_runes"`
	// MaxRunes は区分の上限（含む）。上限が無い区分は 0。
	MaxRunes int `json:"max_runes"`
	// Hits は上位 Cutoff 件以内に入った延べ数。
	Hits int `json:"hits"`
	// Total はこの区分に属する正解チャンクの延べ数。
	Total int `json:"total"`
	// Value は Hits / Total。Total が 0 なら 0。
	Value float64 `json:"value"`
}

// LongChunkRecall は名指しの長文 gold チャンクの追跡。
//
// 🔑 長さの区分だけでは足りない。評価セットの偏りは「長い chunk が多い」では
// なく「特定の3つが繰り返し正解になっている」という形をしているので、
// その3つを名指しで追う (testdata/eval/README.md「既知の性質」)。
type LongChunkRecall struct {
	// Keys はチャンクごとの内訳。
	Keys []LongChunkKey `json:"keys"`
	// Hits は3件ぶんを合わせた延べヒット数。
	Hits int `json:"hits"`
	// Total は3件ぶんを合わせた延べ正解数。
	Total int `json:"total"`
	// Value は Hits / Total。Total が 0 なら 0。
	Value float64 `json:"value"`
}

// LongChunkKey は名指しの長文チャンク1件の内訳。
type LongChunkKey struct {
	// Key は eval_key。
	Key string `json:"eval_key"`
	// Runes はコーパス上の文字数。コーパスに無ければ 0。
	//
	// 🔴 0 は「評価セットを作り直してこのキーが消えた」ことの印である。
	// 名指しの追跡は評価セットの中身に依存しているので、消えたことが
	// レポートから読めなければならない。
	Runes int `json:"runes"`
	// Hits は上位 Cutoff 件以内に入った回数。
	Hits int `json:"hits"`
	// Total このキーが正解になっているクエリ数。
	Total int `json:"total"`
}

// LatencySummary は2系統の所要時間の集計。
type LatencySummary struct {
	// WithEmbedding は Search 側。利用者から見た応答時間に対応する。
	WithEmbedding LatencyStats `json:"with_embedding"`
	// WithoutEmbedding は SearchVector 側。要件定義 §8 の性能要件はこちら。
	WithoutEmbedding LatencyStats `json:"without_embedding"`
}

// LatencyStats は1系統の所要時間の統計。
type LatencyStats struct {
	// Samples はサンプル数（クエリ数 × ラウンド数）。
	Samples int `json:"samples"`
	// MinMS は最小のミリ秒。
	MinMS float64 `json:"min_ms"`
	// P50MS は中央値のミリ秒。
	P50MS float64 `json:"p50_ms"`
	// P95MS は 95 パーセンタイルのミリ秒。
	P95MS float64 `json:"p95_ms"`
	// MaxMS は最大のミリ秒。
	MaxMS float64 `json:"max_ms"`
}

// TagRecall はタグ1つぶんの集計。
type TagRecall struct {
	// Tag はクエリの分類。
	Tag string `json:"tag"`
	// QueryCount はそのタグを持つクエリ数。
	QueryCount int `json:"query_count"`
	// Recall はそのタグに属するクエリの recall@k の平均。
	Recall []RecallAtK `json:"recall"`
}

// Measurement は計測が生んだ部分。
//
// 環境と入力の同一性は配線点 (cmd/eval) が集める。internal/eval は os も
// database/sql も知らないので、そこは持てないし持つべきでもない。
type Measurement struct {
	// Conditions は計測条件。
	Conditions Conditions `json:"conditions"`
	// Queries はクエリごとの生データ。
	Queries []QueryReport `json:"queries"`
	// Summary は集計値。
	Summary Summary `json:"summary"`
}

// NewReport は計測結果に環境と入力の記録を足して、書き出せるレポートにする。
//
// measuredAt を引数で受けるのは、時刻を読む場所を配線点に寄せてテストを
// 決定的に保つため（ARC-002 の精神。このパッケージは中核ではないが、
// 同じ理由で時計を持たない）。
func NewReport(env Environment, in Inputs, m Measurement, measuredAt time.Time) Report {
	return Report{
		Schema:      ReportSchema,
		MeasuredAt:  measuredAt.Format(time.RFC3339),
		Environment: env,
		Inputs:      in,
		Conditions:  m.Conditions,
		Queries:     m.Queries,
		Summary:     m.Summary,
	}
}

// SHA256 は入力の内容ハッシュを16進で返す。
//
// 入力の同一性をレポートに載せるために使う。「同じ評価セットで測った」を
// ファイル名や日付ではなく内容で言えるようにする。
func SHA256(r io.Reader) (string, error) {
	h := sha256.New()

	if _, err := io.Copy(h, r); err != nil {
		return "", fmt.Errorf("%w: hash input: %w", ErrInvalidDataset, err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// durationMS は所要時間をミリ秒の実数にする。
//
// JSON にナノ秒の整数で出すと桁が読めない。人が読む数字は ms にそろえる。
func durationMS(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}
