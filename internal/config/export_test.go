package config

// ValidateForTest は検証ロジックを外部テストパッケージへ公開する。
//
// Load 経由では作れない組み合わせ（既定値があるため環境変数を空にしても
// 空にならないフィールド）まで網羅して検証するために要る。
// _test.go なので本番バイナリには入らない。
//
// 🔴 テストのためだけの公開はこのファイルに閉じること。
// 検証を通すために内部関数を本体側で export しないこと (GO-008)。
func ValidateForTest(c Config) error { return c.validate() }
