package index

import "errors"

// ErrEmbedderMismatch は、保存済みベクトルの埋め込みモデルと現在の設定が
// 一致しないことを表す。
//
// 🔴 次元が同じでも異なるモデルのベクトルは比較できない。放置すると
// 「エラーにならないまま無意味なスコアが返る」状態になる
// (docs/adr/0005-embedding-provider-is-pluggable.md)。
// この失敗を「検索結果が空」で隠さないこと。必ずエラーとして表面化させる。
//
// この sentinel が具体ストアの側ではなく契約パッケージにあるのは、
// 不一致を検知するのがストアなのに対し、それを 503 に写すのは HTTP 層であり、
// HTTP 層は具体ストアを import できない (ARC-001・depguard の
// store-is-wired-only-in-cmd) ためである。errors.Is で判別できる場所は
// 両者が共通して依存するここしかない。
var ErrEmbedderMismatch = errors.New("index: stored vectors were produced by a different embedder")

// ErrInvalidQuery は、Query の内容が契約を満たさないことを表す。
//
// Limit < 1・Text が空 のような、ストアに問い合わせるまでもなく成立しない要求が対象。
// 呼び出し側が 400 と 503 を区別できるよう、モデル不一致 (ErrEmbedderMismatch) とは
// 別の sentinel にしてある。
//
// OrgID の欠落はここに含めない。org.ErrInvalid が境界で先に拒否しており、
// 「未指定の org」を index の層まで通す経路を作らないためである
// (docs/adr/0003-org-id-is-mandatory.md)。
var ErrInvalidQuery = errors.New("index: invalid search query")
