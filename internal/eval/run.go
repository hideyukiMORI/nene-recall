package eval

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/index"
	"github.com/hideyukiMORI/nene-recall/internal/org"
)

// DefaultLimit は1クエリあたりの取得件数。
//
// recall@10 が主指標なので、その計算に要る最小限にそろえてある。
// ⚠️ 正解が 10 件を超えるクエリの注釈が生まれたら、この値と KValues を
// 再判断すること（recall@10 の上限が 1.0 に届かなくなる・ADR 0013）。
const DefaultLimit = 10

// DefaultRounds は各クエリを繰り返す回数。
//
// 5 回にしているのは、p95 を出すのに1クエリ1回では標本が足りず、かといって
// 回数を増やしても検索は決定的なので順位は1回目と変わらないためである。
// 増やして得られるのは latency の標本数だけで、それは線形にしか効かない。
const DefaultRounds = 5

// WarmupRounds は計測に含めないウォームアップの周回数。
//
// 🔴 これを 0 にしないこと。Ollama のコールドスタート（モデルのロードを含む
// 初回）は実測 18.4 秒である (docs/benchmarks/2026-09-01-baseline.md)。
// 1サンプル混ざるだけで p95 が壊れる。
const WarmupRounds = 1

// alphaNote は alpha が調整済みでないことをレポート自身に書き残す文言。
//
// 🔴 レポートは単体で読まれる。数字だけを載せると、読んだ人はそれが調整済みの
// 値だと受け取る。alpha に根拠が無いことは要件定義 Q-3 の未決事項であり、
// この評価が決着させる対象そのものである
// (docs/adr/0009-retrieval-evaluation-is-in-scope.md)。
const alphaNote = "not tuned: alpha is a placeholder until this evaluation settles it " +
	"(requirements Q-3 / ADR 0009). Do not read this value as calibrated."

// GoldLengthThreshold は gold チャンクを長短に分ける文字数の閾値。
//
// 🔑 520 は評価セット自身が分割に使っている値である
// (testdata/eval/README.md: 単独で 520字を超えるブロックはそのまま1チャンクに
// なり、26件が該当する)。閾値を独自に決めると、内訳が評価セットの構造と
// 対応しなくなって読めなくなる。
const GoldLengthThreshold = 520

// LongGoldKeys は名指しで追跡する長文 gold チャンクを返す。
//
// 🔑 評価セットの偏りは「長い chunk が多い」ではなく「特定の3つが繰り返し
// 正解になっている」という形をしている。readme#005 は 1,136字で5クエリの、
// requirements#023 は 564字で5クエリの、requirements#008 は 611字で5クエリの
// 正解である (testdata/eval/README.md「既知の性質」)。長さの区分だけでは
// この偏りが見えないので、3件を名指しで追う。
//
// 🔴 評価セットを作り直したらこの一覧を見直すこと。存在しないキーになっても
// 計測は止まらず、レポートの runes が 0 になって表面化する（止めないのは、
// 付帯情報の欠落で計測そのものを失うほうが損だからである）。
//
// 関数で返すのは可変のパッケージ変数を作らないため (GO-007)。
func LongGoldKeys() []string {
	return []string{"readme#005", "requirements#023", "requirements#008"}
}

// KValues は recall@k を出す k の一覧。
//
// 関数で返すのは、可変のパッケージ変数を作らないため (GO-007)。
// 呼び出し側が返り値を書き換えても他に影響しない。
func KValues() []int { return []int{1, 5, 10} }

// EmbedQuery はクエリ本文を1本のベクトルに変換する。
//
// 🔴 embed.Embedder ではなく関数型で受ける。internal/eval の依存を
// internal/index・internal/chunk・internal/org と標準ライブラリに閉じるためで、
// 具体的な埋め込みプロバイダはもちろん、その契約パッケージも知らない。
// 配線点 (cmd/eval) が embed.KindQuery を渡す閉包を作って注入する。
type EmbedQuery func(ctx context.Context, text string) ([]float32, error)

// vectorSearcher は埋め込み済みのベクトルで検索する口。
//
// 🔴 index.Searcher の契約には無い。計測のための口だからである
// (docs/adr/0013-evaluation-harness-design.md)。ここでローカルに宣言することで、
// internal/eval は具体ストアを知らないまま *postgres.Store を受け取れる。
type vectorSearcher interface {
	SearchVector(ctx context.Context, q index.Query, vector []float32) ([]index.Result, error)
}

