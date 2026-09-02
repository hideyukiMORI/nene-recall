// Package config は環境変数から設定を読む。
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

// 設定の失敗は3種類しかない。呼び出し側が errors.Is で分岐できるよう sentinel を公開し、
// どの環境変数が悪いのかは %w で包んだメッセージ側に持たせる。
//
// 動的 error（fmt.Errorf だけで作る error）を禁止しているのは、メッセージ文字列で
// 分岐するコードが生まれるのを防ぐため (GO-005)。
var (
	// ErrMissingRequired は必須の環境変数が空だったことを表す。
	ErrMissingRequired = errors.New("config: required value is missing")
	// ErrInvalidValue は値の形式・範囲が不正だったことを表す。
	ErrInvalidValue = errors.New("config: value is invalid")
	// ErrUnknownOption は列挙にない選択肢が指定されたことを表す。
	ErrUnknownOption = errors.New("config: unknown option")
)

// Store は永続化バックエンドの種類。
type Store string

const (
	// StorePostgres は PostgreSQL + pgvector。既定 (ADR 0007)。
	StorePostgres Store = "postgres"
	// StoreSQLite は SQLite + Go 側の総当たり内積。比較実測用に**実装済み**
	// (ADR 0017)。既定を移すのは実測を見てからである (ADR 0007)。
	StoreSQLite Store = "sqlite"
)

// Tokenizer は語彙検索のテキスト分割器の種類。
//
// 🔴 既定は bigram のままにする。kagome と union は**比較対象**として入っており
// (ADR 0018・ADR 0021)、既定を移すのは実測を見て別の ADR を書いてからである。
//
// 🔴 分割器を変えると保存済みの lexeme_text と噛み合わない。ストアが
// tokenizer_id の不一致をエラーにするので静かには壊れないが、切り替えたら
// 取り込み直しが要る（ADR 0005 と同じ性質）。
type Tokenizer string

const (
	// TokenizerBigram は文字 bigram。既定 (ADR 0014)。
	TokenizerBigram Tokenizer = "bigram"
	// TokenizerKagome は形態素解析 (kagome + IPA 辞書)。比較実測用 (ADR 0018)。
	TokenizerKagome Tokenizer = "kagome"
	// TokenizerUnion は上の2つのトークンを連結する和集合。比較実測用 (ADR 0021)。
	//
	// 🔑 bigram と kagome の利得が別の機構から来ていた（表記ゆれ／言い換え）ので、
	// 両取りできるかを測るために入れた選択肢である。
	TokenizerUnion Tokenizer = "union"
)

// SearchMode は検索が候補集合をどう作るかの種類。
//
// 🔴 既定は exhaustive（現行の全探索）のままにする。candidates は
// ADR 0022 が実装した**計測モード**であり、既定を移すのは after の実測
// （recall@10 の低下幅・p95）を見て別の ADR を書いてからである。
//
// 🔑 索引（HNSW / GIN）は migration 0004 で常に張られる。張られていても
// exhaustive の SQL は使わない（ORDER BY が合成式なので索引の順序に乗らない）。
// ⇒ 索引の効果と候補生成の効果は分離できない (ADR 0022 Decision 3)。
type SearchMode string

const (
	// SearchModeExhaustive は全行に両方のスコアを付けてから並べる。既定。
	SearchModeExhaustive SearchMode = "exhaustive"
	// SearchModeCandidates は両側 top-K の和集合だけを対象にする (ADR 0022)。
	SearchModeCandidates SearchMode = "candidates"
)

// defaultCandidateK / defaultEfSearch は候補モードの既定値。
//
// 🔴 postgres.DefaultCandidateK / postgres.DefaultEfSearch と同じ値である。
// config はストアを import できない層なので (ARC-001) 定数を共有できず、
// 2箇所に同じ数字が並ぶ。片方だけ変えないこと——変えると「設定を書かなければ
// K=100、書けば K=別の値」という説明のつかない差になる。
const (
	defaultCandidateK = 100
	defaultEfSearch   = 40
)

