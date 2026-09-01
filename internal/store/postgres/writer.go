package postgres

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
	vectors []string
}

// insertChunkSQL は1行を投入して採番された id を返す。
//
// 🔴 複数行 VALUES や CopyFrom による一括投入にしない。10万件の取り込み時間の
// 支配項は埋め込みの生成（実測 約18分・docs/benchmarks/2026-09-01-baseline.md）で
// あり、挿入を速くしても全体に測定可能な利得が無い。QLT-008 は「速さは実測を
// 伴わなければ主張しない」と定めており、利得を測れないうちに1行ずつという
// 読みやすい形を捨てない。ベンチで挿入が支配項だと分かったら、そのときの数字を
// 根拠に変える。
const insertChunkSQL = `INSERT INTO chunks (
	org_id, document_id, source_id, chunk_index, content,
	page_number, section_label, embedder_id, embedding
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::vector)
RETURNING id`

// Put はチャンクを投入し、採番された id を入力と同じ順で返す。
//
// 🔴 1回の呼び出しが1トランザクションであり、全件成功か全件なしかのどちらかに
// なる。部分投入を許すと、呼び出し側は「どこまで入ったか」を知る手段を持たず、
// 再送すれば重複が生まれる。insert-only（UNIQUE 制約なし）である以上、
// 重複を後から見分けることもできない。
func (s *Store) Put(ctx context.Context, orgID org.ID, chunks []chunk.Chunk) ([]int64, error) {
	if err := validateChunks(orgID, chunks); err != nil {
		return nil, err
	}

	// 🔴 埋め込みの生成はトランザクションを開ける前に済ませる。
	// 数十秒かかりうる外部 I/O をトランザクションの中に置くと、その間ずっと
	// 接続と行ロックを占有することになる。
	vectors, err := s.encodeContents(ctx, chunks)
	if err != nil {
		return nil, err
	}

	return s.insertChunks(ctx, pendingWrite{orgID: orgID, chunks: chunks, vectors: vectors})
}

// validateOrgID は分離条件が指定されていることを境界で確かめる。
//
// 🔴 DB の CHECK (org_id >= 1) に任せない。任せると失敗が「制約違反」という
// 読み取れない形になり、Search 側に至っては「一件も一致しない」＝空の結果に
// なって、呼び出し側からは「該当なし」と区別がつかなくなる。
// ゼロ値がストアまで届くのは上流のバグであり、静かに飲み込むと単一テナントで
// 開発している限り誰も気づかない。それが ADR 0003 の言う「症状を出さない」状態である。
//
// org.ID のゼロ値は言語仕様上どうやっても作れるので (GO-003)、境界で必ず検証してから
// 内側へ渡す。この関数を「CHECK があるから不要」と考えて消さないこと。
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
		if c.ID != 0 {
			return fmt.Errorf("%w: chunks[%d] carries id %d", errChunkIDNotAccepted, i, c.ID)
		}

		if c.Content == "" {
			return fmt.Errorf("%w: chunks[%d]", errEmptyContent, i)
		}

		// ゼロ値は「Chunk 側が org を持っていない」を意味する。chunk.Chunk の
		// OrgID は JSON に出ない項目なので、外から来た値では未設定が普通である。
		// 値が入っているのに引数と違うときだけ拒否する。黙って引数で上書きすると、
		// 呼び出し側の取り違えが別テナントへの書き込みとして成功してしまう。
		if c.OrgID != 0 && c.OrgID != orgID {
			return fmt.Errorf("%w: chunks[%d] says %s, argument says %s",
				errOrgMismatch, i, c.OrgID, orgID)
		}
	}

	return nil
}

// encodeContents は本文をまとめて埋め込み、pgvector のテキスト表記に変換する。
func (s *Store) encodeContents(ctx context.Context, chunks []chunk.Chunk) ([]string, error) {
	texts := make([]string, 0, len(chunks))
	for _, c := range chunks {
		texts = append(texts, c.Content)
	}

	// 🔴 全件を1回の呼び出しで渡す。32本ずつといった分割は Ollama クライアントの
	// 責務であってここではない。32 は Ollama の実測から出た値であり、
	// プロバイダに依存しないはずのストアに焼き付けない。
	//
	// Kind は bge-m3 が無視する実装であっても必ず渡す。使うかどうかは実装の都合で、
	// 渡すかどうかは呼び出し側の契約である（ADR 0008）。
	vectors, err := s.embedder.Embed(ctx, texts, embed.KindDocument)
	if err != nil {
		// 🔴 %w を2つ使い、埋め込み側の sentinel を連鎖に残す。
		// ここを %s（文字列化）にすると embed.ErrProviderUnavailable が切れ、
		// httpapi が 503 に写せず 500 に落ちる。SQL の失敗は逆にドライバ内部を
		// 連鎖へ載せない意図で %s のままにしてある。両者の違いは意図的である。
		return nil, fmt.Errorf("%w: embed: %w", errWrite, err)
	}

	if len(vectors) != len(chunks) {
		return nil, fmt.Errorf("%w: embedder returned %d vectors for %d chunks",
			errWrite, len(vectors), len(chunks))
	}

	encoded := make([]string, 0, len(vectors))

	for i, v := range vectors {
		// 契約違反をここで止める。通してしまうと <#> の順位が静かに狂う。
		if err := validateVector(v); err != nil {
			return nil, fmt.Errorf("%w: chunks[%d]", err, i)
		}

		encoded = append(encoded, encodeVector(v))
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
//
// Prepare していた目的（同じ SQL を繰り返しパースさせない）は失われない。
// pgx は DefaultQueryExecMode = QueryExecModeCacheStatement を既定にしており
// (pgx v5.10.0 conn.go:191 で実測確認)、database/sql 経由でも接続ごとに
// 文をキャッシュする。自前の Prepare はその上に重ねるものでしかなかった。
func (s *Store) insertWithinTx(ctx context.Context, tx *sql.Tx, w pendingWrite) ([]int64, error) {
	// 🔴 書き込みの前に確かめる。別モデルのベクトルを混ぜると、次元が同じでも
	// 比較できないベクトルが同じ列に並び、以後の検索が黙って壊れる（ADR 0005）。
	if err := s.assertSameEmbedder(ctx, tx); err != nil {
		return nil, err
	}

	ids := make([]int64, 0, len(w.chunks))

	for i, c := range w.chunks {
		var id int64

		err := tx.QueryRowContext(ctx, insertChunkSQL,
			w.orgID.Int64(), c.DocumentID, c.SourceID, c.ChunkIndex, c.Content,
			c.PageNumber, c.SectionLabel, s.embedderID, w.vectors[i],
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

	const stmt = `DELETE FROM chunks WHERE org_id = $1 AND id = $2`

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
	if err := validateOrgID(orgID); err != nil {
		return 0, err
	}

	const stmt = `DELETE FROM chunks WHERE org_id = $1 AND source_id = $2`

	result, err := s.db.ExecContext(ctx, stmt, orgID.Int64(), sourceID)
	if err != nil {
		return 0, fmt.Errorf("%w: delete by source: %s", errWrite, err.Error())
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("%w: rows affected: %s", errWrite, err.Error())
	}

	return int(affected), nil
}
