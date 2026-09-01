package ollama

import "errors"

// errInvalidConfig は Config の内容が不正で Client を組み立てられないことを表す。
//
// 🔴 これは起動時に落ちるべき失敗であり、embed.ErrProviderUnavailable では包まない。
// 設定の誤りを「プロバイダが一時的に使えない」と同じ扱いにすると、
// /readyz が 503 を返し続けるだけで、運用者は設定を直すべきだと気づけない。
// 不可用（実行時）と設定ミス（起動時）は別の失敗である。
var errInvalidConfig = errors.New("ollama: invalid client configuration")
