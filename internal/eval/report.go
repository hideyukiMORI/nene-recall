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
const ReportSchema = "nene-recall/eval-report/v1"

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
	Alpha float32 `json:"alpha"`
	// AlphaNote は alpha の根拠の有無。
	//
	// 🔴 数字だけを載せると、読んだ人はそれが調整済みの値だと受け取る。
	// 0.7 に根拠は無い（要件定義 Q-3）。レポートはそれ自体で読まれるので、
	// 但し書きを外部の文書に頼らない。
	AlphaNote string `json:"alpha_note"`
	// Limit は1クエリあたりの取得件数。
	Limit int `json:"limit"`
	// Rounds は計測ラウンド数。
	Rounds int `json:"rounds"`
	// WarmupRounds は計測に含めないウォームアップの周回数。
	WarmupRounds int `json:"warmup_rounds"`
	// KValues は recall@k の k。
	KValues []int `json:"k_values"`
	// PercentileMethod はパーセンタイルの定義。
	PercentileMethod string `json:"percentile_method"`
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
	// RankedKeys は返ってきた上位 limit 件の eval_key を順位順に並べたもの。
	RankedKeys []string `json:"ranked_keys"`
	// RelevantRanks は正解ごとの順位。圏外は null。
	RelevantRanks []RelevantRank `json:"relevant_ranks"`
	// Recall はこのクエリの recall@k。
	Recall []RecallAtK `json:"recall"`
	// ReciprocalRank は最初の正解の順位の逆数。圏外なら 0。
	ReciprocalRank float64 `json:"reciprocal_rank"`
	// Latencies はラウンドごとの所要時間。
	Latencies []RoundLatency `json:"latencies"`
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
