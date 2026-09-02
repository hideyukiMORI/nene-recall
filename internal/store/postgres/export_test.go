package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hideyukiMORI/nene-recall/internal/index"
)

// 本ファイルはテストのためだけの公開窓である。
//
// テストは外部テストパッケージから公開 API 越しに書く（QLT-006）が、
// ここで検査したいのは境界の符号化と契約検査という未公開の部品である。
// 本体側で export すると、他パッケージから使える経路が「テストの都合」で
// 増えてしまうので、公開を _test.go に閉じる（GO-008）。

// VectorDimensions は vectorDimensions をテストへ公開する。
const VectorDimensions = vectorDimensions

// NormToleranceSquared は normToleranceSquared をテストへ公開する。
const NormToleranceSquared = normToleranceSquared

// EncodeVector は encodeVector をテストへ公開する。
func EncodeVector(v []float32) string { return encodeVector(v) }

// EncodeInt64Array は encodeInt64Array をテストへ公開する。
func EncodeInt64Array(ids []int64) string { return encodeInt64Array(ids) }

// ValidateVector は validateVector をテストへ公開する。
func ValidateVector(v []float32) error { return validateVector(v) }

// ErrVectorInvalid は errVectorInvalid をテストへ公開する。
func ErrVectorInvalid() error { return errVectorInvalid }

// ErrVectorDimensions は errVectorDimensions をテストへ公開する。
func ErrVectorDimensions() error { return errVectorDimensions }

// ErrVectorNotNormalized は errVectorNotNormalized をテストへ公開する。
func ErrVectorNotNormalized() error { return errVectorNotNormalized }

// ErrEmbedderDimensions は errEmbedderDimensions をテストへ公開する。
func ErrEmbedderDimensions() error { return errEmbedderDimensions }

// ErrEmbedderID は errEmbedderID をテストへ公開する。
func ErrEmbedderID() error { return errEmbedderID }

// ErrEmptyBatch は errEmptyBatch をテストへ公開する。
func ErrEmptyBatch() error { return errEmptyBatch }

// ErrEmptyContent は errEmptyContent をテストへ公開する。
func ErrEmptyContent() error { return errEmptyContent }

// ErrOrgMismatch は errOrgMismatch をテストへ公開する。
func ErrOrgMismatch() error { return errOrgMismatch }

// ErrChunkIDNotAccepted は errChunkIDNotAccepted をテストへ公開する。
func ErrChunkIDNotAccepted() error { return errChunkIDNotAccepted }

// ErrOrgRequired は errOrgRequired をテストへ公開する。
func ErrOrgRequired() error { return errOrgRequired }

// ErrExternalIDInvalid は errExternalIDInvalid をテストへ公開する。
func ErrExternalIDInvalid() error { return errExternalIDInvalid }

// ErrDuplicateExternalID は errDuplicateExternalID をテストへ公開する。
func ErrDuplicateExternalID() error { return errDuplicateExternalID }

// ErrTokenizerID は errTokenizerID をテストへ公開する。
func ErrTokenizerID() error { return errTokenizerID }

// ErrTokenInvalid は errTokenInvalid をテストへ公開する。
func ErrTokenInvalid() error { return errTokenInvalid }

// ErrTokenHasWhitespace は errTokenHasWhitespace をテストへ公開する。
func ErrTokenHasWhitespace() error { return errTokenHasWhitespace }

// ErrTokenHasMetaCharacter は errTokenHasMetaCharacter をテストへ公開する。
func ErrTokenHasMetaCharacter() error { return errTokenHasMetaCharacter }

// EncodeLexemeText は encodeLexemeText をテストへ公開する。
func EncodeLexemeText(tokens []string) (string, error) { return encodeLexemeText(tokens) }

// EncodeTsQuery は encodeTsQuery をテストへ公開する。
func EncodeTsQuery(tokens []string) (string, error) { return encodeTsQuery(tokens) }

// ErrUnknownFusion は errUnknownFusion をテストへ公開する。
func ErrUnknownFusion() error { return errUnknownFusion }

// StatementFor は Fusion.statement をテストへ公開する。
//
// 未知の方式・未知の候補の作り方に対する番人が働くことを、DB を立てずに
// 確かめるために要る。
func StatementFor(mode SearchMode, f Fusion, alpha float32) (string, any, error) {
	return f.statement(mode, alpha)
}

// ErrUnknownSearchMode は errUnknownSearchMode をテストへ公開する。
func ErrUnknownSearchMode() error { return errUnknownSearchMode }

// ErrCandidateK は errCandidateK をテストへ公開する。
func ErrCandidateK() error { return errCandidateK }

