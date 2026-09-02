package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hideyukiMORI/nene-recall/internal/chunk"
	"github.com/hideyukiMORI/nene-recall/internal/embed"
	"github.com/hideyukiMORI/nene-recall/internal/index"
	"github.com/hideyukiMORI/nene-recall/internal/org"
)

// Store が書き込み側の契約を満たしていることをコンパイル時に確かめる。
var _ index.Writer = (*Store)(nil)

// pendingWrite は Put が1トランザクションで書き込む一括分。
//
// 引数を4つ以下に保つための入れ物（GO-011）。チャンクと符号化済みベクトルは
// 同じ添字で対応しており、両者がずれないよう1つの値として持ち回る。
type pendingWrite struct {
	orgID   org.ID
	chunks  []chunk.Chunk
	encoded []encodedChunk
}

// encodedChunk は1チャンクぶんの、SQLite へ渡す形に直した派生値。
//
// ベクトルとトークン列を1つの値にまとめてあるのは、両者がチャンクと同じ添字で
// 対応しており、別々のスライスで持ち回るとずれる余地が生まれるためである。
type encodedChunk struct {
	// vector は float32 × 1024 をリトルエンディアンで並べた BLOB。
	vector []byte
	// lexemeText は Tokenizer の出力を空白区切りで並べたもの。
	// FTS5 の外部コンテンツ表が trigger 経由でこれを読む。
	lexemeText string
}

// insertChunkSQL は1行を投入して採番された id を返す。
//
// 🔴 複数行 VALUES による一括投入にしない。取り込み時間の支配項は埋め込みの
// 生成（実測 約18分／10万件・docs/benchmarks/2026-09-01-baseline.md）であり、
// 挿入を速くしても全体に測定可能な利得が無い。QLT-008 は「速さは実測を伴わ
// なければ主張しない」と定めている。postgres 側と同じ形にしてあるのは、
// 比較実測で取り込み経路の差が結果に混ざらないようにするためでもある。
//
// 🔴 chunks_fts には書かない。3本の trigger が同期する
// (migrations/0001_create_chunks.sql)。アプリケーションが両方を書く形にすると、
// 片方だけ更新された行が作れてしまう。
//
// 🔑 ON CONFLICT を付けたのは Phase 2 の契約である
// (docs/adr/0020-phase2-corpus-integration-contract.md Decision 1)。同じ
// (org_id, external_id) の再投入は置き換えになる。postgres 側と同じ形にしてある。
//
// 🔴 external_id が NULL の行はこの分岐に一切入らない。UNIQUE 索引が NULL どうしを
// 重複とみなさないためで、単体運用の insert-only はそのまま保たれる。
//
// 🔴 更新は chunks_fts_after_update trigger が転置索引へ伝える
// (migrations/0001_create_chunks.sql)。trigger を消すと、置き換えた行の
// 語彙スコアだけが古い本文のまま残る——エラーにならない壊れ方である。
const insertChunkSQL = `INSERT INTO chunks (
	org_id, document_id, source_id, chunk_index, content,
	page_number, section_label, embedder_id, embedding,
	lexeme_text, tokenizer_id, external_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (org_id, external_id) DO UPDATE SET
	document_id   = excluded.document_id,
	source_id     = excluded.source_id,
	chunk_index   = excluded.chunk_index,
	content       = excluded.content,
	page_number   = excluded.page_number,
	section_label = excluded.section_label,
	embedder_id   = excluded.embedder_id,
	embedding     = excluded.embedding,
	lexeme_text   = excluded.lexeme_text,
	tokenizer_id  = excluded.tokenizer_id
RETURNING id`

