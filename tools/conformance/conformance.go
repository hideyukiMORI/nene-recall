// Package conformance は、このリポジトリ固有の規約を機械的に検査する。
//
// golangci-lint が見るのは「Go として一般に危ういこと」であって、
// 「NeNe Recall として守るべきこと」ではない。後者はここで検査する。
//
// 🔑 このパッケージが存在する理由:
// 規約を文章にしただけでは、次に書く人（人間でも AI でも）は読まずに書く。
// テストは書かれた場所しか守らないが、ここは全ファイルを毎回見る。
//
// 依存は標準ライブラリのみ。go.mod を空のまま保つため、
// golang.org/x/tools/go/analysis は使わない（型情報は使えないが、
// ここで見たい規則は識別子の名前と構文で判定できる）。
package conformance

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrWalk は検査対象の走査そのものが失敗したことを表す。
var ErrWalk = errors.New("conformance: failed to walk the repository")

// Finding は規約違反1件。
type Finding struct {
	Rule string // 規則 ID。docs/coding-rules.md と対応する
	File string // リポジトリルートからの相対パス
	Line int
	Msg  string
}

// String は「file:line: [CNF-00X] message」形式で返す。
func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: [%s] %s", f.File, f.Line, f.Rule, f.Msg)
}

// skippedDirs は走査しないディレクトリ。生成物と VCS だけを外す。
func skippedDirs() map[string]bool {
	return map[string]bool{".git": true, "bin": true, "vendor": true, "node_modules": true}
}

// Check は root 以下を検査し、見つかった違反を返す。
//
// 違反が無いときは空スライスを返す。error を返すのは走査自体が失敗したときだけで、
// 「規約違反があること」は error ではない（呼び出し側が報告の仕方を決める）。
func Check(root string) ([]Finding, error) {
	w := &walker{root: root, fset: token.NewFileSet(), findings: nil}
	if err := filepath.WalkDir(root, w.visit); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrWalk, err.Error())
	}

	return w.findings, nil
}

// walker は走査中の状態を持つ。検出結果を引数で引き回さないための入れ物。
type walker struct {
	root     string
	fset     *token.FileSet
	findings []Finding
}

// visit は filepath.WalkDir のコールバック。
func (w *walker) visit(path string, entry fs.DirEntry, err error) error {
	if err != nil {
		return err
	}

	if entry.IsDir() {
		if skippedDirs()[entry.Name()] {
			return fs.SkipDir
		}

		return nil
	}

	rel, err := filepath.Rel(w.root, path)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrWalk, err.Error())
	}

	found, err := checkFile(w.fset, w.root, filepath.ToSlash(rel))
	if err != nil {
		return err
	}

	w.findings = append(w.findings, found...)

	return nil
}

// checkFile は1ファイルに適用できる規則をすべて適用する。
func checkFile(fset *token.FileSet, root, rel string) ([]Finding, error) {
	src, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrWalk, err.Error())
	}

	if strings.HasSuffix(rel, ".md") {
		return checkADRLinks(root, rel, string(src)), nil
	}

	if !strings.HasSuffix(rel, ".go") {
		return nil, nil
	}

	file, err := parser.ParseFile(fset, rel, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrWalk, err.Error())
	}

	checker := fileChecker{fset: fset, rel: rel, inOrgPkg: strings.HasPrefix(rel, "internal/org/")}

	findings := checkADRLinks(root, rel, string(src))
	findings = append(findings, checker.orgIDConversions(file)...)
	findings = append(findings, checker.orgIDTypes(file)...)
	findings = append(findings, checker.naming(file)...)

	return findings, nil
}

// fileChecker は1ファイルぶんの検査文脈。
//
// 「今 internal/org の中を見ているか」を bool 引数で引き回すと制御結合になるので、
// 文脈として持たせる (GO-011 flag-parameter)。
type fileChecker struct {
	fset     *token.FileSet
	rel      string
	inOrgPkg bool
}

// finding は文脈から Finding を組み立てる。
func (c fileChecker) finding(rule string, pos token.Pos, msg string) Finding {
	return Finding{Rule: rule, File: c.rel, Line: c.fset.Position(pos).Line, Msg: msg}
}

// ---------------------------------------------------------------------------
// CNF-001: org.ID への直接変換を internal/org の外で禁止する
// ---------------------------------------------------------------------------

// checkOrgIDConversion は `org.ID(x)` という型変換を検出する。
//
// Go には private constructor が無いので、名前付き型を作っても
// `org.ID(1)` や `org.ID(0)` はコンパイルが通る。ADR 0003 が禁じている
// 「既定の org へのフォールバック」は、まさにこの1行で書けてしまう。
// 生成経路を org.NewID / org.ParseID に限るのがこの規則の目的である。
func (c fileChecker) orgIDConversions(file *ast.File) []Finding {
	if c.inOrgPkg {
		return nil // 生成経路そのもの。ここでだけ変換してよい
	}

	var findings []Finding

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}

		if !isSelector(call.Fun, "org", "ID") {
			return true
		}

		findings = append(findings, c.finding("CNF-001", call.Pos(),
			"org.ID への直接変換は禁止。org.NewID / org.ParseID を通すこと (ADR 0003 / GO-001)"))

		return true
	})

	return findings
}

// ---------------------------------------------------------------------------
// CNF-002: org_id を名乗る識別子の型は org.ID でなければならない
// ---------------------------------------------------------------------------

// orgIDNamePattern は org_id を指す識別子の書き方をすべて拾う。
func orgIDNamePattern() *regexp.Regexp { return regexp.MustCompile(`(?i)^org_?id$`) }

