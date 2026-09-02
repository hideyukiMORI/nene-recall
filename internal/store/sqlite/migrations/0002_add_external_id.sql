-- 0002: 外部システム（Corpus）の id を受けるための列。
--
-- 🔴 マイグレーションは forward-only である。down を書かない。
--    詳細と理由は migrate.go の冒頭コメントを参照。
--
-- 🔴 0001 に列を足して済ませない。0001 は既に適用済みとして
--    schema_migrations に記録されうるので、書き換えても走らない。既存のファイルを
--    直す形にすると「新しく作った DB にだけ列がある」という、開発機でしか
--    再現しない食い違いが生まれる。
--    ⚠️ 0001 の冒頭が「postgres の 0001+0002 を1本にした」と書いているのは、
--    履歴の無いストアを最初に作ったときの話である。以後は前進のみ。
--
-- 判断の正本は docs/adr/0020-phase2-corpus-integration-contract.md の Decision 1。
-- 列の意味・NULL を許す理由・org_id を一意制約の先頭に置く理由は
-- postgres 側の 0003_add_external_id.sql と同一である。
ALTER TABLE chunks
    ADD COLUMN external_id INTEGER;

-- SQLite は ALTER TABLE ADD CONSTRAINT を持たないので、一意索引で同じことを行う。
--
-- 🔑 SQLite の UNIQUE 索引も NULL どうしを重複とみなさない（postgres と同じ）。
--    external_id を持たない行は何行でも入る。
--
-- 🔴 この索引は Writer の ON CONFLICT (org_id, external_id) の対象そのものである。
--    消すと upsert が実行時に落ちる。
CREATE UNIQUE INDEX chunks_org_external_id_key ON chunks (org_id, external_id);