// Put はチャンクを投入し、採番された id を入力と同じ順で返す。
//
// 🔴 1回の呼び出しが1トランザクションであり、全件成功か全件なしかのどちらかに
// なる。部分投入を許すと、呼び出し側は「どこまで入ったか」を知る手段を持たず、
// 再送すれば重複が生まれる。insert-only（UNIQUE 制約なし）である以上、
// 重複を後から見分けることもできない。
//
// 🔴 戻り値の「入力と同じ順」は評価ハーネスが依存している契約である。
// eval_key → 採番 id の写像がここから作られる (ADR 0013)。緩めると、評価
// ハーネスが静かに別の行を正解として数え始める。
func (s *Store) Put(ctx context.Context, orgID org.ID, chunks []chunk.Chunk) ([]int64, error) {
	if err := validateChunks(orgID, chunks); err != nil {
		return nil, err
	}

	// 🔴 埋め込みの生成はトランザクションを開ける前に済ませる。
	// 数十秒かかりうる外部 I/O をトランザクションの中に置くと、その間ずっと
	// ファイル全体の書きロックを占有することになる（SQLite の書きは1本）。
	encoded, err := s.encodeContents(ctx, chunks)
	if err != nil {
		return nil, err
	}

	return s.insertChunks(ctx, pendingWrite{orgID: orgID, chunks: chunks, encoded: encoded})
}

// validateOrgID は分離条件が指定されていることを境界で確かめる。
//
// 🔴 DB の CHECK (org_id >= 1) に任せない。任せると失敗が「制約違反」という
// 読み取れない形になり、Search 側に至っては「一件も一致しない」＝空の結果に
// なって、呼び出し側からは「該当なし」と区別がつかなくなる。
// ゼロ値がストアまで届くのは上流のバグであり、静かに飲み込むと単一テナントで
// 開発している限り誰も気づかない。それが ADR 0003 の言う「症状を出さない」状態である。
func validateOrgID(orgID org.ID) error {
	if orgID < 1 {
		return fmt.Errorf("%w: got %d", errOrgRequired, orgID.Int64())
	}

	return nil
}

// validateChunks は DB にも埋め込みにも触れずに分かる誤りを先に落とす。
func validateChunks(orgID org.ID, chunks []chunk.Chunk) error {
	// 分離条件から先に確かめる。何を書くかより、どのテナントに書くかが先である。
	if err := validateOrgID(orgID); err != nil {
		return err
	}

	if len(chunks) == 0 {
		return fmt.Errorf("%w", errEmptyBatch)
	}

	for i, c := range chunks {
		if err := validateChunk(i, c, orgID); err != nil {
			return err
		}
	}

	return validateExternalIDsAreDistinct(chunks)
}

// validateChunk は1件ぶんの契約違反を見る。
func validateChunk(i int, c chunk.Chunk, orgID org.ID) error {
	if c.ID != 0 {
		return fmt.Errorf("%w: chunks[%d] carries id %d", errChunkIDNotAccepted, i, c.ID)
	}

	if c.Content == "" {
		return fmt.Errorf("%w: chunks[%d]", errEmptyContent, i)
	}

	// 🔴 0 と負値を弾く。「外部 id を持たない」は NULL（Go 側は nil）で表す。
	// 0 を通すと、置き換えの鍵が実在しない 0 番になる。
	if c.ExternalID != nil && *c.ExternalID < 1 {
		return fmt.Errorf("%w: chunks[%d] has %d", errExternalIDInvalid, i, *c.ExternalID)
	}

	// ゼロ値は「Chunk 側が org を持っていない」を意味する。値が入っているのに
	// 引数と違うときだけ拒否する。黙って引数で上書きすると、呼び出し側の
	// 取り違えが別テナントへの書き込みとして成功してしまう。
	if c.OrgID != 0 && c.OrgID != orgID {
		return fmt.Errorf("%w: chunks[%d] says %s, argument says %s",
			errOrgMismatch, i, c.OrgID, orgID)
	}

	return nil
}

// validateExternalIDsAreDistinct は1回の Put に同じ external_id が2回無いことを見る。
//
// 🔴 黙って後勝ちにしない。理由は postgres 側の同名関数と同じである——upsert なので
// DB は成功し、返す id の列は「入力と同じ順」を保ったまま同じ id を2回並べる。
// n 件送って n 件受理されたのに行は n-1 件しか無い、という差がどこにも現れない。
func validateExternalIDsAreDistinct(chunks []chunk.Chunk) error {
	seen := make(map[int64]int, len(chunks))

	for i, c := range chunks {
		if c.ExternalID == nil {
			continue
		}

		if first, dup := seen[*c.ExternalID]; dup {
			return fmt.Errorf("%w: chunks[%d] and chunks[%d] both use %d",
				errDuplicateExternalID, first, i, *c.ExternalID)
		}

		seen[*c.ExternalID] = i
	}

	return nil
}

