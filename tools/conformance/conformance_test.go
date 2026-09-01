package conformance_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hideyukiMORI/nene-recall/tools/conformance"
)

// TestRepositoryHasNoViolations はリポジトリ全体を検査する。
//
// これが `go test ./...` に含まれることで、規約検査は「思い出したら走らせるもの」
// ではなく「常に走っているもの」になる。
func TestRepositoryHasNoViolations(t *testing.T) {
	root := repoRoot(t)

	findings, err := conformance.Check(root)
	if err != nil {
		t.Fatalf("Check() が失敗した: %v", err)
	}

	if len(findings) == 0 {
		return
	}

	for _, f := range findings {
		t.Errorf("%s", f.String())
	}

	t.Fatalf("規約違反 %d 件。docs/coding-rules.md を参照すること", len(findings))
}

// 🔑 以下は「規則が実際に発火すること」の証明である。
//
// 検査器の最大の失敗は、違反を見逃したまま常に緑を返すことで、その状態は
// 本物のコードを検査している限り発覚しない。だから意図的な違反を書いて、
// 落ちることを確認する。NENE-PIXEL の intentional-failure proof と同じ考え方。

func TestCNF001RejectsDirectOrgIDConversion(t *testing.T) {
	src := `package sample

import "github.com/hideyukiMORI/nene-recall/internal/org"

func fallback() org.ID { return org.ID(1) }
`
	assertFires(t, "CNF-001", "sample.go", src)
}

func TestCNF001AllowsConversionInsideOrgPackage(t *testing.T) {
	src := `package org

type ID int64

func NewID(v int64) ID { return ID(v) }
`
	assertQuiet(t, filepath.Join("internal", "org", "org.go"), src)
}

func TestCNF002RejectsOrgIDAsInt64(t *testing.T) {
	src := `package sample

type Query struct {
	OrgID int64
}
`
	assertFires(t, "CNF-002", "sample.go", src)
}

func TestCNF002RejectsOrgIDParameterAsInt64(t *testing.T) {
	src := `package sample

func Delete(orgID int64, chunkID int64) error { return nil }
`
	assertFires(t, "CNF-002", "sample.go", src)
}

func TestCNF002AllowsRawValueInWireDTO(t *testing.T) {
	src := `package sample

type searchRequest struct {
	OrgID *int64
}
`
	assertQuiet(t, "sample.go", src)
}

func TestCNF003RejectsGenericTypeSuffix(t *testing.T) {
	src := `package sample

type ChunkManager struct{}
`
	assertFires(t, "CNF-003", "sample.go", src)
}

func TestCNF003RejectsGenericPackageName(t *testing.T) {
	src := `package utils
`
	assertFires(t, "CNF-003", filepath.Join("internal", "utils", "utils.go"), src)
}

func TestCNF004RejectsDanglingADRReference(t *testing.T) {
	// 🔑 参照を実行時に連結して組み立てているのは、このテストファイル自身が
	// リポジトリ検査の対象だからである。壊れた参照をソースに直接書くと、
	// 検査器が自分のフィクスチャを本物の違反として検出してしまう。
	dangling := "docs/adr/" + "9999-does-not-exist.md"
	src := "package sample\n\n// 判断の根拠は " + dangling + " を参照。\nfunc sample() {}\n"

	assertFires(t, "CNF-004", "sample.go", src)
}

func TestCNF004AcceptsExistingADRReference(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, filepath.Join("docs", "adr", "0003-org-id-is-mandatory.md"), "# ADR 0003\n")
	writeFile(t, root, "sample.go", `package sample

// 根拠は docs/adr/0003-org-id-is-mandatory.md を参照。
func sample() {}
`)

	findings, err := conformance.Check(root)
	if err != nil {
		t.Fatalf("Check() が失敗した: %v", err)
	}

	if len(findings) != 0 {
		t.Fatalf("違反なしを期待したが %v", findings)
	}
}

// assertFires は、与えたソースが指定した規則で検出されることを確認する。
func assertFires(t *testing.T, rule, rel, src string) {
	t.Helper()

	root := t.TempDir()
	writeFile(t, root, rel, src)

	findings, err := conformance.Check(root)
	if err != nil {
		t.Fatalf("Check() が失敗した: %v", err)
	}

	for _, f := range findings {
		if f.Rule == rule {
			return
		}
	}

	t.Fatalf("%s が発火しなかった。検出結果: %v", rule, findings)
}

// assertQuiet は、与えたソースが1件も検出されないことを確認する。
func assertQuiet(t *testing.T, rel, src string) {
	t.Helper()

	root := t.TempDir()
	writeFile(t, root, rel, src)

	findings, err := conformance.Check(root)
	if err != nil {
		t.Fatalf("Check() が失敗した: %v", err)
	}

	if len(findings) != 0 {
		t.Fatalf("違反なしを期待したが %v", findings)
	}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()

	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("ディレクトリを作れない: %v", err)
	}

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("ファイルを書けない: %v", err)
	}
}

// repoRoot はこのテストから見たリポジトリルートを返す。
func repoRoot(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("作業ディレクトリを取得できない: %v", err)
	}

	root := filepath.Dir(filepath.Dir(wd)) // tools/conformance から2つ上
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("リポジトリルートを特定できない（%s に go.mod が無い）", root)
	}

	if !strings.HasSuffix(root, "nene-recall") {
		t.Logf("注意: ルートとして %s を検査する", root)
	}

	return root
}
