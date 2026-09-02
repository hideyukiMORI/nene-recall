# recallctl

NeNe Recall の HTTP API を叩く薄いクライアント。設計の正本は
[ADR 0016](../../docs/adr/0016-cli-is-an-http-client-with-org-default.md)。

- ストア (PostgreSQL) にも埋め込み (Ollama) にも直接触らない。知っているのはサーバの URL だけ。
- 依存は標準ライブラリのみ。
- 🔴 **`org_id` の既定値 `1` を持つのはこの CLI だけ**である。サーバ側には無い（ADR 0003）。
  どの `org_id` で問い合わせたかは毎回 stderr に1行出る。

```
$ make build          # bin/recallctl ができる
$ recallctl help
```

## 共通の約束

| 項目 | 決まり |
| --- | --- |
| サーバ URL | `-url` → `$RECALL_URL` → `http://127.0.0.1:8080` |
| `org_id` | `-org` → `$RECALL_ORG_ID` → `1`（`health` では使わない） |
| Bearer トークン | `-token` → `$RECALL_TOKEN` → **付けない**（既定値は無い・ADR 0020） |
| タイムアウト | `-timeout`、既定 60s（検索は埋め込み往復を含む） |
| 出力 | 標準出力は結果だけ。診断（`org_id=…` / `embedder_id=…`）は stderr |
| `-json` | サーバ応答の生 JSON を整形せずそのまま標準出力へ |

終了コード: `0` 成功 / `1` 使い方・入力の誤り / `2` サーバが 4xx/5xx / `3` 接続失敗。

🔴 **フラグは位置引数より前に置く。** Go の `flag` は最初の非フラグ引数で解釈を止めるので、
`recallctl delete 42 -org 5` の `-org 5` はフラグとして読まれない。

### 認証

サーバ側で `RECALL_API_TOKEN` が設定されていると `/v1/*` に Bearer 認証が掛かる
（[ADR 0020](../../docs/adr/0020-phase2-corpus-integration-contract.md) Decision 3）。
`-token` か `$RECALL_TOKEN` を渡すと `Authorization: Bearer <token>` が付く。

- 🔴 **既定のトークンは無い。** 未指定ならヘッダごと付けない。付け忘れは 401（終了コード 2）で必ず表面化する。
- 🔴 **トークンは診断行に出ない。** `org_id` と違って、出す利益が無く、`2>&1 | tee log` で秘密が落ちる。
- `$RECALL_TOKEN` はサーバ側の `RECALL_API_TOKEN` と**別の名前**にしてある。同じ機械で両方を
  動かすとき、サーバの設定がクライアントにも効くと「設定した覚えのないトークンが付いていた」が起きる。

```console
$ RECALL_TOKEN=devtoken recallctl search ベクトルの索引
```

## 例

### 1. 疎通を見る

```console
$ recallctl health
status=ok
check     status  detail
database  ok
embedder  ok
```

`status` が `ok` でなければ終了コード 2。依存が落ちているとサーバは 503 で
Health を返すが、CLI はそれを読んで「どれが落ちているか」を表に出す。

### 2. チャンクを投入する

入力は **1行1件の JSONL**（OpenAPI の `ChunkInput`）。空行は無視する。
`chunk_id` は指定できない（Recall の採番なので）。含まれていたら送る前に何行目かを言って落ちる。

外部システム（Corpus）の id を持たせたいときは **`external_id`** を書く
（[ADR 0020](../../docs/adr/0020-phase2-corpus-integration-contract.md) Decision 1）。
同じ `(org_id, external_id)` の再投入は**置き換え**になり、`chunk_ids` には既存行の id が返る。
持たない行はキーごと省略する——0 を書くと「外部 id が 0 番」として扱われる（CLI が 400 相当で落とす）。

```console
$ cat chunks.jsonl
{"external_id":901,"document_id":900,"source_id":900,"chunk_index":0,"content":"ベクトルの索引を張ると検索は速くなる。"}
{"document_id":900,"source_id":900,"chunk_index":1,"content":"索引を張ると再現率は落ちる。"}

$ recallctl put < chunks.jsonl
org_id=1 (default)
{
  "accepted": 2,
  "chunk_ids": [
    11,
    12
  ],
  "external_ids": [
    901,
    null
  ]
}
```

`external_ids` は `chunk_ids` と**同じ順・同じ長さ**で返る。自分の id と Recall の採番の
対応は、この1つの応答から作れる。

全行を1リクエストにまとめて送る。分割送信はしない——途中で失敗すると
「一部だけ入った」状態が残り、どこまで入ったかを知る手段が無いため。

### 3. 検索する

```console
$ recallctl search -limit 3 ベクトルの索引
org_id=1 (default)
#  score   vector  lexical  ext  doc  src  idx  content
1  0.8100  0.9000  0.6000   901  900  900  0    ベクトルの索引を張ると検索は速くなる。
2  0.5200  0.6000  0.1000   -    900  900  1    索引を張ると再現率は落ちる。
embedder_id=bge-m3:1024 took_ms=62
```

`ext` は `external_id`。持たない行は `-`（**0 ではない**——0 だと「外部 id が 0 番」と読めてしまう）。

`-limit` と `-alpha` は**指定したときだけ**送る。未指定ならサーバの既定に任せる。
`-alpha 0`（純語彙）は「指定しなかった」とは別の意味なので、両者を取り違えない。

絞り込みは `-document` / `-source` を複数回指定できる。

### 後始末

```console
$ recallctl delete 11                # 1件
$ recallctl delete-source 900        # source ごと
org_id=1 (default)
deleted=2
$ recallctl delete-document 900      # document ごと
org_id=1 (default)
deleted=2
```

削除の単位が2つあるのは、Corpus の削除経路が document 単位と source 単位の2つだからである
（[ADR 0020](../../docs/adr/0020-phase2-corpus-integration-contract.md) Decision 2）。
どちらも `deleted=<件数>` を返すので、**宛先を取り違えても応答の形からは気づけない**。
