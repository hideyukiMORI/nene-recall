package postgres

import (
	"errors"
	"fmt"
)

// errVectorInvalid は、ベクトルが embed.Embedder の契約を満たしていないことを表す。
//
// 呼び出し側が「ベクトルが理由で書けなかった」を一度に判定できるよう、
// 個別の理由（次元・正規化）はこれを包む。errors.Is はこの入れ子を辿る。
var errVectorInvalid = errors.New("postgres: vector does not satisfy the Embedder contract")

// errVectorDimensions は、ベクトルの次元数が vector(1024) 列と一致しないことを表す。
//
// 🔴 これは設定ミスであってデータの問題ではない。次元が違うベクトルは
// そもそも列に入らないので、取り込みを止めて設定を直させること。
var errVectorDimensions = fmt.Errorf("%w: unexpected dimensions", errVectorInvalid)

// errVectorNotNormalized は、ベクトルが L2 正規化されていないことを表す。
//
// 🔴 検索側の <#>（負の内積）は入力が正規化済みであることに依存している。
// これを見逃すと、エラーにならないまま順位だけが静かに狂う
// (docs/adr/0005-embedding-provider-is-pluggable.md)。
// 沈黙させないために、書き込みの時点で失敗にする。
var errVectorNotNormalized = fmt.Errorf("%w: vector is not L2-normalized", errVectorInvalid)