// Dependencies は計測ループが使う口。配線点 (cmd/eval) が組み立てて注入する。
//
// ゼロ値は無効である。すべてのフィールドに値が要る (GO-003)。
type Dependencies struct {
	// Writer は評価コーパスを投入する。返る id の順序が eval_key の写像になる。
	Writer index.Writer
	// Searcher は系統1（埋め込み往復を含む）の計測対象。
	Searcher index.Searcher
	// VectorSearcher は系統2（埋め込み往復を除く）の計測対象。
	VectorSearcher vectorSearcher
	// EmbedQuery は計測の外でクエリを1回だけ埋め込む。
	EmbedQuery EmbedQuery
}

// Options は1回の計測の条件。
//
// ゼロ値は無効である。
type Options struct {
	// OrgID は投入・検索に使うテナント。org.ParseID / org.NewID を通した値を渡すこと。
	OrgID org.ID
	// Alpha は合成の重み。根拠はまだ無い（要件定義 Q-3）。
	Alpha float32
	// Limit は1クエリあたりの取得件数。DefaultLimit を参照。
	Limit int
	// Rounds は計測ラウンド数。DefaultRounds を参照。
	Rounds int
}

// Runner は評価を実行する。
//
// ゼロ値は無効である。必ず NewRunner を通すこと。
type Runner struct {
	deps Dependencies
}

// NewRunner は依存を検証して Runner を作る。
//
// 🔴 nil の口を持ったまま走らせない。計測の途中で落ちると、そこまでの
// 所要時間が「途中で止まった条件で測られた数字」として残りうる。
func NewRunner(deps Dependencies) (*Runner, error) {
	switch {
	case deps.Writer == nil:
		return nil, fmt.Errorf("%w: Writer is required", ErrMissingDependency)
	case deps.Searcher == nil:
		return nil, fmt.Errorf("%w: Searcher is required", ErrMissingDependency)
	case deps.VectorSearcher == nil:
		return nil, fmt.Errorf("%w: VectorSearcher is required", ErrMissingDependency)
	case deps.EmbedQuery == nil:
		return nil, fmt.Errorf("%w: EmbedQuery is required", ErrMissingDependency)
	}

	return &Runner{deps: deps}, nil
}

// plan は計測ループの入力一式。
//
// 引数を4つ以下に保つための入れ物 (GO-011)。queries と vectors は同じ添字で
// 対応しており、両者がずれないよう1つの値として持ち回る。
type plan struct {
	queries []Query
	vectors [][]float32
	// keys は採番 id から eval_key を引く。実行中のプロセス内にだけ存在し、
	// 永続化しない（永続化すると「古い写像」という新たな不安定性が生まれる）。
	keys map[int64]string
	opts Options
}

// roundResult は1ラウンドぶんの計測結果。
type roundResult struct {
	ranked           []RankedEntry
	withEmbedding    time.Duration
	withoutEmbedding time.Duration
}

// Measure はコーパスを投入し、全クエリを計測して結果を返す。
//
// 手順は固定する。(1) コーパスを投入して採番 id の写像を作る
// (2) 全クエリを計測の外で1回ずつ埋め込む (3) 1周ウォームアップする
// (4) 各クエリを Rounds 回計測する。品質指標は1周目の順位から出す
// （検索は決定的なので繰り返しても順位は変わらない）。
func (r *Runner) Measure(ctx context.Context, ds Dataset, opts Options) (Measurement, error) {
	if err := opts.validate(); err != nil {
		return Measurement{}, err
	}

	keys, err := r.ingest(ctx, opts.OrgID, ds.Passages)
	if err != nil {
		return Measurement{}, err
	}

	vectors, err := r.embedQueries(ctx, ds.Queries)
	if err != nil {
		return Measurement{}, err
	}

	p := plan{queries: ds.Queries, vectors: vectors, keys: keys, opts: opts}

	if err := r.warmUp(ctx, p); err != nil {
		return Measurement{}, err
	}

	reports, err := r.measureQueries(ctx, p)
	if err != nil {
		return Measurement{}, err
	}

	return Measurement{
		Conditions: opts.conditions(),
		Queries:    reports,
		Summary:    summarize(reports, goldRunes(ds.Passages)),
	}, nil
}

