# NeNe Recall

[![CI](https://github.com/hideyukiMORI/nene-recall/actions/workflows/ci.yml/badge.svg)](https://github.com/hideyukiMORI/nene-recall/actions/workflows/ci.yml)
[![Go 1.27](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go)](https://go.dev/)
[![pgvector](https://img.shields.io/badge/pgvector-PostgreSQL-336791?logo=postgresql&logoColor=white)](https://github.com/pgvector/pgvector)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](./LICENSE)
[![OpenAPI](https://img.shields.io/badge/OpenAPI-3.1-85EA2D?logo=swagger)](./docs/openapi/openapi.yaml)
[![Status: pre-alpha](https://img.shields.io/badge/status-pre--alpha-lightgrey)]()

**Corpus が知識を蓄え、Recall が引き出す。**

自己ホスト型の検索・取得サービス。文書チャンクを取り込み、ベクトル類似度と語彙一致の
ハイブリッドで検索し、**引用可能な chunk を JSON で返す HTTP API** を提供する。

**完全にローカルで動き、有料サービスに一切依存しない。**
埋め込みの生成もベクトルの保存も検索も、外部 API を呼ばない。

単体で完結して動く。同時に、将来 [NeNe Corpus](https://github.com/hideyukiMORI/nene-corpus) の
検索バックエンドとして環境変数ひとつで差し替えられる形を最初から取っている。

> **Status: pre-alpha。** 骨組みと設計判断が固まった段階で、検索の実装はこれから。
> API の形は `docs/openapi/openapi.yaml` に定義済みだが、`/v1/search` と `/v1/chunks` は
> 現在 `501 Not Implemented` を返す。`org_id` の検証・設定の検証・`/healthz` は動作する。

---

## 構成

```
┌─ Windows ────────────────┐     ┌─ WSL ─────────────────────────────┐
│                          │     │                                   │
│  Ollama + bge-m3         │◀────┤  nene-recall (Go)                 │
│  RTX 3090                │HTTP │    ├ HTTP API  /v1/search         │
│  ※ 変換のみ・状態なし     │────▶│    ├ ハイブリッド検索・スコア合成  │
│                          │     │    ├ org 分離                     │
└──────────────────────────┘     │    └ PostgreSQL + pgvector        │
                                 └───────────────────────────────────┘
                                        ▲
                                        └── CLI / Corpus / Claude(MCP)
```

Ollama は**ベクトル DB ではない**。テキストを渡すとベクトルを返すだけの、
状態を持たない推論サーバ。**外部に出る通信はこの変換リクエストのみで、データは WSL から出ない。**

---

## なぜ

NeNe Corpus の検索は、実測するとベクトル検索でも全文検索でもない
（`src/Search/PdoChunkSearchRepository.php`）:

```sql
CASE WHEN c.content LIKE '%term%' THEN 1 ELSE 0 END   -- を語数ぶん加算
```

空白区切りの部分一致で、スコアはヒット語数のカウント。前方一致でないためインデックスも効かず、
日本語は分かち書きされないのでクエリ全体が部分文字列で含まれる場合しか当たらない。

Recall が置き換える相手は「まだ RAG になっていないもの」であり、差し替えの価値は最初から大きい。
背景の全文は [`docs/requirements.md`](docs/requirements.md)。

---

## 設計の要点

| 判断 | 理由 |
| --- | --- |
| **単独動作が先、Corpus 統合は後** | 統合を前提にすると Corpus レーンの進捗に律速される（[ADR 0001](docs/adr/0001-standalone-first-corpus-swappable.md)） |
| **本文を保持しつつ `chunk_id` を返す** | 単独動作と本文の単一正本を両立させる唯一の形（[ADR 0002](docs/adr/0002-store-content-return-chunk-id.md)） |
| **`org_id` は全 API で必須。既定値なし** | 分離条件が SQL から Go 側へ移る。緩めると静かに情報漏洩する（[ADR 0003](docs/adr/0003-org-id-is-mandatory.md)） |
| **埋め込みはインタフェース越し** | Anthropic は埋め込みを提供しない。プロバイダを差し替え可能に（[ADR 0005](docs/adr/0005-embedding-provider-is-pluggable.md)） |
| **score は float** | ベクトル類似度は float。Corpus 側の int を先に広げる（[ADR 0006](docs/adr/0006-score-is-float.md)） |
| **pgvector。索引なしで始め、実測してから HNSW** | 索引を最初から入れると「なぜ入れたか」を数字で語れなくなる（[ADR 0007](docs/adr/0007-pgvector-over-brute-force.md)） |
| **埋め込みはローカル既定（Ollama + bge-m3）** | ローカル利用が要件。RTX 3090 が使える。費用0円（[ADR 0008](docs/adr/0008-local-embedding-by-default.md)） |
| **検索品質の評価を Phase 1 の必須要件に** | ベクトル DB 選定はもう差別化にならない。測ることが差別化（[ADR 0009](docs/adr/0009-retrieval-evaluation-is-in-scope.md)） |
| **厳格性は文章でなく機械で強制する** | Go はゼロ値・不変性・網羅性を言語で縛れない。落ちる仕組みにする（[ADR 0010](docs/adr/0010-strictness-is-mechanically-enforced.md)） |

> ⚠️ [ADR 0004](docs/adr/0004-brute-force-cosine-no-vector-db.md)（総当たり・ベクトル DB 不使用）は
> ADR 0007 が supersede した。**ただしその性能分析は今も有効**——10万件規模では総当たりで足りる。
> 判断が変わったのは性能ではなく、市場価値という別の軸が加わったため。

---

## Corpus との接続（Phase 2）

Corpus 側の接合部は既に1メソッドのインタフェースに切れている。

```
SearchChunksUseCase  ──▶  ChunkSearchRepositoryInterface   ← どちらも変更なし
                                    │
                          ┌─────────┴─────────┐
                          ▼                   ▼
              PdoChunkSearchRepository   HttpChunkSearchRepository ★新規
                   (MySQL LIKE)                    │
                                                   ▼  POST /v1/search
                                              NeNe Recall
```

必要な変更は **新規1クラス＋`SearchServiceProvider` への env 分岐1箇所**のみ。
ユースケース層とコントローラ層は無傷で、Recall が落ちても PDO 実装に落ちて動き続ける。

---

## 開発

前提: Go 1.27+、Docker（または Postgres 17 + pgvector）、Ollama

```bash
# 1. Postgres + pgvector を起動（make check の前提。テストは実 DB に対して走る）
#    ホスト側ポートは 5433。5432 ではない（理由は compose.yaml のコメント）
docker compose up -d

# 2. Ollama に埋め込みモデルを引く（Windows 側で実行）
ollama pull bge-m3

# 3. 設定
cp .env.example .env      # RECALL_OLLAMA_URL を環境に合わせる

# 4. 動かす
make check                # 品質ゲート一式（CI もこれを呼ぶ）
make run                  # 起動
```

`make check` = fmt-check → vet → lint → conformance → test → cover-check → tidy-check → build。
**Postgres が起動していることが前提**で、ストアのテストはモック SQL ではなく実 DB に対して走る。
CI も同じイメージ・同じ資格情報の service container を立てて同じ `make check` を呼ぶ（QLT-003）。
規則の正本は [`docs/coding-rules.md`](docs/coding-rules.md)、その設計判断は
[ADR 0010](docs/adr/0010-strictness-is-mechanically-enforced.md)。

```bash
curl -s localhost:8080/healthz
# {"checks":{},"embedder_id":"bge-m3:1024","status":"ok"}

curl -s -X POST localhost:8080/v1/search -d '{"query":"x"}'
# {"error":{"code":"org_id_required", ...}}   ← org_id は必須（ADR 0003）
```

### 埋め込みについて

**Anthropic は埋め込みモデルを提供していない**
（[公式ドキュメント](https://platform.claude.com/docs/en/build-with-claude/embeddings)）。
また **ChatGPT / Grok のサブスクリプションは API アクセス権ではなく**、
生成モデルは埋め込みの代替にもならない（詳細は要件定義 §6.1）。

既定は Ollama + **`bge-m3`**（MIT・1024次元・8192トークン文脈長・100言語以上）。
1024次元は `voyage-4` の既定と同じなので、外部 API に切り替えてもスキーマが変わらない。

> ⚠️ **埋め込みモデルを変えたら保存済みベクトルは無効になる。**
> 次元が一致していても異なるモデルのベクトルは比較できず、
> **エラーにならないまま無意味なスコアが返る。** `embedder_id` の記録はこれを検知するためにある。

---

## 費用

**既定構成は 0円。** すべて OSS または所有済みハードウェア。

| 要素 | ライセンス |
| --- | --- |
| Go / PostgreSQL / pgvector | BSD-3-Clause / PostgreSQL License |
| Ollama / bge-m3 / pgx | MIT |
| Docker Desktop | 無料枠（250人未満 **かつ** 年商 $10M 未満） |
| GitHub Actions | public リポジトリは無料 |

Docker Desktop の無料枠を避けたい場合は、Docker Engine（Apache 2.0・規模条件なし）を
WSL に直接入れるか、Postgres を apt で入れる。内訳は要件定義 §10。

**評価（ADR 0009）にも費用はかからない。** recall@k と MRR は正解セットとの
突き合わせで、LLM を1回も呼ばない。

---

## ドキュメント

| 文書 | 内容 |
| --- | --- |
| [`docs/requirements.md`](docs/requirements.md) | 要件定義。スコープ・アーキテクチャ・非機能要件・費用・未決事項 |
| [`docs/adr/`](docs/adr/) | 設計判断の記録。判断の正本はここ |
| [`docs/openapi/openapi.yaml`](docs/openapi/openapi.yaml) | API 定義（OpenAPI 3.1） |
| [`docs/coding-rules.md`](docs/coding-rules.md) | コーディング規約。各規則が active / planned / 不採用の状態を持つ |
| [`docs/benchmarks/`](docs/benchmarks/) | 実測値。索引や実装の判断は必ずここの数字を根拠にする |

## ライセンス

MIT
