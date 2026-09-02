// Command wikidistract は Wikimedia のダンプから日本語の段落を抜き出し、
// 評価ハーネスに投入できる紛れ込み (distractor) の JSONL を書く。
//
// 🔴 これは開発者の道具であって成果物ではない。make build の対象に入れない
// （cmd/eval と同じ線引き。Makefile の build ターゲットのコメントを参照）。
//
// 判断の根拠は docs/adr/0019-large-scale-benchmark-corpus.md。取得手順と
// 形式を選んだ理由は同じディレクトリの README.md にある。
//
// 🔴 ダンプを自分で取りに行かない。数百 MB の取得はツールの仕事ではなく、
// 何をいつ落としたかが手順として残るほうが再現性に効く。README の curl を使う。
package main

import (
	"bufio"
	"compress/bzip2"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/hideyukiMORI/nene-recall/internal/eval"
)

// DefaultCount は既定で選ぶ件数。
//
// 10万件は ADR 0007 が「索引を入れる前に測る」と決めた規模である。
const DefaultCount = 100000

// DocumentIDOffset / DistractorSourceID は紛れ込みに与える id。
//
// 🔴 評価セットと衝突しない範囲に置く (ADR 0019 Decision 1)。評価コーパスの
// document_id は Source 名の初出順に 1 から採番されるので (internal/eval)、
// 下駄を履かせないと数十番台で正面衝突する。衝突しても計測は止まらず、
// document_id で絞り込む検索だけが静かに別のものを返すようになる。
const (
	DocumentIDOffset   = 9_000_000
	DistractorSourceID = 9_000_000
)

// outputFileMode / outputDirMode は書き出す JSONL の権限。
const (
	outputFileMode = 0o644
	outputDirMode  = 0o755
)

var (
	// errFlags はフラグの指定が足りない・矛盾していることを表す。
	errFlags = errors.New("wikidistract: invalid command line flags")
	// errInput はダンプを読めないことを表す。
	errInput = errors.New("wikidistract: cannot read the dump")
	// errOutput は JSONL を書き出せないことを表す。
	errOutput = errors.New("wikidistract: cannot write the output")
)

// wikiPage はダンプの <page> 要素のうち、必要な部分だけ。
//
// 🔑 リダイレクトを判別する項目を持たない。リダイレクトの本文は
// "#REDIRECT [[...]]" の1行だけで、行頭 "#" は Selector が散文でないと
// 判断して落とす。判別を二重に持たないほうが、規則の在り処が1つで済む。
type wikiPage struct {
	// NS は名前空間。本文の記事は 0。
	NS int `xml:"ns"`
	// ID はページ ID。ダンプは昇順に並んでいる。
	ID int64 `xml:"id"`
	// Revision は最新版。
	Revision wikiRevision `xml:"revision"`
}

// wikiRevision は <revision> 要素のうち本文だけ。
type wikiRevision struct {
	// Text は wikitext の本文。
	Text string `xml:"text"`
}

// Result は1回の抽出の結果。
type Result struct {
	// Count は書き出した件数。
	Count int
	// SHA256 は書き出した JSONL の内容ハッシュ。README に記録して再現性を担保する。
	SHA256 string
	// Pages は走査したページ数（名前空間 0 のみ）。
	Pages int
	// LastPageID は最後に採用したページの ID。どこまで読んで止まったかの記録。
	LastPageID int64
}

// Extractor はダンプから紛れ込みを選ぶ。
//
// ゼロ値は無効である。NewExtractor を通すこと。
type Extractor struct {
	stripper *Stripper
	selector Selector
	// count は選ぶ件数の上限。
	count int
}

// NewExtractor は既定の規則で Extractor を作る。
func NewExtractor(count int) *Extractor {
	return &Extractor{stripper: NewStripper(), selector: NewSelector(), count: count}
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := start(log); err != nil {
		log.Error("extraction failed", slog.Any("error", err))
		os.Exit(1)
	}
}

// start はフラグを読み、抽出を実行する。
func start(log *slog.Logger) error {
	in := flag.String("in", "", "Wikimedia のダンプ (.bz2 または素の XML・必須)")
	out := flag.String("out", "", "書き出す JSONL (必須)")
	count := flag.Int("count", DefaultCount, "選ぶ段落の件数")
	flag.Parse()

	switch {
	case *in == "":
		return fmt.Errorf("%w: -in is required", errFlags)
	case *out == "":
		return fmt.Errorf("%w: -out is required", errFlags)
	case *count < 1:
		return fmt.Errorf("%w: -count must be at least 1, got %d", errFlags, *count)
	}

	result, err := NewExtractor(*count).run(*in, *out)
	if err != nil {
		return err
	}

	// 🔴 sha256 と件数を必ず出す。生成物はリポジトリに入らないので (ADR 0019)、
	// この2つだけが「同じ 10万件で測った」と後から言うための手がかりである。
	log.Info("distractors written",
		slog.String("out", *out),
		slog.Int("count", result.Count),
		slog.String("sha256", result.SHA256),
		slog.Int("pages_scanned", result.Pages),
		slog.Int64("last_page_id", result.LastPageID),
	)

	return nil
}