// validate は条件の誤りを計測の前に落とす。
func (o Options) validate() error {
	switch {
	case o.OrgID < 1:
		return fmt.Errorf("%w: org_id is required", ErrMeasure)
	case o.Limit < 1:
		return fmt.Errorf("%w: limit must be at least 1, got %d", ErrMeasure, o.Limit)
	case o.Rounds < 1:
		return fmt.Errorf("%w: rounds must be at least 1, got %d", ErrMeasure, o.Rounds)
	case o.Alpha < 0 || o.Alpha > 1:
		return fmt.Errorf("%w: alpha must be within [0,1], got %v", ErrMeasure, o.Alpha)
	}

	return nil
}

// conditions は計測条件をレポートの形にする。
func (o Options) conditions() Conditions {
	return Conditions{
		OrgID:        o.OrgID,
		Alpha:        o.Alpha,
		AlphaNote:    alphaNote,
		Limit:        o.Limit,
		Rounds:       o.Rounds,
		WarmupRounds: WarmupRounds,
		KValues:      KValues(),

		GoldLengthThresholdRunes: GoldLengthThreshold,
		LongChunkKeys:            LongGoldKeys(),
		PercentileMethod:         PercentileMethod,
	}
}

// indexQuery は評価クエリを検索要求にする。
func (p plan) indexQuery(q Query) index.Query {
	return index.Query{
		OrgID:       p.opts.OrgID,
		Text:        q.Text,
		Limit:       p.opts.Limit,
		Alpha:       p.opts.Alpha,
		DocumentIDs: nil,
		SourceIDs:   nil,
	}
}

// ingest はコーパスを投入し、採番 id から eval_key への写像を作る。
//
// 🔑 ここが ADR 0013 の中心である。index.Writer.Put は「採番された id を入力と
// 同じ順で返す」ことを契約にしているので、投入順を知っている評価ランナーは
// SQL を1本も足さずに写像を得られる。正解セットが id を持たずに済むのは
// このためである。
func (r *Runner) ingest(ctx context.Context, orgID org.ID, passages []Passage) (map[int64]string, error) {
	if len(passages) == 0 {
		return nil, fmt.Errorf("%w: the corpus is empty", ErrInvalidDataset)
	}

	ids, err := r.deps.Writer.Put(ctx, orgID, toChunks(orgID, passages))
	if err != nil {
		return nil, fmt.Errorf("%w: put corpus: %w", ErrMeasure, err)
	}

	// 契約が破られたら写像がずれる。ずれた写像は「順位は正しいのに recall だけが
	// 低い」という、原因に辿り着けない壊れ方をする。ここで止める。
	if len(ids) != len(passages) {
		return nil, fmt.Errorf("%w: the writer returned %d ids for %d passages",
			ErrMeasure, len(ids), len(passages))
	}

	keys := make(map[int64]string, len(ids))
	for i, id := range ids {
		keys[id] = passages[i].Key
	}

	if len(keys) != len(ids) {
		return nil, fmt.Errorf("%w: the writer returned duplicate ids", ErrMeasure)
	}

	return keys, nil
}

// toChunks は評価コーパスを投入できる形にする。
//
// document_id / source_id は Source 名の初出順に採番する。注釈を書く側が
// 数値の id を一切書かずに済むようにするためで、この採番は実行のたびに
// 同じ入力から同じ値になる（写像と違って安定している）。
func toChunks(orgID org.ID, passages []Passage) []chunk.Chunk {
	chunks := make([]chunk.Chunk, 0, len(passages))
	sourceIDs := make(map[string]int64, len(passages))
	nextIndex := make(map[string]int, len(passages))

	for _, p := range passages {
		id, known := sourceIDs[p.Source]
		if !known {
			id = int64(len(sourceIDs) + 1)
			sourceIDs[p.Source] = id
		}

		chunks = append(chunks, chunk.Chunk{
			ID:           0, // 明示 id は受け付けない（Phase 1）
			OrgID:        orgID,
			DocumentID:   id,
			SourceID:     id,
			ChunkIndex:   nextIndex[p.Source],
			Content:      p.Content,
			PageNumber:   nil,
			SectionLabel: nil,
		})

		nextIndex[p.Source]++
	}

	return chunks
}

// embedQueries は全クエリを計測の外で1回ずつ埋め込む。
//
// 🔴 系統2（埋め込み往復を除く）の計測に使うベクトルは、必ず計測の外で作る。
// ループの中で作ると、除いたはずの往復が計測に混ざる。
func (r *Runner) embedQueries(ctx context.Context, queries []Query) ([][]float32, error) {
	if len(queries) == 0 {
		return nil, fmt.Errorf("%w: there are no queries to measure", ErrInvalidDataset)
	}

	vectors := make([][]float32, 0, len(queries))

	for _, q := range queries {
		vector, err := r.deps.EmbedQuery(ctx, q.Text)
		if err != nil {
			return nil, fmt.Errorf("%w: embed query %q: %w", ErrMeasure, q.ID, err)
		}

		vectors = append(vectors, vector)
	}

	return vectors, nil
}

