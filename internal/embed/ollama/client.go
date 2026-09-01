// Package ollama は Ollama を使ってテキストを埋め込みベクトルへ変換する。
//
// 🔴 このクライアントは「入力に接頭辞を必要としないモデル」専用である。
// bge-m3 がこれにあたる。multilingual-e5 系は "query: " / "passage: " の接頭辞が
// 必須で、付け忘れはエラーにならないまま検索品質だけが落ちる。
// RECALL_EMBED_MODEL は自由文字列なので、型では防げない。接頭辞の要るモデルを
// 使うなら、このクライアントを流用せず別の実装を書くこと (ADR 0008)。
//
// 実装がここ（契約パッケージ配下のサブパッケージ）にあるのは、Ollama クライアントが
// net/http と time を要求し、契約パッケージ internal/embed は ARC-002 により
// それらを import できないためである。判断の根拠は
// docs/adr/0012-embedding-implementations-live-in-subpackages.md。
package ollama

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/hideyukiMORI/nene-recall/internal/embed"
)

// DefaultBatchSize は1リクエストにまとめるテキスト数。
//
// 🔑 実測に基づく値である (docs/benchmarks/2026-09-01-baseline.md §3)。
// 230字のチャンクで 1本ずつ 11.8 件/秒 に対し、32本まとめると 87.8 件/秒。
// **8倍**の差があり、10万件の取り込みで 2時間21分 と 18分 の違いになる。
//
// 32 より大きくしても意味が薄い。128本は 93.4 件/秒 で +6% にとどまり、
// GPU 側が頭打ちになる。メモリを余分に使うだけなので既定は 32 に置く。
//
// 🔴 環境変数にはしない。値を変える実測上の理由が現時点で無いためである。
// 評価や再ベンチで別の値を試すときは Config.BatchSize に直接渡せばよい。
// 「設定できること」自体には価値が無く、増やした設定は必ず誰かが誤って触る。
const DefaultBatchSize = 32

// Config は Client の依存と設定。配線点 (cmd) が組み立てて注入する。
//
// ゼロ値は無効である。すべてのフィールドに値が要る (GO-003)。
type Config struct {
	// BaseURL は Ollama の接続先。例 "http://172.23.160.1:11434"。
	BaseURL string
	// Model は埋め込みモデル名。例 "bge-m3"。
	Model string
	// Dimensions は期待する次元数。応答がこれと違えば不可用として扱う。
	Dimensions int
	// BatchSize は1リクエストにまとめるテキスト数。DefaultBatchSize を参照。
	BatchSize int
	// HTTPClient はタイムアウトを持った HTTP クライアント。
	//
	// 🔴 自前で作らず注入させる。タイムアウトの決定は運用の判断であり、
	// ライブラリ側の既定値に埋めると呼び出し側から見えなくなる。
	HTTPClient *http.Client
}

// Client は Ollama の埋め込み API を叩く embed.Embedder の実装。
//
// ゼロ値は無効である。必ず New を通すこと。
type Client struct {
	baseURL    string
	model      string
	dimensions int
	batchSize  int
	httpClient *http.Client
	// id は "bge-m3:1024" 形式。構築時に固定する。
	//
	// 保存済みベクトルのメタデータ (chunks.embedder_id)・/healthz の応答・
	// 検索応答がすべてこの値で一致する必要があるため、呼び出しごとに作らない。
	id string
}

// Client が契約を満たしていることをコンパイル時に確かめる。
var _ embed.Embedder = (*Client)(nil)

// New は Config を検証して Client を作る。
//
// 🔴 すべてのフィールドの非ゼロを確かめる。ゼロ値のまま進むと、症状は
// 「起動はするが埋め込みが毎回失敗する」になり、原因が設定だと分かりにくい。
func New(cfg Config) (*Client, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	return &Client{
		// 末尾のスラッシュを落としてから連結する。"http://host:11434/" と
		// "/api/embed" をそのまま繋ぐと "//api/embed" になる。
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		model:      cfg.Model,
		dimensions: cfg.Dimensions,
		batchSize:  cfg.BatchSize,
		httpClient: cfg.HTTPClient,
		id:         fmt.Sprintf("%s:%d", cfg.Model, cfg.Dimensions),
	}, nil
}

