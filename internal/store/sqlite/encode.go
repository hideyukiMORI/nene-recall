// Package sqlite は SQLite を検索ストアとして実装する。比較実測のための第二の
// バックエンドであり、既定は PostgreSQL + pgvector のままである
// (docs/adr/0007-pgvector-over-brute-force.md)。
//
// ドライバは純 Go の modernc.org/sqlite を database/sql 経由で使う。cgo を
// 要求する mattn/go-sqlite3 と sqlite-vec は選べない（CLAUDE.md 地雷5・ARC-003）。
// ベクトルは BLOB に置いて Go 側で総当たりの内積を取り、語彙は FTS5 の bm25()
// で採点する。判断の記録は docs/adr/0017-sqlite-store-for-comparison.md にある。
//
// 本ファイルは「Go の値を SQLite へ渡す表現に変換する境界」と、その変換の前提が
// 満たされているかの検査を持つ。DB には触れない。
package sqlite

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// vectorDimensions は保存するベクトルの次元数。
//
// 🔴 postgres 側の vectorDimensions と同じ値でなければならない。比較実測は
// 「同一データを2つのストアに入れて測る」ことが前提であり、次元が食い違う
// 構成はそもそも同じデータを入れられない。
//
// 定数を postgres パッケージから import して共有しない。ストア同士は互いを
// 知らない（ARC-001 の依存方向はどちらも配線点から生えている）。同じ値である
// ことは、両者が embed.Embedder の次元と突き合わせる検査で保たれる。
const vectorDimensions = 1024

// vectorBytes は 1 本のベクトルを BLOB にしたときの長さ。
//
// float32 は 4 バイトなので 1024 × 4 = 4096。DDL の
// CHECK (length(embedding) = 4096) と対になる。
const vectorBytes = vectorDimensions * 4

// float32Bytes は float32 1 個ぶんのバイト数。
const float32Bytes = 4

// normToleranceSquared は L2 ノルムの二乗が 1.0 から離れてよい幅。
//
// postgres 側と同じ値。判断の根拠も同じで、float32 の要素 1024 個ぶんの
// 丸め誤差を見込んである (docs/benchmarks/2026-09-01-baseline.md §2)。
const normToleranceSquared = 1e-3

// encodeVector は []float32 をリトルエンディアンの BLOB にする。
//
// 🔑 テキストではなくバイト列にするのは、往復で値が1ビットも変わらないことを
// 保証するためである。pgvector 側はテキスト表記を使っているが、あちらは
// 「pgvector の入力形式に合わせる」という外的な制約があってのことだった。
// こちらは自分で形式を決められるので、丸めの入らない形を選ぶ。
//
// バイト順を明示するのは、保存したファイルを別のアーキテクチャで開いても
// 同じ値が読めるようにするためである。SQLite のファイルは可搬なので、
// ホストのバイト順に従うと「ファイルは開けるがベクトルだけ壊れる」形になる。
func encodeVector(v []float32) []byte {
	buf := make([]byte, len(v)*float32Bytes)

	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[i*float32Bytes:], math.Float32bits(x))
	}

	return buf
}

// decodeVector は BLOB を []float32 に戻す。
//
// 🔴 長さを必ず検査する。DDL の CHECK が 4096 バイトを強制しているが、
// 制約は「Go を通さずに INSERT した行」までは守らない。壊れた行を読んだときの
// 症状が「短いベクトルとの内積＝意味のないスコア」になるのを防ぐ。
func decodeVector(blob []byte) ([]float32, error) {
	if len(blob) != vectorBytes {
		return nil, fmt.Errorf("%w: want %d bytes, got %d",
			errVectorBlobLength, vectorBytes, len(blob))
	}

	v := make([]float32, vectorDimensions)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*float32Bytes:]))
	}

	return v, nil
}

// dot は2本のベクトルの内積を返す。
//
// 🔑 これがこのストアの「ベクトル検索」そのものである。索引は無く、候補集合の
// 全行に対してこれを回す。ADR 0007 が pgvector の比較対象に据えているのは、
// まさにこの Go 側の総当たりである。
//
// 加算を float64 で行うのは validateVector と同じ理由による。float32 のまま
// 1024 要素を足すと誤差が積み上がり、順位が僅差の行で揺れる。
//
// 正規化済み同士の内積はコサイン類似度に等しい。前提は3箇所で支えている:
// embed.Embedder の契約・validateVector の実行時検査・違反時の即エラー化。
// どれかを外すなら、同時にコサインの明示計算（ノルムで割る）へ戻すこと。
func dot(a, b []float32) float64 {
	var sum float64
	for i, x := range a {
		sum += float64(x) * float64(b[i])
	}

	return sum
}

// idFilterJSON は []int64 を JSON 配列のテキストにする。
//
// 用途は検索の document_ids / source_ids フィルタで、SQL 側は
// json_array_length(?) = 0 OR id IN (SELECT value FROM json_each(?)) で受ける。
//
// 🔴 プレースホルダを件数ぶん並べる形にしない。SQLite の変数上限は既定 32766 で、
// 絞り込みの件数が増えると SQL の組み立て自体が失敗する。値を1つの JSON 文字列に
// 畳めば、パラメータの数は絞り込みの件数に依存しなくなる。
//
// 🔴 nil と空スライスをどちらも "[]" にする。両者を区別しない。つまり
// 「フィルタが空」という一つの意味しか持たない。SQL 側が
// json_array_length(...) = 0 を「フィルタ無し」と読む分岐を必ず持つこと。
// 単に id IN (SELECT value FROM json_each('[]')) と書くと一件も一致せず、
// 絞り込まないつもりが全滅する。
func idFilterJSON(ids []int64) string {
	buf := make([]byte, 0, len(ids)*8+2)
	buf = append(buf, '[')

	for i, id := range ids {
		if i > 0 {
			buf = append(buf, ',')
		}

		buf = strconv.AppendInt(buf, id, 10)
	}

	return string(append(buf, ']'))
}

