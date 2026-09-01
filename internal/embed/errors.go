package embed

import "errors"

// ErrUnsupportedKind は Kind が未知の値だったことを表す。
//
// Kind は閉じた選択肢なので、ここに来るのは呼び出し側が値を捏造したときだけである。
// プロバイダごとに Kind の扱いは違う（bge-m3 は無視し、multilingual-e5 は接頭辞に
// 使い、Voyage は input_type として送る）が、「知らない Kind を黙って既定として
// 扱う」ことはどの実装にも許さない。既定に倒すと品質の低下が症状として出ない。
var ErrUnsupportedKind = errors.New("embed: unsupported input kind")

// ErrProviderUnavailable は埋め込みプロバイダが利用できないことを表す。
//
// 接続不能・モデル未取得・応答の破損（本数や次元の不一致）を含む。
// つまり「入力は正しいが、変換を提供できない」状態である。
//
// 🔴 この sentinel が実装側ではなく契約パッケージにあるのは、不可用を検知するのが
// 実装（internal/embed/ollama）なのに対し、それを 503 に写すのは httpapi であり、
// httpapi は具体 Embedder を import できない（ARC-001・depguard の
// embedder-is-wired-only-in-cmd）ためである。errors.Is で判別できる場所は
// 両者が共通して依存するここしかない。index.ErrEmbedderMismatch と同じ理由で
// 同じ場所に置いている。
//
// 🔴 この失敗を「空の結果」や「スコア0」で隠さないこと。埋め込みが得られないなら
// 検索は成立しておらず、成立しなかったことを呼び出し側に伝える必要がある。
var ErrProviderUnavailable = errors.New("embed: embedding provider is unavailable")