// EmbedProvider は埋め込みプロバイダの種類。
//
// string のままにしないのは、選択肢が閉じていることを型で示し、switch の網羅を
// exhaustive linter に見張らせるため (GO-002)。増やすときは ADR を1本立てる。
type EmbedProvider string

const (
	// EmbedProviderOllama はローカル実行。既定 (ADR 0008)。
	EmbedProviderOllama EmbedProvider = "ollama"
	// EmbedProviderVoyage は外部 API。任意経路であり、選んだときだけ課金が発生する。
	EmbedProviderVoyage EmbedProvider = "voyage"
)

// Config はサーバの設定。
type Config struct {
	// Addr は待ち受けアドレス。RECALL_ADDR、既定 ":8080"。
	Addr string

	// Store は永続化バックエンド。RECALL_STORE、既定 "postgres"。
	//
	// sqlite があるのは、同一データで Go 側の全探索と pgvector を比較実測する
	// ため。比較そのものが成果物になる (ADR 0007)。実装は ADR 0017。
	Store Store
	// DatabaseURL は Store=postgres のときの接続文字列。RECALL_DATABASE_URL。
	DatabaseURL string
	// DBPath は Store=sqlite のときのファイルパス。RECALL_DB_PATH、既定 "recall.db"。
	DBPath string

	// Tokenizer は語彙検索の分割器。RECALL_TOKENIZER、既定 "bigram"。
	//
	// kagome があるのは、bigram と形態素のどちらが良いか（要件定義 Q-2）を
	// 同一データで比較実測するため。比較そのものが成果物になる (ADR 0018)。
	// union は両者のトークンを連結したもので、ADR 0021 が次に測るものとして
	// 指名した構成である。
	Tokenizer Tokenizer

	// EmbedProvider は埋め込みプロバイダ。RECALL_EMBEDDER、既定 "ollama"。
	//
	// 既定がローカル実行なのは、ローカル利用が要件だから (ADR 0008)。
	// voyage は任意経路であり、選んだときだけ API キーを要求する。
	EmbedProvider EmbedProvider
	// EmbedModel は埋め込みモデル名。RECALL_EMBED_MODEL、既定 "bge-m3"。
	EmbedModel string
	// EmbedDimensions は埋め込みの次元数。RECALL_EMBED_DIMENSIONS、既定 1024。
	//
	// bge-m3 も voyage-4 も既定 1024 なので、両者の切替でスキーマが変わらない。
	EmbedDimensions int
	// OllamaBaseURL は Embedder=ollama のときの接続先。RECALL_OLLAMA_URL。
	//
	// 既定は localhost だが、想定構成では Ollama を Windows ネイティブで走らせるので
	// 実際にはホスト側のアドレスを明示指定することになる。WSL の CUDA ユーザ空間
	// ライブラリが未配置でも RTX 3090 をそのまま使えるのがその理由 (ADR 0008)。
	// WSL からホストを引くアドレスは再起動で変わるため、既定に頼らず .env に書くこと。
	OllamaBaseURL string
	// VoyageAPIKey は VOYAGE_API_KEY から読む。Embedder=voyage のときだけ要る。
	//
	// ログにもエラー応答にも出さないこと。String() を実装しないのは意図的で、
	// 構造体ごと %v で出力しても値が漏れる。ロギング時は個別フィールドを選ぶ。
	VoyageAPIKey string

	// APIToken は /v1/* を守る共有 Bearer トークン。RECALL_API_TOKEN、任意。
	//
	// 空なら認証なし（個人のローカル利用が既定）。設定されていれば
	// Authorization: Bearer <token> が一致しない要求は 401 になる
	// (docs/adr/0020-phase2-corpus-integration-contract.md Decision 3)。
	//
	// 🔴 VoyageAPIKey と同じ扱いの秘密である。ログにもエラー応答にも出さないこと。
	// String() を実装しないのは意図的で、構造体ごと %v で出力すると漏れる。
	// 起動ログに出してよいのは「有効か無効か」だけである。
	//
	// 🔴 「トークンが設定されていなければ既定のトークンを使う」を書かないこと。
	// 既定値のある共有秘密は、設定を忘れた全員が同じ鍵を使う状態になる。
	APIToken string

	// DefaultAlpha は合成の既定の重み。RECALL_DEFAULT_ALPHA、既定 0.8。
	//
	// 0.8 は 2026-09-02 の評価セット・bge-m3:1024・bigram・語彙スコアのクエリ内
	// 正規化という条件で、プラトー (0.7〜0.9) の中心として選んだ値である
	// (ADR 0015)。argmax ではなくプラトーの中心を採るのは、58クエリの指標が
	// 1クエリ = 0.017 揺れるため、最大点はゆらぎに引きずられるからである。
	//
	// 🔴 「最適値」ではない。正規化方式・分割器 (Tokenizer.ID)・埋め込みモデル
	// (Embedder.ID)・候補集合の作り方のどれかを変えたら測り直すこと
	// (ADR 0015 Decision 3)。
	DefaultAlpha float32

	// SearchMode は候補集合の作り方。RECALL_SEARCH_MODE、既定 "exhaustive"。
	//
	// 🔴 既定を candidates へ移すのは after の実測を見てからである (ADR 0022)。
	SearchMode SearchMode
	// CandidateK は候補モードの両側 top-K。RECALL_CANDIDATE_K、既定 100。
	//
	// 🔴 100 に根拠は無い。ADR 0022 が「まず動かして測る」ために置いた値であり、
	// 掃引して選んだ値ではない。「調整済み」であるかのように書かないこと。
	CandidateK int
	// HNSWEfSearch は候補モードの hnsw.ef_search。RECALL_HNSW_EF_SEARCH、既定 40。
	//
	// 🔴 CandidateK ≤ HNSWEfSearch でなければ HNSW は K 件を返せない。
	// 破ると「recall が少し低い」だけの症状になるので、SearchMode が candidates の
	// ときに Load が起動時に拒否する (ADR 0022 Decision 4)。
	//
	// ⚠️ 既定は K=100 / ef=40 で、この2つは**互いに矛盾している**（ADR 0022 が
	// 選んだ値で、40 は pgvector 自身の既定）。全探索ではどちらも使われないので
	// 害は無いが、candidates へ切り替えるときは ef もいっしょに上げること。
	HNSWEfSearch int
}

