package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"

	// database/sql のドライバ "sqlite" を登録する。純 Go の実装を選ぶ判断は
	// docs/adr/0017-sqlite-store-for-comparison.md にある（cgo を要求する
	// mattn/go-sqlite3 と sqlite-vec は CLAUDE.md 地雷5 で使えない）。
	// blank import なのは、必要なのが init による登録だけだからである。
	_ "modernc.org/sqlite"

	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/index"
	"github.com/hideyukiMORI/nene-recall/internal/lexical"
)

// driverName は modernc.org/sqlite が database/sql に登録するドライバ名。
const driverName = "sqlite"

// connectPragmas は接続ごとに効かせる設定。
//
// 🔴 PRAGMA を接続後に Exec しない。database/sql は接続をプールするので、
// 1本の接続に対して打った PRAGMA は他の接続に効かない。「最初のクエリだけ
// 設定が効いていた」という再現しにくい壊れ方をする。DSN に書けばドライバが
// 接続を作るたびに適用する。
//
// journal_mode(WAL) … 読みが書きを待たない。比較実測では取り込みと検索が
//
//	同じプロセスで続けて走るので、既定の rollback journal より素直に動く。
//	この設定だけはファイルに永続するが、接続ごとに指定しても無害である。
//
// busy_timeout(5000) … 書きの競合を例外ではなく待ちに変える。SQLite の書きは
//
//	ファイル単位で1本なので、これが無いと同時書き込みが即 SQLITE_BUSY になる。
//	🔴 SetMaxOpenConns(1) で直列化しない。読みまで直列になり、Go 側総当たりの
//	検索性能を測るという目的そのものを歪めるからである (ADR 0017)。
//
// foreign_keys(1) … 外部キーは現時点で無いが、既定が OFF なので「足した瞬間に
//
//	黙って効かない」状態を作らないために最初から入れておく。
//
// _txlock=immediate … BeginTx が BEGIN IMMEDIATE を打つ。既定の遅延
//
//	トランザクションは、読みで始まって書きに昇格するときに他の書き手がいると
//	SQLITE_BUSY になり、busy_timeout でも待てない（昇格の失敗は待てない）。
//	書きにしか使っていないので、最初から書きロックを取るのが正しい。
const connectPragmas = "_pragma=journal_mode(WAL)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(1)" +
	"&_txlock=immediate"

// Store は SQLite 上の索引。
//
// index.Searcher と index.Writer を実装する。
// ゼロ値は無効である。必ず New を通すこと（GO-003）。
type Store struct {
	db *sql.DB
	// embedder は検索語と本文をベクトルに変換する。
	embedder embed.Embedder
	// embedderID は構築時に固定する。
	//
	// 毎回 embedder.ID() を呼ばないのは、実装が呼び出しごとに違う値を返した場合に
	// 「書いた値」と「照合する値」がずれるのを防ぐため。ストアが1つの
	// 埋め込み空間に属するという不変条件は、構築時に決まって以後変わらない。
	embedderID string
	// tokenizer は本文と検索語を語彙一致のためのトークン列に分割する。
	//
	// 🔴 取り込みと検索で同じものを使う。別のものを使うと、同じ語を書いた
	// はずのチャンクとクエリが別のトークンになり、語彙スコアが常に 0 になる。
	// ストアが1つ持つ形にしてあるのは、その取り違えを型で防ぐためである。
	tokenizer lexical.Tokenizer
	// tokenizerID は構築時に固定する。理由は embedderID と同じ。
	tokenizerID string
}

// DSN はファイルパスから接続文字列を組み立てる。
//
// 🔑 公開しているのは、評価ランナー（cmd/eval）が同じファイルに素の
// database/sql で繋いで SQLite の版を読むためである。DSN の組み立てが2箇所に
// あると、片方だけ PRAGMA を足したときに「テストと本番で違う設定の SQLite」が
// できる。組み立てはここ1箇所に置く。
//
// パスを丸ごと百分率符号化するのは、SQLite の URI 形式が ? と # を区切りとして
// 読むためである。符号化しないと、それらを含むパスが黙って別のファイルを指す。
func DSN(path string) string {
	return "file:" + url.PathEscape(path) + "?" + connectPragmas
}