// wireDTOPattern は HTTP 境界の入力型。生の JSON 値を持つことを許す。
//
// 「キーが無い」と「0 が来た」を区別するために *int64 が要るため
// (docs/adr/0003-org-id-is-mandatory.md)。ただしドメインへ渡す前に
// 必ず org.NewID を通すこと。DTO はドメイン型ではない。
func wireDTOPattern() *regexp.Regexp { return regexp.MustCompile(`(?i)request$`) }

// checkOrgIDType は org_id を名乗るフィールド・引数が int64 に退化していないか見る。
//
// 型を org.ID にしても、次に書く人が「int64 のほうが楽だ」と戻したら分離は崩れる。
// 戻したことに気づく手段がこの規則である。
func (c fileChecker) orgIDTypes(file *ast.File) []Finding {
	var findings []Finding

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.TypeSpec:
			structType, ok := node.Type.(*ast.StructType)
			if !ok || wireDTOPattern().MatchString(node.Name.Name) {
				return true
			}

			findings = append(findings, c.orgIDFields(structType.Fields)...)
		case *ast.FuncDecl:
			findings = append(findings, c.orgIDFields(node.Type.Params)...)
		}

		return true
	})

	return findings
}

// orgIDFieldFindings はフィールド並び（構造体・引数のどちらも *ast.FieldList）を検査する。
func (c fileChecker) orgIDFields(fields *ast.FieldList) []Finding {
	if fields == nil {
		return nil
	}

	var findings []Finding

	for _, field := range fields.List {
		typeName := exprString(c.fset, field.Type)
		if c.isOrgIDType(typeName) {
			continue
		}

		for _, name := range field.Names {
			if !orgIDNamePattern().MatchString(name.Name) {
				continue
			}

			findings = append(findings, c.finding("CNF-002", name.Pos(),
				fmt.Sprintf("%s の型は org.ID にすること（現在 %s）。int64 に戻すと分離が静かに壊れる (ADR 0003)",
					name.Name, typeName)))
		}
	}

	return findings
}

// isOrgIDType は型表記が org.ID（org パッケージ内では ID）かどうかを判定する。
func (c fileChecker) isOrgIDType(typeName string) bool {
	if typeName == "org.ID" {
		return true
	}

	return c.inOrgPkg && typeName == "ID"
}

// ---------------------------------------------------------------------------
// CNF-003: 役割を語らない名前を禁止する
// ---------------------------------------------------------------------------

// forbiddenTypeSuffixes は、常に禁止する型名の語尾。
//
// 条件付きで許される語（Processor・Data 等）は入れない。
// 機械が拒否してよいのは「常に禁止」だけで、判断が要る語はレビューの仕事である。
func forbiddenTypeSuffixes() []string {
	return []string{"Manager", "Helper", "Util", "Utils", "Common"}
}

// forbiddenPackageNames は、中身を説明しないパッケージ名。
func forbiddenPackageNames() map[string]bool {
	return map[string]bool{"utils": true, "helpers": true, "managers": true, "misc": true, "common": true}
}

// checkNaming は型名とパッケージ名を検査する。
func (c fileChecker) naming(file *ast.File) []Finding {
	var findings []Finding

	if forbiddenPackageNames()[file.Name.Name] {
		findings = append(findings, c.finding("CNF-003", file.Name.Pos(),
			fmt.Sprintf("パッケージ名 %q は中身を説明していない。役割を名前にすること", file.Name.Name)))
	}

	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}

		for _, suffix := range forbiddenTypeSuffixes() {
			if !strings.HasSuffix(spec.Name.Name, suffix) {
				continue
			}

			findings = append(findings, c.finding("CNF-003", spec.Name.Pos(),
				fmt.Sprintf("型名 %q の語尾 %q は役割を語っていない", spec.Name.Name, suffix)))
		}

		return true
	})

	return findings
}

// ---------------------------------------------------------------------------
// CNF-004: 文中の ADR 参照が実在すること
// ---------------------------------------------------------------------------

// adrLinkPattern はコメント・文書中の ADR 参照を拾う。
func adrLinkPattern() *regexp.Regexp {
	return regexp.MustCompile(`docs/adr/(\d{4}-[a-z0-9-]+\.md)`)
}

// checkADRLinks は参照先の ADR が実在するかを見る。
//
// 判断の正本は ADR であるという規約を保つには、参照が切れていないことが前提になる。
// ADR を改名したのにコメントが古いままだと、次に読む人は根拠に辿り着けない。
func checkADRLinks(root, rel, src string) []Finding {
	var findings []Finding

	for _, match := range adrLinkPattern().FindAllStringSubmatchIndex(src, -1) {
		name := src[match[2]:match[3]]
		if _, err := os.Stat(filepath.Join(root, "docs", "adr", name)); err == nil {
			continue
		}

		findings = append(findings, Finding{
			Rule: "CNF-004",
			File: rel,
			Line: strings.Count(src[:match[0]], "\n") + 1,
			Msg:  fmt.Sprintf("参照先の ADR が存在しない: docs/adr/%s", name),
		})
	}

	return findings
}

// ---------------------------------------------------------------------------
// 補助
// ---------------------------------------------------------------------------

// isSelector は式が pkg.name という形かを判定する。
func isSelector(expr ast.Expr, pkg, name string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}

	ident, ok := sel.X.(*ast.Ident)

	return ok && ident.Name == pkg
}

// exprString は型式を元の表記に戻す。型情報は使わず、書かれたとおりに比較する。
func exprString(fset *token.FileSet, expr ast.Expr) string {
	var buf strings.Builder
	if err := printer.Fprint(&buf, fset, expr); err != nil {
		return ""
	}

	return buf.String()
}