// run は入力を開き、出力へ書き出す。
func (e *Extractor) run(in, out string) (Result, error) {
	src, err := os.Open(in)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %s: %w", errInput, in, err)
	}

	result, err := e.toFile(out, decompress(in, src))

	return result, errors.Join(err, closeInput(src))
}

// closeInput は入力を閉じる。握り潰さない (GO-005)。
func closeInput(src *os.File) error {
	if err := src.Close(); err != nil {
		return fmt.Errorf("%w: close: %w", errInput, err)
	}

	return nil
}

// decompress は拡張子から圧縮を判断する。
//
// 素の XML も受けるのは、テストと小さな手元確認のためである。ダンプ本体は
// 常に .bz2 で配布されている。
func decompress(path string, r io.Reader) io.Reader {
	if strings.HasSuffix(path, ".bz2") {
		return bzip2.NewReader(r)
	}

	return r
}

// toFile は抽出結果をファイルへ書き出す。
func (e *Extractor) toFile(path string, r io.Reader) (Result, error) {
	if err := os.MkdirAll(filepath.Dir(path), outputDirMode); err != nil {
		return Result{}, fmt.Errorf("%w: create directory: %w", errOutput, err)
	}

	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, outputFileMode)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %s: %w", errOutput, path, err)
	}

	result, err := e.Extract(r, dst)

	if closeErr := dst.Close(); closeErr != nil {
		return Result{}, errors.Join(err, fmt.Errorf("%w: close: %w", errOutput, closeErr))
	}

	return result, err
}

// Extract はダンプを読み、選んだ段落を JSONL として w へ書く。
//
// 🔴 選択は決定的である。ページ ID 昇順（ダンプの並び順）・各ページの段落を
// 先頭から・件数に達したら即座に止める。同じ入力からは必ず同じ出力が出る
// (ADR 0019 Decision 1)。
func (e *Extractor) Extract(r io.Reader, w io.Writer) (Result, error) {
	hash := sha256.New()
	buffered := bufio.NewWriter(io.MultiWriter(w, hash))
	decoder := xml.NewDecoder(r)

	result := Result{Count: 0, SHA256: "", Pages: 0, LastPageID: 0}

	for result.Count < e.count {
		page, ok, err := nextPage(decoder)
		if err != nil {
			return Result{}, err
		}

		if !ok {
			break // ダンプを読み切った。件数に届かなくても止める。
		}

		result.Pages++

		if err := e.writePage(buffered, page, &result); err != nil {
			return Result{}, err
		}
	}

	if err := buffered.Flush(); err != nil {
		return Result{}, fmt.Errorf("%w: flush: %w", errOutput, err)
	}

	result.SHA256 = hex.EncodeToString(hash.Sum(nil))

	return result, nil
}

// nextPage は次の名前空間 0 のページを読む。
//
// 返り値の bool は「ページが取れたか」。false は入力を読み切ったことを表す。
func nextPage(decoder *xml.Decoder) (wikiPage, bool, error) {
	for {
		start, found, err := nextPageStart(decoder)
		if err != nil || !found {
			return wikiPage{}, false, err
		}

		var page wikiPage
		if err := decoder.DecodeElement(&page, &start); err != nil {
			return wikiPage{}, false, fmt.Errorf("%w: decode page: %w", errInput, err)
		}

		// 🔴 名前空間 0 以外は本文ではない。分類・テンプレート・ノートを
		// 混ぜると、distractor が「現実の日本語の散文」でなくなる。
		if page.NS == 0 {
			return page, true, nil
		}
	}
}

// nextPageStart は次の <page> の開始タグまで読み進める。
//
// 返り値の bool は「見つかったか」。false は入力を読み切ったことを表す。
func nextPageStart(decoder *xml.Decoder) (xml.StartElement, bool, error) {
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return xml.StartElement{}, false, nil
		}

		if err != nil {
			return xml.StartElement{}, false, fmt.Errorf("%w: parse xml: %w", errInput, err)
		}

		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "page" {
			return start, true, nil
		}
	}
}

// writePage は1ページぶんの採用段落を書き出す。
//
// chunk_index はページ内の通番である。件数の上限に達したら途中で止めるので、
// 最後のページだけ通番が途中で切れることがある。
func (e *Extractor) writePage(w io.Writer, page wikiPage, result *Result) error {
	for i, content := range e.selector.Paragraphs(e.stripper.PlainText(page.Revision.Text)) {
		if result.Count >= e.count {
			return nil
		}

		line, err := eval.EncodeDistractor(eval.Distractor{
			DocumentID: DocumentIDOffset + page.ID,
			SourceID:   DistractorSourceID,
			ChunkIndex: i,
			Content:    content,
		})
		if err != nil {
			return fmt.Errorf("%w: page %d: %w", errOutput, page.ID, err)
		}

		if _, err := w.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("%w: page %d: %w", errOutput, page.ID, err)
		}

		result.Count++
		result.LastPageID = page.ID
	}

	return nil
}
