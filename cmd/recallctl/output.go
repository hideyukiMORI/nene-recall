package main

import (
	"fmt"
	"io"
)

// textWriter は人が読む出力の書き出し先。
//
// 🔴 標準出力への書き込みは失敗しうる（`recallctl search ... | head` のように
// 読み手が先に閉じる）。1行ごとに error を見るとコマンドの本体が分岐だらけに
// なり、逆に握り潰すと errcheck の check-blank に引っかかる。最初の失敗だけを
// 覚えておき、書き終わりに1回 Err() で返す。
//
// ゼロ値は無効である。newTextWriter を通すこと。
type textWriter struct {
	w   io.Writer
	err error
}

// newTextWriter は書き出し先を包む。
func newTextWriter(w io.Writer) *textWriter {
	return &textWriter{w: w, err: nil}
}

// printf は1行を書く。既に失敗していれば何もしない。
//
// args が ...any なのは fmt への委譲だからで、この GO-006 の例外は
// 書式付き出力の1関数に閉じる（httpapi.writeJSON の body any と同じ扱い）。
func (t *textWriter) printf(format string, args ...any) {
	if t.err != nil {
		return
	}

	if _, err := fmt.Fprintf(t.w, format, args...); err != nil {
		t.err = err
	}
}

// write は整形せずにそのまま書く。--json の生 JSON はこちらを使う。
func (t *textWriter) write(s string) {
	if t.err != nil {
		return
	}

	if _, err := io.WriteString(t.w, s); err != nil {
		t.err = err
	}
}

// Err は最初に起きた書き込みの失敗を返す。何も起きていなければ nil。
func (t *textWriter) Err() error {
	if t.err == nil {
		return nil
	}

	return fmt.Errorf("recallctl: write output: %w", t.err)
}
