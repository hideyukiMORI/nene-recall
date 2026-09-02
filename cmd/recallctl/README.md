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
| タイムアウト | `-timeout`、既定 60s（検索は埋め込み往復を含む） |
| 出力 | 標準出力は結果だけ。診断（`org_id=…` / `embedder_id=…`）は stderr |
| `-json` | サーバ応答の生 JSON を整形せずそのまま標準出力へ |

終了コード: `0` 成功 / `1` 使い方・入力の誤り / `2` サーバが 4xx/5xx / `3` 接続失敗。

🔴 **フラグは位置引数より前に置く。** Go の `flag` は最初の非フラグ引数で解釈を止めるので、
`recallctl delete 42 -org 5` の `-org 5` はフラグとして読まれない。

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
Phase 1 では `chunk_id` を指定できないので、含まれていたら送る前に何行目かを言って落ちる。

```console
$ cat chunks.jsonl
{"document_id":900,"source_id":900,"chunk_index":0,"content":"ベクトルの索引を張ると検索は速くなる。"}
{"document_id":900,"source_id":900,"chunk_index":1,"content":"索引を張ると再現率は落ちる。"}

$ recallctl put < chunks.jsonl
org_id=1 (default)
{
  "accepted": 2,
  "chunk_ids": [
    11,
    12
  ]
}
```

全行を1リクエストにまとめて送る。分割送信はしない——途中で失敗すると
「一部だけ入った」状態が残り、どこまで入ったかを知る手段が無いため。

### 3. 検索する

```console
$ recallctl search -limit 3 ベクトルの索引
org_id=1 (default)
#  score   vector  lexical  doc  src  idx  content
1  0.8100  0.9000  0.6000   900  900  0    ベクトルの索引を張ると検索は速くなる。
embedder_id=bge-m3:1024 took_ms=62
```

`-limit` と `-alpha` は**指定したときだけ**送る。未指定ならサーバの既定に任せる。
`-alpha 0`（純語彙）は「指定しなかった」とは別の意味なので、両者を取り違えない。

絞り込みは `-document` / `-source` を複数回指定できる。

### 後始末

```console
$ recallctl delete 11              # 1件
$ recallctl delete-source 900      # source ごと
org_id=1 (default)
deleted=2
```