// validateConfig は Config の各項目を確かめる。
func validateConfig(cfg Config) error {
	switch {
	case cfg.BaseURL == "":
		return fmt.Errorf("%w: BaseURL is required", errInvalidConfig)
	case cfg.Model == "":
		return fmt.Errorf("%w: Model is required", errInvalidConfig)
	case cfg.Dimensions < 1:
		return fmt.Errorf("%w: Dimensions must be positive, got %d", errInvalidConfig, cfg.Dimensions)
	case cfg.BatchSize < 1:
		return fmt.Errorf("%w: BatchSize must be positive, got %d", errInvalidConfig, cfg.BatchSize)
	case cfg.HTTPClient == nil:
		return fmt.Errorf("%w: HTTPClient is required", errInvalidConfig)
	}

	// 接続先の形が壊れているなら起動時に落とす。毎リクエスト失敗させない。
	if _, err := url.Parse(cfg.BaseURL); err != nil {
		return fmt.Errorf("%w: BaseURL is not a valid URL: %w", errInvalidConfig, err)
	}

	return nil
}

// Embed は texts を埋め込む。返り値は texts と同順・同数。
//
// 🔴 Kind は受け取るが bge-m3 では使わない。使わないのは bge-m3 の性質であって、
// 引数を廃止してよい理由ではない。接頭辞やパラメータを要求するプロバイダが
// 存在する以上、渡すかどうかは呼び出し側の契約である (ADR 0008)。
// 未知の Kind は既定に倒さず拒否する。既定に倒すと品質低下が症状として出ない。
func (c *Client) Embed(ctx context.Context, texts []string, kind embed.Kind) ([][]float32, error) {
	if kind != embed.KindDocument && kind != embed.KindQuery {
		return nil, fmt.Errorf("%w: %q", embed.ErrUnsupportedKind, kind)
	}

	// 空入力では要求を出さない。空を返すのが「同順・同数」の契約どおりの答えで、
	// 往復を1回節約できる。
	out := make([][]float32, 0, len(texts))

	// 🔴 サブバッチは直列に処理する。並列にすると「texts と同順・同数」を保つのに
	// 添字の管理が要るうえ、GPU 側が 32本前後で頭打ちなので、測っていない速さを
	// 買うために自明さを手放すことになる。
	for batch := range slices.Chunk(texts, c.batchSize) {
		vectors, err := c.embedBatch(ctx, batch)
		if err != nil {
			// 🔴 部分結果を返さない。途中まで成功した配列を返すと、呼び出し側は
			// どこまでが有効か知る手段を持たない。ストアは埋め込みをトランザクション
			// 開始前に行うので、ここで全体を失敗させても DB 副作用はゼロである。
			return nil, err
		}

		out = append(out, vectors...)
	}

	return out, nil
}

// Dimensions は生成されるベクトルの次元数を返す。
func (c *Client) Dimensions() int { return c.dimensions }

// ID は "bge-m3:1024" 形式の識別子を返す。
//
// config.Config.EmbedderID() と同じ書式でなければならない。ずれると、保存済み
// ベクトルの embedder_id と現在の設定が一致しているのに不一致と判定される。
func (c *Client) ID() string { return c.id }

// Ping は Ollama が応答し、モデルが取得済みであることを確かめる。
//
// 🔴 /api/embed ではなく /api/show を使う。埋め込みを1回投げると、モデルが
// ロードされていない場合にコールドスタート (実測 18.4 秒) を誘発してしまう。
// ヘルスチェックが重い処理を起こすと、監視のたびに GPU を占有することになる。
//
// embed.Embedder の契約には足していない。到達性の確認は「テキストをベクトルに
// 変換する」という契約の一部ではなく、実装ごとに意味が違うためである。
func (c *Client) Ping(ctx context.Context) error {
	payload, err := marshalJSON(showRequest{Model: c.model})
	if err != nil {
		return err
	}

	body, status, err := c.post(ctx, "/api/show", payload)
	if err != nil {
		return err
	}

	if status != http.StatusOK {
		return c.classifyStatus(status, body)
	}

	return nil
}