// EffectiveEfSearch は検索と同じトランザクション条件で hnsw.ef_search を読み戻す。
//
// 🔴 検索が使うのと**同じ** setEfSearch を通す。テストが自前で SET を書くと、
// 「テストの SET は効くが検索の SET は効いていない」状態を見逃す。
//
// 🔑 SET LOCAL はトランザクション期間の設定なので、外側からは観測できない。
// 効いていることを確かめるには、同じトランザクションの中で SHOW するしかない。
func (s *Store) EffectiveEfSearch(ctx context.Context) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("%w: begin: %s", errSearch, err.Error())
	}

	if err := s.setEfSearch(ctx, tx); err != nil {
		return "", errors.Join(err, tx.Rollback())
	}

	var value string
	if err := tx.QueryRowContext(ctx, `SHOW hnsw.ef_search`).Scan(&value); err != nil {
		return "", errors.Join(
			fmt.Errorf("%w: show hnsw.ef_search: %s", errSearch, err.Error()), tx.Rollback())
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("%w: commit: %s", errSearch, err.Error())
	}

	return value, nil
}

// ExplainSearch は Search と同じ SQL の実行計画を JSON で返す。
//
// 🔴 テスト専用である。実行計画は「索引が本当に使われているか」を確かめる
// 唯一の手段だが、それは検査の関心であって検索の契約ではない。
// SearchVector を index.Searcher に足さなかったのと同じ判断軸である (ADR 0013)。
//
// 🔑 Search と**同じ経路**で文と引数を組み立てる。テストが SQL を自前で
// 組み直すと、本番が使う文とは別の文の計画を見ることになり、
// 「索引が使われている」の証明にならない。
func (s *Store) ExplainSearch(ctx context.Context, q index.Query) (string, error) {
	if err := s.prepareSearch(ctx, q); err != nil {
		return "", err
	}

	vector, err := s.encodeQueryText(ctx, q.Text)
	if err != nil {
		return "", err
	}

	plan, err := s.searchPlan(q, vector)
	if err != nil {
		return "", err
	}

	return s.explainWithinTx(ctx, plan)
}

// explainWithinTx は Search と同じトランザクション条件で EXPLAIN する。
//
// hnsw.ef_search を SET LOCAL してから計画を取る。設定が違えば planner の
// 見積りも変わるので、本番と同じ条件で読む。
//
// 🔴 enable_seqscan と enable_sort を切る。理由を残す:
//
// テストのコーパスは 259 件しかない。embedding は TOAST へ追い出されるので
// ヒープ自体は小さく、planner の見積りでは全行走査 (cost 9.89) が HNSW の
// 起動コスト (cost 1041.94) を桁で下回る。seq scan だけを禁じると、今度は
// (org_id, ...) の B-tree を舐めてから Sort するほうが安くなる。
// ⇒ どちらも 2026-09-02 に実測した。**索引の欠陥ではなくデータ量の帰結**である。
//
// 🔑 ここで確かめたいのは**SQL の形が索引の順序に乗っているか**であって、
// planner がこの件数で索引を選ぶかではない。2つの代替（全行走査・B-tree＋Sort）を
// 両方とも高くしてやると、乗れる形なら HNSW が現れ、乗れない形なら現れない。
// 全探索の SQL は ORDER BY が合成式なので、この設定の下でも HNSW には乗れない
// ——2つのモードの違いは、まさにこの条件で分かれる (ADR 0022 Decision 3)。
//
// ⚠️ したがってこの計画は「速いこと」の証拠ではない。速さは計測の仕事で
// あって検査の仕事ではない (ADR 0013 Decision 5)。10万件での実際の計画は
// docs/benchmarks/ に残す。
func (s *Store) explainWithinTx(ctx context.Context, plan searchPlan) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("%w: begin: %s", errSearch, err.Error())
	}

	if err := s.setEfSearch(ctx, tx); err != nil {
		return "", errors.Join(err, tx.Rollback())
	}

	if err := discourageIndexlessPlans(ctx, tx); err != nil {
		return "", errors.Join(err, tx.Rollback())
	}

	var encoded string

	row := tx.QueryRowContext(ctx, `EXPLAIN (FORMAT JSON) `+plan.statement, plan.args...)
	if err := row.Scan(&encoded); err != nil {
		return "", errors.Join(
			fmt.Errorf("%w: explain: %s", errSearch, err.Error()), tx.Rollback())
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("%w: commit: %s", errSearch, err.Error())
	}

	return encoded, nil
}

// discourageIndexlessPlans は、索引を使わない代替の計画を高く見積もらせる。
//
// トランザクション期間だけの設定である（第3引数 true = SET LOCAL）。
// 禁止ではなく discourage なので、乗れる形が無ければ planner はそれでも
// 全行走査や Sort を選ぶ——「切ったから索引が出た」にはならない。
func discourageIndexlessPlans(ctx context.Context, tx *sql.Tx) error {
	for _, setting := range []string{"enable_seqscan", "enable_sort"} {
		const stmt = `SELECT set_config($1, 'off', true)`
		if _, err := tx.ExecContext(ctx, stmt, setting); err != nil {
			return fmt.Errorf("%w: disable %s: %s", errSearch, setting, err.Error())
		}
	}

	return nil
}
