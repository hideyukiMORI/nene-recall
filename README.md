# NeNe Recall

[![CI](https://github.com/hideyukiMORI/nene-recall/actions/workflows/ci.yml/badge.svg)](https://github.com/hideyukiMORI/nene-recall/actions/workflows/ci.yml)
[![Go 1.27](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](./LICENSE)
[![OpenAPI](https://img.shields.io/badge/OpenAPI-3.1-85EA2D?logo=swagger)](./docs/openapi/openapi.yaml)
[![Status: pre-alpha](https://img.shields.io/badge/status-pre--alpha-lightgrey)]()

**Corpus が知識を蓄え、Recall が引き出す。**

自己ホスト型の検索・取得サービス。文書チャンクを取り込み、ベクトル類似度と語彙一致の
ハイブリッドで検索し、**引用可能な chunk を JSON で返す HTTP API** を提供する。
単一バイナリ＋SQLite ファイルで動き、外部のベクトル DB を必要としない。

単体で完結して動く。同時に、将来 [NeNe Corpus](https://github.com/hideyukiMORI/nene-corpus) の
検索バックエンドとして環境変数ひとつで差し替えられる形を最初から取っている。

> **Status: pre-alpha。** 骨組みと設計判断が固まった段階で、検索の実装はこれから。
> API の形は `docs/openapi/openapi.yaml` に定義済みだが、`/v1/search` と `/v1/chunks` は
> 現在 `501 Not Implemented` を返す。`org_id` の検証と `/healthz` は動作する。

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
| **ベクトル DB も cgo も使わない** | 10万チャンクまでは総当たり内積で足りる。単一バイナリを保つ（[ADR 0004](docs/adr/0004-brute-force-cosine-no-vector-db.md)） |
| **埋め込みはインタフェース越し** | Anthropic は埋め込みを提供しない。自己ホストの道を閉じない（[ADR 0005](docs/adr/0005-embedding-provider-is-pluggable.md)） |
| **score は float** | ベクトル類似度は float。Corpus 側の int を先に広げる（[ADR 0006](docs/adr/0006-score-is-float.md)） |

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

```bash
# 前提: Go 1.27+
cp .env.example .env      # VOYAGE_API_KEY を埋める
make test                 # go test ./... -race -cover
make build                # bin/recall
make run                  # 起動
```

```bash
curl -s localhost:8080/healthz
# {"checks":{},"embedder_id":"voyage-4:1024","status":"ok"}

curl -s -X POST localhost:8080/v1/search -d '{"query":"x"}'
# {"error":{"code":"org_id_required", ...}}   ← org_id は必須（ADR 0003）
```

### 埋め込みプロバイダ

Anthropic は埋め込みモデルを提供していない
（[公式ドキュメント](https://platform.claude.com/docs/en/build-with-claude/embeddings)）。
既定は Voyage AI の `voyage-4`（32,000 文脈長・1024次元・多言語）。

`voyage-4-nano` は Apache 2.0 で Hugging Face に公開されているため、
将来ローカル推論へ寄せる道が開いている。`RECALL_EMBEDDER=local` の口は用意済み（実装は未着手）。

> ⚠️ **埋め込みモデルを変えたら保存済みベクトルは無効になる。**
> 次元が一致していても異なるモデルのベクトルは比較できず、
> **エラーにならないまま無意味なスコアが返る。** `embedder_id` の記録はこれを検知するためにある。

---

## ドキュメント

| 文書 | 内容 |
| --- | --- |
| [`docs/requirements.md`](docs/requirements.md) | 要件定義。スコープ・アーキテクチャ・非機能要件・未決事項 |
| [`docs/adr/`](docs/adr/) | 設計判断の記録。判断の正本はここ |
| [`docs/openapi/openapi.yaml`](docs/openapi/openapi.yaml) | API 定義（OpenAPI 3.1） |

## ライセンス

MIT
