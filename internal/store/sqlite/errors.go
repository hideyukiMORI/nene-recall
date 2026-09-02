package sqlite

import (
	"errors"
	"fmt"
)

// 🔑 sentinel の並びは internal/store/postgres/errors.go と同じ順・同じ意味に
// してある。2つのストアは比較のために並べて読まれるので、同じ失敗が同じ名前で
// 呼ばれていないと差分が読めなくなる。

// ---------------------------------------------------------------------------
// ベクトルの契約違反
// ---------------------------------------------------------------------------

// errVectorInvalid は、ベクトルが embed.Embedder の契約を満たしていないことを表す。
//
// 呼び出し側が「ベクトルが理由で書けなかった」を一度に判定できるよう、
// 個別の理由（次元・正規化）はこれを包む。errors.Is はこの入れ子を辿る。
var errVectorInvalid = errors.New("sqlite: vector does not satisfy the Embedder contract")

// errVectorDimensions は、ベクトルの次元数が想定と一致しないことを表す。
//
// 🔴 これは設定ミスであってデータの問題ではない。次元が違うベクトルは
// 4096 バイトの BLOB にならないので、取り込みを止めて設定を直させること。
var errVectorDimensions = fmt.Errorf("%w: unexpected dimensions", errVectorInvalid)

// errVectorNotNormalized は、ベクトルが L2 正規化されていないことを表す。
//
// 🔴 順位付けの内積は入力が正規化済みであることに依存している。これを見逃すと、
// エラーにならないまま順位だけが静かに狂う
// (docs/adr/0005-embedding-provider-is-pluggable.md)。
var errVectorNotNormalized = fmt.Errorf("%w: vector is not L2-normalized", errVectorInvalid)

// errVectorBlobLength は、保存済みの embedding 列が 4096 バイトでないことを表す。
//
// 🔴 DDL の CHECK (length(embedding) = 4096) は Go を通した書き込みを守るが、
// 外から直接 INSERT された行までは守らない。読み取り時に検査しないと、
// 短いベクトルとの内積が「意味のないスコア」として順位に混ざる。
var errVectorBlobLength = errors.New("sqlite: stored embedding has an unexpected byte length")

// ---------------------------------------------------------------------------
// 接続と構築
// ---------------------------------------------------------------------------

// errConnect は接続そのものが確立できないことを表す。
var errConnect = errors.New("sqlite: cannot reach the database")

// errFTS5Unavailable は、繋いだ SQLite が FTS5 を持たないことを表す。
//
// 🔴 これを検査せずに進めると、CREATE VIRTUAL TABLE が失敗するか、
// 最悪の場合「語彙スコアが常に 0 のまま動き続ける」ことになる。ADR 0017 は
// 語彙採点を FTS5 の bm25() に置いており、それが無い環境は設計の前提を
// 満たさない。黙って縮退させず、接続の時点で止める。
var errFTS5Unavailable = errors.New("sqlite: the driver does not provide the FTS5 module")

// errEmbedderDimensions は Embedder の次元数がスキーマと噛み合わないことを表す。
//
// 🔴 これを New で弾かないと「起動はするが取り込みが全部落ちる」構成が作れてしまう。
// config.EmbedDimensions は実行時に変えられるが、BLOB の長さは DDL の CHECK で
// 固定されている。
var errEmbedderDimensions = errors.New("sqlite: embedder dimensions do not match the schema")

// errEmbedderID は Embedder が空の識別子を返したことを表す。
//
// embedder_id 列は空文字を拒否する CHECK を持つので、空のまま進むと
// 取り込みの瞬間に制約違反という分かりにくい形で落ちる。構築時に潰す。
var errEmbedderID = errors.New("sqlite: embedder returned an empty identifier")

// errTokenizerID は Tokenizer が無い、または空の識別子を返したことを表す。
//
// tokenizer_id 列も空文字を拒否する CHECK を持つ。理由は errEmbedderID と同じ。
var errTokenizerID = errors.New("sqlite: tokenizer is missing or returned an empty identifier")

// ---------------------------------------------------------------------------
// トークンの契約違反
// ---------------------------------------------------------------------------

// errTokenInvalid は、トークンが lexical.Tokenizer の契約を満たしていないことを表す。
//
// 個別の理由（空白・メタ文字）はこれを包む。errors.Is はこの入れ子を辿る。
var errTokenInvalid = errors.New("sqlite: token does not satisfy the Tokenizer contract")