// Load は環境変数から設定を組み立てる。
//
// org_id の既定値は**意図的に存在しない**。サーバ側にフォールバックを置くと
// テナント分離が静かに壊れるため (docs/adr/0003-org-id-is-mandatory.md)。
func Load() (Config, error) {
	c := Config{
		Addr:            env("RECALL_ADDR", ":8080"),
		Store:           Store(env("RECALL_STORE", string(StorePostgres))),
		DatabaseURL:     os.Getenv("RECALL_DATABASE_URL"),
		DBPath:          env("RECALL_DB_PATH", "recall.db"),
		Tokenizer:       Tokenizer(env("RECALL_TOKENIZER", string(TokenizerBigram))),
		EmbedProvider:   EmbedProvider(env("RECALL_EMBEDDER", string(EmbedProviderOllama))),
		EmbedModel:      env("RECALL_EMBED_MODEL", "bge-m3"),
		EmbedDimensions: 1024,
		OllamaBaseURL:   env("RECALL_OLLAMA_URL", "http://localhost:11434"),
		VoyageAPIKey:    os.Getenv("VOYAGE_API_KEY"),
		APIToken:        os.Getenv("RECALL_API_TOKEN"),
		DefaultAlpha:    0.8,
		SearchMode:      SearchMode(env("RECALL_SEARCH_MODE", string(SearchModeExhaustive))),
		CandidateK:      defaultCandidateK,
		HNSWEfSearch:    defaultEfSearch,
	}

	c, err := applyNumericOverrides(c)
	if err != nil {
		return Config{}, err
	}

	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) validate() error {
	if err := c.validateStore(); err != nil {
		return err
	}

	if err := c.validateTokenizer(); err != nil {
		return err
	}

	if err := c.validateSearch(); err != nil {
		return err
	}

	return c.validateEmbedder()
}

