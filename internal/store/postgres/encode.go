// Package postgres は pgvector を載せた PostgreSQL を検索ストアとして実装する。
//
// ドライバは github.com/jackc/pgx/v5 を pgx/v5/stdlib 経由で database/sql として使い、
// pgvector の値は pgvector-go を使わずテキスト表記で自前に符号化する
// (docs/adr/0011-pgx-stdlib-driver.md)。
// 索引は最初から作らない。全探索で完成させ、実測してから HNSW を入れる
// (docs/adr/0007-pgvector-over-brute-force.md)。
//
// 本ファイルは「Go の値を Postgres へ渡すテキスト表現に変換する境界」と、
// その変換の前提が満たされているかの検査を持つ。DB には触れない。
package postgres

import (
	"fmt"
	"math"
	"strconv"
)

// vectorDimensions は保存するベクトルの次元数。
//
// bge-m3 も voyage-4 も既定で 1024 次元である
// (docs/adr/0008-local-embedding-by-default.md / docs/adr/0005-embedding-provider-is-pluggable.md)。
//
// 🔴 この定数は、後半のステップで作る embeddings 列の型 vector(1024) と対になる。
// 列の次元は DDL で固定されるので、次元の異なるプロバイダへ移るには
// マイグレーションが要る。したがってこれは実行時の設定値ではなくスキーマの一部であり、
// 定数として持つ。config.Config.EmbedDimensions が別の値になっている構成は
// この列に書けないので、接続時に突き合わせて拒否すること。
const vectorDimensions = 1024

// normToleranceSquared は L2 ノルムの二乗が 1.0 から離れてよい幅。
//
// float32 の要素 1024 個ぶんの丸め誤差を見込んだ値。bge-m3 の実測ノルムは
// ちょうど 1.0 である (docs/benchmarks/2026-09-01-baseline.md §2)。
const normToleranceSquared = 1e-3

// encodeVector は []float32 を pgvector のテキスト表記 "[0.1,0.2,...]" に変換する。
//
// 精度を落とさないために strconv の -1 精度（往復可能な最短表記）を bitSize 32 で使う。
// 固定桁数で丸めると、埋め込みの下位ビットが静かに失われて類似度の順位が変わりうる。
// 'f'（指数表記なし）を選ぶのは、pgvector のテキスト入力が受ける形に確実に収めるため。
//
// 空スライスは "[]" になる。これは vector(1024) 列には書けない値なので、
// 書き込み経路は必ず validateVector を先に通すこと。
func encodeVector(v []float32) string {
	// 1要素あたり最短表記は概ね 12 バイト以内に収まる。括弧と区切りを足して確保する。
	buf := make([]byte, 0, len(v)*13+2)
	buf = append(buf, '[')

	for i, x := range v {
		if i > 0 {
			buf = append(buf, ',')
		}

		buf = strconv.AppendFloat(buf, float64(x), 'f', -1, 32)
	}

	return string(append(buf, ']'))
}

// encodeInt64Array は []int64 を Postgres の配列表記 "{1,2,3}" に変換する。
//
// 用途は検索の document_ids / source_ids フィルタで、$3::bigint[] として渡す。
// ドライバのスライス変換仕様に依存せず、渡す形を自分で決めるためにテキスト化する。
//
// 🔴 nil と空スライスをどちらも "{}" にする。両者を区別しない。
// つまり「フィルタが空」という一つの意味しか持たない。
// したがって「フィルタ無し（＝全件が対象）」を "{}" で表してはならない。
// WHERE 句を `id = ANY($3::bigint[])` と書くと "{}" は一件も一致しないので、
// 絞り込まないつもりが全滅する。フィルタ無しは
// `cardinality($3::bigint[]) = 0 OR id = ANY($3::bigint[])` のように
// SQL 側で明示的に分岐させること。
func encodeInt64Array(ids []int64) string {
	buf := make([]byte, 0, len(ids)*8+2)
	buf = append(buf, '{')

	for i, id := range ids {
		if i > 0 {
			buf = append(buf, ',')
		}

		buf = strconv.AppendInt(buf, id, 10)
	}

	return string(append(buf, '}'))
}

// validateVector は embed.Embedder の契約（次元数と L2 正規化）を実行時に検査する。
//
// 🔑 なぜ要るか。後半で実装する検索は pgvector の <#>（負の内積）を使う予定で、
// これは「入力が常に正規化されている」という前提に依存する。正規化済み同士なら
// 内積とコサイン類似度の順位が一致するので軽い <#> を選べる、という理屈だからである
// (docs/benchmarks/2026-09-01-baseline.md §2)。ベンチマークはこの前提を
// 「黙って前提にしない」と明示的に警告している。
//
// 契約を破るプロバイダが将来足されたとき（ADR 0005 はプロバイダの差し替えを
// 前提に置いている）、検査が無ければ症状は「順位が静かに狂う」になる。
// これは ADR 0005 が記録した罠——エラーにならないまま無意味なスコアが返る——と
// 同じ形で、単一プロバイダで開発している限り一切表面化しない。
// この検査は、その失敗を「取り込み・検索が即エラーになる」に転化させるためにある。
//
// ノルムは二乗のまま比べる。Σv² = 1 と ‖v‖ = 1 は同値なので平方根は要らない。
// 加算は float64 で行う。float32 のまま 1024 要素を足すと、
// 誤差が許容幅と同じ桁まで積み上がって検査自体が信用できなくなる。
func validateVector(v []float32) error {
	if len(v) != vectorDimensions {
		return fmt.Errorf("%w: want %d, got %d", errVectorDimensions, vectorDimensions, len(v))
	}

	var sumSquares float64
	for _, x := range v {
		sumSquares += float64(x) * float64(x)
	}

	if math.Abs(sumSquares-1.0) > normToleranceSquared {
		return fmt.Errorf("%w: squared L2 norm is %g, want 1.0 within %g",
			errVectorNotNormalized, sumSquares, normToleranceSquared)
	}

	return nil
}