// Open はファイルを開き、実際に使える状態であることを確認する。
//
// sql.Open は接続を張らないので、それだけでは設定ミスが最初のクエリまで
// 表面化しない。起動時に落とすために PingContext と FTS5 の確認まで行う。
func Open(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open(driverName, DSN(path))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errConnect, err.Error())
	}

	if err := db.PingContext(ctx); err != nil {
		// 使えないプールを呼び出し側に持たせない。閉じる側の失敗も握り潰さない。
		return nil, errors.Join(fmt.Errorf("%w: %s", errConnect, err.Error()), db.Close())
	}

	if err := assertFTS5(ctx, db); err != nil {
		return nil, errors.Join(err, db.Close())
	}

	return db, nil
}

// assertFTS5 は FTS5 モジュールが使えることを確かめる。
//
// 🔴 「CREATE VIRTUAL TABLE が落ちるから不要」ではない。落ちるのはスキーマを
// 作る瞬間だけで、既にスキーマのあるファイルを FTS5 無しのビルドで開くと、
// 語彙検索だけが静かに失敗しうる。ADR 0017 は語彙採点を bm25() に置いており、
// それが無い環境は設計の前提を満たさない。接続の時点で止める。
func assertFTS5(ctx context.Context, db *sql.DB) error {
	var name string

	err := db.QueryRowContext(ctx,
		`SELECT name FROM pragma_module_list WHERE name = 'fts5'`).Scan(&name)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w", errFTS5Unavailable)
	case err != nil:
		return fmt.Errorf("%w: fts5 check: %s", errConnect, err.Error())
	default:
		return nil
	}
}

// New は接続プールと Embedder・Tokenizer から Store を組み立てる。
//
// 🔴 ここで次元を突き合わせるのが要点である。embedding 列は 4096 バイトの
// BLOB で固定されており、次元の異なる Embedder を渡した構成は「起動はするが
// 取り込みが全部落ちる」状態になる。設定の誤りは設定を読んだ直後に落とす。
//
// 🔴 Tokenizer も識別子の空を構築時に弾く。tokenizer_id 列は空文字を拒否する
// CHECK を持つので、空のまま進むと取り込みの瞬間に制約違反という分かりにくい
// 形で落ちる。
//
// 🔑 postgres.New と違って Fusion を受け取らない。比較は既定同士——加重和——で
// 行うので、RRF はこちらに実装していない (ADR 0017 Decision 4)。方式を1つしか
// 持たない型を引数に足すと、「選べる」という誤った印象を配線点に与える。
func New(db *sql.DB, e embed.Embedder, t lexical.Tokenizer) (*Store, error) {
	if got := e.Dimensions(); got != vectorDimensions {
		return nil, fmt.Errorf("%w: embedder %q produces %d, the column holds %d",
			errEmbedderDimensions, e.ID(), got, vectorDimensions)
	}

	id := e.ID()
	if id == "" {
		return nil, fmt.Errorf("%w", errEmbedderID)
	}

	if t == nil {
		return nil, fmt.Errorf("%w: tokenizer is required", errTokenizerID)
	}

	tokenizerID := t.ID()
	if tokenizerID == "" {
		return nil, fmt.Errorf("%w", errTokenizerID)
	}

	return &Store{
		db: db, embedder: e, embedderID: id,
		tokenizer: t, tokenizerID: tokenizerID,
	}, nil
}

// RankingSettings はストアが順位付けに使った条件。
//
// 🔑 レポートに残すためだけの値である。どの方式・どの採点関数で測ったかが
// 記録されていないレポートは、後から条件を特定できないので正本になれない
// (docs/adr/0013-evaluation-harness-design.md)。
//
// 🔴 postgres 側の同名の型とフィールドが違う。あちらは ts_rank のフラグと
// RRF の k を持ち、こちらは持たない。**同じ型を共有しない**のは、共有すると
// 「SQLite でも ts_rank のフラグが効いている」ように読める記録ができるためで
// ある。写し替えは配線点 (cmd/eval) の仕事である。
type RankingSettings struct {
	// Fusion は融合方式の名前。加重和しか無い (ADR 0017 Decision 4)。
	Fusion string
	// LexicalScorer は語彙スコアの採点関数の名前。
	//
	// 🔴 これを記録しないと、2つのストアのレポートを並べたときに recall の差が
	// 「ストアの差」なのか「bm25 と ts_rank の差」なのか読めなくなる。
	LexicalScorer string
	// Store はバックエンドの名前。
	Store string
}