// applyNumericOverrides は数値の環境変数を読んで既定値を上書きする。
//
// 🔑 Load から切り出してあるのは、環境変数が増えるたびに Load の分岐が
// 積み上がるからである (GO-011)。読む順序に意味は無く、どれか1つでも
// 壊れていれば設定を読んだ直後に落ちる、という性質だけが要る。
func applyNumericOverrides(c Config) (Config, error) {
	dimensions, err := positiveEnvInt("RECALL_EMBED_DIMENSIONS", c.EmbedDimensions)
	if err != nil {
		return Config{}, err
	}

	candidateK, err := positiveEnvInt("RECALL_CANDIDATE_K", c.CandidateK)
	if err != nil {
		return Config{}, err
	}

	efSearch, err := positiveEnvInt("RECALL_HNSW_EF_SEARCH", c.HNSWEfSearch)
	if err != nil {
		return Config{}, err
	}

	alpha, err := alphaFromEnv(c.DefaultAlpha)
	if err != nil {
		return Config{}, err
	}

	c.EmbedDimensions = dimensions
	c.CandidateK = candidateK
	c.HNSWEfSearch = efSearch
	c.DefaultAlpha = alpha

	return c, nil
}

// alphaFromEnv は合成の重みを読む。未設定なら fallback を返す。
//
// 🔴 [0,1] の外を拒む。範囲外の alpha は「重み」ではなく、加重和の意味が
// 壊れた状態で静かに順位を歪める。
func alphaFromEnv(fallback float32) (float32, error) {
	v := os.Getenv("RECALL_DEFAULT_ALPHA")
	if v == "" {
		return fallback, nil
	}

	f, err := strconv.ParseFloat(v, 32)
	if err != nil || f < 0 || f > 1 {
		return 0, fmt.Errorf("%w: RECALL_DEFAULT_ALPHA must be within [0,1], got %q",
			ErrInvalidValue, v)
	}

	return float32(f), nil
}

// positiveEnvInt は正の整数の環境変数を読む。未設定なら fallback を返す。
//
// 🔴 「0 なら既定を使う」にしない。0 は候補モードでは意味を持たない値であり、
// 「未設定」と「0 と書いた」を同じ扱いにすると、書き間違いが黙って既定で走る。
// 未設定は空文字でしか表せないので、そこだけを既定への入口にする。
func positiveEnvInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}

	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: %s must be a positive integer, got %q", ErrInvalidValue, key, v)
	}

	return n, nil
}

// validateSearch は候補の作り方と、それが要求する値の整合を検証する。
//
// 🔴 未知の値を既定へ黙って倒さない。綴り誤りが exhaustive として起動すると、
// 「candidates で測ったつもりの数字」が全探索のものになる。索引の after を
// 取り違えるので、設定の誤りは設定を読んだ直後に落とす (ADR 0022 Decision 3)。
//
// 🔴 K ≤ ef_search を確かめるのは candidates のときだけである。ADR 0022 が
// 選んだ既定は K=100（「まず動かして測る」ための値）と ef_search=40（pgvector 自身の
// 既定）で、**この2つは互いに矛盾している**。全探索ではどちらのつまみも使われない
// ので矛盾は害を持たないが、無条件に検査すると**既定の .env がそのままでは
// 起動しない**ことになる。⇒ 検査は、その値が実際に効く経路に限る。
//
// ⚠️ 帰結として、RECALL_SEARCH_MODE=candidates へ切り替えるときは
// RECALL_HNSW_EF_SEARCH も K 以上へ上げる必要がある。切り替えた瞬間に
// 起動時エラーで分かる（.env.example にも書いてある）。
//
// 🔑 K < 1 のほうはモードに関わらず拒否する。「候補を 0 件取る」は
// どの経路でも意味を持たない値であり、値そのものが誤りだからである。
func (c Config) validateSearch() error {
	switch c.SearchMode {
	case SearchModeExhaustive, SearchModeCandidates:
	default:
		return fmt.Errorf("%w: RECALL_SEARCH_MODE must be %q or %q, got %q",
			ErrUnknownOption, SearchModeExhaustive, SearchModeCandidates, c.SearchMode)
	}

	if c.CandidateK < 1 {
		return fmt.Errorf("%w: RECALL_CANDIDATE_K must be at least 1, got %d",
			ErrInvalidValue, c.CandidateK)
	}

	if c.SearchMode == SearchModeCandidates && c.CandidateK > c.HNSWEfSearch {
		return fmt.Errorf(
			"%w: RECALL_CANDIDATE_K (%d) must not exceed RECALL_HNSW_EF_SEARCH (%d) "+
				"when RECALL_SEARCH_MODE=%s: HNSW cannot return more rows than ef_search",
			ErrInvalidValue, c.CandidateK, c.HNSWEfSearch, SearchModeCandidates)
	}

	return nil
}