// errTokenHasWhitespace は、トークンが空白文字を含むことを表す。
//
// 🔴 トークン列は空白区切りの1本の文字列として lexeme_text に保存される。
// 空白を含むトークンは FTS5 の中で2つ以上に割れ、「Go 側は1トークンのつもりなのに
// DB では別物」という検出しにくい壊れ方をする。取り込みの時点で失敗にする。
var errTokenHasWhitespace = fmt.Errorf("%w: token contains whitespace", errTokenInvalid)

// errTokenHasMetaCharacter は、トークンが MATCH 式のメタ文字を含むことを表す。
//
// 🔴 検索式は「トークンを引用符で囲んで OR で繋いだもの」である。トークンが
// 引用符や逆斜線を含むと囲みが破れて構文が壊れる。黙って落とす実装にはしない——
// 落とすと、語彙スコアが静かに欠けたまま「動いている」状態になる。
var errTokenHasMetaCharacter = fmt.Errorf("%w: token contains a MATCH meta character", errTokenInvalid)

// ---------------------------------------------------------------------------
// マイグレーション
// ---------------------------------------------------------------------------

// errMigrate はスキーマの適用に失敗したことを表す。
var errMigrate = errors.New("sqlite: migration failed")

// ---------------------------------------------------------------------------
// 書き込み
// ---------------------------------------------------------------------------

// errWrite は書き込み経路の SQL が失敗したことを表す。
var errWrite = errors.New("sqlite: write failed")

// errEmptyBatch は Put に空のチャンク列が渡されたことを表す。
//
// 空を「成功・0件」で返すと (nil, nil) になり GO-004 に反する。
// 呼び出し側の組み立てミスを黙って飲み込まないよう、明示的に拒否する。
var errEmptyBatch = errors.New("sqlite: no chunks to write")

// errEmptyContent は Content が空のチャンクが混じっていたことを表す。
var errEmptyContent = errors.New("sqlite: chunk content is empty")

// errOrgRequired は org の識別子が未指定（ゼロ値）のまま届いたことを表す。
//
// 🔴 DB の CHECK (org_id >= 1) に守らせない。制約に落とすと失敗が「制約違反」に
// なり、呼び出し側は原因に辿り着けない。「org_id が無いとき」を表す ID は
// 存在せず、未指定は error である (ADR 0003)。
var errOrgRequired = errors.New("sqlite: org id is required")

// errOrgMismatch は Chunk.OrgID が引数の orgID と食い違うことを表す。
//
// 🔴 引数の orgID を唯一の正とし、食い違いを黙って上書きしない。上書きすると、
// 呼び出し側の取り違えが「別テナントへの書き込み」として成功する (ADR 0003)。
var errOrgMismatch = errors.New("sqlite: chunk org_id does not match the requested org")

// errChunkIDNotAccepted は明示的な chunk_id が渡されたことを表す。
//
// 🔴 Corpus 由来の chunk_id の受け入れ方式は Phase 2 の ADR で決める（施主決定）。
// postgres 側と同じく Phase 1 では受け付けない。
var errChunkIDNotAccepted = errors.New("sqlite: explicit chunk id is not accepted in phase 1")

// errExternalIDInvalid は external_id に 0 以下が渡されたことを表す。
//
// 🔴 「外部 id を持たない」は NULL（Go 側は nil）で表す。0 を通すと、置き換えの
// 鍵が実在しない 0 番になり、外部 id を持たないはずの行どうしが互いを上書きする。
var errExternalIDInvalid = errors.New("sqlite: external id must be a positive integer")

// errDuplicateExternalID は1回の Put に同じ external_id が2回現れたことを表す。
//
// 🔴 黙って後勝ちにしない。理由は postgres 側と同じで、upsert が成功したうえで
// 「n 件受理されたのに行は n-1 件」という差がどこにも現れない状態になる
// (docs/adr/0020-phase2-corpus-integration-contract.md Decision 1・ADR 0013)。
var errDuplicateExternalID = errors.New("sqlite: the same external id appears twice in one batch")

// ---------------------------------------------------------------------------
// 検索
// ---------------------------------------------------------------------------

// errSearch は検索経路の SQL が失敗したことを表す。
var errSearch = errors.New("sqlite: search failed")