// RankingSettings はこのストアが順位付けに使っている条件を返す。
//
// 🔴 定数を直接公開せず、ストアに聞く形にしてある。定数を変えたときに
// レポートが自動で追随し、「コードは変えたが記録は古いまま」が起きない。
func (s *Store) RankingSettings() RankingSettings {
	return RankingSettings{
		Fusion:        fusionWeightedSumName,
		LexicalScorer: lexicalScorerName,
		Store:         storeName,
	}
}

// Ping は DB へ到達できることを確かめる。/readyz から呼ぶ。
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("%w: %s", errConnect, err.Error())
	}

	return nil
}

// Close は接続プールを閉じる。
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("%w: %s", errConnect, err.Error())
	}

	return nil
}

// assertSameEmbedderAndTokenizer は、保存済みの行がすべて現在の Embedder と
// Tokenizer のものかを確かめる。
//
// 🔴 検索側で WHERE embedder_id = <current> と黙って絞り込む実装にしないこと。
// 不一致の行が「検索に出てこないだけ」になり、ADR 0005 が警告する静かな破損
// （エラーにならないまま無意味な結果が返る）の変種になる。必ずエラーにする。
// 分割器についても同じで、規則の違うトークン列が混ざったときの症状は
// 「語彙スコアが少し低い」だけであり、単一の分割器で開発している限り
// 一切表面化しない。
//
// 🔴 2つの検査を1本の SQL にまとめてある。分けると検索1回あたりの問い合わせが
// 1本増え、2系統の p95 の差に検査のぶんが混ざる。postgres 側と同じ形にして
// あるのは、比較実測が「同じ経路の同じ回数」でなければ意味を持たないためである
// (CLAUDE.md 地雷10)。
//
// 検査は org を跨いで全行を対象にする。「ストア全体が単一の埋め込み空間・
// 単一の分割規則である」という不変条件はテナントより上位の性質であり、
// org ごとに別のモデルを混ぜてよいという意味ではないため。
func (s *Store) assertSameEmbedderAndTokenizer(ctx context.Context, q queryer) error {
	const stmt = `SELECT embedder_id, tokenizer_id FROM chunks
WHERE embedder_id <> ? OR tokenizer_id <> ?
LIMIT 1`

	var storedEmbedder, storedTokenizer string

	err := q.QueryRowContext(ctx, stmt, s.embedderID, s.tokenizerID).
		Scan(&storedEmbedder, &storedTokenizer)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil // 不一致の行が無い。空のストアもここに来る
	case err != nil:
		return fmt.Errorf("%w: embedder and tokenizer check: %s", errConnect, err.Error())
	case storedEmbedder != s.embedderID:
		// 両方が食い違う行もありうる。埋め込みを先に報告するのは、
		// ベクトルが比較できない状態のほうが被害が大きく、取り込み直しの
		// 判断もそちらが決めるためである。
		return fmt.Errorf("sqlite: stored=%s current=%s: %w",
			storedEmbedder, s.embedderID, index.ErrEmbedderMismatch)
	default:
		return fmt.Errorf("sqlite: stored=%s current=%s: %w",
			storedTokenizer, s.tokenizerID, index.ErrTokenizerMismatch)
	}
}

// queryer は *sql.DB と *sql.Tx が共通して持つ問い合わせ口。
//
// embedder_id の検査をトランザクションの内側（Put）と外側（Search）の両方から
// 呼ぶために要る。database/sql はこの共通部分を型として提供していないので、
// 必要な1メソッドだけを自前で宣言する。
//
// 引数が ...any なのは database/sql の署名をそのまま写しているからで、
// 型を any で代用しているわけではない（GO-006 が禁じているのは後者）。
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
