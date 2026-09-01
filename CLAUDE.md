# CLAUDE.md — NeNe Recall

Claude Code / AI エージェント向け実行ガイド。このファイルだけで作業を開始できる状態を保つ。

---

## 🔴 このリポジトリはフリート統治の射程外

**NeNe Recall は施主個人の必要から始まったプロジェクトであり、以下の対象に含めない:**

- 週次監査・棚卸し・フリート横断施策
- `_work/board.txt` の横断 TODO（このリポの TODO はこのリポ内で持つ）
- 統合リナの指揮対象（`nene-trace` と同じ「木の中にあるが射程外」の扱い）

将来 Corpus と統合しても、この扱いは施主が明示的に変えるまで維持する。
根拠: 2026-09-01 の施主判断。前例は `nene-trace`・reito 系。

---

## プロダクト一言要約

**Corpus が知識を蓄え、Recall が引き出す。**

Go 製の自己ホスト型 検索・取得サービス。チャンクを取り込み、ベクトル類似度と語彙一致の
ハイブリッドで検索し、引用可能な chunk を JSON で返す HTTP API。
単一バイナリ＋SQLite。外部ベクトル DB なし。cgo なし。

```
個人利用 (CLI/script) ──┐
                        ├─▶ NeNe Recall (Go) ─▶ SQLite
NeNe Corpus (PHP) ┄┄┄┄┄┘         │
  Phase 2・env で切替             └─▶ 埋め込みプロバイダ (Voyage AI / ローカル)
```

---

## 現在の状況

| フェーズ | 状態 |
| --- | --- |
| Phase 0 骨組み・設計判断 | ✅ 完了（2026-09-01） |
| Phase 1 単独動作する検索 API | 🔲 未着手 |
| Phase 2 Corpus 統合 | 🔲 未着手 |

**動くもの:** `/healthz`・`/readyz`・全エンドポイントの `org_id` 検証・設定読み込み・graceful shutdown
**動かないもの:** `/v1/search` と `/v1/chunks` 系は `501 Not Implemented`

### Phase 1 の残作業（着手順）

1. **SQLite ストア** — `internal/store`。`modernc.org/sqlite`（純 Go・cgo なし）。
   スキーマ・マイグレーション・埋め込みの BLOB 保存
2. **Voyage 埋め込みクライアント** — `internal/embed/voyage.go`。
   `Embedder` を実装。`input_type` を必ず指定する
3. **ベクトル検索** — 起動時にメモリへロードし、総当たり内積（ADR 0004）
4. **語彙検索** — 未決。SQLite FTS5 か Go 側 BM25 か（`docs/requirements.md` Q-1）
5. **ハイブリッド合成** — `alpha*vector + (1-alpha)*lexical`
6. **CLI** — 個人利用の入口。`org_id` の既定値は**CLI 側に置く。サーバ側には置かない**

---

## 開発コマンド

```bash
make test     # go test ./... -race -cover
make vet      # go vet ./...
make build    # bin/recall
make run      # 起動（.env が要る）
make fmt      # gofmt
```

Go は `/usr/local/go/bin`（1.27.0・公式 tarball から導入）。
`/etc/profile.d/go.sh` と各 `.bashrc` で PATH に入っている。

---

## 🔴 触る前に読む地雷

### 1. `org_id` に既定値を置かない

サーバ側のどこにも「org_id が無かったら 1 を使う」を書かないこと。
Corpus では分離条件が SQL の WHERE 句に埋まっていた（`PdoChunkSearchRepository.php:73`）が、
検索を外出しした結果、**分離の責任が Go 側へ移っている**。

ここを緩めると、あるテナントの検索が別テナントの文書を返す。
そして**単一テナントで開発・テストしている限り、この欠陥は一切症状を出さない**。

`internal/httpapi/server_test.go` の `TestOrgIDIsMandatory` が縛っている。
このテストを緩める変更は入れないこと。正本は `docs/adr/0003-org-id-is-mandatory.md`。

### 2. 埋め込みモデルを変えたら保存済みベクトルは無効

次元が一致していても異なるモデルのベクトルは比較できない。
**エラーにならないまま無意味なスコアが返る。**
`Embedder.ID()`（例 `voyage-4:1024`）を保存済みベクトルのメタデータに記録し、
不一致を検知して拒否すること。ADR 0005。

### 3. `input_type` を省略しない

Voyage の `input_type` は取り込み時 `document`、検索時 `query`。
公式 FAQ が「省略や `None` は検索品質を落とす」と明記している。
これは実装の任意事項ではなく**プロバイダの要求**。
`Embedder.Embed` が `Kind` を必須引数に取るのはそのため——既定値を持たせない設計は意図的。

### 4. Anthropic に埋め込み API は無い

「Claude で埋め込みを取る」は**できない**。公式ドキュメントが明記している:
> Anthropic does not offer its own embedding model.

探しに行かないこと。埋め込みは Voyage AI か自前のローカル推論。

### 5. cgo を持ち込まない

`sqlite-vec` や `mattn/go-sqlite3` は cgo を要求する。
単一バイナリ・クロスコンパイル可能という前提が壊れる（ADR 0004）。
SQLite ドライバは純 Go の `modernc.org/sqlite` を使う。

### 6. ADR 0004 の正しさには期限がある

総当たり内積は 10万チャンクまでの判断。**再評価トリガー:**
- チャンク数が 10万を超えた
- 検索 p95 が 200ms を超えた

どちらかに触れたら ADR 0004 を supersede して近似最近傍を検討する。
監視していなければ、遅くなってから気づくことになる。

---

## Corpus 側への先行依頼（Phase 2 を待たない）

`ChunkSearchResult::$score` を `int` → `float` に広げる作業を、
**Phase 2 の差し替えより先に** Corpus レーンへ出す。

現行の PDO 実装が返すのは整数値なので、いま広げても値は変わらず既存の挙動は壊れない。
**つまり今なら無風で通せる。** 差し替えと同時にやると、型変更とバックエンド交換が
同じ PR に乗って切り分けが難しくなる。正本は ADR 0006。

---

## 規約

- **ライセンス**: MIT（フリート標準）
- **公開リポの docs**: 要件定義・ADR・OpenAPI のみを正とする。
  運用ログ・日報の類はここに置かない
- **設計判断は必ず ADR にする**。`docs/adr/0000-template.md` を複製して書く。
  「なぜそうしなかったか」（却下した選択肢とその理由）を必ず残すこと
- **コメントは「なぜ」を書く**。「何を」はコードが語る。
  特に上の地雷に関わる箇所は、次に読む人が緩めないようコメントで理由を残す
- **エラー応答に API キーを混ぜない**。`config.Config` は `String()` を実装していないので、
  構造体ごと `%v` でログに出すと `VoyageAPIKey` が漏れる。個別フィールドを選んでログする