// matchMetaCharacters は FTS5 の MATCH 式で意味を持つ文字。
//
// 二重引用符は字句の囲み、単引用符と逆斜線は SQLite の文字列表現で意味を持つ。
// * は前方一致、( ) は結合、: は列指定、^ は先頭一致、- は NOT の綴りに現れる。
//
// 🔑 postgres 側の tsqueryMetaCharacters と同じ集合にしてある。方言の差で
// 「片方のストアだけが受け付けるトークン」を作らないためである。同じ Tokenizer の
// 出力が両方のストアで同じ扱いを受けることが、比較実測の前提になる。
const matchMetaCharacters = `&|!():*<>'"\`

// matchOr はトークンを繋ぐ演算子。
//
// 🔴 AND にしないこと。長いクエリで1語でも本文に無ければ1件も当たらなくなり、
// 「どれだけ重なったか」という段階的な情報が失われる。ハイブリッド合成が
// 欲しいのは連続値であって真偽値である。postgres 側の tsqueryOr と同じ判断。
const matchOr = " OR "

// encodeLexemeText はトークン列を lexeme_text 列に入れる形にする。
//
// 空白区切りで並べるだけである。FTS5 の外部コンテンツ表がこの列を読み、
// ascii トークナイザで空白と ASCII 記号の位置で割る。
//
// 空のトークン列は空文字になる。これは正常な入力（分割できる語が無い本文）で
// あり、FTS には何も登録されず、どの MATCH にも当たらない。
func encodeLexemeText(tokens []string) (string, error) {
	if err := validateTokens(tokens); err != nil {
		return "", err
	}

	return strings.Join(tokens, " "), nil
}

// encodeMatchExpression はトークン列を FTS5 の MATCH 式にする。
//
// 各トークンを二重引用符で囲んで OR で繋ぐ。
//
// 🔑 引用符は「1レキシームとして扱え」という指示ではなく、FTS5 では
// **フレーズ**（囲みの中を同じトークナイザで割った並びが、その順で隣接すること）
// を表す。したがって "recall_store" は ascii トークナイザで recall と store に
// 割れたうえで隣接一致になり、postgres 側の 'recall' <-> 'store' と同じ意味を持つ。
// 実測で確認済み——"recall_store" は "recall_store を postgres にする" に当たり、
// "recall は store とは別の語である" には当たらない。
//
// 🔴 囲まずに裸で並べると、トークンに含まれる記号が FTS5 の演算子として
// 解釈され、構文が壊れるか、意図しない検索式になる。囲みを外さないこと。
//
// トークンが1つも無ければ空文字を返す。🔴 呼び出し側はこれを MATCH に渡さないこと。
// 空の MATCH 式は SQLite の構文エラー（fts5: syntax error near ""）になる。
// postgres の to_tsquery が空文字を許すのとは挙動が違う——同じ「語彙スコア 0」に
// 落とすのは、ストア側が明示的に分岐して行う（searcher.go の lexicalScores）。
func encodeMatchExpression(tokens []string) (string, error) {
	if err := validateTokens(tokens); err != nil {
		return "", err
	}

	if len(tokens) == 0 {
		return "", nil
	}

	quoted := make([]string, 0, len(tokens))
	for _, token := range tokens {
		quoted = append(quoted, `"`+token+`"`)
	}

	return strings.Join(quoted, matchOr), nil
}

// validateTokens は lexical.Tokenizer の契約を実行時に検査する。
//
// 🔑 なぜ要るか。契約は doc コメントに書いてあるだけで、Go の型では表せない。
// 分割器は差し替えられる前提で設計してあるから、新しい実装が契約を破る機会は
// 将来に確実にある。
//
// 検査が無ければ症状は2通りで、どちらも静かである。空白を含むトークンは
// lexeme_text の中で勝手に割れ、メタ文字を含むトークンは MATCH 式を壊す。
// 前者は「語彙スコアが少し変」、後者は「検索が失敗する」。前者に気づける人はいない。
func validateTokens(tokens []string) error {
	for i, token := range tokens {
		if strings.ContainsFunc(token, unicode.IsSpace) {
			return fmt.Errorf("%w: tokens[%d]=%q", errTokenHasWhitespace, i, token)
		}

		if strings.ContainsAny(token, matchMetaCharacters) {
			return fmt.Errorf("%w: tokens[%d]=%q", errTokenHasMetaCharacter, i, token)
		}
	}

	return nil
}

// validateVector は embed.Embedder の契約（次元数と L2 正規化）を実行時に検査する。
//
// 🔑 なぜ要るか。順位付けに内積を使うのは「入力が常に正規化されている」という
// 前提があってのことで、正規化済み同士なら内積とコサイン類似度の順位が一致する
// からである (docs/benchmarks/2026-09-01-baseline.md §2)。ベンチマークはこの
// 前提を「黙って前提にしない」と明示的に警告している。
//
// 契約を破るプロバイダが将来足されたとき、検査が無ければ症状は「順位が静かに
// 狂う」になる。ADR 0005 が記録した罠——エラーにならないまま無意味なスコアが
// 返る——と同じ形で、単一プロバイダで開発している限り一切表面化しない。
//
// ノルムは二乗のまま比べる。Σv² = 1 と ‖v‖ = 1 は同値なので平方根は要らない。
// 加算は float64 で行う。float32 のまま 1024 要素を足すと、誤差が許容幅と同じ
// 桁まで積み上がって検査自体が信用できなくなる。
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