// validateTokenizer は分割器の選択を検証する。
//
// 🔴 未知の値を既定へ黙って倒さない。綴り誤りが bigram として起動すると、
// 「kagome で測ったつもりの数字」が bigram のものになる。設定の誤りは
// 設定を読んだ直後に落とす。
func (c Config) validateTokenizer() error {
	switch c.Tokenizer {
	case TokenizerBigram, TokenizerKagome, TokenizerUnion:
		return nil
	default:
		return fmt.Errorf("%w: RECALL_TOKENIZER must be %q, %q or %q, got %q",
			ErrUnknownOption, TokenizerBigram, TokenizerKagome, TokenizerUnion, c.Tokenizer)
	}
}

// validateStore は永続化バックエンドの選択と、その選択が要求する値の有無を検証する。
func (c Config) validateStore() error {
	switch c.Store {
	case StorePostgres:
		if c.DatabaseURL == "" {
			return fmt.Errorf("%w: RECALL_DATABASE_URL is required when RECALL_STORE=postgres", ErrMissingRequired)
		}
	case StoreSQLite:
		if c.DBPath == "" {
			return fmt.Errorf("%w: RECALL_DB_PATH is required when RECALL_STORE=sqlite", ErrMissingRequired)
		}
	default:
		return fmt.Errorf("%w: RECALL_STORE must be %q or %q, got %q",
			ErrUnknownOption, StorePostgres, StoreSQLite, c.Store)
	}

	return nil
}

// validateEmbedder は埋め込みプロバイダの選択と、その選択が要求する値の有無を検証する。
//
// API キーを voyage のときだけ要求するのは、既定構成（ローカル）に外部サービスの
// 前提を持ち込まないため (ADR 0008)。
func (c Config) validateEmbedder() error {
	switch c.EmbedProvider {
	case EmbedProviderOllama:
		if c.OllamaBaseURL == "" {
			return fmt.Errorf("%w: RECALL_OLLAMA_URL is required when RECALL_EMBEDDER=ollama", ErrMissingRequired)
		}
	case EmbedProviderVoyage:
		if c.VoyageAPIKey == "" {
			return fmt.Errorf("%w: VOYAGE_API_KEY is required when RECALL_EMBEDDER=voyage", ErrMissingRequired)
		}
	default:
		return fmt.Errorf("%w: RECALL_EMBEDDER must be %q or %q, got %q",
			ErrUnknownOption, EmbedProviderOllama, EmbedProviderVoyage, c.EmbedProvider)
	}

	return nil
}

// EmbedderID は保存済みベクトルとの互換判定に使う識別子を返す。
//
// モデルを変えると、次元が同じでもベクトル空間の互換性が失われる。
// 検知しないと「エラーにならないまま無意味なスコアが返る」(ADR 0005)。
func (c Config) EmbedderID() string {
	return fmt.Sprintf("%s:%d", c.EmbedModel, c.EmbedDimensions)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
