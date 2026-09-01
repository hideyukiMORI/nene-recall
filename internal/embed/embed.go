// Package embed は埋め込みベクトルの生成を抽象化する。
//
// Anthropic は埋め込みモデルを提供していない
// (platform.claude.com/docs/en/build-with-claude/embeddings)。
// 既定はローカル実行 (Ollama + bge-m3) で、外部 API を1回も呼ばずに完結する。
// Voyage AI は任意経路に降格した。プロバイダを差し替えても呼び出し側が変わらないよう
// インタフェースで隔離する。
// 判断の背景は docs/adr/0005-embedding-provider-is-pluggable.md と
// docs/adr/0008-local-embedding-by-default.md を参照。
package embed

import (
	"context"
	"errors"
)

// Kind は埋め込みの用途を表す。
//
// 要求はプロバイダごとに異なる。bge-m3 は接頭辞もパラメータも不要、
// multilingual-e5 は "query: " / "passage: " の接頭辞が必須、
// Voyage は input_type として送る (省略は品質低下・公式 FAQ が明記)。
// この差異を実装側に閉じ込めるのがインタフェースの役割なので、
// 呼び出し側は実装が使うかどうかに関わらず必ず指定する (ADR 0008)。
// 既定値を持たせないのは意図的である。
type Kind string

const (
	// KindDocument は取り込み時に使う。
	KindDocument Kind = "document"
	// KindQuery は検索時に使う。
	KindQuery Kind = "query"
)

// ErrUnsupportedKind は Kind が未知の値だったことを表す。
var ErrUnsupportedKind = errors.New("embed: unsupported input kind")

// Embedder はテキストを埋め込みベクトルに変換する。
//
// 🔴 返されるベクトルが長さ1に正規化されていることを、実装側の契約とする。
// 正規化済み同士なら内積がそのままコサイン類似度になり、pgvector の
// <#> (負の内積) と <=> (コサイン距離) で順位が一致して、軽い <#> を選べる。
// つまり検索側の演算子の選択がこの契約に依存している。
// bge-m3 は正規化済みで返す (ノルム 1.0 を実測・docs/benchmarks/2026-09-01-baseline.md)
// が、それはあくまで bge-m3 の性質であって全プロバイダの性質ではない。
// 別プロバイダを足す際は、実装側で正規化してからこの契約を満たすこと。
type Embedder interface {
	// Embed は texts を kind の用途で埋め込む。返り値は texts と同順・同数。
	Embed(ctx context.Context, texts []string, kind Kind) ([][]float32, error)

	// Dimensions は生成されるベクトルの次元数を返す。
	Dimensions() int

	// ID は "bge-m3:1024" のような識別子を返す。
	//
	// 保存済みベクトルのメタデータに記録し、モデル切替を検知するために使う。
	// 次元が一致していても異なるモデルのベクトルは比較できず、
	// 放置すると「エラーにならないまま無意味なスコアが返る」状態になる。
	ID() string
}
