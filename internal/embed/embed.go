// Package embed は埋め込みベクトルの生成を抽象化する。
//
// Anthropic は埋め込みモデルを提供していないため、プロバイダは外部になる
// (platform.claude.com/docs/en/build-with-claude/embeddings)。
// 既定は Voyage AI だが、自己ホストの建前を守るためインタフェースで隔離する。
// 判断の背景は docs/adr/0005-embedding-provider-is-pluggable.md を参照。
package embed

import (
	"context"
	"errors"
)

// Kind は Voyage の input_type に対応する。
//
// 公式 FAQ が「省略や None は検索品質を落とす」と明記しているため、
// 呼び出し側が必ず指定するようインタフェースの引数にしている。
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
// 返されるベクトルは長さ1に正規化されていることを前提とする。
// Voyage の埋め込みは正規化済みなので、内積がそのままコサイン類似度になる。
// 別プロバイダを足す際は、実装側で正規化してからこの契約を満たすこと。
type Embedder interface {
	// Embed は texts を kind の用途で埋め込む。返り値は texts と同順・同数。
	Embed(ctx context.Context, texts []string, kind Kind) ([][]float32, error)

	// Dimensions は生成されるベクトルの次元数を返す。
	Dimensions() int

	// ID は "voyage-4:1024" のような識別子を返す。
	//
	// 保存済みベクトルのメタデータに記録し、モデル切替を検知するために使う。
	// 次元が一致していても異なるモデルのベクトルは比較できず、
	// 放置すると「エラーにならないまま無意味なスコアが返る」状態になる。
	ID() string
}