// warmUp は計測に含めない1周。
//
// 🔴 コールドスタート（実測 18.4 秒）を計測から締め出すのが目的である。
// 2系統とも通すのは、片方だけ温めると温めなかった側に初回コストが乗るため。
func (r *Runner) warmUp(ctx context.Context, p plan) error {
	for i, q := range p.queries {
		if _, err := r.deps.Searcher.Search(ctx, p.indexQuery(q)); err != nil {
			return fmt.Errorf("%w: warm-up search %q: %w", ErrMeasure, q.ID, err)
		}

		if _, err := r.deps.VectorSearcher.SearchVector(ctx, p.indexQuery(q), p.vectors[i]); err != nil {
			return fmt.Errorf("%w: warm-up vector search %q: %w", ErrMeasure, q.ID, err)
		}
	}

	return nil
}

// measureQueries は全クエリを計測する。
func (r *Runner) measureQueries(ctx context.Context, p plan) ([]QueryReport, error) {
	reports := make([]QueryReport, 0, len(p.queries))

	for i := range p.queries {
		report, err := r.measureQuery(ctx, p, i)
		if err != nil {
			return nil, err
		}

		reports = append(reports, report)
	}

	return reports, nil
}

// measureQuery は1クエリを Rounds 回計測する。
//
// 順位は1周目のものを採る。検索は決定的なので繰り返しても変わらないが、
// 「どのラウンドの順位を報告したか」を曖昧にしないために固定する。
func (r *Runner) measureQuery(ctx context.Context, p plan, i int) (QueryReport, error) {
	latencies := make([]RoundLatency, 0, p.opts.Rounds)

	var ranked []RankedEntry

	for round := 1; round <= p.opts.Rounds; round++ {
		result, err := r.runRound(ctx, p, i)
		if err != nil {
			return QueryReport{}, fmt.Errorf("query %q round %d: %w", p.queries[i].ID, round, err)
		}

		if round == 1 {
			ranked = result.ranked
		}

		latencies = append(latencies, RoundLatency{
			Round:              round,
			WithEmbeddingMS:    durationMS(result.withEmbedding),
			WithoutEmbeddingMS: durationMS(result.withoutEmbedding),
		})
	}

	return newQueryReport(p.queries[i], ranked, latencies), nil
}

// runRound は2系統を1回ずつ実行して所要時間を測る。
//
// 系統1 を先に走らせるのは、利用者から見た経路（埋め込みを含む）を
// 毎ラウンドの先頭に置くためである。順番を入れ替えると、キャッシュの
// 温まり方が系統2 に有利に働く。
func (r *Runner) runRound(ctx context.Context, p plan, i int) (roundResult, error) {
	q := p.indexQuery(p.queries[i])

	included, withEmbedding, err := timedSearch(ctx, r.deps.Searcher, q)
	if err != nil {
		return roundResult{}, err
	}

	excluded, withoutEmbedding, err := timedSearchVector(ctx, r.deps.VectorSearcher, q, p.vectors[i])
	if err != nil {
		return roundResult{}, err
	}

	rankedIncluded, err := rankedEntries(included, p.keys)
	if err != nil {
		return roundResult{}, err
	}

	rankedExcluded, err := rankedEntries(excluded, p.keys)
	if err != nil {
		return roundResult{}, err
	}

	// 🔴 突き合わせるのは順位（eval_key の並び）だけで、スコアの値は比べない。
	// 系統1 は Search が内部で埋め込んだベクトル、系統2 は計測の外で1回だけ
	// 埋め込んだベクトルを使う。同じモデル・同じ入力なので実際には一致するが、
	// 浮動小数の最下位ビットまで一致することを契約にすると、この検査は
	// 「2系統が同じ順位を返すか」ではなく「埋め込みがビット単位で再現するか」を
	// 測る別のものに変わってしまう。守りたいのは前者である。
	if !slices.Equal(RankedKeysOf(rankedIncluded), RankedKeysOf(rankedExcluded)) {
		return roundResult{}, fmt.Errorf("%w: with embedding %v, without embedding %v",
			ErrRankingDiverged, RankedKeysOf(rankedIncluded), RankedKeysOf(rankedExcluded))
	}

	return roundResult{
		ranked:           rankedIncluded,
		withEmbedding:    withEmbedding,
		withoutEmbedding: withoutEmbedding,
	}, nil
}

