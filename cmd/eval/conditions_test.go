package main_test

import (
	"errors"
	"strings"
	"testing"

	main "github.com/hideyukiMORI/nene-recall/cmd/eval"
)

// TestAlphaNoteIsChosenPerStore は、alpha の但し書きが測ったストアに応じて
// 変わることを見る。
//
// 🔴 様式 v3 までは、SQLite のレポートにも Postgres 由来の「既定 0.8 は
// ADR 0015 のプラトーの中心」という文言が載っていた。SQLite で 0.8 が
// 選ばれた形跡があるように読めるが、実測のプラトーは 0.8〜0.9 で
// Postgres の 0.7〜0.9 とずれている
// (docs/benchmarks/2026-09-02-eval-store-comparison.md)。
// **測っていないことをレポートに書かない**、がこの検査の中身である。
func TestAlphaNoteIsChosenPerStore(t *testing.T) {
	postgres, err := main.AlphaNoteFor("postgres")
	if err != nil {
		t.Fatalf("AlphaNoteFor(postgres): %v", err)
	}

	sqlite, err := main.AlphaNoteFor("sqlite")
	if err != nil {
		t.Fatalf("AlphaNoteFor(sqlite): %v", err)
	}

	if postgres != main.AlphaNotePostgres() {
		t.Errorf("postgres の但し書き = %q", postgres)
	}

	if sqlite != main.AlphaNoteSQLite() {
		t.Errorf("sqlite の但し書き = %q", sqlite)
	}

	if postgres == sqlite {
		t.Error("2つのストアで同じ但し書きが返った。ADR 0015 は postgres の掃引である")
	}

	// 🔴 SQLite 側は「ADR 0015 の対象外」と言い切っていること。ここが抜けると、
	// 数字だけを見た読み手が 0.8 を「このバックエンドで調整済み」と受け取る。
	if !strings.Contains(sqlite, "does not cover the sqlite store") {
		t.Errorf("sqlite の但し書きが ADR 0015 の射程外だと言っていない: %q", sqlite)
	}

	if !strings.Contains(postgres, "ADR 0015") {
		t.Errorf("postgres の但し書きが根拠 (ADR 0015) を指していない: %q", postgres)
	}
}

// TestAlphaNoteRejectsAnUnknownStore は、未知のストア名を既定へ黙って
// 倒さないことを見る。
//
// 倒すと SQLite のレポートに Postgres の但し書きが載り、v4 で塞いだ
// 読み違えがそのまま戻る。openEvalStore が未知の -store を拒むのと同じ判断。
func TestAlphaNoteRejectsAnUnknownStore(t *testing.T) {
	got, err := main.AlphaNoteFor("postgress")
	if err == nil {
		t.Fatalf("未知のストア名が通った: %q", got)
	}

	if got != "" {
		t.Errorf("失敗したのに文言が返った: %q", got)
	}
}

// TestDecimalOfFloat32RecoversTheDecimal は、float32 で持っている既定の
// alpha が、設定に書いた10進のまま float64 になることを見る。
//
// 🔴 float64(float32(0.8)) は 0.8000000119209290 である。レポートの
// conditions.alpha は機械で突き合わせる正本なので (ADR 0013)、== 0.8 が
// 偽になる値を刻まない。
func TestDecimalOfFloat32RecoversTheDecimal(t *testing.T) {
	cases := map[float32]float64{
		0.8:  0.8,
		0.6:  0.6,
		0.85: 0.85,
		1:    1,
		0:    0,
	}

	for in, want := range cases {
		got, err := main.DecimalOfFloat32(in)
		if err != nil {
			t.Fatalf("DecimalOfFloat32(%v): %v", in, err)
		}

		if got != want {
			t.Errorf("DecimalOfFloat32(%v) = %v, want %v", in, got, want)
		}
	}
}

// TestDecimalOfFloat32IsNotAPlainWidening は、素の float64() 変換では
// 目的を果たせないことを見る。
//
// 🔑 検査器を書いたら意図的な違反で発火することを証明する、という規律
// (QLT-006) の変形である。この検査が無いと、誰かが decimalOfFloat32 を
// 「冗長だ」と言って float64() に戻したとき、他のどのテストも落ちない。
func TestDecimalOfFloat32IsNotAPlainWidening(t *testing.T) {
	if float64(float32(0.8)) == 0.8 {
		t.Skip("この環境では float32 経由でも 0.8 になる。丸めの問題が存在しない")
	}

	got, err := main.DecimalOfFloat32(0.8)
	if err != nil {
		t.Fatalf("DecimalOfFloat32: %v", err)
	}

	if got == float64(float32(0.8)) {
		t.Errorf("素の広げ方と同じ値 %v。10進を経由していない", got)
	}
}

// TestAlphaNoteErrorIsWrapped は、失敗が errors.Is で分岐できる形で
// 返ることを見る (GO-005)。
func TestAlphaNoteErrorIsWrapped(t *testing.T) {
	_, err := main.AlphaNoteFor("")
	if err == nil {
		t.Fatal("空のストア名が通った")
	}

	// sentinel の連鎖が切れていないこと。呼び出し側はここで分岐する。
	if !errors.Is(err, main.ErrUnknownStore()) {
		t.Errorf("errUnknownStore を連鎖に含んでいない: %v", err)
	}
}