// encodeContents は本文をまとめて埋め込み、SQLite へ渡す形に変換する。
//
// 🔴 分割（Tokenize）は埋め込みと同じ本文に対して、同じ場所で行う。
// 別の経路から別の文字列を分割する形にすると、ベクトルとトークン列が
// 違う本文を指す行を作れてしまい、その行は検索でどちらのスコアが正しいのか
// 分からなくなる。
func (s *Store) encodeContents(ctx context.Context, chunks []chunk.Chunk) ([]encodedChunk, error) {
	texts := make([]string, 0, len(chunks))
	for _, c := range chunks {
		texts = append(texts, c.Content)
	}

	// 🔴 全件を1回の呼び出しで渡す。32本ずつといった分割は Ollama クライアントの
	// 責務であってここではない。プロバイダに依存しないはずのストアに焼き付けない。
	//
	// Kind は bge-m3 が無視する実装であっても必ず渡す。使うかどうかは実装の都合で、
	// 渡すかどうかは呼び出し側の契約である（ADR 0008）。
	vectors, err := s.embedder.Embed(ctx, texts, embed.KindDocument)
	if err != nil {
		// 🔴 %w を2つ使い、埋め込み側の sentinel を連鎖に残す。ここを %s に
		// すると embed.ErrProviderUnavailable が切れ、httpapi が 503 に写せず
		// 500 に落ちる。SQL の失敗は逆にドライバ内部を連鎖へ載せない意図で
		// %s のままにしてある。両者の違いは意図的である。
		return nil, fmt.Errorf("%w: embed: %w", errWrite, err)
	}

	if len(vectors) != len(chunks) {
		return nil, fmt.Errorf("%w: embedder returned %d vectors for %d chunks",
			errWrite, len(vectors), len(chunks))
	}

	return s.encodeVectors(vectors, chunks)
}

// encodeVectors は生成されたベクトルとトークン列を1件ずつ検査して並べる。
func (s *Store) encodeVectors(vectors [][]float32, chunks []chunk.Chunk) ([]encodedChunk, error) {
	encoded := make([]encodedChunk, 0, len(vectors))

	for i, v := range vectors {
		// 契約違反をここで止める。通してしまうと内積の順位が静かに狂う。
		if err := validateVector(v); err != nil {
			return nil, fmt.Errorf("%w: chunks[%d]", err, i)
		}

		// Kind に相当する使い分けは分割には無い。取り込みと検索で同じ関数を
		// 通すことがそのまま契約である（lexical.Tokenizer の doc を参照）。
		lexemeText, err := encodeLexemeText(s.tokenizer.Tokenize(chunks[i].Content))
		if err != nil {
			return nil, fmt.Errorf("%w: chunks[%d]", err, i)
		}

		encoded = append(encoded, encodedChunk{vector: encodeVector(v), lexemeText: lexemeText})
	}

	return encoded, nil
}

// insertChunks は1トランザクションで全件を投入する。
func (s *Store) insertChunks(ctx context.Context, w pendingWrite) ([]int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: begin: %s", errWrite, err.Error())
	}

	ids, err := s.insertWithinTx(ctx, tx, w)
	if err != nil {
		// 巻き戻しの失敗も握り潰さない（errors.Join は nil を落とす）。
		return nil, errors.Join(err, tx.Rollback())
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("%w: commit: %s", errWrite, err.Error())
	}

	return ids, nil
}

