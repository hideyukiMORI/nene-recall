package main

import "io"

// RunForTest は引数の解釈からサブコマンドの実行までを外部テストパッケージへ公開する。
//
// 🔴 テストのためだけの公開はこのファイルに閉じること (GO-008)。本体側で
// export しないのは、cmd がライブラリとして使われる余地を作らないためである。
// _test.go なので配布されるバイナリには入らない。
func RunForTest(args []string, in io.Reader, out, errOut io.Writer) int {
	return run(args, streams{in: in, out: out, err: errOut})
}

// PreviewForTest は本文の切り詰めを外部テストパッケージへ公開する。
func PreviewForTest(content string) string { return preview(content) }

// DefaultOrgIDForTest は CLI が持つ唯一の既定 org_id を返す。
//
// テストが 1 という数値を自前で書くと、定数を変えたときにテストだけが古くなる。
func DefaultOrgIDForTest() int64 { return defaultOrgID }