// timedSearch は系統1（埋め込み往復を含む）を測る。
func timedSearch(
	ctx context.Context, s index.Searcher, q index.Query,
) ([]index.Result, time.Duration, error) {
	start := time.Now()
	results, err := s.Search(ctx, q)
	elapsed := time.Since(start)

	if err != nil {
		return nil, 0, fmt.Errorf("%w: search: %w", ErrMeasure, err)
	}

	return results, elapsed, nil
}

// timedSearchVector は系統2（埋め込み往復を除く）を測る。
func timedSearchVector(
	ctx context.Context, s vectorSearcher, q index.Query, vector []float32,
) ([]index.Result, time.Duration, error) {
	start := time.Now()
	results, err := s.SearchVector(ctx, q, vector)
	elapsed := time.Since(start)

	if err != nil {
		return nil, 0, fmt.Errorf("%w: vector search: %w", ErrMeasure, err)
	}

	return results, elapsed, nil
}

// rankedEntries は検索結果を eval_key とスコアの並びにする。
//
// 🔴 写像に無い id が返ってきたら止める。評価コーパス以外の行が混ざっている
// ということで、その行は順位を汚染する。評価専用 DB を毎回作り直すのは、
// まさにここが発火しない状態を作るためである (ADR 0013)。
//
// 🔑 スコアを2つに分けたまま運ぶ。合成後の値だけを残すと、外した原因が
// ベクトル側か語彙側かを後から切り分けられず、alpha (Q-3) と語彙手法 (Q-1) の
// 判断が当てずっぽうになる。
func rankedEntries(results []index.Result, keys map[int64]string) ([]RankedEntry, error) {
	out := make([]RankedEntry, 0, len(results))

	for _, result := range results {
		key, known := keys[result.Chunk.ID]
		if !known {
			return nil, fmt.Errorf("%w: chunk id %d is not part of the evaluation corpus",
				ErrMeasure, result.Chunk.ID)
		}

		out = append(out, RankedEntry{
			Key:          key,
			Score:        float64(result.Score),
			VectorScore:  float64(result.VectorScore),
			LexicalScore: float64(result.LexicalScore),
		})
	}

	return out, nil
}

// RankedKeysOf は順位の並びから eval_key だけを取り出す。
//
// 指標の計算（RecallAt・RankOf・ReciprocalRank）は eval_key の並びだけを見る。
// スコアは診断のための生データであって、指標の定義には入らない。
//
// 🔑 公開しているのは、レポートの生データから集計値を再計算する側がこれを
// 要るためである。RecallAt などが []string を受け取る以上、スコア付きの並びを
// 鍵の並びに落とす手順もレポートを読む側から見えていなければならない
// (ADR 0013 Decision 7)。
func RankedKeysOf(entries []RankedEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Key)
	}

	return out
}

// goldRunes は eval_key からコーパス上の文字数を引く表を作る。
//
// 🔑 文字数はバイト数ではなくルーン数で数える。評価セットの README が
// 「平均327字・最大1,136字」と書いているのはこの数え方であり、
// 閾値 520 もその数え方の上で決まっている。
func goldRunes(passages []Passage) map[string]int {
	runes := make(map[string]int, len(passages))
	for _, p := range passages {
		runes[p.Key] = utf8.RuneCountInString(p.Content)
	}

	return runes
}

// newQueryReport は1クエリぶんの生データと指標を組み立てる。
func newQueryReport(q Query, ranked []RankedEntry, latencies []RoundLatency) QueryReport {
	ks := KValues()
	keys := RankedKeysOf(ranked)

	recalls := make([]RecallAtK, 0, len(ks))
	for _, k := range ks {
		recalls = append(recalls, RecallAtK{K: k, Value: RecallAt(keys, q.Relevant, k)})
	}

	ranks := make([]RelevantRank, 0, len(q.Relevant))
	for _, key := range q.Relevant {
		ranks = append(ranks, RelevantRank{Key: key, Rank: RankOf(keys, key)})
	}

	return QueryReport{
		QueryID:        q.ID,
		Text:           q.Text,
		Tags:           q.Tags,
		Relevant:       q.Relevant,
		RankedKeys:     ranked,
		RelevantRanks:  ranks,
		Recall:         recalls,
		ReciprocalRank: ReciprocalRank(keys, q.Relevant),
		Latencies:      latencies,
	}
}

