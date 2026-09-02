package main_test

import (
	"net/http"
	"strings"
	"testing"
)

// TestExitCodesDistinguishFailures は終了コードの3分岐を固定する。
//
// 🔴 スクリプトから呼んだときに再試行してよいのがどれかを区別するための
// 分類である (ADR 0016 Decision 3)。3（接続失敗）だけが「後でもう一度やれば
// 通るかもしれない」失敗で、1 と 2 は打ち直しても同じ結果になる。
func TestExitCodesDistinguishFailures(t *testing.T) {
	fake400, url400 := startFake(t, http.StatusBadRequest,
		`{"error":{"code":"limit_out_of_range","message":"limit must be between 1 and 100"}}`)
	_, url503 := startFake(t, http.StatusServiceUnavailable,
		`{"error":{"code":"embedder_unavailable","message":"the embedding provider is unavailable"}}`)

	cases := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{name: "コマンドが無い", args: []string{"存在しない"}, wantCode: 1},
		{name: "サーバが 400", args: []string{"search", "-url", url400, "問い"}, wantCode: 2},
		{name: "サーバが 503", args: []string{"search", "-url", url503, "問い"}, wantCode: 2},
		{name: "接続できない", args: []string{"search", "-url", closedPortURL, "問い"}, wantCode: 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runCLI(t, "", tc.args...).code; got != tc.wantCode {
				t.Errorf("exit = %d, want %d", got, tc.wantCode)
			}
		})
	}

	if len(fake400.requests) == 0 {
		t.Error("400 の経路でサーバに届いていない")
	}
}

// TestServerErrorReportsCodeAndMessage は拒否の内容を stderr に出すことを見る。
//
// code は OpenAPI の契約なので、それを見れば利用者は次の一手を選べる。
func TestServerErrorReportsCodeAndMessage(t *testing.T) {
	_, url := startFake(t, http.StatusBadRequest,
		`{"error":{"code":"org_id_required","message":"org_id is required"}}`)

	got := runCLI(t, "", "search", "-url", url, "問い")

	for _, want := range []string{"org_id_required", "org_id is required", "400"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr に %q が無い: %q", want, got.stderr)
		}
	}

	if got.stdout != "" {
		t.Errorf("失敗したのに標準出力へ書いている: %q", got.stdout)
	}
}

// TestUnparsableErrorBodyKeepsStatus は Error スキーマでない拒否も捨てないことを見る。
//
// リバースプロキシが HTML を返すことがある。状態コードは分かるので、
// 本文をそのまま見せて「なぜか失敗した」で終わらせない。
func TestUnparsableErrorBodyKeepsStatus(t *testing.T) {
	_, url := startFake(t, http.StatusBadGateway, "<html>502 Bad Gateway</html>")

	got := runCLI(t, "", "search", "-url", url, "問い")

	if got.code != 2 {
		t.Errorf("exit = %d, want 2", got.code)
	}

	if !strings.Contains(got.stderr, "502") || !strings.Contains(got.stderr, "Bad Gateway") {
		t.Errorf("状態も本文も残っていない: %q", got.stderr)
	}
}

// TestUsageIsPrinted は引数なしと help の扱いを固定する。
func TestUsageIsPrinted(t *testing.T) {
	t.Run("引数なしは stderr に出して 1", func(t *testing.T) {
		got := runCLI(t, "")
		if got.code != 1 {
			t.Errorf("exit = %d, want 1", got.code)
		}

		if !strings.Contains(got.stderr, "recallctl <command>") {
			t.Errorf("使い方が stderr に無い: %q", got.stderr)
		}
	})

	t.Run("help は stdout に出して 0", func(t *testing.T) {
		got := runCLI(t, "", "help")
		if got.code != 0 {
			t.Errorf("exit = %d, want 0", got.code)
		}

		if !strings.Contains(got.stdout, "recallctl <command>") {
			t.Errorf("使い方が stdout に無い: %q", got.stdout)
		}
	})

	t.Run("サブコマンドの -h は 0", func(t *testing.T) {
		got := runCLI(t, "", "search", "-h")
		if got.code != 0 {
			t.Errorf("exit = %d, want 0 (stderr=%s)", got.code, got.stderr)
		}
	})
}

// TestUnknownFlagIsUsageError は知らないフラグを終了コード 1 で落とすことを見る。
func TestUnknownFlagIsUsageError(t *testing.T) {
	if got := runCLI(t, "", "search", "-nope", "問い").code; got != 1 {
		t.Errorf("exit = %d, want 1", got)
	}
}

// TestDeleteRejectsNonIntegerID は id が整数でないときに通信しないことを見る。
func TestDeleteRejectsNonIntegerID(t *testing.T) {
	fake, url := startFake(t, http.StatusNoContent, "")

	cases := []struct {
		name string
		args []string
	}{
		{name: "整数ではない", args: []string{"delete", "-url", url, "abc"}},
		{name: "id が無い", args: []string{"delete", "-url", url}},
		{name: "id が2つ", args: []string{"delete", "-url", url, "1", "2"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runCLI(t, "", tc.args...).code; got != 1 {
				t.Errorf("exit = %d, want 1", got)
			}
		})
	}

	if len(fake.requests) != 0 {
		t.Errorf("🔴 id が不正なのに %d 件送っている", len(fake.requests))
	}
}