// insertWithinTx はモデルの一致を確かめてから全件を投入する。
//
// 🔴 *sql.Stmt を自前で用意しない。理由は規約の組み合わせにある:
// sqlclosecheck は Close を defer で呼ぶことを要求し、errcheck (check-blank) は
// その戻り値の破棄を禁じ、nonamedreturns は defer から返り値へ結果を反映する
// 名前付き戻り値を禁じている。三つが同時に成り立つ書き方が無い。
// 抑制で通さず設計側で直す、という規約に従って Stmt を持たない形にした。
// postgres 側と同じ判断であり、同じ理由である。
func (s *Store) insertWithinTx(ctx context.Context, tx *sql.Tx, w pendingWrite) ([]int64, error) {
	// 🔴 書き込みの前に確かめる。別モデルのベクトルを混ぜると、次元が同じでも
	// 比較できないベクトルが同じ列に並び、以後の検索が黙って壊れる（ADR 0005）。
	// 分割規則が違うトークン列を混ぜたときも同じ形で壊れる。
	if err := s.assertSameEmbedderAndTokenizer(ctx, tx); err != nil {
		return nil, err
	}

	ids := make([]int64, 0, len(w.chunks))

	for i, c := range w.chunks {
		var id int64

		err := tx.QueryRowContext(ctx, insertChunkSQL,
			w.orgID.Int64(), c.DocumentID, c.SourceID, c.ChunkIndex, c.Content,
			c.PageNumber, c.SectionLabel, s.embedderID, w.encoded[i].vector,
			w.encoded[i].lexemeText, s.tokenizerID, c.ExternalID,
		).Scan(&id)
		if err != nil {
			return nil, fmt.Errorf("%w: chunks[%d]: %s", errWrite, i, err.Error())
		}

		ids = append(ids, id)
	}

	return ids, nil
}

// Delete は1件を削除する。対象が無くてもエラーにしない。
//
// 🔴 org_id を条件に含める。id が存在しても org が違えば消えない。
// 「id を知っていること」を権限の代わりにしない（ADR 0003）。
// 対象ゼロを成功とするのは、削除の意味が「その状態にすること」であり、
// 既にその状態なら要求は満たされているため。
func (s *Store) Delete(ctx context.Context, orgID org.ID, chunkID int64) error {
	if err := validateOrgID(orgID); err != nil {
		return err
	}

	const stmt = `DELETE FROM chunks WHERE org_id = ? AND id = ?`

	if _, err := s.db.ExecContext(ctx, stmt, orgID.Int64(), chunkID); err != nil {
		return fmt.Errorf("%w: delete: %s", errWrite, err.Error())
	}

	return nil
}

// DeleteBySource は取り込み元の単位でまとめて削除し、消した件数を返す。
//
// 🔴 org_id を条件に含める理由は Delete と同じ。
// 件数を返すのは、再取り込みが DeleteBySource → Put の2手順である以上、
// 呼び出し側が「何を消したうえで入れ直したか」を記録できる必要があるため。
func (s *Store) DeleteBySource(ctx context.Context, orgID org.ID, sourceID int64) (int, error) {
	const stmt = `DELETE FROM chunks WHERE org_id = ? AND source_id = ?`

	return s.deleteBy(ctx, orgID, stmt, sourceID)
}

// DeleteByDocument は文書の単位でまとめて削除し、消した件数を返す。
//
// 🔴 org_id を条件に含める理由は Delete と同じ。
//
// source 単位と別に持つのは、Corpus の削除経路が document 単位と source 単位の
// 2つだからである (docs/adr/0020-phase2-corpus-integration-contract.md Decision 2)。
// postgres 側と同じ契約であり、2つのストアが同じ形をしていることを配線点が
// コンパイル時に確かめている (cmd/recall の backingStore)。
func (s *Store) DeleteByDocument(ctx context.Context, orgID org.ID, documentID int64) (int, error) {
	const stmt = `DELETE FROM chunks WHERE org_id = ? AND document_id = ?`

	return s.deleteBy(ctx, orgID, stmt, documentID)
}

// deleteBy は「org と1つの id で絞って消し、件数を返す」を実行する。
//
// 🔴 stmt を呼び出し側から受けるが、org_id が WHERE の先頭に来ることは
// どの呼び出し元でも変わらない。分離条件を文字列連結で組み立てないこと——
// 組み立てにすると、org_id を落とした SQL を書けるようになる。
func (s *Store) deleteBy(ctx context.Context, orgID org.ID, stmt string, id int64) (int, error) {
	if err := validateOrgID(orgID); err != nil {
		return 0, err
	}

	result, err := s.db.ExecContext(ctx, stmt, orgID.Int64(), id)
	if err != nil {
		return 0, fmt.Errorf("%w: bulk delete: %s", errWrite, err.Error())
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("%w: rows affected: %s", errWrite, err.Error())
	}

	return int(affected), nil
}