// summarize は per-query の生データから集計値を出す。
//
// 🔑 集計は必ず QueryReport から計算する。内部にだけある値を使わないので、
// レポートを読んだ第三者が同じ手順で再計算できる。これが
// 「後から検証できない数字は正本になれない」への回答である (ADR 0013)。
func summarize(reports []QueryReport, runes map[string]int) Summary {
	return Summary{
		QueryCount:       len(reports),
		Recall:           meanRecall(reports),
		MRR:              meanReciprocalRank(reports),
		Latency:          summarizeLatency(reports),
		TagRecall:        summarizeTags(reports),
		MicroRecall:      summarizeMicro(reports),
		GoldLengthRecall: summarizeGoldLength(reports, runes),
		LongChunkRecall:  summarizeLongChunks(reports, runes),
	}
}

// meanRecall は k ごとに recall の平均を出す。
func meanRecall(reports []QueryReport) []RecallAtK {
	ks := KValues()
	out := make([]RecallAtK, 0, len(ks))

	for _, k := range ks {
		values := make([]float64, 0, len(reports))
		for _, report := range reports {
			values = append(values, recallOf(report, k))
		}

		out = append(out, RecallAtK{K: k, Value: Mean(values)})
	}

	return out
}

// recallOf は1クエリの recall@k を取り出す。
func recallOf(report QueryReport, k int) float64 {
	for _, r := range report.Recall {
		if r.K == k {
			return r.Value
		}
	}

	return 0
}

// meanReciprocalRank は MRR を出す。
func meanReciprocalRank(reports []QueryReport) float64 {
	values := make([]float64, 0, len(reports))
	for _, report := range reports {
		values = append(values, report.ReciprocalRank)
	}

	return Mean(values)
}

// summarizeLatency は2系統の所要時間を集計する。
//
// 🔴 系統2 を系統1 からの引き算で出さない。p95 同士の差は差の p95 ではなく、
// 引き算に統計的な意味が無い (ADR 0009 / ADR 0013)。両方を実測して並べる。
func summarizeLatency(reports []QueryReport) LatencySummary {
	var included, excluded []time.Duration

	for _, report := range reports {
		for _, latency := range report.Latencies {
			included = append(included, msToDuration(latency.WithEmbeddingMS))
			excluded = append(excluded, msToDuration(latency.WithoutEmbeddingMS))
		}
	}

	return LatencySummary{
		WithEmbedding:    latencyStats(included),
		WithoutEmbedding: latencyStats(excluded),
	}
}

// latencyStats は1系統の統計を出す。
func latencyStats(samples []time.Duration) LatencyStats {
	return LatencyStats{
		Samples: len(samples),
		MinMS:   durationMS(Percentile(samples, 0)),
		P50MS:   durationMS(Percentile(samples, 50)),
		P95MS:   durationMS(Percentile(samples, 95)),
		MaxMS:   durationMS(Percentile(samples, 100)),
	}
}

// summarizeTags はタグ別に recall を集計する。
//
// 🔑 総合値は数十クエリでは動きにくい。どのカテゴリが壊れたかのほうが
// 診断情報として濃いので必ず併記する (ADR 0013)。
func summarizeTags(reports []QueryReport) []TagRecall {
	grouped := groupByTag(reports)

	out := make([]TagRecall, 0, len(grouped))
	// タグ順を安定させる。実行のたびに並びが変わると差分が読めない。
	for _, tag := range slices.Sorted(maps.Keys(grouped)) {
		group := grouped[tag]
		out = append(out, TagRecall{
			Tag:        tag,
			QueryCount: len(group),
			Recall:     meanRecall(group),
		})
	}

	return out
}

// groupByTag はクエリをタグ別に振り分ける。1つのクエリが複数のタグに属してよい。
func groupByTag(reports []QueryReport) map[string][]QueryReport {
	grouped := map[string][]QueryReport{}

	for _, report := range reports {
		for _, tag := range report.Tags {
			grouped[tag] = append(grouped[tag], report)
		}
	}

	return grouped
}

// msToDuration はレポートのミリ秒を所要時間に戻す。
//
// 集計を生データ（ミリ秒）から計算するために要る。内部の time.Duration を
// 別に持ち回らないのは、レポートを読んだ第三者と同じ入力で集計するためである。
func msToDuration(ms float64) time.Duration {
	return time.Duration(ms * float64(time.Millisecond))
}
